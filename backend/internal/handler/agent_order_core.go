package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"

	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 代理商下单的核心（AgentPlaceOrder 的第 3–7 步）：后台下单和异次元店铺共享对接的取码共用。
// 这里不写 HTTP 响应、不写审计，只返回订单和码或者具名错误；调用方按错误类型映射成自己的响应，按 Replayed 写审计。
// 完整的码只出现在返回值里，不进日志、错误信息。

// 具名错误，调用方用 errors.Is / errors.As 区分。
var (
	errAgentOrderNoKey    = errors.New("主密钥不可用")      // 具体是 *agentOrderNoKeyError
	errAgentOrderBusy     = errors.New("数据库忙")        // 具体是 *agentOrderBusyError
	errAgentOrderSeal     = errors.New("码加密失败")       // 后台：503 maintenance，不发主密钥告警（原因已由 sealLocalCDKCode 记下）
	errAgentOrderInternal = errors.New("生成码失败")       // 后台：500 internal
	errProductUnavailable = errors.New("商品不可售")       // 不存在、下架、这个代理商取不到价
	errProductNotReady    = errors.New("商品暂时无法出码")    // 含就绪检查被 ctx 打断
	errRequestConflict    = errors.New("请求号已用于另一笔订单") // 同号订单的商品或数量不同
)

// agentOrderNoKeyError 主密钥不可用：下单前检查不通过，或重放时码一张也解不开。Kind 是 secretErrKind 给出的错误类型。
type agentOrderNoKeyError struct{ Kind string }

func (e *agentOrderNoKeyError) Error() string        { return "主密钥不可用: " + e.Kind }
func (e *agentOrderNoKeyError) Is(target error) bool { return target == errAgentOrderNoKey }

// agentOrderBusyError 读写库失败（含等同号锁时请求被取消）。Op 是哪一步，后台日志写成「代理商 N <Op>失败: <Err>」；
// Err 是库或 ctx 的错误，里面没有码。用同一个请求号重试是安全的。
type agentOrderBusyError struct {
	Op  string
	Err error
}

func (e *agentOrderBusyError) Error() string        { return fmt.Sprintf("%s失败: %v", e.Op, e.Err) }
func (e *agentOrderBusyError) Is(target error) bool { return target == errAgentOrderBusy }
func (e *agentOrderBusyError) Unwrap() error        { return e.Err }

// priceChangedError 代理商确认时看到的单价和现在的不一致。CurrentCents 是现价。
type priceChangedError struct{ CurrentCents int64 }

func (e *priceChangedError) Error() string {
	return fmt.Sprintf("价格已变化，现价 %d 美分", e.CurrentCents)
}

// agentOrderInput 是一次下单的参数。调用方负责校验：商品编码已去掉首尾空白、数量 1–MaxIssueCount、
// 请求号格式合法、备注不超长。
type agentOrderInput struct {
	AgentID                int64
	Username               string // 写 issued_by 和流水操作人："agent:" + Username
	ProductCode            string
	Quantity               int
	RequestNo              string // 已带前缀（异次元为 "acg:" + 对方单号）
	Note                   string
	ExpectedUnitPriceCents int64 // <= 0 不比价
	Source, ExternalRef    string
}

// agentOrderOutcome 是下单结果。Codes 是明文码，新单的状态都是未使用，重放的带上现在的状态和换出的新码。
type agentOrderOutcome struct {
	Order    db.AgentOrder
	Codes    []exportedCode
	Replayed bool
}

// ---- 同一个请求号串行 ----

type agentOrderKeyLock struct {
	ch   chan struct{}
	refs int // 持有者加等待者；归零时从表里删掉
}

var (
	agentOrderLocksMu sync.Mutex
	agentOrderLocks   = map[string]*agentOrderKeyLock{}
)

func agentOrderLockKey(agentID int64, requestNo string) string {
	return strconv.FormatInt(agentID, 10) + "|" + requestNo
}

// agentOrderLock 拿「代理商 + 请求号」的锁（裁定 P26）。测试可以替换成空锁，模拟多进程时锁不共享。
var agentOrderLock = lockAgentOrderRequest

