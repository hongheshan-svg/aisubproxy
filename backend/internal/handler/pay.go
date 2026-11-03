package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/paybrowser"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
)

// payEngine 为运行中的付款引擎；未启动时为 nil。
var payEngine *payengine.Service

// SetPayEngine 注入付款引擎（服务启动时调用；测试可传 nil）。
func SetPayEngine(s *payengine.Service) { payEngine = s }

// payRoute 包装付款接口：引擎未启动时统一返回 503。
func payRoute(fn func(c *gin.Context, s *payengine.Service)) gin.HandlerFunc {
	return func(c *gin.Context) {
		s := payEngine
		if s == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "付款引擎未启动"})
			return
		}
		fn(c, s)
	}
}

const (
	payBodyLimit      = 64 << 10
	payNoteAuditMax   = 200
	payBrowserTimeout = 60 * time.Second
)

var (
	payOrderNoRe  = regexp.MustCompile(`^PO\d{12}[0-9a-f]{8}$`)
	payShotNameRe = regexp.MustCompile(`^\d{2}-[a-z0-9_]+\.png$`)
)

// payFail 把引擎错误映射成 HTTP 响应。Resolve 的状态冲突同时满足 ErrBadRequest 与 ErrStateChanged，
// 所以 ErrStateChanged 必须排在 ErrBadRequest 之前判断，否则会被当成 400。
// 响应只给固定文案或不含输入值的引擎错误说明；会话错误一律只回「会话无效」。
func payFail(c *gin.Context, err error) {
	switch {
	case errors.Is(err, payengine.ErrStateChanged):
		c.JSON(http.StatusConflict, gin.H{"error": "单据状态已变化"})
	case errors.Is(err, payengine.ErrInflight): // 引擎错误里带账号邮箱，对外只给固定文案
		c.JSON(http.StatusConflict, gin.H{"error": payengine.ErrInflight.Error()})
	case errors.Is(err, payengine.ErrBadSession):
		c.JSON(http.StatusBadRequest, gin.H{"error": "会话无效"})
	case errors.Is(err, payengine.ErrPlanUnavailable), errors.Is(err, payengine.ErrBadRequest), errors.Is(err, payengine.ErrProviderUnavailable):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, payengine.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	default:
		log.Printf("payengine api %s %s: %v", c.Request.Method, c.FullPath(), err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "操作失败"})
	}
}

// bindPayJSON 解析请求体（上限 64KB）；失败回 400，不回显内容。
func bindPayJSON(c *gin.Context, dst any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, payBodyLimit)
	if err := c.ShouldBindJSON(dst); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
		return false
	}
	return true
}

// AdminPaySubmit 建一张付款单据（测试或真实付款，由 dry_run、dry_run_fill 决定）。session 可以是 JSON 字符串，也可以直接是会话对象。
// provider 是用哪家供卡型服务商的卡池，不填为 MarsWallex；必须是已注册的供卡型服务商。
func AdminPaySubmit(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		var req struct {
			Provider   string          `json:"provider"`
			PlanKey    string          `json:"plan_key"`
			Region     string          `json:"region"`
			Session    json.RawMessage `json:"session"`
			DryRun     bool            `json:"dry_run"`
			DryRunFill bool            `json:"dry_run_fill"`
		}
		if !bindPayJSON(c, &req) {
			return
		}
		session := strings.TrimSpace(string(req.Session))
		if strings.HasPrefix(session, `"`) {
			var str string
			if err := json.Unmarshal(req.Session, &str); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
				return
			}
			session = str
		}
		prov := provider.MarsWallex
		if v := strings.TrimSpace(req.Provider); v != "" {
			p, _ := provider.Get(provider.Name(v))
			cs, ok := provider.AsCardSupply(p)
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": "只能选已注册的供卡型服务商"})
				return
			}
			prov = cs.Name()
		}
		o, err := s.Submit(c.Request.Context(), payengine.Request{
			Provider:   string(prov),
			PlanKey:    strings.TrimSpace(req.PlanKey),
			Region:     strings.TrimSpace(req.Region),
			Session:    session,
			Source:     "admin_test",
			SourceRef:  adminName(c),
			DryRun:     req.DryRun,
			DryRunFill: req.DryRunFill,
		})
		if err != nil {
			payFail(c, err)
			return
		}
		auditAdmin(c, "pay_test_submit", fmt.Sprintf("provider=%s plan=%s region=%s dry_run=%t dry_run_fill=%t order_no=%s", o.Provider, o.PlanKey, o.Region, o.DryRun, o.DryRunFill, o.OrderNo))
		c.JSON(http.StatusOK, gin.H{"order_no": o.OrderNo, "order": o})
	})(c)
}

