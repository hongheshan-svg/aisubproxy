package handler

import (
	"container/list"
	"context"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/tuzi/cdk-recharge-system/internal/acgshared"
	"github.com/tuzi/cdk-recharge-system/internal/auth"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/secretbox"
)

// 异次元（acg-faka）店铺共享协议的上游接口 /shared/*：代理商在自己的发卡站里把本站当作上游店铺对接。
// 业务结果一律 HTTP 200：成功 {"code":200,"msg":"success","data":…}，失败 {"code":0,"msg":"…"}——
// 下游遇到 4xx/5xx 会丢掉 msg；只有路径不存在才是 404（下游对可选接口的 404 有降级逻辑）。
// 所有响应带 Cache-Control: no-store。日志只写商户 ID、接口名和对方订单号，不写密钥、app_key、sign 和请求体。

const (
	sharedMsgHTTPS     = "请使用 https 地址"
	sharedMsgRate      = "请求太频繁，请稍后再试"
	sharedMsgBadForm   = "请求格式不正确"
	sharedMsgNoApp     = "商户ID不存在"
	sharedMsgBadSign   = "密钥错误"
	sharedMsgIP        = "来源 IP 不在白名单"
	sharedMsgAdminOff  = "对接已被管理员关闭"
	sharedMsgAgentOff  = "代理商账号已停用"
	sharedMsgMaint     = "系统维护中"
	sharedMsgBusy      = "系统繁忙，请稍后再试"
	sharedMsgNoProduct = "商品不存在"
	sharedMsgOffSale   = "当前商品已停售"
	sharedMsgNoSpec    = "该商品没有可选规格"
	sharedMsgNoStock   = "库存不足"
	sharedMsgQty       = "购买数量需为 1–200"
	sharedShopSuffix   = "（美元报价）"
)

const (
	sharedIPRateLimit = 120 // 每个来源 IP 每分钟，整个 /shared/*
	sharedRateWindow  = time.Minute
	sharedMaxBody     = 64 << 10
	sharedFormType    = "application/x-www-form-urlencoded"
	sharedShopNameMax = 128                   // 下游要求 shopName 1–128 个字
	sharedStockReady  = acgshared.MaxQuantity // 就绪时报的库存：一次最多买这么多

	// 和 agentOrderNoKey 同一个键、同一段文字：同一个原因一天只报一次。
	sharedNoKeyAlertKey  = "agent-order-nokey"
	sharedNoKeyAlertText = "APP_MASTER_KEY 缺失或不合法：代理商暂时不能下单和取码（接口返回「系统维护中」）。请配置回原来的主密钥，换成新密钥的话已加密的码和两步验证密钥都解不开。"
)

var (
	sharedNow          = func() time.Time { return agentNow() } // 测试替换 agentNow 后跟着 e.clock 走
	sharedReadyTimeout = 10 * time.Second
	// 验签失败锁定：同一「商户 ID|IP」或（未知商户 ID 时）同一 IP，15 分钟内错 10 次锁 15 分钟。测试替换 .now
	sharedSignFails = newFailLimiter(10, 15*time.Minute, 20000)
)

// RegisterSharedRoutes 挂 /shared/*，挂在根引擎上（不在 /api/v1 下）。没挂的路径（draft、draftCard、query）是 404。
func RegisterSharedRoutes(r *gin.RouterGroup) {
	g := r.Group("/shared", SharedAuth())
	g.POST("/authentication/connect", SharedConnect)
	g.POST("/commodity/items", SharedItems)
	g.POST("/commodity/item", SharedItem)
	g.POST("/commodity/stock", SharedStock)
	g.POST("/commodity/valuation", SharedValuation)
	g.POST("/commodity/inventory", SharedInventory)
	g.POST("/commodity/inventoryState", SharedInventoryState)
	g.POST("/commodity/trade", SharedTrade)
}

