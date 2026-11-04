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

/** 设计 §4 指引表里按钮为「无」的编号：码即使已恢复也不给重新兑换。 */
const NO_RESUBMIT_GUIDES = new Set(['credit_region_mismatch', 'manual_review', 'provider_wait'])

/** 这两条指引的文案说「原因如下」，没有服务商原文时改用不带「如下」的 *_plain 文案。 */
const NOTE_GUIDES = new Set(['provider_retry', 'provider_wait'])

const KNOWN_STATES = new Set([
  'unused', 'used', 'processing', 'failed', 'replaced', 'unsupported', 'disabled', 'expired', 'unknown',
])

/** 状态值对应的语言包键；不认得的用 unknown。 */
const stateKey = (status: string) => `aftersale.state.${KNOWN_STATES.has(status) ? status : 'unknown'}`

/** RFC3339 字符串转北京时间；空或解析不了返回 undefined。售后结果和 /history 批量表共用。 */
export function fmtLookupTime(s?: string): string | undefined {
  if (!s) return undefined
  const ms = Date.parse(s)
  if (Number.isNaN(ms)) return undefined
  return fmtBeijing(Math.floor(ms / 1000))
}

function usedFields(r: LookupResult): AfterSaleView['fields'] {
  // 到期时间不显示：只有关续费核实成功的新单才有，多数码查不到，显示出来反而让客户以为少了信息。
  const out: AfterSaleView['fields'] = []
  const add = (labelKey: string, value?: string) => {
    if (value) out.push({ labelKey, value })
  }
  add('aftersale.field.plan', r.plan)
  add('aftersale.field.usedAt', fmtLookupTime(r.used_at))
  add('aftersale.field.email', r.account_email)
  return out
}

export function afterSaleView(r: LookupResult): AfterSaleView {
  const view: AfterSaleView = { tone: 'info', titleKey: 'aftersale.state.unknown', fields: [], actions: [] }
  if (r.guide && (GUIDE_CODES as readonly string[]).includes(r.guide)) {
    view.guideKey = `aftersale.guide.${r.guide}${NOTE_GUIDES.has(r.guide) && !r.provider_note ? '_plain' : ''}`
  }
  if (r.provider_note) view.note = r.provider_note
  const resubmit: AfterSaleAction[] = r.guide && NO_RESUBMIT_GUIDES.has(r.guide) ? [] : ['resubmit']

  switch (r.status) {
    case 'unused':
      if (r.can_resubmit && r.guide) {
        view.tone = 'warn'
        view.titleKey = 'aftersale.state.failed_restored'
        view.actions = resubmit
      } else {
        view.titleKey = 'aftersale.state.unused'
        view.actions = ['redeem']
      }
      break
    case 'failed':
      view.tone = 'warn'
      if (r.can_resubmit) {
        view.titleKey = 'aftersale.state.failed_restored'
        view.actions = resubmit
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
      if (r.replacement?.status === 'replaced') {
        // 链停在一张又换过码、但查不到它的新码的码上：那张码已作废，不显示也不给复制、兑换
        view.tone = 'warn'
        view.titleKey = 'aftersale.state.replaced_again'
      } else if (r.replacement) {
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

export interface BatchSummary {
  used: number
  failed: number
  unused: number
  processing: number
  replaced: number
  unsupported: number
  unknown: number
}

/** /history 批量结果顶部的统计。replaced、unsupported 各占一栏；disabled、expired、unknown 和不认得的状态算「未找到」。 */
export function batchSummary(rows: LookupResult[]): BatchSummary {
  const s: BatchSummary = { used: 0, failed: 0, unused: 0, processing: 0, replaced: 0, unsupported: 0, unknown: 0 }
  for (const row of rows) {
    switch (row.status) {
      case 'used':
      case 'failed':
      case 'unused':
      case 'processing':
      case 'replaced':
      case 'unsupported':
        s[row.status as Exclude<keyof BatchSummary, 'unknown'>]++
        break
      default:
        s.unknown++
    }
  }
  return s
}

/** 批量表和 CSV 里的 message：有服务商原因时追加「（原因：…）」，失败原因不会因为只在 provider_note 里而丢掉。 */
export function batchMessage(r: LookupResult): string {
  const msg = r.message || ''
  return r.provider_note ? `${msg}（原因：${r.provider_note}）` : msg
}

/**
 * 文本单元格以 = + - @、制表符或回车开头时前面加单引号，防止 Excel/WPS 把它当公式执行（CSV 公式注入）。
 * cdk_code 会原样回显用户输入，所以必须处理。
 */
export function csvSafe(text: string): string {
  return /^[=+\-@\t\r]/.test(text) ? `'${text}` : text
}

const csvCell = (v: unknown) => `"${csvSafe(String(v)).replace(/"/g, '""')}"`

/** CSV 的一行（列：cdk_code, status, can_resubmit, account_email, plan, used_at, message）。can_resubmit 只会是 0/1，值不变。 */
export function csvRow(r: LookupResult): string {
  return [
    csvCell(r.cdk_code),
    csvCell(r.status),
    csvCell(r.can_resubmit ? '1' : '0'),
    csvCell(r.account_email || ''),
    csvCell(r.plan || ''),
    csvCell(r.used_at || ''),
    csvCell(batchMessage(r)),
  ].join(',')
}
