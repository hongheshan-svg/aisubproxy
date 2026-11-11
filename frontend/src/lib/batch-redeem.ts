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
 * 兑换请求回来后这一张怎么显示（F2-03）。httpStatus 为 0 表示请求发出后没收到回复（断线、读回复时断开）。
 * - 断线、5xx：服务器可能已经受理（建单不跟随客户端连接），结果未知 → 标「处理中」并只读地查结果，绝不重发；
 * - 其余非 2xx：服务器明确拒绝，没有用掉卡密 → 失败；
 * - 2xx：按单据状态。
 */
export function redeemOutcome(httpStatus: number, orderStatus: string): RedeemOutcome {
  if (httpStatus < 200 || httpStatus >= 500) return 'unknown'
  if (httpStatus >= 300) return 'rejected'
  if (isSuccessStatus(orderStatus)) return 'success'
  if (isFailStatus(orderStatus)) return 'failed'
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
  email: string
  gptPassword: string
  emailPassword: string
  accessToken: string
}

const exportPart = (value: string) => String(value || '').replace(/[\r\n]+/g, ' ').trim()

/** 导出本批成功号的表格行：只要开通成功的，值保持原样（不打码），只把换行压成空格。 */
export function successRows(items: ReadonlyArray<ExportItem>): Array<[string, string, string, string]> {
  return items
    .filter((i) => i.status === 'success')
    .map((i) => [exportPart(i.email), exportPart(i.gptPassword), exportPart(i.emailPassword), exportPart(i.accessToken)])
}

/** 导出表里「结果未知」那一页的行：带卡密（拿去卡密查询页核对），最后一列写明结果未知；值保持原样。 */
export function unknownRows(items: ReadonlyArray<ExportItem>, label: string): string[][] {
  return items
    .filter((i) => i.status === 'unknown')
    .map((i) => [
      exportPart(i.cardKey),
      exportPart(i.email),
      exportPart(i.gptPassword),
      exportPart(i.emailPassword),
      exportPart(i.accessToken),
      label,
    ])
}
