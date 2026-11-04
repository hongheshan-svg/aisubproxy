// 首页「售后查询」的显示模型：把后端的一条 LookupResult 转成标题、指引、字段、按钮。
import { fmtBeijing } from './mwwebhook'

export interface LookupResult {
  cdk_code: string
  status: string
  used: boolean
  can_resubmit?: boolean
  account_email?: string
  plan?: string
  plan_kind?: string
  used_at?: string
  expires_at?: string
  renewal?: string
  guide?: string
  provider_note?: string
  notes?: string
  message?: string
  replacement?: { code: string; status: string; depth: number }
  replaced_unknown?: boolean
}

export type AfterSaleAction = 'redeem' | 'resubmit' | 'copy_replacement' | 'redeem_replacement'

export interface AfterSaleView {
  tone: 'ok' | 'info' | 'warn' | 'err'
  titleKey: string
  guideKey?: string
  note?: string
  fields: { labelKey: string; value: string }[]
  renewalKey?: string
  replacement?: { code: string; statusKey: string }
  actions: AfterSaleAction[]
}

/** 后端指引编号的固定列表，文字在语言包 aftersale.guide 下。 */
export const GUIDE_CODES = [
  'session_invalid',
  'already_subscribed',
  'plan_unavailable',
  'credit_needs_subscription',
  'credit_not_personal',
  'credit_delinquent',
  'credit_region_mismatch',
  'retry_later',
  'manual_review',
  'provider_retry',
  'provider_wait',
] as const

const KNOWN_STATES = new Set([
  'unused', 'used', 'processing', 'failed', 'replaced', 'unsupported', 'disabled', 'expired', 'unknown',
])

/** 状态值对应的语言包键；不认得的用 unknown。 */
const stateKey = (status: string) => `aftersale.state.${KNOWN_STATES.has(status) ? status : 'unknown'}`

/** RFC3339 字符串转北京时间；空或解析不了返回 undefined。 */
function fmtTime(s?: string): string | undefined {
  if (!s) return undefined
  const ms = Date.parse(s)
  if (Number.isNaN(ms)) return undefined
  return fmtBeijing(Math.floor(ms / 1000))
}

function usedFields(r: LookupResult): AfterSaleView['fields'] {
  const credit = r.plan_kind === 'credit'
  const out: AfterSaleView['fields'] = []
  const add = (labelKey: string, value?: string) => {
    if (value) out.push({ labelKey, value })
  }
  add('aftersale.field.plan', r.plan)
  add('aftersale.field.usedAt', fmtTime(r.used_at))
  if (!credit) add('aftersale.field.expiresAt', fmtTime(r.expires_at))
  add('aftersale.field.email', r.account_email)
  return out
}

export function afterSaleView(r: LookupResult): AfterSaleView {
  const view: AfterSaleView = { tone: 'info', titleKey: 'aftersale.state.unknown', fields: [], actions: [] }
  if (r.guide && (GUIDE_CODES as readonly string[]).includes(r.guide)) view.guideKey = `aftersale.guide.${r.guide}`
  if (r.provider_note) view.note = r.provider_note

  switch (r.status) {
    case 'unused':
      if (r.can_resubmit && r.guide) {
        view.tone = 'warn'
        view.titleKey = 'aftersale.state.failed_restored'
        view.actions = ['resubmit']
      } else {
        view.titleKey = 'aftersale.state.unused'
        view.actions = ['redeem']
      }
      break
    case 'failed':
      view.tone = 'warn'
      if (r.can_resubmit) {
        view.titleKey = 'aftersale.state.failed_restored'
        view.actions = ['resubmit']
      } else {
        view.titleKey = 'aftersale.state.failed_manual'
      }
      break
    case 'processing':
      view.titleKey = 'aftersale.state.processing'
      break
    case 'used': {
      view.tone = 'ok'
      view.titleKey = r.plan_kind === 'credit' ? 'aftersale.state.credit_used' : 'aftersale.state.used'
      view.fields = usedFields(r)
      if (r.plan_kind !== 'credit' && (r.renewal === 'off' || r.renewal === 'closing')) view.renewalKey = `aftersale.renewal.${r.renewal}`
      break
    }
    case 'replaced':
      if (r.replacement) {
        view.titleKey = 'aftersale.state.replaced'
        view.replacement = { code: r.replacement.code, statusKey: stateKey(r.replacement.status) }
        view.actions = ['copy_replacement']
        if (r.replacement.status === 'unused') view.actions.push('redeem_replacement')
      } else if (r.replaced_unknown) {
        view.tone = 'warn'
        view.titleKey = 'aftersale.state.replaced_unknown'
      } else {
        view.tone = 'warn'
        view.titleKey = 'aftersale.state.replaced_pending'
      }
      break
    case 'unsupported':
      view.titleKey = 'aftersale.state.unsupported'
      break
    case 'disabled':
      view.tone = 'err'
      view.titleKey = 'aftersale.state.disabled'
      break
    case 'expired':
      view.tone = 'err'
      view.titleKey = 'aftersale.state.expired'
      break
    default:
      view.tone = 'warn'
      view.titleKey = 'aftersale.state.unknown'
  }
  return view
}
