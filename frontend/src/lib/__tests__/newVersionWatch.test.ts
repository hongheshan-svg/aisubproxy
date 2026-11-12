import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  FETCH_TIMEOUT_MS, MIN_GAP_MS, POLL_INTERVAL_MS, checkNewVersionOnce, createVersionWatcher, entryScriptOf, shouldFullReload,
} from '../newVersionWatch'

describe('entryScriptOf', () => {
  it('属性顺序不同、带 crossorigin 都能取到', () => {
    expect(entryScriptOf('<script type="module" crossorigin src="/assets/index-AAA.js"></script>')).toBe('/assets/index-AAA.js')
    expect(entryScriptOf('<script src="/assets/index-BBB.js" crossorigin type="module"></script>')).toBe('/assets/index-BBB.js')
    expect(entryScriptOf("<script type='module' src='/assets/i-C.js'></script>")).toBe('/assets/i-C.js')
  })
  it('带 base 路径', () => {
    expect(entryScriptOf('<script type="module" src="/sub/assets/index-1.js"></script>')).toBe('/sub/assets/index-1.js')
  })
  it('绝对 URL 只取路径名，查询串去掉', () => {
    expect(entryScriptOf('<script type="module" src="https://cdn.example.com/assets/index-2.js?v=1"></script>')).toBe('/assets/index-2.js')
  })
  it('跳过非模块脚本和没有 src 的模块脚本；没有入口返回 null', () => {
    const html = '<script src="/a.js"></script><script type="module">x()</script><script type="module" src="/assets/m.js"></script>'
    expect(entryScriptOf(html)).toBe('/assets/m.js')
    expect(entryScriptOf('<html><body>no script</body></html>')).toBeNull()
    expect(entryScriptOf('')).toBeNull()
  })
})

function stubDocument(src: string | null) {
  vi.stubGlobal('document', {
    querySelector: () => (src === null ? null : { getAttribute: () => src }),
  })
}
const page = (entry: string) => `<html><script type="module" crossorigin src="${entry}"></script></html>`

describe('checkNewVersionOnce', () => {
  beforeEach(() => { stubDocument('/assets/index-OLD.js') })
  afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

  const reply = (status: number, body: string) =>
    vi.stubGlobal('fetch', vi.fn(async () => ({ status, ok: status === 200, text: async () => body })))

  it('入口相同：没有新版本', async () => {
    reply(200, page('/assets/index-OLD.js'))
    expect(await checkNewVersionOnce()).toBe(false)
  })
  it('入口不同：有新版本，且用 no-store 取 index.html', async () => {
    reply(200, page('/assets/index-NEW.js'))
    expect(await checkNewVersionOnce()).toBe(true)
    const [url, init] = (fetch as any).mock.calls[0]
    expect(String(url)).toMatch(/index\.html$/)
    expect(init.cache).toBe('no-store')
  })
  it('fetch 抛错：没有新版本', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('network') }))
    expect(await checkNewVersionOnce()).toBe(false)
  })
  it('超时：没有新版本', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
    const p = checkNewVersionOnce()
    await vi.advanceTimersByTimeAsync(FETCH_TIMEOUT_MS + 1)
    expect(await p).toBe(false)
  })
  it('非 200（含 404、502、304）：没有新版本', async () => {
    for (const st of [404, 502, 304]) {
      reply(st, page('/assets/index-NEW.js'))
      expect(await checkNewVersionOnce()).toBe(false)
    }
  })
  it('返回内容里找不到入口：没有新版本', async () => {
    reply(200, '<html>maintenance</html>')
    expect(await checkNewVersionOnce()).toBe(false)
  })
  it('当前页面取不到入口：没有新版本，也不发请求', async () => {
    stubDocument(null)
    reply(200, page('/assets/index-NEW.js'))
    expect(await checkNewVersionOnce()).toBe(false)
    expect(fetch).not.toHaveBeenCalled()
  })
})

describe('检查时机', () => {
  let visible: boolean
  let fire: () => void
  let removed: number
  let check: ReturnType<typeof vi.fn>
  let found: ReturnType<typeof vi.fn>

  function make(result: boolean | (() => boolean) = false) {
    visible = true
    removed = 0
    fire = () => {}
    check = vi.fn(async () => (typeof result === 'function' ? result() : result))
    found = vi.fn()
    return createVersionWatcher({
      check,
      isVisible: () => visible,
      onVisibilityChange: (cb) => { fire = cb; return () => { removed++ } },
      onFound: found,
    })
  }
  beforeEach(() => { vi.useFakeTimers() })
  afterEach(() => { vi.useRealTimers() })

  it('页面刚加载不检查；第一次变为可见立即检查', async () => {
    const w = make(); w.start()
    expect(check).not.toHaveBeenCalled()
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(check).toHaveBeenCalledTimes(1)
    w.stop()
  })
  it('标签页不可见时不检查', async () => {
    const w = make(); w.start()
    visible = false
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(check).not.toHaveBeenCalled()
    w.stop()
  })
  it('60 秒内不重复检查，满 60 秒再检查', async () => {
    const w = make(); w.start()
    fire(); await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(MIN_GAP_MS - 1000)
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(check).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1000)
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(check).toHaveBeenCalledTimes(2)
    w.stop()
  })
  it('可见期间每 5 分钟检查一次；不可见的那一拍跳过', async () => {
    const w = make(); w.start()
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS)
    expect(check).toHaveBeenCalledTimes(1)
    visible = false
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS)
    expect(check).toHaveBeenCalledTimes(1)
    visible = true
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS)
    expect(check).toHaveBeenCalledTimes(2)
    w.stop()
  })
  it('检测到新版本：通知一次，之后停止检查并取消订阅', async () => {
    const w = make(true); w.start()
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(found).toHaveBeenCalledTimes(1)
    expect(removed).toBe(1)
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS * 3)
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(check).toHaveBeenCalledTimes(1)
    expect(found).toHaveBeenCalledTimes(1)
  })
  it('检查本身抛错不当作新版本，也不影响后续检查', async () => {
    const w = make(() => { throw new Error('x') }); w.start()
    fire(); await vi.advanceTimersByTimeAsync(0)
    expect(found).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(POLL_INTERVAL_MS)
    expect(check).toHaveBeenCalledTimes(2)
    w.stop()
  })
})

describe('shouldFullReload', () => {
  const base = { hasNew: true, initial: false, toPath: '/ops/b', fromPath: '/ops/a' }
  it('已有新版本且换页：整页跳转', () => { expect(shouldFullReload(base)).toBe(true) })
  it('没有新版本：不跳', () => { expect(shouldFullReload({ ...base, hasNew: false })).toBe(false) })
  it('首次导航：不跳', () => { expect(shouldFullReload({ ...base, initial: true })).toBe(false) })
  it('只改 query 或 hash（路径相同）：不跳', () => {
    expect(shouldFullReload({ ...base, toPath: '/ops/a' })).toBe(false)
  })
})
