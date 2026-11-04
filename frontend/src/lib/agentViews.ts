// 代理商后台页面用到的纯函数：标签、下单预览、日期范围、查询串、导出请求体。

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

/** 只接受 1–200 的整数（数字或纯数字字符串）。 */
export function validQuantity(input: unknown): number | null {
  const s = typeof input === 'string' ? input.trim() : input
  if (s === '' || s === null || s === undefined || typeof s === 'boolean') return null
  const n = Number(s)
  if (!Number.isInteger(n) || n < 1 || n > ORDER_MAX_QTY) return null
  return n
}

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
