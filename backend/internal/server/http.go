package server

import (
	"context"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/tuzi/cdk-recharge-system/internal/cardpool"
	"github.com/tuzi/cdk-recharge-system/internal/config"
	"github.com/tuzi/cdk-recharge-system/internal/db"
	"github.com/tuzi/cdk-recharge-system/internal/handler"
	"github.com/tuzi/cdk-recharge-system/internal/notify"
	"github.com/tuzi/cdk-recharge-system/internal/payengine"
	"github.com/tuzi/cdk-recharge-system/internal/plansync"
	"github.com/tuzi/cdk-recharge-system/internal/provider"
	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

type Server struct {
	engine *gin.Engine
	cfg    *config.Config
	pay    drainer // 付款引擎：停止服务时先排空在途单

	mu   sync.Mutex
	http *http.Server // Serve 开始后才有
}

func New(ctx context.Context, cfg *config.Config) (*Server, error) {
	// Initialize database
	if err := db.Init(&cfg.Database); err != nil {
		return nil, err
	}
	// 卡台 API Key、webhook 密钥：旧版明文加密迁移；webhook 原文里的卡号、CVV、有效期清洗。失败就不启动，不静默留着明文
	if err := db.MigrateCardSecrets(); err != nil {
		return nil, fmt.Errorf("卡台密钥迁移: %w", err)
	}
	if err := db.MigrateWebhookPayloads(); err != nil {
		return nil, fmt.Errorf("webhook 原文清洗: %w", err)
	}
	// Telegram 告警：后台配置优先，没配就用环境变量；旧版明文保存的令牌先加密迁移
	handler.MigrateLegacyTelegramToken()
	notify.SetSettingsSource(handler.TelegramSettings)
	// 后台协程 panic 被兜住后的告警、没有自己告警通道的模块（如付款浏览器）的告警，都走这里发到 Telegram
	safego.SetAlert(alertSender("system", notify.SendText))

	// 生产模式、或已配置动钱上游密钥时，强制要求安全的 JWT secret，避免 token 被伪造后调用动钱接口
	upstream, err := upstreamKeysConfigured()
	if err != nil {
		return nil, err
	}
	if err := enforceJWTSecret(cfg.Server.Mode, upstream); err != nil {
		return nil, err
	}

	engine := newEngine()

	// 不信任任意代理头（X-Forwarded-For 等），保证限流拿到的是真实来源 IP。
	// 若部署在已知反代后，用 TRUSTED_PROXIES（逗号分隔）显式声明。
	if tp := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")); tp != "" {
		_ = engine.SetTrustedProxies(splitAndTrim(tp))
	} else {
		_ = engine.SetTrustedProxies(nil)
	}

	// Setup middleware
	engine.Use(SecurityHeadersMiddleware())
	engine.Use(CORSMiddleware())

	// MarsWallex 卡池与付款引擎先建好并注入 handler，再注册路由；付款引擎依赖卡池，所以排在其后
	pool := cardpool.New(cardPoolOptions(notify.SendText))
	handler.SetCardPool(pool)
	pools, err := payPools(pool)
	if err != nil {
		return nil, err
	}
	// 每家注册了的服务商都要接上 handler 层的适配器，否则它的码会被分派过来却没人接
	if err := handler.CheckProviderAdapters(); err != nil {
		return nil, err
	}
	pay := payengine.New(payengine.Options{Pools: pools, Alert: alertSender("payengine", notify.SendText)})
	handler.SetPayEngine(pay)

	// Setup routes
	setupRoutes(engine)

	// 启动卡台产品状态后台同步（每3分钟）
	plansync.Start(ctx)

	// 启动 MarsWallex 卡池（对账、回收、补卡、Webhook 事件处理）
	pool.Start(ctx)
	// 启动付款引擎：Start 内含同步的重启恢复，这里返回后 HTTP 才会开始监听（Run 在 New 之后）
	pay.Start(ctx)

	// CDK 链路后台维护：清理到期或已完成订单的用户会话
	handler.SetOpsAlert(alertSender("cdk", notify.SendText))
	handler.StartOpsMaintenance(ctx)
	handler.StartLocalRateSweep(ctx)
	handler.StartCardHeadSync(ctx)

	return &Server{
		engine: engine,
		cfg:    cfg,
		pay:    pay,
	}, nil
}

