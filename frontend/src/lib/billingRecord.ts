// 账单页的显示逻辑：凭卡密查到的本站开通记录逐行怎么显示，以及会话查询的账单金额格式化。
import { planLabelT } from './plan'

/** 凭卡密查账单返回的本站开通记录（后端 gpt_billing.go 的 billingCDKRecord）。时间是 Unix 秒。 */
export interface BillingRecord {
  email: string
  plan: string
  plan_kind: string
  redeemed_at?: number
  access_until?: number
  renewal_off: boolean
  amount?: string // 后端已按币种小数位格式化好，如 "1120.00"
  currency?: string
}

export interface BillingRecordRow {
  key: string
  label: string
  value: string
}

type TFn = (key: string, params?: Record<string, unknown>) => string

/** Stripe 里没有小数的币种：金额的最小单位就是 1 元，不能除以 100。 */
const ZERO_DECIMAL = new Set(['BIF', 'CLP', 'DJF', 'GNF', 'JPY', 'KMF', 'KRW', 'MGA', 'PYG', 'RWF', 'UGX', 'VND', 'VUV', 'XAF', 'XOF', 'XPF'])

/** 会话查询拿到的 ChatGPT（Stripe）账单金额是最小单位，按币种小数位格式化，如 2000 usd → "20.00 USD"、3000 jpy → "3000 JPY"。 */
export function formatInvoiceAmount(total: unknown, currency: unknown): string {
  const n = Number(total)
  if (total == null || total === '' || !Number.isFinite(n)) return '—'
  const cur = String(currency || 'usd').toUpperCase()
  return ZERO_DECIMAL.has(cur) ? `${Math.round(n)} ${cur}` : `${(n / 100).toFixed(2)} ${cur}`
}

/** 界面语言对应的日期格式；不传时跟浏览器。 */
function dateLocale(locale?: string): string | undefined {
  if (!locale) return undefined
  return locale.startsWith('zh') ? 'zh-CN' : 'en-US'
}

/** Unix 秒按本地时区显示，日期格式跟界面语言（英文界面不出现「上午」）；0、空、读不出给 —。 */
export function formatUnixTs(v: unknown, locale?: string): string {
  const n = Number(v)
  if (!Number.isFinite(n) || n <= 0) return '—'
  try {
    return new Date(n * 1000).toLocaleString(dateLocale(locale))
  } catch {
    return String(v)
  }
}

/**
 * 开通记录逐行显示的内容。订阅码多两行：权益到期、自动续费。
 * 自动续费只有后端核实关掉时才显示「已关闭」，其余一律「—」：本站记录说明不了现在是否还在续费。
 */
export function billingRecordRows(rec: BillingRecord, t: TFn, locale?: string): BillingRecordRow[] {
  const rows: BillingRecordRow[] = [
    { key: 'account', label: t('billingCheck.account'), value: rec.email || '—' },
    { key: 'plan', label: t('billingCheck.plan'), value: rec.plan ? planLabelT(rec.plan, t) : '—' },
    { key: 'redeemedAt', label: t('billingCheck.redeemedAt'), value: formatUnixTs(rec.redeemed_at, locale) },
    { key: 'amount', label: t('billingCheck.amount'), value: rec.amount && rec.currency ? `${rec.amount} ${rec.currency}` : '—' },
  ]
  if (rec.plan_kind === 'subscription') {
    rows.push(
      { key: 'accessUntil', label: t('billingCheck.accessUntil'), value: formatUnixTs(rec.access_until, locale) },
      { key: 'autoRenew', label: t('billingCheck.autoRenew'), value: rec.renewal_off ? t('billingCheck.renewOff') : '—' },
    )
  }
  return rows
}
