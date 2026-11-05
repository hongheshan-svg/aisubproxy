// 代理商后台页面用到的纯函数：标签、下单预览、日期范围、查询串、导出请求体；结果未知的下单锁定（落 localStorage）。
import { createLeaveGuard, localStore, ORDER_RETRY_KEY, RetryLock, type LeaveGuard, type RetryStore } from './retryLock'

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

/**
 * 下单页的锁定状态：请求中、确认框打开、有待重试的未知结果、还没核对当前代理商（ready=false）时，输入都禁用；
 * 有待重试且没在请求才算「结果未知」。ready 不传视为已核对。
 */
export function orderLockState(s: { busy: boolean; confirming: boolean; hasPending: boolean; ready?: boolean }) {
  return { inputsDisabled: s.busy || s.confirming || s.hasPending || s.ready === false, unknown: s.hasPending && !s.busy }
}

export type PendingOrder = { product_code: string; quantity: number; note: string; expected_unit_price_cents: number }

/** 存储里读出的待重试内容：字段不全、数量不合法就不认。返回固定字段顺序的新对象（比较内容时用）。 */
export function parsePendingOrder(v: unknown): PendingOrder | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  const qty = typeof o.quantity === 'number' ? validQuantity(o.quantity) : null
  const price = o.expected_unit_price_cents
  if (typeof o.product_code !== 'string' || !o.product_code || qty === null) return null
  if (typeof o.note !== 'string' || o.note.length > NOTE_MAX || typeof price !== 'number' || !Number.isInteger(price) || price < 0) return null
  return { product_code: o.product_code, quantity: qty, note: o.note, expected_unit_price_cents: price }
}

/** 两笔待重试内容是否相同（都没有也算相同）。 */
export function samePendingOrder(a: PendingOrder | null, b: PendingOrder | null): boolean {
  if (!a || !b) return !a && !b
  return JSON.stringify(parsePendingOrder(a)) === JSON.stringify(parsePendingOrder(b))
}

/** 提交前重读的结论：go 照常发；locked 有一笔结果未知的订单（可能是别的标签页的），改显示它；settled 页面上锁定的那笔已在别处处理完，不许再发。 */
export type OrderRecheck = { kind: 'go' } | { kind: 'locked'; pending: PendingOrder } | { kind: 'settled' }

/**
 * 结果未知的下单：请求号和待重试内容（不含码）落 localStorage，绑定代理商登录名。
 * - 发出前就锁定落盘：请求还在路上时刷新、关标签页，回来也沿用原请求号，服务端按请求号找回第一次的结果。
 * - 刷新、关标签页重开、新标签页都能找回；多个标签页共用一份，每次提交前重读。
 * - 换了代理商登录（登录名不符）就丢弃，免得用新代理商的钱「原样重试」上一个人的订单；退出登录时清掉。
 */
export class OrderRetry {
  private owner = ''
  readonly lock: RetryLock<PendingOrder>

  constructor(store: RetryStore | undefined, private guard: LeaveGuard | undefined = undefined, now: () => number = Date.now) {
    this.lock = new RetryLock(ORDER_RETRY_KEY, store, parsePendingOrder, now)
  }

  get bound(): boolean {
    return !!this.owner
  }

  /** 记下当前代理商（/me 的 username）并重读：存储里别人的记录丢弃。返回当前代理商结果未知的那笔。 */
  bind(owner: string): PendingOrder | null {
    if (owner) this.owner = owner
    return this.refresh()
  }

  /** 重读存储：当前代理商结果未知的那笔（别的标签页写的也算），没有为 null。还没核对身份时一律 null、不动存储。 */
  refresh(): PendingOrder | null {
    const p = this.owner ? this.lock.current(this.owner)?.payload ?? null : null
    this.guard?.set(!!p)
    return p
  }

  /** 提交前调用：shown 是页面上显示的待重试订单（没有为 null）。 */
  recheck(shown: PendingOrder | null): OrderRecheck {
    const cur = this.refresh()
    if (cur) return shown && samePendingOrder(cur, shown) ? { kind: 'go' } : { kind: 'locked', pending: cur }
    return shown ? { kind: 'settled' } : { kind: 'go' }
  }

  /** 发请求前调用：先锁定落盘再发，返回请求号。已有别的待重试订单且内容不同时抛 IdempotencyLockedError。 */
  begin(p: PendingOrder): string {
    if (!this.owner) throw new Error('还没核对当前代理商，不能下单')
    const key = this.lock.lock(p, this.owner)
    this.guard?.set(true)
    return key
  }

  /** 有了明确结果（成功、明确失败、首次提交被限流）：解锁。只清请求号相同的那笔。 */
  settle(requestNo: string): void {
    this.lock.done(requestNo)
    this.refresh()
  }

  /** 代理商确认放弃：清掉锁定，下一笔用新请求号。 */
  abandon(): void {
    this.lock.done()
    this.refresh()
  }

  /** 退出登录：清掉记录，忘掉身份。 */
  clear(): void {
    this.lock.clear()
    this.owner = ''
    this.guard?.set(false)
  }
}

/** 模块级单例：代理商后台各页面、多个标签页共用（经 localStorage）。 */
export const orderRetry = new OrderRetry(localStore(), createLeaveGuard())

/** 「最近一笔订单」：订单列表第一页第一条里用到的字段。 */
export type RecentOrder = { id: number; product_code: string; product_name: string; quantity: number; total_cents: number; created_at: number }

/** 从 GET /orders?page=1&page_size=1 的响应里取最近一笔；没有或数据不对返回 null。 */
export function pickLatestOrder(data: any): RecentOrder | null {
  const o = data && Array.isArray(data.items) ? data.items[0] : null
  if (!o || !Number.isInteger(o.id) || o.id <= 0 || typeof o.product_code !== 'string' || !Number.isInteger(o.quantity)) return null
  const at = Number(o.created_at)
  if (!Number.isFinite(at) || at <= 0) return null
  return {
    id: o.id, product_code: o.product_code, product_name: String(o.product_name || o.product_code),
    quantity: o.quantity, total_cents: Number(o.total_cents) || 0, created_at: at,
  }
}

/** 重复下单提醒的时间窗：10 分钟（含整 10 分钟）。 */
export const RECENT_ORDER_WINDOW_SEC = 600

/**
 * 「确认下单」时的重复提醒：最近一笔在 10 分钟内（含整 10 分钟；本机时钟偏慢、订单时间在「未来」也算）、
 * 商品和数量都相同才提醒。只是提醒，不拦截。用于换浏览器、换电脑后本地锁定不在的情形。
 */
export function recentOrderWarning(latest: RecentOrder | null | undefined, productCode: string, quantity: number, nowSec: number): string {
  if (!latest || !productCode || !quantity) return ''
  if (latest.product_code !== productCode || latest.quantity !== quantity) return ''
  if (nowSec - latest.created_at > RECENT_ORDER_WINDOW_SEC) return ''
  return `你在 ${hmBeijing(latest.created_at)} 刚买过同样的 ${quantity} 张，确认要再买一次吗？`
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

/** Unix 秒 → 北京时间的 HH:mm。 */
export function hmBeijing(sec: number): string {
  const d = new Date((sec + 8 * 3600) * 1000)
  return `${String(d.getUTCHours()).padStart(2, '0')}:${String(d.getUTCMinutes()).padStart(2, '0')}`
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
