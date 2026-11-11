// 版本更新页的进度跟踪（F2-01，用户选 1）：
// - 状态查询失败不等于「已经重载」：/health 通了以后再问一次更新状态，还在下载、校验、替换就回去继续轮询；
// - 重载阶段旧进程还活着时 /health 也是通的，要等到新进程（状态回到 idle）才算结束；
// - 轮询上一次返回后才排下一次，同一时间只有一个请求；离开页面后不再发请求。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import {
  DRAIN_HINT_MS, HEALTH_GAP_MS, HEALTH_TRIES, RELOAD_WAIT_MS, REQUEST_TIMEOUT_MS, afterHealth, createUpdateWatcher, pollStep, withTimeout,
  type StatusResult, type UpdateWatchDeps,
} from '../updateWatch'

const st = (phase: string, running = !['idle', 'done', 'failed'].includes(phase)): StatusResult =>
  ({ running, state: { phase, message: phase, progress: 10, error: phase === 'failed' ? 'boom' : undefined } })

/** 按脚本回答状态和健康检查；记录调用顺序。 */
function fake(statuses: StatusResult[], health: boolean[] = []) {
  const log: string[] = []
  let inFlight = 0
  let maxInFlight = 0
  const deps: UpdateWatchDeps = {
    fetchStatus: vi.fn(async () => {
      inFlight++
      maxInFlight = Math.max(maxInFlight, inFlight)
      await Promise.resolve()
      inFlight--
      const r = statuses.length ? statuses.shift()! : st('idle')
      log.push('status:' + (r ? r.state.phase : 'fail'))
      return r
    }),
    checkHealth: vi.fn(async () => {
      const ok = health.length ? health.shift()! : true
      log.push('health:' + ok)
      return ok
    }),
    sleep: vi.fn(async () => {}),
    onState: vi.fn(),
    onFailed: vi.fn(),
    onRecovered: vi.fn(async () => { log.push('recovered') }),
    onTimeout: vi.fn(() => { log.push('timeout') }),
  }
  return { deps, log, maxInFlight: () => maxInFlight }
}

describe('pollStep：轮询时拿到一次状态后怎么走', () => {
  it('请求失败（断网、网关 502）：去等服务恢复，不直接宣布结果', () => {
    expect(pollStep(null)).toBe('reload')
  })
  it('失败阶段：显示失败', () => {
    expect(pollStep(st('failed'))).toBe('failed')
  })
  it('重载阶段、或已不在进行中（没见到重载阶段新进程就起来了）：去等服务恢复', () => {
    expect(pollStep(st('reloading'))).toBe('reload')
    expect(pollStep(st('idle'))).toBe('reload')
    expect(pollStep(st('done'))).toBe('reload')
  })
  it('下载、校验、替换中：继续轮询', () => {
    for (const p of ['checking', 'downloading', 'verifying', 'staging', 'applying']) expect(pollStep(st(p))).toBe('poll')
  })
})

describe('afterHealth：/health 通了以后再问到的状态怎么走', () => {
  it('状态还读不到：继续等', () => {
    expect(afterHealth(null)).toBe('wait')
  })
  it('还在下载等阶段：刚才只是网络抖了一下，回去继续轮询', () => {
    expect(afterHealth(st('downloading'))).toBe('poll')
    expect(afterHealth(st('applying'))).toBe('poll')
  })
  it('还是重载阶段：旧进程还没重载完，继续等（新进程起来后是 idle）', () => {
    expect(afterHealth(st('reloading'))).toBe('wait')
  })
  it('失败：显示失败', () => {
    expect(afterHealth(st('failed'))).toBe('failed')
  })
  it('idle 或 done：更新已结束，显示结果', () => {
    expect(afterHealth(st('idle'))).toBe('finished')
    expect(afterHealth(st('done'))).toBe('finished')
  })
})

