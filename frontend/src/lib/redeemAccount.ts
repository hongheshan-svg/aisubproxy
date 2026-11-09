// 第 ③ 屏「核对账号」的账号摘要和状态判断（规格 §5.4、§6.4）。纯函数：不碰 DOM、存储和网络。
import { maskEmail } from './sessionCheck'

export interface AccountSummary {
  /** 脱敏后的邮箱，完整邮箱不进摘要 */
  maskedEmail: string
  currentPlan: string
  subscriptionHasActive: boolean | null
  recoveryRequired: boolean
  delinquent: boolean
  /** true 查过；false MarsWallex 返回查不了；null 对方没返回账号信息 */
  checked: boolean | null
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export function accountFromPreflight(data: any): AccountSummary {
  // 上游码可能是 { code, msg, data: {...} } 包裹；MarsWallex 是平铺的
  const body = data?.data && typeof data.data === 'object' ? data.data : data || {}
  const email = String(body.email || body.account_email || '')
  const currentPlan = String(body.currentPlan || body.current_plan || body.plan || '')
  const subscriptionHasActive = typeof body.subscription_has_active === 'boolean' ? body.subscription_has_active : null
  let checked: boolean | null
  if (typeof body.account_checked === 'boolean') checked = body.account_checked
  else checked = email || currentPlan || subscriptionHasActive !== null ? true : null
  return {
    maskedEmail: maskEmail(email),
    currentPlan,
    subscriptionHasActive,
    recoveryRequired: body.subscription_recovery_required === true,
    delinquent: body.subscription_is_delinquent === true,
    checked,
  }
}

export type ConfirmState = 'ok' | 'blocked' | 'recover' | 'self_check'

/** 规格 §6.4 的状态表，先后：要先恢复订阅 → 不能用 → 请自己确认 → 可以开通。 */
export function confirmState(i: {
  account: AccountSummary | null
  blockingCode: string
  blockingReason: string
  satisfied: boolean
}): ConfirmState {
  if (i.account && (i.account.recoveryRequired || i.account.delinquent)) return 'recover'
  if (i.blockingCode || i.blockingReason || i.satisfied) return 'blocked'
  if (!i.account || i.account.checked !== true) return 'self_check'
  return 'ok'
}

// 两个「暂时没法确认」的码文案相同，共用 credit_account_unknown 的键
const BLOCKING_KEYS: Record<string, string> = {
  already_subscribed: 'already_subscribed',
  credit_no_subscription: 'credit_no_subscription',
  credit_plan_ineligible: 'credit_plan_ineligible',
  credit_not_personal: 'credit_not_personal',
  credit_delinquent: 'credit_delinquent',
  credit_currency_mismatch: 'credit_currency_mismatch',
  credit_account_unknown: 'credit_account_unknown',
  credit_delinquency_unknown: 'credit_account_unknown',
}

/** MarsWallex 放弃码对应的 redeemUi.blocking.* 键；表里没有的返回 null，页面改为显示 blocking_reason。 */
export function blockingKey(code: string): string | null {
  const k = Object.prototype.hasOwnProperty.call(BLOCKING_KEYS, code) ? BLOCKING_KEYS[code] : undefined
  return k ? `redeemUi.blocking.${k}` : null
}