// SharedAuth 是 /shared/* 的公共校验（规格 7.1），按顺序：
//  1. 只接受 HTTPS，只有本机直连（对端是回环且没有转发头，见 sharedLocalDirect）的放行；
//  2. 按来源 IP 限流，只收 form-urlencoded，请求体最多 64 KB；
//  3. 自己解析请求体（保留字段顺序）；
//  4. 主密钥可用（验签前要先用它解出商户密钥，所以从规格的第 8 步提到这里）；
//  5. 查商户 ID 并解出密钥，找不到按 IP 计一次验签失败；
//  6. 验签，错了按「商户 ID|IP」计失败，两种计数锁定期间都直接返回「密钥错误」、不再算签名；
//  7. IP 白名单；8. 管理员关闭、代理商停用；
//  9. 记最近使用时间，把代理商、对接设置、表单放进上下文。
func SharedAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		ctx := c.Request.Context()
		now := sharedNow()

		if !auth.RequestIsHTTPS(c.Request) && !sharedLocalDirect(c.Request) {
			sharedFail(c, sharedMsgHTTPS)
			return
		}
		ip := c.ClientIP()
		if !sharedRateAllow("ip|"+ip, sharedIPRateLimit, now) {
			sharedFail(c, sharedMsgRate)
			return
		}
		if mt, _, err := mime.ParseMediaType(c.GetHeader("Content-Type")); err != nil || mt != sharedFormType {
			sharedFail(c, sharedMsgBadForm)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, sharedMaxBody))
		if err != nil {
			sharedFail(c, sharedMsgBadForm)
			return
		}
		form, err := acgshared.ParseForm(body)
		if err != nil {
			sharedFail(c, sharedMsgBadForm)
			return
		}
		mk, err := secretbox.LoadKey()
		if err != nil {
			log.Printf("[acg-shared] 主密钥不可用: %s", secretErrKind(err))
			opsAlertDaily(sharedNoKeyAlertKey, sharedNoKeyAlertText, now)
			sharedFail(c, sharedMsgMaint)
			return
		}

		ipKey := "ip|" + ip
		if ok, _ := sharedSignFails.allowed(ipKey); !ok {
			sharedFail(c, sharedMsgBadSign)
			return
		}
		creds, key, found, err := sharedOpenCreds(ctx, form, mk)
		if err != nil {
			log.Printf("[acg-shared] 查商户 ID 失败: %v", err)
			sharedFail(c, sharedMsgBusy)
			return
		}
		if !found {
			sharedSignFails.failed(ipKey)
			sharedFail(c, sharedMsgNoApp)
			return
		}
		pairKey := creds.AppID + "|" + ip
		if ok, _ := sharedSignFails.allowed(pairKey); !ok {
			sharedFail(c, sharedMsgBadSign)
			return
		}
		if !acgshared.Verify(form, key) {
			sharedSignFails.failed(pairKey)
			log.Printf("[acg-shared] 商户 %s 签名错误: %s", creds.AppID, c.FullPath())
			sharedFail(c, sharedMsgBadSign)
			return
		}
		sharedSignFails.succeeded(pairKey)

		if len(creds.IPAllowlist) > 0 && !sharedIPAllowed(creds.IPAllowlist, ip) {
			sharedFail(c, sharedMsgIP)
			return
		}
		if creds.AdminDisabled {
			sharedFail(c, sharedMsgAdminOff)
			return
		}
		a, err := db.GetAgent(ctx, creds.AgentID)
		switch {
		case errors.Is(err, db.ErrAgentNotFound):
			sharedFail(c, sharedMsgNoApp)
			return
		case err != nil:
			log.Printf("[acg-shared] 商户 %s 读取代理商失败: %v", creds.AppID, err)
			sharedFail(c, sharedMsgBusy)
			return
		case a.Status != db.AgentActive:
			sharedFail(c, sharedMsgAgentOff)
			return
		}

		if err := db.TouchAgentSharedUsed(ctx, a.ID, now); err != nil {
			log.Printf("[acg-shared] 商户 %s 记录最近使用时间失败: %v", creds.AppID, err)
		}
		c.Set("agent_id", a.ID)
		c.Set("agent_username", a.Username)
		c.Set("acg_creds", creds)
		c.Set("acg_form", form)
		c.Next()
	}
}

// sharedForwardHeaders 是反代会加的转发头。带了任何一个（值为空也算），请求就不是本机直连。
var sharedForwardHeaders = []string{"X-Forwarded-Proto", "X-Forwarded-For", "Forwarded", "X-Real-IP"}