describe('createUpdateWatcher', () => {
  it('下载中有一次状态请求失败：/health 通了再问一次，还在下载就继续轮询，不显示「服务已恢复」', async () => {
    const f = fake([st('downloading'), null, st('downloading'), st('downloading'), st('reloading'), st('idle')])
    await createUpdateWatcher(f.deps).start()
    expect(f.log).toEqual([
      'status:downloading',
      'status:fail', 'health:true', 'status:downloading', // 抖动后回到轮询
      'status:downloading',
      'status:reloading', 'health:true', 'status:idle',
      'recovered',
    ])
    expect(f.deps.onRecovered).toHaveBeenCalledTimes(1)
    expect(f.deps.onFailed).not.toHaveBeenCalled()
  })

  it('重载阶段旧进程还活着：/health 通了但状态仍是重载，继续等，直到 idle 才算结束', async () => {
    const f = fake([st('reloading'), st('reloading'), null, st('idle')], [true, false, true, true])
    await createUpdateWatcher(f.deps).start()
    expect(f.log).toEqual([
      'status:reloading',
      'health:true', 'status:reloading',
      'health:false',
      'health:true', 'status:fail',
      'health:true', 'status:idle',
      'recovered',
    ])
  })

  it('没见到重载阶段新进程就起来了（状态回到 idle）：也能收尾，不会一直转', async () => {
    const f = fake([st('applying'), st('idle'), st('idle')])
    await createUpdateWatcher(f.deps).start()
    expect(f.log).toEqual(['status:applying', 'status:idle', 'health:true', 'status:idle', 'recovered'])
  })

  it('状态是失败：显示失败，不再请求', async () => {
    const f = fake([st('downloading'), st('failed')])
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.onFailed).toHaveBeenCalledWith(expect.objectContaining({ phase: 'failed', error: 'boom' }))
    expect(f.deps.onRecovered).not.toHaveBeenCalled()
    expect(f.log).toEqual(['status:downloading', 'status:failed'])
  })

  it('重载后再问到失败（重载失败）：显示失败', async () => {
    const f = fake([st('reloading'), st('failed')])
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.onFailed).toHaveBeenCalledTimes(1)
    expect(f.deps.onRecovered).not.toHaveBeenCalled()
  })

  it('/health 一直不通：试满次数后报超时', async () => {
    const f = fake([null], Array(HEALTH_TRIES).fill(false))
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.checkHealth).toHaveBeenCalledTimes(HEALTH_TRIES)
    expect(f.deps.onTimeout).toHaveBeenCalledTimes(1)
    expect(f.deps.onRecovered).not.toHaveBeenCalled()
  })

  it('防重入：同一时间只有一个状态请求；重复 start 不会起第二个循环', async () => {
    const f = fake([st('downloading'), st('downloading'), st('downloading'), st('idle'), st('idle')])
    const w = createUpdateWatcher(f.deps)
    await Promise.all([w.start(), w.start(), w.start()])
    expect(f.maxInFlight()).toBe(1)
    expect(f.deps.onRecovered).toHaveBeenCalledTimes(1)
    expect(f.log.filter((x) => x === 'recovered')).toHaveLength(1)
  })

  it('离开页面（stop）后不再发请求，也不显示结果', async () => {
    const f = fake([st('downloading'), st('downloading'), st('idle')])
    const w = createUpdateWatcher(f.deps)
    let release!: () => void
    // 第一次排下一轮时停住，模拟这时离开页面
    ;(f.deps.sleep as any).mockImplementationOnce(() => new Promise<void>((r) => { release = r }))
    const run = w.start()
    await vi.waitFor(() => expect(f.deps.sleep).toHaveBeenCalledTimes(1))
    w.stop()
    release()
    await run
    expect(f.deps.fetchStatus).toHaveBeenCalledTimes(1)
    expect(f.deps.checkHealth).not.toHaveBeenCalled()
    expect(f.deps.onRecovered).not.toHaveBeenCalled()
    expect(f.deps.onTimeout).not.toHaveBeenCalled()
  })

  it('等服务恢复时离开页面：不再查 /health，也不报超时', async () => {
    const f = fake([null])
    const w = createUpdateWatcher(f.deps)
    let release!: () => void
    ;(f.deps.sleep as any).mockImplementationOnce(() => new Promise<void>((r) => { release = r }))
    const run = w.start()
    await vi.waitFor(() => expect(f.deps.sleep).toHaveBeenCalledTimes(1))
    w.stop()
    release()
    await run
    expect(f.deps.checkHealth).not.toHaveBeenCalled()
    expect(f.deps.onTimeout).not.toHaveBeenCalled()
  })

  it('依赖抛异常也当作请求失败，不让循环中断', async () => {
    const f = fake([st('downloading'), st('idle'), st('idle')])
    ;(f.deps.fetchStatus as any).mockImplementationOnce(async () => { throw new Error('net') })
    ;(f.deps.checkHealth as any).mockImplementationOnce(async () => { throw new Error('net') })
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.onRecovered).toHaveBeenCalledTimes(1)
  })
})

