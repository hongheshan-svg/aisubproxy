// MarsWallex 兑换（付款引擎）后台页面的公共函数。
import { authFetch } from './api'
import type { Resp } from './cardpool'

/** 调用 /api/v1/admin/pay 下的接口，写法同 poolApi。 */
export async function payApi<T = any>(path: string, method = 'GET', body?: unknown): Promise<Resp<T>> {
  try {
    const r = await authFetch('/api/v1/admin/pay' + path, {
      method,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const data = await r.json().catch(() => ({}))
    return { ok: r.ok, status: r.status, data }
  } catch {
    return { ok: false, status: 0, data: { error: '网络错误' } as any }
  }
}

const STATE_LABEL: Record<string, string> = {
  queued: '已受理', checking: '预检中', link_created: '已生成链接', opening: '打开支付页',
  funding: '备卡中', filling: '填表中', submitted: '已提交', requires_3ds: '等待 3DS',
  verifying: '确认中', succeeded: '成功', failed: '失败', needs_review: '待人工', previewed: '试运行完成',
}

export const STATES = Object.keys(STATE_LABEL)

export function stateLabel(state: string): string {
  return STATE_LABEL[state] ?? state
}

const FAILURE_LABEL: Record<string, string> = {
  not_submitted: '提交前放弃，未扣款',
  declined: '提交后被拒',
}

export function failureLabel(f: string): string {
  return FAILURE_LABEL[f] ?? f
}

/** 订单详情「失败原因」的类别文字。人工定论的单显示定论本身：管理员判定没扣款时，系统记的 declined（提交后被拒）会误导。 */
export function failureText(failure: string, resolvedBy: string): string {
  return resolvedBy ? '人工定论为失败' : failureLabel(failure)
}

const PAGE_OUTCOME_LABEL: Record<string, string> = {
  success: '支付页显示成功',
  declined: '支付页显示被拒',
  form_rejected: '表单校验未通过',
  unknown: '没有明确结果',
}

export function pageOutcomeLabel(o: string): string {
  if (!o) return '—'
  return PAGE_OUTCOME_LABEL[o] ?? o
}

const TERMINAL = new Set(['succeeded', 'failed', 'needs_review', 'previewed'])

export function isTerminal(state: string): boolean {
  return TERMINAL.has(state)
}

/** 币种小数位，与后端一致：JPY、CLP 为 0，其余为 2。 */
export function currencyDigits(currency: string): number {
  return currency === 'JPY' || currency === 'CLP' ? 0 : 2
}

/** 主单位金额转最小单位（Math.round 避免 19.99 × 100 的浮点误差）。 */
export function toMinor(amount: number, currency: string): number {
  return Math.round(amount * 10 ** currencyDigits(currency))
}

/** 最小单位转主单位。 */
export function fromMinor(minor: number, currency: string): number {
  return minor / 10 ** currencyDigits(currency)
}

/** 最小单位金额按币种小数位、千分位显示，后接币种代码，如 1,120.00 PHP。 */
export function formatMinor(minor: number, currency: string): string {
  const d = currencyDigits(currency)
  const neg = minor < 0
  const abs = Math.abs(minor)
  const unit = 10 ** d
  const whole = Math.floor(abs / unit)
  const frac = abs - whole * unit
  const w = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const text = d > 0 ? `${w}.${String(frac).padStart(d, '0')}` : w
  return `${neg ? '-' : ''}${text} ${currency}`
}

// payBlockReason 返回这个套餐与地区不能提交的原因，可以提交时返回空串。
// 试运行只要求有 ChatGPT 套餐名；真实付款还要求地区已启用且金额都已配置。与后端 ResolvePlan 的规则一致。
export function payBlockReason(
  plan: { chatgpt_plan_name: string; regions?: Record<string, { enabled: boolean; cap_minor: number; fund_cents: number }> } | undefined,
  region: string,
  dryRun: boolean,
): string {
  if (!plan) return '请选择套餐'
  if (!plan.chatgpt_plan_name) return '该套餐没有 ChatGPT 套餐名，无法提交'
  if (dryRun) return ''
  const rc = plan.regions?.[region]
  if (!rc || !rc.enabled) return '该套餐在此地区未启用，不能真实付款（可先试运行）'
  if (!(rc.cap_minor > 0) || !(rc.fund_cents > 0)) return '该套餐在此地区的金额未配置，不能真实付款（可先试运行）'
  return ''
}