// lockAgentOrderRequest 让同一个代理商的同一个请求号在进程内串行通过「查请求号 → 商品检查 → 生成码 → 建单」：
// 否则请求 A 已过检查、还没提交时，管理员恰好改价、下架或熔断，同号重试 B 会拿到明确失败，
// 前端随后换新请求号重下，而 A 照常扣款，用户就买了两单。
// 等锁时请求被取消返回 ok=false。release 可以重复调用；没人持有也没人等待时这一项从表里删掉，表不会越长越大。
// 多进程部署时锁不共享，靠 (agent_id, request_no) 唯一约束兜底（撞上后回到查请求号重放）。
func lockAgentOrderRequest(ctx context.Context, agentID int64, requestNo string) (release func(), ok bool) {
	k := agentOrderLockKey(agentID, requestNo)
	agentOrderLocksMu.Lock()
	l := agentOrderLocks[k]
	if l == nil {
		l = &agentOrderKeyLock{ch: make(chan struct{}, 1)}
		agentOrderLocks[k] = l
	}
	l.refs++
	agentOrderLocksMu.Unlock()
	drop := func() {
		agentOrderLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(agentOrderLocks, k)
		}
		agentOrderLocksMu.Unlock()
	}
	select {
	case l.ch <- struct{}{}:
	case <-ctx.Done():
		drop()
		return nil, false
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-l.ch
			drop()
		})
	}, true
}

// 测试钩子，生产环境都是 nil。
var (
	agentOrderPausePoint func() // 第 5 步检查全部通过之后、生成码之前
	agentOrderCollided   func() // 建单撞上同号唯一约束、转回第 4 步之前
)

// ---- 就绪检查 ----

// agentReadyCheck 是下单时用的就绪检查，测试可以替换。
var agentReadyCheck = agentProductReady

// agentProductReadyCtx 跟着 ctx 做就绪检查：ctx 先结束就按不能出码返回 (nil, false)。
// ctx 没有截止时间也没结束时（后台下单的请求 ctx）直接同步调用，和以前一样；
// 有截止时间时另起协程检查，结果通道带 1 个缓冲，ctx 先到时检查做完也不会卡住协程。
func agentProductReadyCtx(ctx context.Context, p db.AgentProduct) (provider.CardSupply, bool) {
	check := agentReadyCheck // 在这里取一次：协程里读全局变量会和测试换回原函数抢
	if ctx.Err() != nil {
		return nil, false
	}
	if _, ok := ctx.Deadline(); !ok {
		return check(ctx, p)
	}
	type result struct {
		cs provider.CardSupply
		ok bool
	}
	ch := make(chan result, 1)
	go func() {
		cs, ok := check(ctx, p)
		ch <- result{cs, ok}
	}()
	select {
	case r := <-ch:
		return r.cs, r.ok
	case <-ctx.Done():
		return nil, false
	}
}

// ---- 取回一笔订单的码 ----

