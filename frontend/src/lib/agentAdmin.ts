// 管理员后台「代理商」相关页面的纯逻辑：价格改动、调账结果判断、错误文案。
import { AGENT_ERROR_TEXT } from './agentApi'
import { usdToCents } from './provider'

export type PriceCell = { tier_id: number; product_id: number; price_cents: number }
export type OverrideCell = { product_id: number; price_cents: number }

const FALLBACK = '操作失败，请稍后再试'

/** 空串或 0 表示删除（价格 0）；其余按美元解析。 */
function parsePrice(text: string): { ok: true; cents: number } | { ok: false; error: string } {
  const s = String(text ?? '').trim()
  if (s === '' || /^0+(\.0+)?$/.test(s)) return { ok: true, cents: 0 }
  const r = usdToCents(s)
  return r.ok ? { ok: true, cents: r.cents } : { ok: false, error: r.error }
}

/** 价格矩阵：edited 的键为 "<tier_id>:<product_id>"，只返回和 original 不同的格子；价格 0 = 这个等级不卖。 */
export function matrixChanges(original: PriceCell[], edited: Record<string, string>): { cells: PriceCell[]; errors: string[] } {
  const cur = new Map<string, number>()
  for (const c of original) cur.set(`${c.tier_id}:${c.product_id}`, c.price_cents)
  const cells: PriceCell[] = []
  const errors: string[] = []
  for (const [key, text] of Object.entries(edited)) {
    const m = /^(\d+):(\d+)$/.exec(key)
    if (!m) { errors.push(`格子 ${key} 不正确`); continue }
    const p = parsePrice(text)
    if (!p.ok) { errors.push(`格子 ${key}：${p.error}`); continue }
    if (p.cents !== (cur.get(key) ?? 0)) cells.push({ tier_id: Number(m[1]), product_id: Number(m[2]), price_cents: p.cents })
  }
  return { cells, errors }
}

/** 单个代理商的覆盖价：键为 product_id；价格 0 = 取消覆盖。 */
export function overrideChanges(original: { product_id: number; price_cents: number }[], edited: Record<string, string>): { cells: OverrideCell[]; errors: string[] } {
  const cur = new Map<string, number>()
  for (const c of original) cur.set(String(c.product_id), c.price_cents)
  const cells: OverrideCell[] = []
  const errors: string[] = []
  for (const [key, text] of Object.entries(edited)) {
    if (!/^\d+$/.test(key)) { errors.push(`商品 ${key} 不正确`); continue }
    const p = parsePrice(text)
    if (!p.ok) { errors.push(`商品 ${key}：${p.error}`); continue }
    if (p.cents !== (cur.get(key) ?? 0)) cells.push({ product_id: Number(key), price_cents: p.cents })
  }
  return { cells, errors }
}

/** 商品编码规则，和后端一致。 */
export function validProductCode(code: string): boolean {
  return /^[a-z0-9-]{2,48}$/.test(code)
}

/** 管理员侧的编号文案，覆盖或补充代理商侧的。 */
export const ADMIN_ERROR_TEXT: Record<string, string> = {
  ...AGENT_ERROR_TEXT,
  insufficient_balance: '余额不足，不能扣成负数',
  username_taken: '登录名已被使用',
  idempotency_conflict: '同一个幂等键已用于别的代理商或别的金额，请关闭对话框后重新填写',
  tier_in_use: '还有代理商在用这个等级，不能删除',
  name_taken: '名称已存在',
  product_code_taken: '商品编码已存在',
}

/** 管理员页面的错误显示：优先 message，其次编号表，再其次旧格式的中文文本，最后兜底。 */
export function adminErrorText(data: any, status?: number): string {
  if (data && typeof data.message === 'string' && data.message) return data.message
  const code = data && typeof data.error === 'string' ? data.error : ''
  if (ADMIN_ERROR_TEXT[code]) return ADMIN_ERROR_TEXT[code]
  if (code && !/^[a-z0-9_]+$/.test(code)) return code
  if (status === 429) return ADMIN_ERROR_TEXT.rate_limited
  return FALLBACK
}

export type AdjustOutcome = {
  kind: 'ok' | 'replayed' | 'failed' | 'unknown'
  /** 是否可以清掉幂等键（成功或明确失败）；false 表示必须原样重试 */
  clearKey: boolean
  message: string
}

export const REPLAYED_TEXT = '这是之前同一次调账的结果，没有重复入账'
export const UNKNOWN_TEXT = '结果未知，请原样重试（金额和原因已锁定，沿用同一个幂等键，不会重复入账）'

/** 调账请求的结果归类：网络异常（0）和 5xx 不知道有没有入账，保留键；其余都是明确结果。 */
export function classifyAdjust(status: number, data: any): AdjustOutcome {
  if (status >= 200 && status < 300) {
    if (data && data.replayed === true) return { kind: 'replayed', clearKey: true, message: REPLAYED_TEXT }
    return { kind: 'ok', clearKey: true, message: '调账成功' }
  }
  if (status === 0 || status >= 500) return { kind: 'unknown', clearKey: false, message: UNKNOWN_TEXT }
  return { kind: 'failed', clearKey: true, message: adminErrorText(data, status) }
}
