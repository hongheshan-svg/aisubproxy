package payengine

import (
	"fmt"
	"strings"

	"github.com/tuzi/cdk-recharge-system/internal/chatgpt"
)

// 账号能不能开通、能不能买点数的判断规则。付款引擎的检查步骤和只读的账号核对共用同一份，
// 保证「核对说能」与「下单时放行」不会各说各话。都是纯函数：不联网、不改状态，符合条件返回 nil。
// 调用方必须写 if f := xxx(acc); f != nil { return f }，不能直接 return xxx(acc)：
// 类型化的 nil 指针放进 error 接口后不等于 nil。

// creditPlanTypes 是可以买点数的账号套餐类型（accounts/check 的 plan_type，比较时转小写）：Plus 与 Pro 各档。
// 取值依据前端包与开通单的期望套餐类型，上线前要在测试账号上核对（见报告）。
var creditPlanTypes = map[string]bool{"plus": true, "prolite": true, "pro": true, "promax": true}

// defaultBillingCurrency 是账号接口没有返回计费币种时按什么算：前端包里缺省也按美元。
// 按错了只会多放弃（地区币种对不上），打开支付页后还会再核对一次页面币种。
const defaultBillingCurrency = "USD"

// subscriptionEligibility 判断开通单：账号已有有效订阅就不能直接开通。
func subscriptionEligibility(acc chatgpt.Account) *stepFailure {
	if acc.HasActiveSubscription {
		return &stepFailure{code: "already_subscribed", msg: fmt.Sprintf("账号已有有效订阅（%s），不能直接开通", acc.PlanType)}
	}
	return nil
}

// creditAccountEligibility 判断点数单里只看账号本身的条件：账号 ID、生效订阅、套餐类型、个人账号。欠费与币种见 creditBillingEligibility。
func creditAccountEligibility(acc chatgpt.Account) *stepFailure {
	if strings.TrimSpace(acc.AccountID) == "" {
		return &stepFailure{code: "credit_account_unknown", msg: "账号接口没有返回账号 ID，无法购买点数"}
	}
	if !acc.HasActiveSubscription {
		return &stepFailure{code: "credit_no_subscription", msg: "账号没有生效中的订阅：点数只卖给已订阅 Plus 或 Pro 的账号"}
	}
	if pt := strings.ToLower(strings.TrimSpace(acc.PlanType)); !creditPlanTypes[pt] {
		return &stepFailure{code: "credit_plan_ineligible", msg: fmt.Sprintf("账号套餐类型 %q 不能买点数：只限 Plus 或 Pro", acc.PlanType)}
	}
	switch acc.Structure {
	case "personal":
	case "":
		return &stepFailure{code: "credit_not_personal", msg: "账号接口没有返回账号类型，无法确认是个人账号，不能买点数"}
	default:
		return &stepFailure{code: "credit_not_personal", msg: fmt.Sprintf("账号不是个人账号（%s），不能买点数", acc.Structure)}
	}
	return nil
}

// creditBillingEligibility 判断点数单里的欠费与计费币种。delinquent 是账号接口或订阅接口查到的欠费状态（nil 表示都没查到），
// currency 是码的地区币种。
func creditBillingEligibility(acc chatgpt.Account, delinquent *bool, currency string) *stepFailure {
	if delinquent == nil {
		return &stepFailure{code: "credit_delinquency_unknown", msg: "账号接口与订阅接口都没有返回欠费状态，无法确认账号没有欠费"}
	}
	if *delinquent {
		return &stepFailure{code: "credit_delinquent", msg: "账号有欠费，不能买点数"}
	}
	cur := acc.BillingCurrency
	if cur == "" {
		cur = defaultBillingCurrency
	}
	if !strings.EqualFold(cur, currency) {
		return &stepFailure{code: "credit_currency_mismatch", msg: fmt.Sprintf("账号计费币种是 %s，与码的地区币种 %s 不一致：点数按账号计费币种付款", cur, currency)}
	}
	return nil
}
