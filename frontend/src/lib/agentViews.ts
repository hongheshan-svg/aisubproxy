// 代理商后台页面用到的纯函数：标签、下单预览、日期范围、查询串、导出请求体。
import { IdempotencyKeeper } from './idempotency'

export const CODE_STATUS_LABELS: Record<string, string> = {
  unused: '未使用', processing: '处理中', used: '已开通', failed: '失败', disabled: '已禁用',
}
export const LEDGER_KIND_LABELS: Record<string, string> = { purchase: '下单', adjust: '调账', topup: '充值' }

/** 单次导出码数上限，与后端一致。 */
export const EXPORT_MAX = 10000
export const ORDER_MAX_QTY = 200
export const NOTE_MAX = 200

export function orderPreview(unitCents: number, qty: number, balanceCents: number) {
  const totalCents = unitCents * qty
  const afterCents = balanceCents - totalCents
  return { totalCents, afterCents, enough: afterCents >= 0 }
}

/** 只接受 1–200 的整数：字符串必须是纯数字（不接受 1e2、0x10、空白、符号），数字类型按整数判断。 */
export function validQuantity(input: unknown): number | null {
  let n: number
  if (typeof input === 'string') {
    if (!/^\d+$/.test(input)) return null
    n = Number(input)
  } else if (typeof input === 'number') n = input
  else return null
  if (!Number.isInteger(n) || n < 1 || n > ORDER_MAX_QTY) return null
  return n
}

const DEFINITE_ORDER_FAILURES = new Set(['insufficient_balance', 'invalid_request', 'product_unavailable', 'product_not_ready', 'price_changed', 'request_no_conflict'])
/** 服务端明确拒绝、不会有同请求号结果的错误：可以换新请求号。网络异常、503、未知错误都不算。 */
export function isDefiniteOrderFailure(code: string): boolean {
  return DEFINITE_ORDER_FAILURES.has(code)
}

/** 下单失败分类：definite 换新请求号；rate_limited（含 429）后端没处理订单，保留请求号、不锁表单；其余视为结果未知，锁定并原样重试。 */
export function orderFailureKind(status: number, code: string): 'definite' | 'rate_limited' | 'unknown' {
  if (isDefiniteOrderFailure(code)) return 'definite'
  if (status === 429 || code === 'rate_limited') return 'rate_limited'
  return 'unknown'
}

/** 限流（429）后是否保留待重试内容：首次提交（之前没有结果未知的订单）不保留；对结果未知订单的原样重试遇到 429 必须保留并继续锁定。 */
export function keepPendingOnRateLimit(wasRetry: boolean): boolean {
  return wasRetry
}

/** 下单页的锁定状态：请求中、确认框打开、有待重试的未知结果时，输入都禁用；有待重试且没在请求才算「结果未知」。 */
export function orderLockState(s: { busy: boolean; confirming: boolean; hasPending: boolean }) {
  return { inputsDisabled: s.busy || s.confirming || s.hasPending, unknown: s.hasPending && !s.busy }
}

export type PendingOrder = { product_code: string; quantity: number; note: string; expected_unit_price_cents: number }
/** 模块级：只存请求号和待重试的请求内容（不存码），切换页面后仍可原样重试。不写 localStorage / sessionStorage。 */
export const orderKeeper = new IdempotencyKeeper()
export const orderRetry: { pending: PendingOrder | null } = { pending: null }

const DAY = 86400
function ymdToBeijingSec(d: string): number | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(String(d || '').trim())
  if (!m) return null
  const t = Date.UTC(+m[1], +m[2] - 1, +m[3])
  const chk = new Date(t)
  if (chk.getUTCFullYear() !== +m[1] || chk.getUTCMonth() !== +m[2] - 1 || chk.getUTCDate() !== +m[3]) return null
  return t / 1000 - 8 * 3600
}

/** YYYY-MM-DD（北京时间）→ Unix 秒；to 取结束那天的次日 0 点。起止颠倒或格式不对返回 null。 */
export function beijingRange(fromDate: string, toDate: string): { from: number; to: number } | null {
  const f = ymdToBeijingSec(fromDate)
  const t = ymdToBeijingSec(toDate)
  if (f === null || t === null || t < f) return null
  return { from: f, to: t + DAY }
}

/** Unix 秒 → 北京时间的 YYYY-MM-DD。 */
export function ymdBeijing(sec: number): string {
  const d = new Date((sec + 8 * 3600) * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())}`
}

/** 流水默认范围：最近 30 天（北京时间）。 */
export function defaultLedgerRange(nowSec: number): { from: string; to: string } {
  return { from: ymdBeijing(nowSec - 30 * DAY), to: ymdBeijing(nowSec) }
}

export type CodeFilter = { product: string; status: string; orderId: string; from: string; to: string }

function filterParams(f: CodeFilter): Record<string, string | number> {
  const o: Record<string, string | number> = {}
  if (f.product) o.product = f.product
  if (f.status) o.status = f.status
  const oid = Number(f.orderId)
  if (f.orderId && Number.isInteger(oid) && oid > 0) o.order_id = oid
  if (f.from && f.to) {
    const r = beijingRange(f.from, f.to)
    if (r) { o.from = r.from; o.to = r.to }
  }
  return o
}

/** 筛选条件 → 「我的码」查询串（带前导 ?）。 */
export function codesQuery(f: CodeFilter, page: number, pageSize: number): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(filterParams(f))) q.set(k, String(v))
  q.set('page', String(page))
  q.set('page_size', String(pageSize))
  return '?' + q.toString()
}

/** POST /codes/export 的请求体：勾选的 ids，或按筛选条件。 */
export function exportBody(src: { ids: number[] } | { filter: CodeFilter }): Record<string, unknown> {
  if ('ids' in src) return { ids: src.ids }
  const f = src.filter
  const p = filterParams(f)
  return { filter: { product: f.product || '', status: f.status || '', order_id: (p.order_id as number) || 0, from: (p.from as number) || 0, to: (p.to as number) || 0 } }
}

export function ledgerQuery(from: number, to: number): string {
  return `from=${from}&to=${to}`
}
