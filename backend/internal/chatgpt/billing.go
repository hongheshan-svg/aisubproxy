package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
)

// 付款引擎关闭续费、买 Codex 点数用到的接口。路径、请求体和请求头都取自调研保存的 chatgpt.com 前端包
// （docs/research/2026-11-03-chatgpt-upgrade-credits-research.md），没有在真实账号上核实，上线前要用测试账号确认。
//
// 只有两个写操作：
//   - CancelRenewal：POST /backend-api/subscriptions/cancel，取消下期续费，本期权益不受影响；
//   - CreateCreditCheckout：POST /backend-api/payments/checkout，建点数的结账会话，不花钱。
//
// 其余都是只读。绝不调用 /subscriptions/update、/subscriptions/renew、/payments/payment_method* 的写操作。

const (
	pathSubscriptions  = "/backend-api/subscriptions"
	pathCancelRenewal  = "/backend-api/subscriptions/cancel"
	pathCheckout       = "/backend-api/payments/checkout"
	pathPaymentMethods = "/backend-api/payments/payment_methods"
	pathWhamUsage      = "/backend-api/wham/usage"

	// headerExpectedAccount 是前端对账号作用域请求附加的头，名字由前端常量 _b 指定（js/807489…：tj="X-OpenAI-Expected-Account-Id"）。
	headerExpectedAccount = "X-OpenAI-Expected-Account-Id"
	headerChatGPTAccount  = "ChatGPT-Account-ID"

	// creditEntryPoint 是 Codex 用量页「购买点数」路由的入口值，个人账号用它（前端 entry_point 枚举 CODEX_PURCHASE_CREDITS_ROUTE）。
	creditEntryPoint = "codex_purchase_credits_route"

	// CheckoutTagCustom 是自定义结账页（chatgpt.com 自己的页面）。引擎不支持，遇到就放弃。
	CheckoutTagCustom = "custom_checkout_session"
	// CheckoutTagHosted 是 Stripe 托管结账页。
	CheckoutTagHosted = "hosted_checkout_session"
)

// accountScopedHeaders 是账号作用域请求的头：Bearer，加上前端附带的账号头。
// cancel 只带 X-OpenAI-Expected-Account-Id（前端 additionalHeaders:{[_b]:p}）；结账、已存卡、用量另带 ChatGPT-Account-ID。
func accountScopedHeaders(s Session, accountID string, withChatGPTAccount bool) map[string]string {
	h := bearer(s)
	h[headerExpectedAccount] = accountID
	if withChatGPTAccount {
		h[headerChatGPTAccount] = accountID
	}
	return h
}

func requireAccountID(accountID string) (string, error) {
	id := strings.TrimSpace(accountID)
	if id == "" {
		return "", errors.New("缺少账号 ID")
	}
	return id, nil
}

// RenewalInfo 是 GET /subscriptions 里和续费有关的字段。字段没返回时为 nil，调用方按「不确定」处理。
type RenewalInfo struct {
	WillRenew    *bool
	IsDelinquent *bool
}

// SubscriptionStatus 只读查询账号当前订阅的续费与欠费状态：GET /backend-api/subscriptions?account_id=。
// 前端用法：safeGet("/subscriptions",{parameters:{query:{account_id}}})，读 will_renew、is_delinquent。
func (c *Client) SubscriptionStatus(ctx context.Context, s Session, accountID string) (RenewalInfo, error) {
	id, err := requireAccountID(accountID)
	if err != nil {
		return RenewalInfo{}, err
	}
	b, err := c.do(ctx, fhttp.MethodGet, pathSubscriptions+"?account_id="+url.QueryEscape(id), accountScopedHeaders(s, id, false), nil, nil)
	if err != nil {
		return RenewalInfo{}, err
	}
	var data map[string]interface{}
	if json.Unmarshal(b, &data) != nil {
		return RenewalInfo{}, errors.New("订阅查询返回非 JSON")
	}
	return RenewalInfo{WillRenew: optBool(data["will_renew"]), IsDelinquent: optBool(data["is_delinquent"])}, nil
}

// CancelRenewal 取消下期续费：POST /backend-api/subscriptions/cancel，请求体只有 {account_id}，
// 头带 X-OpenAI-Expected-Account-Id。绝不带 cancellation_outcome：前端在另一处传 "deactivate" 表示立即终止，那会让用户当期权益也没了。
// 返回成功只说明请求被接受，是否真的不再续费要用 SubscriptionStatus 核实。
func (c *Client) CancelRenewal(ctx context.Context, s Session, accountID string) error {
	id, err := requireAccountID(accountID)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(map[string]string{"account_id": id})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, fhttp.MethodPost, pathCancelRenewal, accountScopedHeaders(s, id, false), nil, payload)
	return err
}

// CreditCheckoutResult 是建点数结账会话的结果。Tag 是返回体的 tag：custom_checkout_session 时没有可用的 Stripe 链接。
type CreditCheckoutResult struct {
	Tag, SessionID, URL, ProcessorEntity string
}

// CreateCreditCheckout 建 Codex 点数的结账会话：POST /backend-api/payments/checkout，
// 请求体 {credit_purchase_data:{account_id, quantity, unit:"credit"}, entry_point:"codex_purchase_credits_route", checkout_ui_mode:"redirect"}，
// 头带 ChatGPT-Account-ID 与 X-OpenAI-Expected-Account-Id。前端还会带几项完整性头（我们造不出来），服务器是否要求要在测试账号上确认。
// 不带 billing_details：点数按账号现有的计费币种报价，改币种由引擎在 checking 拦下。
// 订阅开通用的 CreateCheckout 请求体不变。
// creditCheckoutBody 是买点数建结账会话的请求体；自动付款和免付款探测共用，保证探测的请求和真实下单一致。
func creditCheckoutBody(accountID string, quantity int) map[string]interface{} {
	return map[string]interface{}{
		"credit_purchase_data": map[string]interface{}{"account_id": accountID, "quantity": quantity, "unit": "credit"},
		"entry_point":          creditEntryPoint,
		"checkout_ui_mode":     "redirect",
	}
}

