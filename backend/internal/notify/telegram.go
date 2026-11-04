package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tuzi/cdk-recharge-system/internal/safego"
)

// 配置来源：后台配置优先，没配就用环境变量。
const (
	SourceSettings = "settings" // 后台「Telegram 告警」页保存的
	SourceEnv      = "env"      // 环境变量 TELEGRAM_BOT_TOKEN、TELEGRAM_CHAT_ID
	SourceNone     = "none"
)

// Config 是一次发送用的配置。Token 是机器人令牌，绝不能写进日志、审计或接口返回。
type Config struct {
	Token  string
	ChatID string
	Source string
}

// Ready 表示令牌和 chat id 都有。
func (c Config) Ready() bool { return c.Token != "" && c.ChatID != "" }

// maxTextLen 是一条消息的最大长度（Telegram 上限 4096 字符），超出部分截掉。
const maxTextLen = 4000

// sendTimeout 是一次发送（含一次限流重试）的总时限。
const sendTimeout = 30 * time.Second

// maxRetryAfter 是被限流（429）时最多等多久再重试；Telegram 要求等更久就不重试，只记日志。
const maxRetryAfter = 20 * time.Second

// ProxyEnv 是只给 Telegram 用的代理（http://、https://、socks5://），不影响其他接口的请求。
// 没设时沿用标准的 HTTPS_PROXY 等环境变量。
const ProxyEnv = "TELEGRAM_PROXY"

var (
	httpClient = &http.Client{Timeout: 10 * time.Second}

	proxyMu     sync.Mutex
	proxyKey    string
	proxyClient *http.Client

	mu sync.RWMutex
	// apiBase 测试时换成本地假服务
	apiBase = "https://api.telegram.org"
	// settingsSource 读后台保存的配置（服务启动时由 SetSettingsSource 注入）；返回空串表示没配
	settingsSource func() (token, chatID string, err error)
)

// SetSettingsSource 注入读取后台配置的函数。nil 表示只用环境变量。
func SetSettingsSource(fn func() (token, chatID string, err error)) {
	mu.Lock()
	settingsSource = fn
	mu.Unlock()
}

// SetAPIBase 改 Telegram API 地址，只给测试用；返回恢复原值的函数。
func SetAPIBase(base string) (restore func()) {
	mu.Lock()
	old := apiBase
	apiBase = strings.TrimRight(base, "/")
	mu.Unlock()
	return func() {
		mu.Lock()
		apiBase = old
		mu.Unlock()
	}
}