// sharedLocalDirect 判断是不是真正的本机直连（例如在服务器上用 curl 自测），只有这种请求能不走 HTTPS：
// TCP 对端（RemoteAddr，不看 ClientIP——代理头可以伪造成 127.0.0.1）是回环地址，并且没有任何转发头。
// 光看对端不够：原生包部署时 nginx、caddy 通常和本站在同一台机器上，经反代进来的请求对端全是 127.0.0.1，
// 反代把 80 端口的明文 HTTP 也转给 /shared、或者 Cloudflare Flexible 让源站这一段走明文时，请求会被静默接受。
// 下游默认在表单里明文发 app_key，HTTPS 检查是让「下游填了 http 地址」暴露出来的唯一手段，
// 所以带了转发头就只认 auth.RequestIsHTTPS（可信反代用 X-Forwarded-Proto: https 标明）。
func sharedLocalDirect(r *http.Request) bool {
	for _, h := range sharedForwardHeaders {
		if _, ok := r.Header[http.CanonicalHeaderKey(h)]; ok {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sharedKeyName 是商户密钥密文的 AAD：把密文绑定到这个代理商的这个商户 ID，搬到别的行解不开。
func sharedKeyName(agentID int64, appID string) string {
	return "acg-shared:" + strconv.FormatInt(agentID, 10) + ":" + appID
}

// sharedOpenCreds 按表单里的 app_id 取对接设置并解出商户密钥。
// app_id 不是标量、查不到、没有有效密钥、密钥解不开（换过主密钥）都返回 found=false；读库失败返回 err。
func sharedOpenCreds(ctx context.Context, form acgshared.Form, mk []byte) (db.AgentSharedCreds, string, bool, error) {
	id, ok := form.Get("app_id")
	if !ok || id == "" {
		return db.AgentSharedCreds{}, "", false, nil
	}
	creds, err := db.GetAgentSharedCredsByAppID(ctx, id)
	if errors.Is(err, db.ErrAgentSharedNotFound) {
		return db.AgentSharedCreds{}, "", false, nil
	}
	if err != nil {
		return db.AgentSharedCreds{}, "", false, err
	}
	if creds.KeySealed == "" {
		return db.AgentSharedCreds{}, "", false, nil
	}
	key, err := secretbox.Open(mk, sharedKeyName(creds.AgentID, creds.AppID), creds.KeySealed)
	if err != nil {
		log.Printf("[acg-shared] 商户 %s 的密钥解不开: %s", creds.AppID, secretErrKind(err))
		return db.AgentSharedCreds{}, "", false, nil
	}
	return creds, key, true, nil
}

// sharedIPAllowed 判断来源 IP 是否在白名单里：项是单个地址就比较相等，是网段就看是否包含。
// 两边都去掉 IPv4 映射（::ffff:1.2.3.4 当 1.2.3.4）和 IPv6 zone。解析不了的项跳过。
func sharedIPAllowed(allow []string, ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, item := range allow {
		item = strings.TrimSpace(item)
		if strings.Contains(item, "/") {
			if p, err := netip.ParsePrefix(item); err == nil && p.Contains(addr) {
				return true
			}
			continue
		}
		if a, err := netip.ParseAddr(item); err == nil && a.Unmap() == addr {
			return true
		}
	}
	return false
}

// ---- 响应和上下文 ----

// sharedOK 写成功响应：HTTP 200 {"code":200,"msg":"success","data":data}。
func sharedOK(c *gin.Context, data any) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"code": 200, "msg": "success", "data": data})
}

// sharedFail 写失败响应：HTTP 200 {"code":0,"msg":msg}，并中止后面的处理。
func sharedFail(c *gin.Context, msg string) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(http.StatusOK, gin.H{"code": 0, "msg": msg})
}

// sharedBusy 读写库失败：日志写商户 ID 和哪一步，响应「系统繁忙」。
func sharedBusy(c *gin.Context, op string, err error) {
	log.Printf("[acg-shared] 商户 %s %s失败: %v", sharedCredsOf(c).AppID, op, err)
	sharedFail(c, sharedMsgBusy)
}

// sharedForm 是 SharedAuth 解析好的表单。
func sharedForm(c *gin.Context) acgshared.Form {
	v, _ := c.Get("acg_form")
	f, _ := v.(acgshared.Form)
	return f
}

// sharedCredsOf 是验签通过的对接设置。
func sharedCredsOf(c *gin.Context) db.AgentSharedCreds {
	v, _ := c.Get("acg_creds")
	cr, _ := v.(db.AgentSharedCreds)
	return cr
}

// ---- 限流 ----

// sharedRateMaxKeys 是限流表最多记多少个键，满了淘汰窗口开始得最早的那个。测试可调小。
var sharedRateMaxKeys = 20000

type sharedRateEntry struct {
	key   string
	start time.Time
	count int
	elem  *list.Element
}

var (
	sharedRateMu   sync.Mutex
	sharedRateHits = map[string]*sharedRateEntry{}
	// sharedRateOrder 按窗口开始时间从早到晚排：新键放到队尾，窗口到期重开的也挪到队尾
	sharedRateOrder = list.New()
)

