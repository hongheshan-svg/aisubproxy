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