// alertLabels 是发到 Telegram 时每条告警开头的模块名，和 Claude 的回复等其他消息区分开。
var alertLabels = map[string]string{
	"payengine": "付款",
	"cardpool":  "卡池",
	"cdk":       "CDK",
	"system":    "系统",
}

// alertSender 包装模块告警：先写日志（前缀标明模块；Telegram 未配置时 SendText 静默返回，告警不能因此丢失），
// 再 HTML 转义、加上「【模块】」前缀后发送。告警里有平台错误信息、商户名、持卡人备注等不受控文本，SendText 用
// parse_mode=HTML，未转义的 < 或 & 会让 Telegram 拒收整条消息。告警文本不含卡号、CVV2、验证码。
func alertSender(prefix string, send func(string)) func(string) {
	label := alertLabels[prefix]
	if label == "" {
		label = prefix
	}
	return func(text string) {
		log.Printf("[%s] 告警: %s", prefix, text)
		send("【" + html.EscapeString(label) + "】" + html.EscapeString(text))
	}
}

// cardPoolAlert 是卡池的告警函数。
func cardPoolAlert(send func(string)) func(string) { return alertSender("cardpool", send) }

// cardPoolOptions 是卡池的启动参数。补卡闸的判定在这里接上：漏接就等于 MarsWallex 没选用时照样开卡花钱，有测试钉着。
func cardPoolOptions(send func(string)) cardpool.Options {
	return cardpool.Options{
		Client:             handler.MarsWallexClient,
		Alert:              cardPoolAlert(send),
		MarsWallexSelected: provider.MarsWallexSelected,
	}
}

// payPools 是付款引擎按服务商取的卡池。每家注册了的供卡型服务商都必须在这里有卡池，漏了就拒绝启动：
// 否则它的码能发、能兑换，付款时才发现没有卡池。目前只有 MarsWallex 一家，用的是本站的 MarsWallex 卡池。
func payPools(mw payengine.Pool) (map[string]payengine.Pool, error) {
	pools := map[string]payengine.Pool{string(provider.MarsWallex): mw}
	for _, p := range provider.List() {
		if _, ok := provider.AsCardSupply(p); ok && pools[string(p.Name())] == nil {
			return nil, fmt.Errorf("供卡型服务商 %s 没有接入卡池", p.Name())
		}
	}
	return pools, nil
}

// upstreamKeysConfigured 判断库里是否已配置 MarsWallex 或卡台 API Key（动钱上游）。
// 只看是否非空，不解密、不记录内容。
func upstreamKeysConfigured() (bool, error) {
	for _, key := range []string{db.SettingMarsWallexDeveloperID, db.SettingMarsWallexAPIToken, "card_api_key"} {
		v, err := db.GetSetting(key)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(v) != "" {
			return true, nil
		}
	}
	return false, nil
}

// enforceJWTSecret 拒绝使用空/默认/过短的 JWT 密钥启动：release 模式总是拒绝；
// 非 release 模式下，只要已配置动钱上游密钥（hasUpstream）也拒绝，防止公开默认密钥伪造管理员 token 调用动钱接口。
func enforceJWTSecret(mode string, hasUpstream bool) error {
	secret := os.Getenv("JWT_SECRET")
	weak := secret == "" ||
		secret == "your-secret-key-change-in-production" ||
		secret == "dev-secret-key-change-in-production" ||
		len(secret) < 16
	if mode == "release" && weak {
		return fmt.Errorf("不安全的 JWT_SECRET：release 模式必须设置至少 16 位的随机 JWT_SECRET 环境变量")
	}
	if hasUpstream && weak {
		return fmt.Errorf("不安全的 JWT_SECRET：已配置 MarsWallex 或卡台 API Key，必须设置至少 16 位的随机 JWT_SECRET 环境变量")
	}
	if weak {
		log.Printf("⚠️  当前 JWT_SECRET 不安全（默认/过短）。仅可用于本地开发，部署前请设置强随机 JWT_SECRET（release 模式将拒绝启动）。")
	}
	return nil
}