// AdminPayList 分页列出单据，可按 state 筛选。
func AdminPayList(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		page, _ := strconv.Atoi(c.Query("page"))
		size, _ := strconv.Atoi(c.Query("page_size"))
		orders, total, err := s.List(c.Request.Context(), strings.TrimSpace(c.Query("state")), page, size)
		if err != nil {
			payFail(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"orders": nonNil(orders), "total": total})
	})(c)
}

// AdminPayGet 返回单据、时间线与「提交前 5 分钟起该卡的交易」。交易查不到时不影响单据本身的展示。
func AdminPayGet(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		ctx := c.Request.Context()
		o, events, err := s.Get(ctx, c.Param("no"))
		if err != nil {
			payFail(c, err)
			return
		}
		resp := gin.H{"order": o, "events": nonNil(events)}
		ev, err := s.Evidence(ctx, o.OrderNo)
		if err != nil {
			log.Printf("payengine api evidence %s: %v", o.OrderNo, err)
			resp["evidence"] = []cardpool.Transaction{}
			resp["evidence_error"] = "交易记录暂时无法获取"
		} else {
			resp["evidence"] = nonNil(ev)
		}
		c.JSON(http.StatusOK, resp)
	})(c)
}

// AdminPayResolve 人工定论一张待人工的单据。
func AdminPayResolve(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		var req struct {
			Result string `json:"result"`
			Note   string `json:"note"`
		}
		if !bindPayJSON(c, &req) {
			return
		}
		no := c.Param("no")
		if err := s.Resolve(c.Request.Context(), no, strings.TrimSpace(req.Result), req.Note, adminName(c)); err != nil {
			payFail(c, err)
			return
		}
		note := []rune(strings.TrimSpace(req.Note))
		if len(note) > payNoteAuditMax {
			note = note[:payNoteAuditMax]
		}
		auditAdmin(c, "pay_order_resolve", fmt.Sprintf("order_no=%s result=%s note=%s", no, strings.TrimSpace(req.Result), string(note)))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})(c)
}

// AdminPayShot 返回某单的一张截图。单号与文件名先按正则校验，再确认是普通文件（不是符号链接、不是目录）。
func AdminPayShot(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		no, name := c.Param("no"), c.Param("name")
		if !payOrderNoRe.MatchString(no) || !payShotNameRe.MatchString(name) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "截图参数无效"})
			return
		}
		dir := filepath.Join(s.ShotDir(), no)
		if !isPlainDir(dir) || !isPlainFile(filepath.Join(dir, name)) {
			c.JSON(http.StatusNotFound, gin.H{"error": "截图不存在"})
			return
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": "截图不存在"})
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "image/png", b)
	})(c)
}

func isPlainFile(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// isPlainDir 判断 p 是真实目录（不是符号链接）。
func isPlainDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsDir()
}

// payConfigBody 组装 GET/PUT /config 的响应。租约提示：卡池租约短于一单最长占用时间时为 true。
func payConfigBody(s *payengine.Service) (gin.H, error) {
	plans, err := payengine.LoadPlanTable()
	if err != nil {
		return nil, err
	}
	pol, err := cardpool.LoadPolicy()
	if err != nil {
		log.Printf("payengine api: 读取卡池策略失败，租约提示按默认策略计算: %v", err)
	}
	maxPay := payengine.MaxPayDuration(s.Timeouts())
	return gin.H{
		"plans":           plans,
		"concurrency":     payengine.LoadConcurrency(),
		"lease_minutes":   pol.LeaseMinutes,
		"max_pay_minutes": int(math.Ceil(maxPay.Minutes())),
		"max_concurrency": payengine.MaxConcurrency,
		"lease_warning":   time.Duration(pol.LeaseMinutes)*time.Minute < maxPay,
	}, nil
}

func AdminPayGetConfig(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		body, err := payConfigBody(s)
		if err != nil {
			payFail(c, err)
			return
		}
		c.JSON(http.StatusOK, body)
	})(c)
}