// sharedRateAllow 1 分钟固定窗口计数，key 在窗口内最多 limit 次。结构照 localRateAllow。
// 没有清理协程：每次调用先从队头删掉窗口已过的键（均摊 O(1)），表里只留最近一分钟有动静的键。
func sharedRateAllow(key string, limit int, now time.Time) bool {
	sharedRateMu.Lock()
	defer sharedRateMu.Unlock()
	for el := sharedRateOrder.Front(); el != nil; el = sharedRateOrder.Front() {
		w := el.Value.(*sharedRateEntry)
		if now.Sub(w.start) < sharedRateWindow {
			break
		}
		sharedRateOrder.Remove(el)
		delete(sharedRateHits, w.key)
	}
	w := sharedRateHits[key]
	switch {
	case w == nil:
		if len(sharedRateHits) >= sharedRateMaxKeys {
			if oldest := sharedRateOrder.Front(); oldest != nil {
				sharedRateOrder.Remove(oldest)
				delete(sharedRateHits, oldest.Value.(*sharedRateEntry).key)
			}
		}
		w = &sharedRateEntry{key: key, start: now, count: 1}
		w.elem = sharedRateOrder.PushBack(w)
		sharedRateHits[key] = w
		return true
	case now.Sub(w.start) >= sharedRateWindow: // 时钟往回拨过时队列不一定有序，这里兜底
		w.start, w.count = now, 1
		sharedRateOrder.MoveToBack(w.elem)
		return true
	case w.count >= limit:
		return false
	}
	w.count++
	return true
}

// resetSharedState 清空限流表和验签失败计数，给测试用。
func resetSharedState() {
	sharedRateMu.Lock()
	sharedRateHits = map[string]*sharedRateEntry{}
	sharedRateOrder = list.New()
	sharedRateMu.Unlock()
	sharedSignFails.reset()
}

// ---- 商品 ----

// sharedReady 带 sharedReadyTimeout 做就绪检查，超时按未就绪。
func sharedReady(ctx context.Context, p db.AgentProduct) bool {
	ctx, cancel := context.WithTimeout(ctx, sharedReadyTimeout)
	defer cancel()
	_, ok := agentProductReadyCtx(ctx, p)
	return ok
}

func sharedStock(ready bool) int {
	if ready {
		return sharedStockReady
	}
	return 0
}

// sharedLookupProduct 取商品和这个代理商的单价。不存在 →「商品不存在」；下架或取不到价 →「当前商品已停售」；
// 读库失败 →「系统繁忙」。失败时已写好响应，返回 false。
func sharedLookupProduct(c *gin.Context, code string) (db.AgentProduct, int64, bool) {
	ctx := c.Request.Context()
	p, err := db.GetAgentProductByCode(ctx, code)
	switch {
	case errors.Is(err, db.ErrAgentProductNotFound):
		sharedFail(c, sharedMsgNoProduct)
		return db.AgentProduct{}, 0, false
	case err != nil:
		sharedBusy(c, "读取商品", err)
		return db.AgentProduct{}, 0, false
	case !p.Enabled:
		sharedFail(c, sharedMsgOffSale)
		return db.AgentProduct{}, 0, false
	}
	price, priced, err := db.AgentPriceFor(ctx, agentID(c), p.ID)
	switch {
	case err != nil:
		sharedBusy(c, "取价", err)
		return db.AgentProduct{}, 0, false
	case !priced:
		sharedFail(c, sharedMsgOffSale)
		return db.AgentProduct{}, 0, false
	}
	return p, price, true
}

// sharedRejectSpec 我们的商品没有种类和规格：race 非空（含嵌套）或带 sku 时回「该商品没有可选规格」，返回 true。
func sharedRejectSpec(c *gin.Context) bool {
	f := sharedForm(c)
	race, scalar := f.Get("race")
	if f.Has("sku") || (f.Has("race") && (!scalar || race != "")) {
		sharedFail(c, sharedMsgNoSpec)
		return true
	}
	return false
}

func sharedItemOf(p db.AgentProduct, priceCents int64, ready bool) acgshared.Item {
	return acgshared.NewItem(acgshared.ItemInput{
		ID: p.ID, Code: p.Code, Name: p.Name, Sort: p.Sort, PriceCents: priceCents, Stock: sharedStock(ready),
	})
}

// ---- 只读接口 ----

// SharedConnect POST /shared/authentication/connect：店铺名（站点名 + 「（美元报价）」，最多 128 个字）和余额（美元）。
func SharedConnect(c *gin.Context) {
	a, err := db.GetAgent(c.Request.Context(), agentID(c))
	if err != nil {
		sharedBusy(c, "读取余额", err)
		return
	}
	sharedOK(c, gin.H{"shopName": sharedShopName(), "balance": acgshared.Money(max(0, a.BalanceCents))})
}