func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func setupRoutes(r *gin.Engine) {
	webDir := strings.TrimSpace(os.Getenv("WEB_DIR"))

	// Root route for quick manual checks in browser (仅当未托管前端时)
	if webDir == "" {
		r.GET("/", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"status":   "ok",
				"message":  "Recharge System backend is running",
				"health":   "/health",
				"api_base": "/api/v1",
			})
		})
	}

	// Health check
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":  "ok",
			"message": "Recharge System is running",
		})
	})

	// API v1 routes
	api := r.Group("/api/v1")
	{
		api.GET("", func(c *gin.Context) {
			c.JSON(200, gin.H{
				"status":  "ok",
				"message": "API v1 is available",
			})
		})

		auth := api.Group("/auth")
		{
			auth.POST("/admin/login", handler.AdminLogin)
			// 登出也要有效会话，cookie 会话还要过 CSRF：第三方页面不能随意把管理员登出（审计 A-13）
			auth.POST("/admin/logout", JWTAuthMiddleware(), handler.AdminLogout)
			auth.GET("/admin/me", JWTAuthMiddleware(), handler.AdminMe)
			auth.POST("/admin/change-password", JWTAuthMiddleware(), AdminAuthMiddleware(), handler.AdminChangePassword)
		}

		// 首次安装向导（仅 pending；完成后 410）
		setup := api.Group("/setup")
		{
			setup.GET("/status", handler.SetupStatus)
			setup.POST("/bootstrap", handler.SetupBootstrap)
		}

		// 公开接口（/public、/lookup）：先按 IP 限流，再读请求体（上限 256 KB），见 handler.PublicGuard
		public := api.Group("/public", handler.PublicGuard())

		// 公开站点配置（品牌/皮肤，无密钥）
		public.GET("/site", handler.PublicSiteConfig)

		// 卡台 CDK 公开兑换 BFF + 实时服务费展示
		pubCDK := public.Group("/cdk")
		{
			pubCDK.GET("/plans", handler.PublicCDKPlans)
			pubCDK.POST("/preview", handler.PublicCDKPreview)
			pubCDK.POST("/preflight", handler.PublicCDKPreflight)
			pubCDK.POST("/recover-subscription", handler.PublicCDKGraceRecovery)
			pubCDK.POST("/redeem", handler.PublicCDKRedeem)
			pubCDK.GET("/result", handler.PublicCDKResult)
			// 刷新进度 / 任务查询：凭卡密反查本站绑定的 redemption_token
			pubCDK.GET("/result-by-code", handler.PublicCDKResultByCode)
			// 代理隐藏换码：密码 + 失败未扣款 CDK → 新码
			pubCDK.POST("/exchange", handler.PublicAgentCDKExchange)
		}

		// 卡密状态查询：是否已用 + 充值邮箱（不返回 token）
		lookup := api.Group("/lookup", handler.LookupGuard())
		{
			lookup.GET("/cdk", handler.LookupCDKStatus)
			lookup.POST("/cdk/batch", handler.LookupCDKStatusBatch)
			// 兼容旧路径
			lookup.GET("/task", handler.LookupCDKStatus)
		}

		// 卡台 Webhook（须在卡台开发者页配置 https://你的域名/api/v1/webhooks/cardplatform）
		api.POST("/webhooks/cardplatform", handler.CardPlatformWebhook)
		// MarsWallex Webhook（须在 MarsWallex 后台配置 https://你的域名/api/v1/webhooks/marswallex）
		api.POST("/webhooks/marswallex", handler.MarsWallexWebhook)

		// 账单：粘贴 session 查 ChatGPT 订阅 + hosted_invoice（小助手同款）
		public.POST("/billing/check", handler.SessionBillingCheck)
		// 旧路径：和 /public 下的一样先限流、再读请求体（同一个额度）
		api.POST("/billing/check", handler.PublicGuard(), handler.SessionBillingCheck)

		// Stats routes
		stats := api.Group("/stats")
		stats.Use(JWTAuthMiddleware(), AdminAuthMiddleware())
		{
			stats.GET("/system", handler.GetSystemStats)
		}

		// Admin routes
		admin := api.Group("/admin")
		admin.Use(JWTAuthMiddleware())
		admin.Use(AdminAuthMiddleware())
		{
			// 版本：本机 VERSION + GitHub 最新 release/tag
			admin.GET("/system/version", handler.AdminSystemVersion)
			admin.GET("/system/update/status", handler.AdminSystemUpdateStatus)
			admin.POST("/system/update", handler.AdminSystemUpdate)

			// MarsWallex 对接：配置 / 测试连接 / Webhook 事件
			admin.GET("/marswallex/config", handler.AdminGetMarsWallexConfig)
			admin.PUT("/marswallex/config", handler.AdminPutMarsWallexConfig)
			admin.POST("/marswallex/test", handler.AdminTestMarsWallex)
			admin.GET("/marswallex/egress", handler.AdminMarsWallexEgress)
			admin.POST("/marswallex/keypair", handler.AdminGenerateMarsWallexKeypair)
			admin.GET("/marswallex/webhook-events", handler.AdminListMarsWallexWebhookEvents)
			admin.POST("/marswallex/webhook-events/:id/reprocess", handler.AdminReprocessMarsWallexWebhookEvent)

			// MarsWallex 卡池
			admin.GET("/cardpool/overview", handler.AdminCardPoolOverview)
			admin.GET("/cardpool/products", handler.AdminCardPoolProducts)
			admin.GET("/cardpool/cardholders", handler.AdminCardPoolCardholders)
			admin.POST("/cardpool/cardholders/import", handler.AdminCardPoolImportCardholder)
			admin.POST("/cardpool/cardholders/:id/refresh", handler.AdminCardPoolRefreshCardholder)
			admin.GET("/cardpool/cards", handler.AdminCardPoolCards)
			admin.GET("/cardpool/cards/:id", handler.AdminCardPoolCardDetail)
			admin.POST("/cardpool/cards/apply", handler.AdminCardPoolApplyCards)
			admin.POST("/cardpool/cards/import", handler.AdminCardPoolImportCard)
			admin.POST("/cardpool/cards/preview", handler.AdminCardPoolPreviewCard)
			admin.POST("/cardpool/cards/:id/refresh", handler.AdminCardPoolRefreshCard)
			admin.PUT("/cardpool/cards/:id/limit", handler.AdminCardPoolSetCardLimit)
			admin.GET("/cardpool/cards/:id/limit-usage", handler.AdminCardPoolCardLimitUsage)
			admin.POST("/cardpool/cards/:id/disable", handler.AdminCardPoolDisableCard)
			admin.POST("/cardpool/cards/:id/enable", handler.AdminCardPoolEnableCard)
			admin.POST("/cardpool/cards/:id/freeze", handler.AdminCardPoolFreezeCard)
			admin.POST("/cardpool/cards/:id/unfreeze", handler.AdminCardPoolUnfreezeCard)
			admin.POST("/cardpool/cards/:id/cancel", handler.AdminCardPoolCancelCard)
			admin.POST("/cardpool/cards/:id/sweep", handler.AdminCardPoolSweepCard)
			admin.POST("/cardpool/cards/:id/lease", handler.AdminCardPoolLeaseCard)
			admin.POST("/cardpool/cards/:id/release", handler.AdminCardPoolReleaseCard)
			admin.POST("/cardpool/cards/:id/fund", handler.AdminCardPoolFundCard)
			admin.POST("/cardpool/cards/:id/sensitive", handler.AdminCardPoolSensitive)
			admin.POST("/cardpool/cards/:id/otp", handler.AdminCardPoolOTP)
			admin.GET("/cardpool/orders", handler.AdminCardPoolOrders)
			admin.POST("/cardpool/orders/:no/recheck", handler.AdminCardPoolRecheck)
			admin.POST("/cardpool/orders/:no/confirm-failed", handler.AdminCardPoolConfirmFailed)
			admin.GET("/cardpool/transactions", handler.AdminCardPoolTransactions)
			admin.GET("/cardpool/policy", handler.AdminCardPoolGetPolicy)
			admin.PUT("/cardpool/policy", handler.AdminCardPoolPutPolicy)
			admin.GET("/cardpool/selection", handler.AdminCardPoolGetSelection)
			admin.PUT("/cardpool/selection", handler.AdminCardPoolPutSelection)
			admin.GET("/cardpool/health", handler.AdminCardPoolHealth)
			admin.POST("/cardpool/health/resume", handler.AdminCardPoolResume)
			// MarsWallex 钱包：账户、收款地址、流水、兑换、钱包转账
			admin.GET("/cardpool/wallet/accounts", handler.AdminCardPoolAccounts)
			admin.POST("/cardpool/wallet/accounts", handler.AdminCardPoolCreateAccount)
			admin.POST("/cardpool/wallet/accounts/:no/status", handler.AdminCardPoolAccountStatus)
			admin.GET("/cardpool/wallet/receiving-addresses", handler.AdminCardPoolReceivingAddresses)
			admin.GET("/cardpool/wallet/serials", handler.AdminCardPoolSerials)
			admin.POST("/cardpool/wallet/conversions", handler.AdminCardPoolConversion)
			admin.POST("/cardpool/wallet/transfers", handler.AdminCardPoolWalletTransfer)
			// 持卡人新建、修改、启停与提交记录（资料不落地、不记日志）
			admin.POST("/cardpool/cardholders/photos", handler.AdminCardPoolUploadPhoto)
			admin.POST("/cardpool/cardholders", handler.AdminCardPoolCreateCardholder)
			admin.GET("/cardpool/cardholders/:id/profile", handler.AdminCardPoolCardholderProfile)
			admin.PUT("/cardpool/cardholders/:id", handler.AdminCardPoolUpdateCardholder)
			admin.POST("/cardpool/cardholders/:id/status", handler.AdminCardPoolCardholderStatus)
			admin.GET("/cardpool/cardholder-submissions", handler.AdminCardPoolSubmissions)
			admin.POST("/cardpool/cardholder-submissions/:out_id/abandon", handler.AdminCardPoolAbandonSubmission)
			// 交易补拉与卡转入转出明细
			admin.GET("/cardpool/backfill", handler.AdminCardPoolBackfillStatus)
			admin.POST("/cardpool/backfill", handler.AdminCardPoolBackfill)
			admin.GET("/cardpool/cards/:id/balance-transactions", handler.AdminCardPoolCardTransfers)

			// MarsWallex 兑换（付款引擎）：建单、单据、人工定论、截图、配置、代理、浏览器自检
			admin.POST("/pay/orders", handler.AdminPaySubmit)
			admin.GET("/pay/orders", handler.AdminPayList)
			admin.GET("/pay/orders/:no", handler.AdminPayGet)
			admin.POST("/pay/orders/:no/resolve", handler.AdminPayResolve)
			admin.POST("/pay/orders/:no/renewal/retry", handler.AdminPayRetryRenewal)
			admin.GET("/pay/orders/:no/shots/:name", handler.AdminPayShot)
			admin.GET("/pay/config", handler.AdminPayGetConfig)
			admin.PUT("/pay/config", handler.AdminPayPutConfig)
			admin.GET("/pay/proxies", handler.AdminPayGetProxies)
			admin.PUT("/pay/proxies", handler.AdminPayPutProxies)
			admin.POST("/pay/proxies/test", handler.AdminPayTestProxy)
			admin.POST("/pay/browser-check", handler.AdminPayBrowserCheck)
			admin.GET("/pay/breaker", handler.AdminPayBreakerStatus)
			admin.POST("/pay/breaker/resume", handler.AdminPayBreakerResume)
			handler.RegisterPayProbeRoutes(admin)

			// 卡台 Open API：实时价格 / 余额 / 发码 / 列码 / CDK 订单
			admin.GET("/cardplatform/ping", handler.CardPlatformPing)
			admin.GET("/cardplatform/plans", handler.CardPlatformPlans)
			admin.GET("/cardplatform/balance", handler.CardPlatformBalance)
			admin.GET("/cardplatform/cdks", handler.CardPlatformListCDKs)
			admin.GET("/cardplatform/cdks/stored", handler.CardPlatformListStoredCDKs)
			admin.POST("/cardplatform/cdks", handler.CardPlatformIssueCDKs)
			// 卡服务商：列表与配置、默认发码服务商（旧接口）、就绪检查、本地码
			admin.GET("/providers", handler.AdminListProviders)
			admin.PUT("/providers/config", handler.AdminPutProviderConfig)
			admin.GET("/providers/default", handler.AdminGetDefaultProvider)
			admin.PUT("/providers/default", handler.AdminPutDefaultProvider)
			admin.GET("/providers/readiness", handler.AdminProviderReadiness)
			// 各服务商的卡头：列表、同步、标注、手动维护
			admin.GET("/providers/:name/card-heads", handler.AdminListCardHeads)
			admin.POST("/providers/:name/card-heads", handler.AdminAddCardHead)
			admin.POST("/providers/:name/card-heads/sync", handler.AdminSyncCardHeads)
			admin.PUT("/providers/:name/card-heads/:product_id", handler.AdminUpdateCardHead)
			admin.DELETE("/providers/:name/card-heads/:product_id", handler.AdminDeleteCardHead)
			admin.POST("/cdks/issue", handler.CardPlatformIssueCDKs)
			admin.GET("/cdks", handler.AdminListLocalCDKs)
			admin.POST("/cdks/:id/disable", handler.AdminDisableLocalCDK)
			admin.POST("/cdks/:id/enable", handler.AdminEnableLocalCDK)
			admin.POST("/cardplatform/cdks/sync", handler.CardPlatformSyncUpstreamCDKs)
			admin.POST("/cardplatform/cdks/store", handler.CardPlatformStoreCDKCodes)
			admin.POST("/cardplatform/cdks/batch-disable", handler.CardPlatformBatchDisableCDKs)
			admin.POST("/cardplatform/cdks/batch-enable", handler.CardPlatformBatchEnableCDKs)
			admin.POST("/cardplatform/cdks/batch-note", handler.CardPlatformBatchSetCDKNote)
			admin.POST("/cardplatform/cdks/batch-clear-note", handler.CardPlatformBatchClearCDKNote)
			admin.PUT("/cardplatform/cdks/:id/note", handler.CardPlatformSetCDKNote)
			admin.POST("/cardplatform/cdks/:id/disable", handler.CardPlatformDisableCDK)
			admin.POST("/cardplatform/cdks/:id/enable", handler.CardPlatformEnableCDK)
			admin.GET("/cardplatform/agent-exchanges", handler.AdminListAgentExchanges)
			admin.POST("/cardplatform/agent-exchanges/:id/resolve", handler.AdminResolveAgentExchange)
			admin.GET("/cardplatform/cdk-orders", handler.CardPlatformListCDKOrders)
			admin.GET("/cardplatform/cdk-orders/:id", handler.CardPlatformGetCDKOrder)
			admin.DELETE("/cardplatform/cards/:id", handler.CardPlatformDeleteCard)

			// Webhook 事件列表 + 配置提示
			admin.GET("/webhooks/events", handler.AdminListWebhooks)

			// 本机出口 IP（卡台 API 白名单）
			admin.GET("/network/egress", handler.NetworkEgress)

			// Audit logs
			admin.GET("/audit-logs", handler.ListAuditLogs)

			// 站点设置（品牌/皮肤/卡台密钥保险箱）
			admin.GET("/settings", handler.AdminGetSettings)
			admin.PUT("/settings", handler.AdminPutSettings)
			// Telegram 告警：令牌加密保存、只写不读；测试消息同步发送并返回结果
			admin.GET("/notify/telegram", handler.AdminGetTelegram)
			admin.PUT("/notify/telegram", handler.AdminPutTelegram)
			admin.POST("/notify/telegram/test", handler.AdminTestTelegram)

			// 自动选卡权重配置
			admin.GET("/card-selection/rules", handler.AdminGetCardSelectionRules)
			admin.PUT("/card-selection/rules", handler.AdminPutCardSelectionRules)
			admin.GET("/card-selection/plan-status", handler.AdminGetPlanStatus)
			admin.POST("/card-selection/sync", handler.AdminSyncPlanStatus)
			admin.GET("/card-selection/site-policy", handler.AdminGetSiteRedeemPolicy)
			admin.PUT("/card-selection/site-policy", handler.AdminPutSiteRedeemPolicy)

			// 卡健康：同卡失败 × 邮箱归因 → 坏卡冻结
			admin.GET("/card-health", handler.AdminListCardHealth)
			admin.GET("/card-health/policy", handler.AdminGetCardHealthPolicy)
			admin.PUT("/card-health/policy", handler.AdminPutCardHealthPolicy)
			admin.POST("/card-health/unblock", handler.AdminUnblockCard)
			admin.POST("/card-health/observe", handler.AdminReobserveCardOrder)
		}
	}

	// 托管前端 SPA（当设置了 WEB_DIR 时）：真实存在的文件直出，其余回退到 index.html
	if webDir != "" {
		indexFile := filepath.Join(webDir, "index.html")
		r.NoRoute(func(c *gin.Context) {
			p := c.Request.URL.Path
			if strings.HasPrefix(p, "/api") || strings.HasPrefix(p, "/health") {
				c.JSON(404, gin.H{"error": "not found"})
				return
			}
			if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
				c.JSON(404, gin.H{"error": "not found"})
				return
			}
			// 防目录穿越：path.Clean 后再拼接，确保不越出 webDir
			clean := path.Clean("/" + strings.TrimPrefix(p, "/"))
			if clean != "/" {
				fp := filepath.Join(webDir, filepath.FromSlash(clean))
				if st, err := os.Stat(fp); err == nil && !st.IsDir() {
					c.File(fp)
					return
				}
			}
			c.File(indexFile)
		})
	}
}

func (s *Server) Run(addr string) error {
	return s.engine.Run(addr)
}