// 审查修复（重要 1）：后端进入重载阶段后先等在途付款单走完（最多 payengine.DrainMax = 3 分钟）再 re-exec，
// 这段时间旧进程活着，/health 通、状态一直是 reloading。这段单独计时，不占「服务连不上」的 40 次。
describe('重载前等在途付款单（审查修复 F2-01）', () => {
  /** 时钟跟着 sleep 走 */
  function clocked(f: ReturnType<typeof fake>) {
    let clock = 0
    f.deps.now = () => clock
    f.deps.sleep = vi.fn(async (ms: number) => { clock += ms })
    f.deps.onDraining = vi.fn()
    return { elapsed: () => clock }
  }

  it('上限不短于 4 分钟（后端最多等 3 分钟，再加 re-exec 和余量）', () => {
    expect(RELOAD_WAIT_MS).toBeGreaterThanOrEqual(4 * 60_000)
  })

  it('/health 通、状态是重载阶段持续 60 秒后变成 idle：显示已恢复，不报超时；期间提示在等付款单', async () => {
    const n = 60_000 / HEALTH_GAP_MS
    const f = fake([st('reloading'), ...Array(n).fill(st('reloading')), st('idle')])
    const c = clocked(f)
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.onTimeout).not.toHaveBeenCalled()
    expect(f.deps.onRecovered).toHaveBeenCalledTimes(1)
    expect(c.elapsed()).toBeGreaterThanOrEqual(60_000)
    expect(f.deps.onDraining).toHaveBeenCalled()
    const waited = (f.deps.onDraining as any).mock.calls.map((a: number[]) => a[0])
    expect(Math.min(...waited)).toBeGreaterThanOrEqual(DRAIN_HINT_MS)
  })

  it('重载阶段一直不结束：等满 RELOAD_WAIT_MS 才报超时', async () => {
    const f = fake([])
    ;(f.deps.fetchStatus as any).mockImplementation(async () => st('reloading'))
    const c = clocked(f)
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.onTimeout).toHaveBeenCalledTimes(1)
    expect(f.deps.onRecovered).not.toHaveBeenCalled()
    expect(c.elapsed()).toBeGreaterThanOrEqual(RELOAD_WAIT_MS)
    expect(c.elapsed()).toBeLessThan(RELOAD_WAIT_MS + 5_000)
  })

  it('正常重载（亚秒级）：不显示「在等付款单」', async () => {
    const f = fake([st('reloading'), st('reloading'), st('idle')])
    clocked(f)
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.onRecovered).toHaveBeenCalledTimes(1)
    expect(f.deps.onDraining).not.toHaveBeenCalled()
  })

  it('等付款单之后服务连不上（re-exec 中）：连不上的次数照旧另算 40 次', async () => {
    const n = 10_000 / HEALTH_GAP_MS
    const f = fake([st('reloading'), ...Array(n).fill(st('reloading'))], [...Array(n).fill(true), ...Array(HEALTH_TRIES).fill(false)])
    clocked(f)
    await createUpdateWatcher(f.deps).start()
    expect(f.deps.checkHealth).toHaveBeenCalledTimes(n + HEALTH_TRIES)
    expect(f.deps.onTimeout).toHaveBeenCalledTimes(1)
  })
})

describe('withTimeout：状态和 /health 请求带超时（审查修复 F2-01）', () => {
  it('到点没返回：中止请求，当作没拿到', async () => {
    vi.useFakeTimers()
    try {
      let signal!: AbortSignal
      const p = withTimeout((s) => { signal = s; return new Promise<string>(() => {}) }, REQUEST_TIMEOUT_MS, 'fallback')
      await vi.advanceTimersByTimeAsync(REQUEST_TIMEOUT_MS)
      expect(await p).toBe('fallback')
      expect(signal.aborted).toBe(true)
    } finally { vi.useRealTimers() }
  })
  it('按时返回：用返回值，不中止', async () => {
    let signal!: AbortSignal
    expect(await withTimeout(async (s) => { signal = s; return 'ok' }, 1000, 'fallback')).toBe('ok')
    expect(signal.aborted).toBe(false)
  })
  it('请求抛异常：当作没拿到', async () => {
    expect(await withTimeout(async () => { throw new Error('net') }, 1000, null)).toBeNull()
  })
})

describe('版本更新页接线（F2-01）', () => {
  const src = readFileSync(resolve(__dirname, '../../views/admin/SystemUpdate.vue'), 'utf8')
  it('用 createUpdateWatcher 跟踪进度，不再用 setInterval 轮询', () => {
    expect(src).toContain('createUpdateWatcher(')
    expect(src).not.toContain('setInterval')
    expect(src).not.toContain('function waitForReload')
  })
  it('离开页面时停掉跟踪；提交更新返回时已离开就不再开始跟踪', () => {
    expect(src).toMatch(/onBeforeUnmount\(\(\) => \{[^}]*unmounted = true[^}]*watcher\?\.stop\(\)/)
    expect(src).toMatch(/if \(unmounted\) return\s*\n\s*watcher\?\.stop\(\)\s*\n\s*watcher = createUpdateWatcher\(/)
  })
  it('状态和 /health 请求都带超时（审查修复）', () => {
    expect(src).toMatch(/withTimeout\(\s*async \(signal\) => \{[\s\S]*?authFetch\('\/api\/v1\/admin\/system\/update\/status', \{ signal \}\)/)
    expect(src).toMatch(/withTimeout\(\s*async \(signal\) => \(await fetch\('\/health', \{ credentials: 'include', signal \}\)\)\.ok/)
  })
  it('重载前在等付款单时提示原因，不报重载超时（审查修复）', () => {
    expect(src).toMatch(/onDraining: \(ms\) => \{[^}]*正在等进行中的付款单结束/)
  })
})
