// 批量兑换页（BatchRedeemView）的提交编排。纯函数：页面没有组件测试，判断提到这里单测，页面只接线。
//
// 资金安全：同一张卡密在一个批次里只提交一次；重置、换批次、离开页面之后不再提交还没开始的卡密，
// 已经发出的兑换请求不取消、不重发。

/**
 * 自动提交的并发循环：最多 concurrency 张同时在提交，按顺序取下一张，每张只取一次。
 * 每取一张之前先问 isLive()：重置、换批次或离开页面之后不再取新的；已经在提交的那一张照常等它返回。
 */
export async function runSubmitPool<T>(
  items: T[],
  concurrency: number,
  isLive: () => boolean,
  submit: (item: T) => Promise<unknown>,
): Promise<void> {
  let cursor = 0
  const runners = Array.from({ length: Math.min(concurrency, items.length) }, async () => {
    while (isLive()) {
      const i = cursor++
      if (i >= items.length) return
      await submit(items[i])
    }
  })
  await Promise.all(runners)
}

/**
 * 登记「这张卡密要发兑换请求了」（FE2 审查重要 3）。本批已经登记过的返回 false，调用方不能再发。
 * 这是在途守卫之外的结构性保险：即使以后守卫被改坏、同一张卡密被排进两次，也发不出第二个兑换请求。
 * 卡密按粘贴去重的写法比较（去空格、转大写，见 lib/batch-session.ts parseCdks）。sent 随批次号重建。
 */
export function claimRedeem(sent: Set<string>, cardKey: string): boolean {
  const k = String(cardKey || '').trim().toUpperCase()
  if (!k || sent.has(k)) return false
  sent.add(k)
  return true
}

const FAIL_STATUSES = new Set(['declined', 'failed_precharge', 'cancelled', 'failed'])

/** 单据状态是开通成功 */
export function isSuccessStatus(st: string): boolean {
  return String(st || '').toLowerCase() === 'completed'
}

/** 单据状态是明确失败 */
export function isFailStatus(st: string): boolean {
  return FAIL_STATUSES.has(String(st || '').toLowerCase())
}

export type RedeemOutcome = 'unknown' | 'rejected' | 'success' | 'failed' | 'processing'

/**
 * 这个 503 是不是本站后端自己明确拒绝、没有建单（F2-03 裁定 2 补）。三条都满足才算：
 * - 响应体是本站后端的 JSON：localReply 写出，带 provider_kind 'card_supply' 和非空的 error 字符串；
 * - 任何一层都没有 order 字段；
 * - 状态码正好是 503。
 * 依据：兑换路径上本站后端回 503 的地方都在建单之前，或者是建单事务失败、已回滚（见 fe2-report.md）。
 * 网关、代理自己回的 503（HTML、空体、别的 JSON）证明不了没建单，不算。
 */
export function ownRejectedBeforeOrder(httpStatus: number, body: unknown): boolean {
  if (httpStatus !== 503) return false
  if (!body || typeof body !== 'object' || Array.isArray(body)) return false
  const b = body as Record<string, unknown>
  if (b.provider_kind !== 'card_supply') return false
  if (typeof b.error !== 'string' || b.error.trim() === '') return false
  if ('order' in b) return false
  const inner = b.data
  if (inner && typeof inner === 'object' && 'order' in (inner as Record<string, unknown>)) return false
  return true
}

/**
 * 兑换请求回来后这一张怎么显示（F2-03）。httpStatus 为 0 表示请求发出后没收到回复（断线、读回复时断开）。
 * - 本站后端明确回的 503（ownRejectedBeforeOrder）：没有建单 → 失败；
 * - 其余 5xx、断线：服务器可能已经受理（建单不跟随客户端连接），结果未知 → 标「处理中」并只读地查结果，绝不重发；
 * - 其余非 2xx：服务器明确拒绝，没有用掉卡密 → 失败；
 * - 2xx：按单据状态。
 *
 * 不直接用单张兑换页的 redeemRejectedBeforeOrder（lib/mwredeem.ts）：它只看状态码，分不清本站后端的 503
 * 和网关、代理回的 503（请求可能已经到了后端），所以这里另加了「响应体是本站后端的」这一条。
 */