// AdminPayPutConfig 保存套餐表与并发数。并发数便宜，先校验；再存套餐表（它自己先校验再写）；最后存并发数。
// 残余风险：套餐表存成功后并发数写库失败（数据库错误）时，套餐表已生效而并发数未变，管理员会看到 500 并可重试。
func AdminPayPutConfig(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		var req struct {
			Plans       []payengine.PlanConf `json:"plans"`
			Concurrency int                  `json:"concurrency"`
		}
		if !bindPayJSON(c, &req) {
			return
		}
		// 套餐表必须完整：缺失的套餐或地区会被整体覆盖掉，金额配置随之清零（未知键与重复键由 SavePlanTable 拒绝）
		complete := len(req.Plans) == len(payengine.PlanKeys)
		for _, p := range req.Plans {
			complete = complete && len(p.Regions) == len(payengine.Regions)
		}
		if !complete {
			c.JSON(http.StatusBadRequest, gin.H{"error": "套餐表不完整"})
			return
		}
		// 套餐名与期望类型为空时沿用默认值，免得误传空串清掉
		for i := range req.Plans {
			for _, d := range payengine.DefaultPlanTable() {
				if d.Key != req.Plans[i].Key {
					continue
				}
				if req.Plans[i].ChatGPTPlanName == "" {
					req.Plans[i].ChatGPTPlanName = d.ChatGPTPlanName
				}
				if req.Plans[i].ExpectPlanType == "" {
					req.Plans[i].ExpectPlanType = d.ExpectPlanType
				}
			}
		}
		if err := payengine.ValidateConcurrency(req.Concurrency); err != nil {
			payFail(c, err)
			return
		}
		if err := payengine.SavePlanTable(req.Plans); err != nil {
			payFail(c, err)
			return
		}
		if err := paySaveConcurrency(req.Concurrency); err != nil {
			// 套餐表已生效而并发数没存上：补一条审计，管理员可重试
			auditAdmin(c, "pay_config_update", payConfigAudit(req.Plans, req.Concurrency)+" 套餐表已保存，并发数保存失败")
			payFail(c, err)
			return
		}
		auditAdmin(c, "pay_config_update", payConfigAudit(req.Plans, req.Concurrency))
		body, err := payConfigBody(s)
		if err != nil {
			payFail(c, err)
			return
		}
		c.JSON(http.StatusOK, body)
	})(c)
}

// payConfigAudit 列出启用的「套餐:地区」与并发数。
func payConfigAudit(plans []payengine.PlanConf, concurrency int) string {
	var on []string
	for _, p := range plans {
		var regions []string
		for r, rc := range p.Regions {
			if rc.Enabled {
				regions = append(regions, r)
			}
		}
		sort.Strings(regions)
		for _, r := range regions {
			on = append(on, p.Key+":"+r)
		}
	}
	return fmt.Sprintf("enabled=[%s] concurrency=%d", strings.Join(on, ","), concurrency)
}

func AdminPayGetProxies(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		st, err := payengine.ProxyStatus()
		if err != nil {
			payFail(c, err)
			return
		}
		c.JSON(http.StatusOK, st)
	})(c)
}

// AdminPayPutProxies 合并保存地区代理（空串不变、"-" 清除）。响应与审计都只给地区与是否已设置，不含地址。
func AdminPayPutProxies(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		var req map[string]string
		if !bindPayJSON(c, &req) {
			return
		}
		if len(req) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "请求体无效"})
			return
		}
		if err := payengine.SaveProxies(req); err != nil {
			payFail(c, err)
			return
		}
		var parts []string
		for r, v := range req {
			switch v {
			case "":
			case "-":
				parts = append(parts, r+":已清空")
			default:
				parts = append(parts, r+":已设置")
			}
		}
		sort.Strings(parts)
		auditAdmin(c, "pay_proxies_update", strings.Join(parts, " "))
		st, err := payengine.ProxyStatus()
		if err != nil {
			payFail(c, err)
			return
		}
		c.JSON(http.StatusOK, st)
	})(c)
}

var paySaveConcurrency = payengine.SaveConcurrency

var (
	payBrowserCheck = paybrowser.Check
	payCheckMu      sync.Mutex
)

// AdminPayBrowserCheck 启动一次浏览器自检；同一时间只允许一个在跑。
func AdminPayBrowserCheck(c *gin.Context) {
	payRoute(func(c *gin.Context, s *payengine.Service) {
		if !payCheckMu.TryLock() {
			c.JSON(http.StatusConflict, gin.H{"error": "已有检查在进行"})
			return
		}
		defer payCheckMu.Unlock()
		ctx, cancel := context.WithTimeout(c.Request.Context(), payBrowserTimeout)
		defer cancel()
		c.JSON(http.StatusOK, payBrowserCheck(ctx))
	})(c)
}
