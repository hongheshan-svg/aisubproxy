/** 批量兑换页的轮询策略（审计 F-4）：多个令牌合并成一次请求，在途越多间隔越长，429 时提示并退避。 */

/** 一次批量查询最多带多少个令牌，与后端 lookupResultsMax 一致 */
export const POLL_BATCH_MAX = 100
export const POLL_BASE_MS = 3000
export const POLL_BACKOFF_START_MS = 15000
export const POLL_BACKOFF_MAX_MS = 120000
export const POLL_RATE_LIMIT_NOTICE = '查询太频繁，稍后自动重试'

/** 在途条目越多，轮询间隔越长：每小时一个 IP 最多 6000 次查询，3 秒一次就是 1200 次/小时。 */
export function pollIntervalMs(inFlight: number): number {
  if (inFlight <= 10) return POLL_BASE_MS
  if (inFlight <= 30) return 5000
  if (inFlight <= 60) return 8000
  return 12000
}

/** 429 之后的下一次等待：第一次 15 秒，之后翻倍，封顶 2 分钟。prev 为 0 表示还没退避过。 */
export function nextBackoffMs(prev: number): number {
  if (!(prev > 0)) return POLL_BACKOFF_START_MS
  return Math.min(prev * 2, POLL_BACKOFF_MAX_MS)
}

export function chunkTokens<T>(items: T[], size = POLL_BATCH_MAX): T[][] {
  const out: T[][] = []
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size))
  return out
}

export interface PollResultItem {
  token: string
  status: number
  data: any
}

export interface PollOutcome {
  /** 整个请求被限流（429）：要提示并退避 */
  rateLimited: boolean
  /** 整个请求失败（网络、5xx 等）：下一轮重试，不改条目状态 */
  failed: boolean
  items: PollResultItem[]
}

/** 把批量接口的 HTTP 状态码和响应体整理成统一结果。 */
export function parsePollResponse(httpStatus: number, body: any): PollOutcome {
  if (httpStatus === 429) return { rateLimited: true, failed: false, items: [] }
  if (httpStatus < 200 || httpStatus >= 300 || !body || !Array.isArray(body.results)) {
    return { rateLimited: false, failed: true, items: [] }
  }
  const items: PollResultItem[] = []
  for (const r of body.results) {
    if (!r || typeof r.token !== 'string') continue
    items.push({ token: r.token, status: Number(r.status) || 0, data: r.data ?? null })
  }
  return { rateLimited: false, failed: false, items }
}

/** 下一轮轮询的等待时间与页面提示。rateLimited 时返回退避时间和提示，否则按在途数取间隔并清掉退避。 */
export function nextPollPlan(
  outcome: Pick<PollOutcome, 'rateLimited' | 'failed'>,
  inFlight: number,
  prevBackoff: number,
): { delayMs: number; backoffMs: number; notice: string } {
  if (outcome.rateLimited) {
    const backoffMs = nextBackoffMs(prevBackoff)
    return { delayMs: backoffMs, backoffMs, notice: POLL_RATE_LIMIT_NOTICE }
  }
  return { delayMs: pollIntervalMs(inFlight), backoffMs: 0, notice: '' }
}