export function redeemOutcome(httpStatus: number, orderStatus: string, body?: unknown): RedeemOutcome {
  if (ownRejectedBeforeOrder(httpStatus, body)) return 'rejected'
  if (httpStatus < 200 || httpStatus >= 500) return 'unknown'
  if (httpStatus >= 300) return 'rejected'
  if (isSuccessStatus(orderStatus)) return 'success'
  if (isFailStatus(orderStatus)) return 'failed'
  return 'processing'
}

/** 只读查询查到的一张单里页面要用的东西。canRetry：后端说这张卡密此刻仍未使用、可以重新提交（只有 true 才算）。 */
export interface PolledOrder {
  status: string
  message: string
  email: string
  canRetry: boolean
  order: Record<string, any>
}

/** 从只读查询的回包里读出单据。后端写法见 backend/internal/handler/local_cdk.go 的 writeLocalResult、localOrderPublic。 */
export function readPolledOrder(data: any): PolledOrder {
  const order = data?.order || data?.data?.order || data?.data || data || {}
  return {
    status: String(order.status || data?.status || data?.data?.status || ''),
    message: String(order.message || order.user_message || data?.message || data?.user_message || ''),
    email: String(order.account_email || order.email || '').trim(),
    canRetry: order.can_retry === true || data?.can_retry === true,
    order,
  }
}

/** pending：还没查到这次的结果，条目保持原状，下一轮再查；结果未知的到时限标「结果未知」。 */
export type PollVerdict = 'pending' | 'success' | 'failed' | 'processing'

/**
 * 只读查询的一条结果怎么处理（F2-03、FE2 审查重要 1）。unknownPending：这一张是结果未知、还没查到过这次记录的。
 *
 * 后端允许干净失败的卡密重新兑换，这种卡密查结果不回 404，而是回上一次失败的那张单（failed_precharge、can_retry）。
 * 公开结果里没有建单时间、单号这类能区分「这次」和「上次」的字段（只有 status、stage、message、plan、region、
 * 打码邮箱、can_retry、attempts_left；attempts_left 只数走到注资的失败，注资前的失败不减），所以结果未知的条目
 * 查到「失败、可以重新提交」一律当作还没查到，继续查到时限，到时限标「结果未知」，绝不因为旧记录显示「可以重新提交」。
 * 代价：这次新建的单很快干净失败时，显示成「结果未知」而不是「失败」，方向是安全的。
 * 别的失败（declined、failed、cancelled）、处理中、成功都说明卡密已被占用或有了定论，照常处理。
 */
export function pollVerdict(httpStatus: number, polled: Pick<PolledOrder, 'status' | 'canRetry'>, unknownPending: boolean): PollVerdict {
  if (httpStatus < 200 || httpStatus >= 300) return 'pending'
  const st = String(polled.status || '').toLowerCase()
  if (isSuccessStatus(st)) return 'success'
  if (isFailStatus(st)) {
    if (unknownPending && (polled.canRetry || st === 'failed_precharge')) return 'pending'
    return 'failed'
  }
  return 'processing'
}

/**
 * 结果未知的那一张最多查多久。到时限还没查到兑换记录（不管这期间是 404、5xx 还是断线），就停止查询，
 * 条目标「结果未知」，提示去卡密查询页核对、不要重复提交；不判失败（判失败容易让买家重提同一张卡密）。
 * 服务端建单不跟随客户端连接、最多 30 秒（后端 localSubmitLimit），这里留足余量。
 */
export const UNKNOWN_QUERY_MS = 90_000

/** 结果未知的一张是否已到时限。unknownSince 为 0 表示不是结果未知的条目（正常受理的、已查到过记录的），永远不按时限停。 */
export function unknownExpired(unknownSince: number, now: number): boolean {
  return unknownSince > 0 && now - unknownSince >= UNKNOWN_QUERY_MS
}

/**
 * 离最早一张结果未知（没到终态、带 unknownSince）的时限还有多久，毫秒，最少 0；没有就返回 null（FE2 审查次要 1）。
 * 页面按它另排一个计时器检查时限：时限按墙钟算，不跟着查询间隔、429 退避（最长 2 分钟）、挂住的请求往后拖。
 */