func (c *Client) CreateCreditCheckout(ctx context.Context, s Session, accountID string, quantity int) (CreditCheckoutResult, error) {
	id, err := requireAccountID(accountID)
	if err != nil {
		return CreditCheckoutResult{}, err
	}
	if quantity <= 0 {
		return CreditCheckoutResult{}, errors.New("点数数量必须大于 0")
	}
	payload, err := json.Marshal(creditCheckoutBody(id, quantity))
	if err != nil {
		return CreditCheckoutResult{}, err
	}
	b, err := c.do(ctx, fhttp.MethodPost, pathCheckout, accountScopedHeaders(s, id, true), nil, payload)
	if err != nil {
		return CreditCheckoutResult{}, err
	}
	var data struct {
		Tag             string `json:"tag"`
		SessionID       string `json:"checkout_session_id"`
		URL             string `json:"url"`
		ProcessorEntity string `json:"processor_entity"`
	}
	if json.Unmarshal(b, &data) != nil {
		return CreditCheckoutResult{}, errors.New("创建点数支付会话返回非 JSON")
	}
	return CreditCheckoutResult{
		Tag: strings.TrimSpace(data.Tag), SessionID: strings.TrimSpace(data.SessionID),
		URL: strings.TrimSpace(data.URL), ProcessorEntity: strings.TrimSpace(data.ProcessorEntity),
	}, nil
}

// SavedCard 是账号里的一张已存付款方式，只取识别用的字段，不取账单地址。
type SavedCard struct {
	ID, Type, Brand, Last4 string
	ExpMonth, ExpYear      int
}

// SavedCards 只读列出账号的已存付款方式：GET /backend-api/payments/payment_methods?account_id=。
// 前端（设置页）读 payment_methods[] 的 id、type、card.brand、card.last4；快捷购买另一处用 payment_method_id、last4、exp_month、exp_year，两种写法都认。
func (c *Client) SavedCards(ctx context.Context, s Session, accountID string) ([]SavedCard, error) {
	id, err := requireAccountID(accountID)
	if err != nil {
		return nil, err
	}
	b, err := c.do(ctx, fhttp.MethodGet, pathPaymentMethods+"?account_id="+url.QueryEscape(id), accountScopedHeaders(s, id, true), nil, nil)
	if err != nil {
		return nil, err
	}
	var data struct {
		Methods []map[string]interface{} `json:"payment_methods"`
	}
	if json.Unmarshal(b, &data) != nil {
		return nil, errors.New("已存卡查询返回非 JSON")
	}
	out := make([]SavedCard, 0, len(data.Methods))
	for _, m := range data.Methods {
		card, _ := m["card"].(map[string]interface{})
		out = append(out, SavedCard{
			ID:       firstString(m["id"], m["payment_method_id"]),
			Type:     firstString(m["type"]),
			Brand:    firstString(card["brand"], m["brand"]),
			Last4:    firstString(card["last4"], m["last4"], m["display_last4"]),
			ExpMonth: firstInt(card["exp_month"], m["exp_month"]),
			ExpYear:  firstInt(card["exp_year"], m["exp_year"]),
		})
	}
	return out, nil
}

// CreditBalanceInfo 是 Codex 点数余额。Available 为 false 表示接口没给出可比较的余额（没有 credits、无限额度或余额为空）。
type CreditBalanceInfo struct {
	Available bool
	Balance   string // 原样保留的余额文本，比较时按十进制数解析
}

// CreditBalance 只读查询 Codex 点数余额：GET /backend-api/wham/usage，头带 x-openai-codex-pricing-chooser: 1。
// 前端还带 OAI-App-Brand，取值来自前端常量，没有确认，这里不带；返回结构 credits:{has_credits, unlimited, balance}。
func (c *Client) CreditBalance(ctx context.Context, s Session, accountID string) (CreditBalanceInfo, error) {
	h := bearer(s)
	h["x-openai-codex-pricing-chooser"] = "1"
	if id := strings.TrimSpace(accountID); id != "" {
		h[headerChatGPTAccount] = id
	}
	b, err := c.do(ctx, fhttp.MethodGet, pathWhamUsage, h, nil, nil)
	if err != nil {
		return CreditBalanceInfo{}, err
	}
	var data struct {
		Credits *struct {
			HasCredits bool    `json:"has_credits"`
			Unlimited  bool    `json:"unlimited"`
			Balance    *string `json:"balance"`
		} `json:"credits"`
	}
	if json.Unmarshal(b, &data) != nil {
		return CreditBalanceInfo{}, errors.New("点数余额查询返回非 JSON")
	}
	if data.Credits == nil || data.Credits.Unlimited || data.Credits.Balance == nil || strings.TrimSpace(*data.Credits.Balance) == "" {
		return CreditBalanceInfo{}, nil
	}
	return CreditBalanceInfo{Available: true, Balance: strings.TrimSpace(*data.Credits.Balance)}, nil
}

// optBool 取 JSON 布尔值；不是布尔（含缺失、null）返回 nil。
func optBool(v interface{}) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}

// firstInt 取第一个能当整数用的值（JSON 数字或数字文本），都不是返回 0。
func firstInt(vals ...interface{}) int {
	for _, v := range vals {
		switch x := v.(type) {
		case float64:
			return int(x)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
				return n
			}
		}
	}
	return 0
}