// loadAgentOrderCodes 取一笔订单当时发出的码并解密，带上每张码现在的状态和换出的新码（重放和导出共用）。
// 读库失败返回 *agentOrderBusyError（Op 写明是哪一步）；主密钥不可用返回 *agentCodesNoKeyError。
func loadAgentOrderCodes(ctx context.Context, agentID int64, o db.AgentOrder) ([]exportedCode, error) {
	ids, err := db.LocalCDKIDsForAgentOrder(ctx, agentID, o.ID)
	if err != nil {
		return nil, &agentOrderBusyError{Op: "读取订单的码", Err: err}
	}
	if len(ids) != o.Quantity { // 不应发生：码和订单在同一个事务里写入，码只会被禁用、不会被删
		log.Printf("[agent-order] 订单 %d 现有 %d 张码，和订单数量 %d 不一致", o.ID, len(ids), o.Quantity)
	}
	codes, err := openAgentCodes(ctx, agentID, ids)
	var noKey *agentCodesNoKeyError
	if errors.As(err, &noKey) {
		return nil, noKey
	}
	if err != nil {
		return nil, &agentOrderBusyError{Op: "读取码的密文", Err: err}
	}
	// 补上每张码现在的状态和换出的新码：只读这笔订单这些码（限定 agent_id）
	rows, err := db.LoadLocalCDKRowsByAgentIDs(ctx, agentID, ids)
	if err != nil {
		return nil, &agentOrderBusyError{Op: "读取码的状态", Err: err}
	}
	byID := make(map[int64]db.LocalCDKRow, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	for i := range codes {
		if r, ok := byID[codes[i].ID]; ok {
			codes[i].Status, codes[i].ReplacedBy = agentCodeStatus(r.View.Status), r.ReplacedBy
		}
	}
	return codes, nil
}

// ---- 下单 ----

// placeAgentOrder 下单。处理顺序（编号沿用 AgentPlaceOrder）：
//  3. 主密钥可用；
//  4. 查请求号——放在商品检查之前：第一次成功后商品下架或改价，用同一个请求号重试也要拿回第一次的码；
//  5. 商品已上架、取得到价格、价格和代理商确认时看到的一致（ExpectedUnitPriceCents <= 0 时不比价）、现在能出码；
//  6. 生成码并逐张加密；7. 一个事务里建单、扣款、写流水、写码。
//
// 第 4–7 步按「代理商 + 请求号」在进程内串行（见 lockAgentOrderRequest），建单一返回就放锁。
// 建单撞上同号唯一约束（多进程时锁不共享）时回到第 4 步重放。
//
// 3–6 步的拒绝都在写库之前，第 7 步失败整体回滚，所以除了成功和重放，任何错误都没有扣款、没有建单。
// 错误：*agentOrderNoKeyError、*agentOrderBusyError、errAgentOrderSeal、errAgentOrderInternal、errProductUnavailable、
// *priceChangedError、errProductNotReady、errRequestConflict，以及建单原样透传的
// *db.InsufficientBalanceError、db.ErrAgentDisabled、db.ErrAgentNotFound。
func placeAgentOrder(ctx context.Context, in agentOrderInput) (agentOrderOutcome, error) {
	if _, err := secretbox.LoadKey(); err != nil {
		return agentOrderOutcome{}, &agentOrderNoKeyError{Kind: secretErrKind(err)}
	}
	// 第 4–7 步同号串行：锁在查请求号之前拿，建单返回后放（提前返回由 defer 放）
	release, locked := agentOrderLock(ctx, in.AgentID, in.RequestNo)
	if !locked {
		return agentOrderOutcome{}, &agentOrderBusyError{Op: "等同号请求", Err: ctx.Err()}
	}
	defer release()
	if out, found, err := agentOrderFindReplay(ctx, in); err != nil || found {
		return out, err
	}

	p, err := db.GetAgentProductByCode(ctx, in.ProductCode)
	switch {
	case errors.Is(err, db.ErrAgentProductNotFound):
		return agentOrderOutcome{}, errProductUnavailable
	case err != nil:
		return agentOrderOutcome{}, &agentOrderBusyError{Op: "读取商品", Err: err}
	case !p.Enabled:
		return agentOrderOutcome{}, errProductUnavailable
	}
	price, priced, err := db.AgentPriceFor(ctx, in.AgentID, p.ID)
	if err != nil {
		return agentOrderOutcome{}, &agentOrderBusyError{Op: "取价", Err: err}
	}
	if !priced {
		return agentOrderOutcome{}, errProductUnavailable
	}
	if in.ExpectedUnitPriceCents > 0 && price != in.ExpectedUnitPriceCents {
		return agentOrderOutcome{}, &priceChangedError{CurrentCents: price}
	}
	cs, ready := agentProductReadyCtx(ctx, p)
	if !ready {
		return agentOrderOutcome{}, errProductNotReady
	}
	if agentOrderPausePoint != nil {
		agentOrderPausePoint()
	}

	issuedBy := "agent:" + in.Username
	// 不按 in.Quantity 预分配容量（CodeQL #26 #27）：数量已由调用方限制在 MaxIssueCount 以内，追加的开销可以忽略。
	plain := make([]string, 0)
	rows := make([]db.NewLocalCDK, 0)
	for i := 0; i < in.Quantity; i++ {
		lc, err := provider.GenerateCode(cs)
		if err != nil {
			log.Printf("[agent-order] 代理商 %d 生成码失败: %v", in.AgentID, err)
			return agentOrderOutcome{}, errAgentOrderInternal
		}
		sealed := sealLocalCDKCode(lc.Code, lc.CodeHash)
		if sealed == "" { // 加密失败（原因已由 sealLocalCDKCode 记下），整单拒绝
			return agentOrderOutcome{}, errAgentOrderSeal
		}
		plain = append(plain, lc.Code)
		rows = append(rows, db.NewLocalCDK{
			Provider: p.Provider, CodeHash: lc.CodeHash, TokenHash: lc.TokenHash, CodePrefix: lc.Prefix,
			Product: p.Product, PlanKey: p.PlanKey, Region: p.Region, IssuedBy: issuedBy, Note: in.Note, CodeSealed: sealed,
		})
	}

	ord, ids, err := db.CreateAgentOrder(ctx, db.NewAgentOrder{
		AgentID: in.AgentID, Product: p, UnitPriceCents: price, RequestNo: in.RequestNo, Note: in.Note, Operator: issuedBy,
		Source: in.Source, ExternalRef: in.ExternalRef, Codes: rows,
	}, agentNow())
	release()
	var short *db.InsufficientBalanceError
	switch {
	case errors.Is(err, db.ErrAgentOrderExists): // 同一个请求号的并发请求先提交了：回到第 4 步
		if agentOrderCollided != nil {
			agentOrderCollided()
		}
		out, found, rerr := agentOrderFindReplay(ctx, in)
		if rerr != nil {
			return agentOrderOutcome{}, rerr
		}
		if !found {
			return agentOrderOutcome{}, &agentOrderBusyError{Op: "下单", Err: err}
		}
		return out, nil
	case errors.As(err, &short), errors.Is(err, db.ErrAgentDisabled), errors.Is(err, db.ErrAgentNotFound):
		return agentOrderOutcome{}, err
	case err != nil: // 含数据库忙、码的哈希撞上唯一约束；已整体回滚
		return agentOrderOutcome{}, &agentOrderBusyError{Op: "下单", Err: err}
	}

	codes := make([]exportedCode, len(ids))
	for i, id := range ids {
		codes[i] = exportedCode{ID: id, Code: plain[i], Status: agentCStatusUnused} // 刚发出的码一定是未使用
	}
	return agentOrderOutcome{Order: ord, Codes: codes}, nil
}

// agentOrderFindReplay 查这个请求号有没有订单（第 4 步）。没有返回 found=false。
// 有的话：商品编码和数量都一致就取回原来的码重放（码从密文解出，不再扣钱；备注不参与比较），否则 errRequestConflict。
// 读库失败返回 *agentOrderBusyError；码一张也解不开返回 *agentOrderNoKeyError。err 不为空时 found 没有意义。
func agentOrderFindReplay(ctx context.Context, in agentOrderInput) (agentOrderOutcome, bool, error) {
	o, found, err := db.GetAgentOrderByRequestNo(ctx, in.AgentID, in.RequestNo)
	if err != nil {
		return agentOrderOutcome{}, false, &agentOrderBusyError{Op: "查请求号", Err: err}
	}
	if !found {
		return agentOrderOutcome{}, false, nil
	}
	if o.ProductCode != in.ProductCode || o.Quantity != in.Quantity {
		return agentOrderOutcome{}, true, errRequestConflict
	}
	codes, err := loadAgentOrderCodes(ctx, in.AgentID, o)
	var noKey *agentCodesNoKeyError
	if errors.As(err, &noKey) {
		return agentOrderOutcome{}, true, &agentOrderNoKeyError{Kind: noKey.kind}
	}
	if err != nil {
		return agentOrderOutcome{}, true, err
	}
	return agentOrderOutcome{Order: o, Codes: codes, Replayed: true}, true, nil
}
