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
 * 结果未知的那一张，查询一直回 404（没有兑换记录）要等多久才下结论。
 * 服务端建单不跟随客户端连接、最多 30 秒（后端 localSubmitLimit），这里留足余量。
 */
export const UNKNOWN_NOT_FOUND_MS = 90_000

/**
 * 结果未知的一张，这次查询回来不是 2xx 时怎么办：
 * - 'not_found'：从结果未知起已过 UNKNOWN_NOT_FOUND_MS 仍查不到兑换记录（404）→ 卡密没被用掉，停止查询、提示去卡密查询页核对；
 * - 'keep'：还在等服务端建单、暂时出错（5xx、429 等），保持原状下一轮再查。
 * unknownSince 为 0 表示不是结果未知的条目（正常受理的），一律 'keep'。
 */
export function unknownLookupVerdict(httpStatus: number, unknownSince: number, now: number): 'not_found' | 'keep' {
  if (!(unknownSince > 0) || httpStatus !== 404) return 'keep'
  return now - unknownSince >= UNKNOWN_NOT_FOUND_MS ? 'not_found' : 'keep'
}

/** 页面顶部的四格统计。结果未知的那一张是「处理中」，算在处理中，不算失败。 */
export function batchStats(items: ReadonlyArray<{ status: string }>) {
  const s = { total: items.length, ok: 0, fail: 0, run: 0, wait: 0 }
  for (const i of items) {
    if (i.status === 'success') s.ok++
    else if (i.status === 'failed' || i.status === 'verify_fail') s.fail++
    else if (i.status === 'processing' || i.status === 'submitting') s.run++
    else if (i.status === 'waiting_session' || i.status === 'verify_ok') s.wait++
  }
  return s
}

interface ExportItem {
  status: string
  email: string
  gptPassword: string
  emailPassword: string
  accessToken: string
}

/** 导出本批成功号的表格行：只要开通成功的，值保持原样（不打码），只把换行压成空格。 */
export function successRows(items: ReadonlyArray<ExportItem>): string[][] {
  const part = (value: string) => String(value || '').replace(/[\r\n]+/g, ' ').trim()
  return items
    .filter((i) => i.status === 'success')
    .map((i) => [part(i.email), part(i.gptPassword), part(i.emailPassword), part(i.accessToken)])
}