// EnvConfig 是环境变量里的配置（没配则为空）。
func EnvConfig() Config {
	c := Config{Token: strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")), ChatID: strings.TrimSpace(os.Getenv("TELEGRAM_CHAT_ID"))}
	if c.Ready() {
		c.Source = SourceEnv
	}
	return c
}

// Current 返回当前生效的配置：后台配置（令牌和 chat id 都有）优先，否则环境变量，都没有则 Source 为 none。
func Current() Config {
	mu.RLock()
	src := settingsSource
	mu.RUnlock()
	if src != nil {
		token, chatID, err := src()
		if err != nil {
			// 读不出后台配置（例如 APP_MASTER_KEY 没配或换了）：退回环境变量，并记一笔，免得告警悄悄丢失
			log.Printf("[telegram] 读取后台配置失败，改用环境变量: %v", err)
		} else if c := (Config{Token: strings.TrimSpace(token), ChatID: strings.TrimSpace(chatID), Source: SourceSettings}); c.Ready() {
			return c
		}
	}
	if c := EnvConfig(); c.Ready() {
		return c
	}
	return Config{Source: SourceNone}
}

// SendText 异步发一条消息（text 已按 HTML 转义）；没配置时什么也不做。失败只记日志，日志里不含令牌。
func SendText(text string) {
	cfg := Current()
	if !cfg.Ready() {
		return
	}
	go func() {
		defer safego.RecoverNoAlert("发送 Telegram 告警") // 只记日志：告警出错不能再触发告警
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		if err := Send(ctx, cfg, text); err != nil {
			log.Printf("[telegram] 发送失败: %v", err)
		}
	}()
}

// APIError 是 Telegram 返回的错误（ok=false）。Description 是 Telegram 的原文，不含令牌。
type APIError struct {
	Code        int
	Description string
	RetryAfter  int // 秒；只在 429 时有
}

func (e *APIError) Error() string {
	return fmt.Sprintf("Telegram 返回 %d：%s", e.Code, e.Description)
}

// Send 同步发一条消息（text 已按 HTML 转义）。被限流时按 Telegram 要求的时间等一次再发。
// 返回的错误已去掉令牌，可以直接写日志或返回给后台页面。
func Send(ctx context.Context, cfg Config, text string) error {
	if !cfg.Ready() {
		return errors.New("未配置 Telegram")
	}
	text = truncateHTML(text, maxTextLen)
	err := sendOnce(ctx, cfg, text)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Code == http.StatusTooManyRequests && apiErr.RetryAfter > 0 {
		wait := time.Duration(apiErr.RetryAfter) * time.Second
		if wait > maxRetryAfter {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		err = sendOnce(ctx, cfg, text)
	}
	return err
}

func sendOnce(ctx context.Context, cfg Config, text string) error {
	mu.RLock()
	base := apiBase
	mu.RUnlock()
	form := url.Values{}
	form.Set("chat_id", cfg.ChatID)
	form.Set("text", text)
	form.Set("parse_mode", "HTML")
	form.Set("disable_web_page_preview", "true")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+cfg.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return redactErr(err, cfg.Token)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client, err := telegramClient()
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		// 网络错误里带着请求 URL，URL 里有令牌
		return redactErr(err, cfg.Token)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var r struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if jerr := json.Unmarshal(body, &r); jerr != nil {
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		return &APIError{Code: resp.StatusCode, Description: redact(strings.TrimSpace(string(body)), cfg.Token)}
	}
	if r.OK {
		return nil
	}
	code := r.ErrorCode
	if code == 0 {
		code = resp.StatusCode
	}
	return &APIError{Code: code, Description: redact(r.Description, cfg.Token), RetryAfter: r.Parameters.RetryAfter}
}

// telegramClient 按 TELEGRAM_PROXY 给出发送用的客户端；代理地址不变时复用同一个。
func telegramClient() (*http.Client, error) {
	raw := strings.TrimSpace(os.Getenv(ProxyEnv))
	if raw == "" {
		return httpClient, nil
	}
	proxyMu.Lock()
	defer proxyMu.Unlock()
	if proxyClient != nil && proxyKey == raw {
		return proxyClient, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || !validProxyScheme(u.Scheme) {
		// 不回显原值：里面可能有代理密码
		return nil, errors.New(ProxyEnv + " 格式不对：应形如 http://127.0.0.1:7890 或 socks5://用户名:密码@主机:端口")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(u)
	proxyKey, proxyClient = raw, &http.Client{Timeout: 10 * time.Second, Transport: tr}
	return proxyClient, nil
}

func validProxyScheme(s string) bool {
	switch strings.ToLower(s) {
	case "http", "https", "socks5", "socks5h":
		return true
	}
	return false
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	s = strings.ReplaceAll(s, token, "***")
	// 错误里的 URL 可能是转义过的
	if esc := url.PathEscape(token); esc != token {
		s = strings.ReplaceAll(s, esc, "***")
	}
	return s
}

func redactErr(err error, token string) error {
	if err == nil {
		return nil
	}
	return errors.New(redact(err.Error(), token))
}

// truncateHTML 把已转义的文本截到 max 个字符以内，不截断 &amp; 这类实体（截断的实体会让 Telegram 拒收整条消息）。
func truncateHTML(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	const tail = "…（已截断）"
	keep := max - utf8.RuneCountInString(tail)
	n := 0
	for i := range s {
		if n == keep {
			s = s[:i]
			break
		}
		n++
	}
	if amp := strings.LastIndexByte(s, '&'); amp >= 0 && !strings.Contains(s[amp:], ";") {
		s = s[:amp]
	}
	return s + tail
}

// NotifyNewOrder 发「新订单」通知：卡密已使用、会话已提交（ConfirmRechargeTask）。
// Telegram 是外部服务：卡密只显示末 4 位，邮箱打码。
func NotifyNewOrder(taskID, cdkCode, sessionJSON string) {
	email := maskEmail(extractAccountEmail(sessionJSON))
	if email == "" {
		email = "（待后台确认）"
	}
	now := time.Now().In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02 15:04:05")
	text := fmt.Sprintf(
		"🔔 <b>新订单</b>\n"+
			"🆔 任务号: <code>%s</code>\n"+
			"🎟 CDK: <code>%s</code>\n"+
			"📧 账号: %s\n"+
			"🕐 %s（北京时间）\n"+
			"请到后台「ZovoCard → 兑换订单」处理",
		escapeHTML(taskID), escapeHTML(maskTail(cdkCode, 4)), escapeHTML(email), now,
	)
	SendText(text)
}

// maskTail 只留末 n 位：ABCD-EFGH-1234 → ****1234。
func maskTail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return strings.Repeat("*", len(r))
	}
	return "****" + string(r[len(r)-n:])
}

// maskEmail 打码邮箱：buyer@example.com → bu***@example.com。
func maskEmail(e string) string {
	e = strings.TrimSpace(e)
	at := strings.LastIndexByte(e, '@')
	if at <= 0 {
		return ""
	}
	local, domain := []rune(e[:at]), e[at:]
	keep := 2
	if len(local) <= 2 {
		keep = 1
	}
	return string(local[:keep]) + "***" + domain
}

// extractAccountEmail best-effort parses an account email from a ChatGPT session JSON blob.
func extractAccountEmail(sessionJSON string) string {
	sessionJSON = strings.TrimSpace(sessionJSON)
	if sessionJSON == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(sessionJSON), &m); err != nil {
		return ""
	}
	// common shapes: {"user":{"email":...}} or {"email":...} or {"account":{"email":...}}
	if u, ok := m["user"].(map[string]any); ok {
		if e, ok := u["email"].(string); ok && e != "" {
			return e
		}
	}
	if a, ok := m["account"].(map[string]any); ok {
		if e, ok := a["email"].(string); ok && e != "" {
			return e
		}
	}
	if e, ok := m["email"].(string); ok && e != "" {
		return e
	}
	return ""
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
