// 版本更新页的进度跟踪（F2-01）。后端的更新状态只在进程内：重载（re-exec）后新进程的状态是 idle。
// - 状态查询失败不等于已经重载：等 /health 通了再问一次状态，还在下载、校验、替换就回去继续轮询；
// - 重载阶段旧进程还活着时 /health 也是通的：状态仍是 reloading 就继续等，回到 idle（或 done）才算结束；
//   后端重载前要先等在途付款单走完（最多 3 分钟），这段单独计时（RELOAD_WAIT_MS），不占「连不上」的次数；
// - 只有一个循环：上一次返回后才排下一次，不会并发；每个请求有超时；stop() 之后不再发请求、不再回调。

export interface UpdateState {
  phase?: string
  message?: string
  progress?: number
  error?: string
}

/** 一次状态查询的结果；null 表示没拿到（断网、超时、重载时网关回 502 等）。 */
export type StatusResult = { running: boolean; state: UpdateState } | null

export interface UpdateWatchDeps {
  fetchStatus: () => Promise<StatusResult>
  checkHealth: () => Promise<boolean>
  sleep: (ms: number) => Promise<void>
  /** 拿到一次进行中的状态：更新进度显示 */
  onState: (st: UpdateState) => void
  onFailed: (st: UpdateState) => void
  /** 服务已恢复且更新已结束：刷新版本并显示结果 */
  onRecovered: () => Promise<void> | void
  onTimeout: () => void
  /** /health 通、状态仍是重载阶段超过 DRAIN_HINT_MS：后端在等在途付款单，waitedMs 是已等的时间 */
  onDraining?: (waitedMs: number) => void
  /** 取当前时间（毫秒），测试里换成跟着 sleep 走的时钟 */
  now?: () => number
}

export const POLL_MS = 800
export const HEALTH_GAP_MS = 500
/** 服务连不上（/health 不通或状态读不到）最多试几次 */
export const HEALTH_TRIES = 40
/**
 * /health 通、状态一直是重载阶段时最多等多久。后端进入重载阶段后先睡 400 毫秒，再等在途付款单进入确认或结束，
 * 最多 payengine.DrainMax = 3 分钟（backend/internal/payengine/drain.go:16，调用在 backend/internal/handler/update.go:412-414），
 * 然后才 re-exec。取 4 分钟：3 分钟加 re-exec 和余量。
 */
export const RELOAD_WAIT_MS = 4 * 60_000
/** 重载阶段持续超过这个时间（正常重载在一秒内），就提示在等进行中的付款单 */
export const DRAIN_HINT_MS = 3_000
/** 单个状态、/health 请求的超时：重载那一刻网关挂住不返回时，不让跟踪停在那里 */
export const REQUEST_TIMEOUT_MS = 5_000

/** 带超时的请求：到点中止（signal）并返回 fallback；请求抛异常也返回 fallback。 */
export async function withTimeout<T>(run: (signal: AbortSignal) => Promise<T>, ms: number, fallback: T): Promise<T> {
  const ctl = new AbortController()
  let timer: ReturnType<typeof setTimeout> | undefined
  const late = new Promise<T>((resolve) => {
    timer = setTimeout(() => { ctl.abort(); resolve(fallback) }, ms)
  })
  try {
    return await Promise.race([run(ctl.signal).catch(() => fallback), late])
  } finally {
    clearTimeout(timer)
  }
}

/** 轮询拿到一次状态后：继续轮询、去等服务恢复、或显示失败。 */
export function pollStep(res: StatusResult): 'poll' | 'reload' | 'failed' {
  if (!res) return 'reload'
  const phase = res.state.phase || ''
  if (phase === 'failed') return 'failed'
  // 没在进行中了也去等：轮询间隔里可能没见到重载阶段，新进程就已经起来了
  if (phase === 'reloading' || !res.running) return 'reload'
  return 'poll'
}

/** /health 通了以后再问到的状态：继续等、回去轮询、显示失败、或已结束。 */
export function afterHealth(res: StatusResult): 'wait' | 'poll' | 'failed' | 'finished' {
  if (!res) return 'wait'
  const phase = res.state.phase || ''
  if (phase === 'failed') return 'failed'
  if (phase === 'reloading') return 'wait' // 旧进程还没重载完
  if (res.running) return 'poll' // 刚才只是网络抖了一下，更新还在跑
  return 'finished'
}

export function createUpdateWatcher(deps: UpdateWatchDeps) {
  let stopped = false
  let runP: Promise<void> | null = null

  const status = async (): Promise<StatusResult> => {
    try { return await deps.fetchStatus() } catch { return null }
  }
  const healthy = async (): Promise<boolean> => {
    try { return await deps.checkHealth() } catch { return false }
  }

  const now = deps.now ?? Date.now

  // 等服务恢复；返回 'poll' 表示更新其实还在跑，要回去轮询。
  // 两种等待分开算：连不上（/health 不通、状态读不到）最多 HEALTH_TRIES 次；
  // 旧进程还活着、状态仍是重载阶段（后端在等在途付款单）从第一次见到起最多 RELOAD_WAIT_MS。
  async function waitHealthy(): Promise<'poll' | 'end'> {
    let misses = 0
    let reloadingSince = -1
    for (;;) {
      await deps.sleep(HEALTH_GAP_MS)
      if (stopped) return 'end'
      const ok = await healthy()
      if (stopped) return 'end'
      if (ok) {
        const res = await status()
        if (stopped) return 'end'
        const next = afterHealth(res)
        if (next === 'failed') { deps.onFailed(res!.state); return 'end' }
        if (next === 'poll') { deps.onState(res!.state); return 'poll' }
        if (next === 'finished') { await deps.onRecovered(); return 'end' }
        if (res && res.state.phase === 'reloading') {
          const t = now()
          if (reloadingSince < 0) reloadingSince = t
          const waited = t - reloadingSince
          if (waited >= RELOAD_WAIT_MS) break
          if (waited >= DRAIN_HINT_MS) deps.onDraining?.(waited)
          continue
        }
      }
      if (++misses >= HEALTH_TRIES) break
    }
    if (!stopped) deps.onTimeout()
    return 'end'
  }

  async function run() {
    for (;;) {
      if (stopped) return
      const res = await status()
      if (stopped) return
      if (res) deps.onState(res.state)
      const step = pollStep(res)
      if (step === 'failed') { deps.onFailed(res!.state); return }
      if (step === 'reload' && (await waitHealthy()) === 'end') return
      await deps.sleep(POLL_MS)
    }
  }

  return {
    /** 开始跟踪；重复调用返回同一个循环。 */
    start(): Promise<void> {
      if (!runP) runP = run()
      return runP
    },
    /** 离开页面时调用：之后不再发请求、不再回调。 */
    stop() { stopped = true },
  }
}