export function msUntilUnknownDeadline(
  targets: Readonly<Record<string, { terminal: boolean; unknownSince?: number }>>,
  now: number,
): number | null {
  let earliest = Infinity
  for (const v of Object.values(targets)) {
    const since = v.unknownSince ?? 0
    if (v.terminal || since <= 0) continue
    earliest = Math.min(earliest, since + UNKNOWN_QUERY_MS)
  }
  if (earliest === Infinity) return null
  return Math.max(0, earliest - now)
}

/**
 * 页面顶部的统计。查询中的结果未知是「处理中」，算在处理中；
 * 到时限停查的结果未知（status 'unknown'）单独计，不算失败也不算处理中。
 */
export function batchStats(items: ReadonlyArray<{ status: string }>) {
  const s = { total: items.length, ok: 0, fail: 0, run: 0, wait: 0, unknown: 0 }
  for (const i of items) {
    if (i.status === 'success') s.ok++
    else if (i.status === 'failed' || i.status === 'verify_fail') s.fail++
    else if (i.status === 'unknown') s.unknown++
    else if (i.status === 'processing' || i.status === 'submitting') s.run++
    else if (i.status === 'waiting_session' || i.status === 'verify_ok') s.wait++
  }
  return s
}

/**
 * 这一张还能提交吗：只有「待凭证」「卡密有效」能进手动提交和一键自动提交。
 * 结果未知、处理中、提交中、成功、失败一律不能，同一张卡密绝不提交第二次。
 */
export function isSubmittable(status: string): boolean {
  return status === 'waiting_session' || status === 'verify_ok'
}

interface ExportItem {
  status: string
  cardKey: string
  /** 屏幕显示用的邮箱：兑换回包、查询结果会用服务端打过码的值覆盖它 */
  email: string
  /** 买家自己提供的完整邮箱（FE2 审查重要 2）。导出优先用它；页面拿不到时为空 */
  ownEmail?: string
  gptPassword: string
  emailPassword: string
  accessToken: string
}

/**
 * 买家自己提供的完整邮箱：按顺序取第一个像完整邮箱的（有 @、没有打码的 *），都不像就返回空。
 * 来源只能是买家自己粘贴或导入的登录信息、邮箱密码列表，或核对账号时后端从买家登录信息里读出的；
 * 兑换回包、查询结果里的 account_email 是后端打过码的（local_cdk.go localOrderPublic），不能当来源。
 */
export function ownEmailOf(...candidates: Array<string | null | undefined>): string {
  for (const c of candidates) {
    const e = String(c ?? '').trim()
    if (e.includes('@') && !e.includes('*')) return e
  }
  return ''
}

const exportPart = (value: string) => String(value || '').replace(/[\r\n]+/g, ' ').trim()

/** 导出用的邮箱：买家自己提供的完整邮箱优先；页面从头到尾拿不到时才用显示用的那个（可能是服务端打过码的），不硬造。 */
const exportEmail = (i: ExportItem) => exportPart(i.ownEmail || i.email)

/** 导出本批成功号的表格行：只要开通成功的，值不打码（邮箱见 exportEmail），只把换行压成空格。 */
export function successRows(items: ReadonlyArray<ExportItem>): Array<[string, string, string, string]> {
  return items
    .filter((i) => i.status === 'success')
    .map((i) => [exportEmail(i), exportPart(i.gptPassword), exportPart(i.emailPassword), exportPart(i.accessToken)])
}

/** 导出表里「结果未知」那一页的行：带卡密（拿去卡密查询页核对），最后一列写明结果未知；值不打码。 */
export function unknownRows(items: ReadonlyArray<ExportItem>, label: string): string[][] {
  return items
    .filter((i) => i.status === 'unknown')
    .map((i) => [
      exportPart(i.cardKey),
      exportEmail(i),
      exportPart(i.gptPassword),
      exportPart(i.emailPassword),
      exportPart(i.accessToken),
      label,
    ])
}

/**
 * 还有结果未知、正在查询的卡密吗（没到终态、带 unknownSince）。这时不能重置：重置会停掉查询，
 * 买家就看不到这一张最后有没有开通（F2-04b 裁定 3）。到时限停查或查到记录之后才能重置。
 */
export function hasUnknownQuerying(targets: Readonly<Record<string, { terminal: boolean; unknownSince?: number }>>): boolean {
  return Object.values(targets).some((v) => !v.terminal && (v.unknownSince ?? 0) > 0)
}