// sharedShopName 去掉站点名里的控制字符，截到能接上后缀的长度，再接「（美元报价）」。
func sharedShopName() string {
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, settingOr("brand_name", "Recharge Portal"))
	name = strings.TrimSpace(name)
	if limit := sharedShopNameMax - utf8.RuneCountInString(sharedShopSuffix); utf8.RuneCountInString(name) > limit {
		name = string([]rune(name)[:limit])
	}
	return name + sharedShopSuffix
}

// SharedItems POST /shared/commodity/items：一个「全部商品」分类，下面是这个代理商买得了的商品（已上架、取得到价格）。
// 整个接口共用一个 sharedReadyTimeout，同一个服务商 × 产品线 × 套餐 × 地区只查一次就绪，超时后剩下的按未就绪。
func SharedItems(c *gin.Context) {
	list, err := db.ListAgentPurchasable(c.Request.Context(), agentID(c))
	if err != nil {
		sharedBusy(c, "列出商品", err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), sharedReadyTimeout)
	defer cancel()
	ready := map[string]bool{}
	items := make([]acgshared.Item, 0, len(list))
	for _, p := range list {
		k := p.Provider + "|" + p.Product + "|" + p.PlanKey + "|" + p.Region
		ok, seen := ready[k]
		if !seen {
			_, ok = agentProductReadyCtx(ctx, p.AgentProduct)
			ready[k] = ok
		}
		items = append(items, sharedItemOf(p.AgentProduct, p.PriceCents, ok))
	}
	sharedOK(c, acgshared.AllCategory(items))
}

// SharedItem POST /shared/commodity/item：单个商品对象。商品编码先取 code，没有再取 sharedCode（3.1.x 下游只发后者）。
func SharedItem(c *gin.Context) {
	f := sharedForm(c)
	code, _ := f.Get("code")
	if code == "" {
		code, _ = f.Get("sharedCode")
	}
	p, price, ok := sharedLookupProduct(c, code)
	if !ok {
		return
	}
	sharedOK(c, sharedItemOf(p, price, sharedReady(c.Request.Context(), p)))
}

// SharedStock POST /shared/commodity/stock：{"stock": 就绪 ? 200 : 0}。
func SharedStock(c *gin.Context) {
	if sharedRejectSpec(c) {
		return
	}
	code, _ := sharedForm(c).Get("code")
	p, _, ok := sharedLookupProduct(c, code)
	if !ok {
		return
	}
	sharedOK(c, gin.H{"stock": sharedStock(sharedReady(c.Request.Context(), p))})
}

// SharedValuation POST /shared/commodity/valuation：这个数量的总价（美元）。
func SharedValuation(c *gin.Context) {
	if sharedRejectSpec(c) {
		return
	}
	f := sharedForm(c)
	numText, _ := f.Get("num")
	num, ok := acgshared.ParseQuantity(numText)
	if !ok {
		sharedFail(c, sharedMsgQty)
		return
	}
	code, _ := f.Get("code")
	_, price, ok := sharedLookupProduct(c, code)
	if !ok {
		return
	}
	sharedOK(c, gin.H{"price": acgshared.Money(price * int64(num)), "currency_code": "USD"})
}

// SharedInventory POST /shared/commodity/inventory：老下游在 stock、valuation 不可用时的降级接口。
func SharedInventory(c *gin.Context) {
	if sharedRejectSpec(c) {
		return
	}
	code, _ := sharedForm(c).Get("sharedCode")
	p, price, ok := sharedLookupProduct(c, code)
	if !ok {
		return
	}
	sharedOK(c, gin.H{
		"count": sharedStock(sharedReady(c.Request.Context(), p)), "delivery_way": 0, "draft_status": 0,
		"price": acgshared.Money(price), "user_price": 0, "config": "", "factory_price": acgshared.Money(price), "is_category": 0,
	})
}

// SharedInventoryState POST /shared/commodity/inventoryState：就绪且数量在 1–200 时成功（data 为空对象），否则「库存不足」。
func SharedInventoryState(c *gin.Context) {
	if sharedRejectSpec(c) {
		return
	}
	f := sharedForm(c)
	code, _ := f.Get("shared_code")
	p, _, ok := sharedLookupProduct(c, code)
	if !ok {
		return
	}
	numText, _ := f.Get("num")
	if _, ok := acgshared.ParseQuantity(numText); !ok || !sharedReady(c.Request.Context(), p) {
		sharedFail(c, sharedMsgNoStock)
		return
	}
	sharedOK(c, gin.H{})
}
