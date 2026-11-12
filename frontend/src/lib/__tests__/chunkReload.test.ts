import { describe, expect, it, vi } from 'vitest'
import { RELOAD_GUARD_KEY, RELOAD_GUARD_MS, isChunkLoadError, recoverFromChunkError } from '../chunkReload'

describe('isChunkLoadError', () => {
  it('认得各浏览器的分包加载失败', () => {
    for (const m of [
      'Failed to fetch dynamically imported module: https://x/assets/Foo-1.js',
      'Importing a module script failed.',
      'error loading dynamically imported module: https://x/assets/Foo-1.js',
      'Unable to preload CSS for /assets/Foo-1.css',
    ]) expect(isChunkLoadError(new TypeError(m))).toBe(true)
  })
  it('普通错误、空值认不出', () => {
    expect(isChunkLoadError(new Error('Network Error'))).toBe(false)
    expect(isChunkLoadError(new TypeError('x is not a function'))).toBe(false)
    expect(isChunkLoadError(null)).toBe(false)
    expect(isChunkLoadError(undefined)).toBe(false)
    expect(isChunkLoadError({})).toBe(false)
  })
})

describe('recoverFromChunkError', () => {
  function setup(opts: { now?: number; store?: Record<string, string>; throwOn?: 'get' | 'set' | 'access' } = {}) {
    const data: Record<string, string> = { ...(opts.store || {}) }
    const reload = vi.fn()
    const toast = vi.fn()
    const storage = () => {
      if (opts.throwOn === 'access') throw new Error('denied')
      return {
        getItem: (k: string) => { if (opts.throwOn === 'get') throw new Error('x'); return k in data ? data[k] : null },
        setItem: (k: string, v: string) => { if (opts.throwOn === 'set') throw new Error('x'); data[k] = v },
      }
    }
    const run = (now: number) => recoverFromChunkError({ now: () => now, storage, reload, toast })
    return { data, reload, toast, run }
  }

  it('第一次：记录时间并刷新', () => {
    const s = setup()
    expect(s.run(100_000)).toBe('reloaded')
    expect(s.reload).toHaveBeenCalledTimes(1)
    expect(s.toast).not.toHaveBeenCalled()
    expect(s.data[RELOAD_GUARD_KEY]).toBe('100000')
  })
  it('10 秒内第二次：不刷新，只提示', () => {
    const s = setup()
    s.run(100_000)
    expect(s.run(100_000 + RELOAD_GUARD_MS - 1)).toBe('toasted')
    expect(s.reload).toHaveBeenCalledTimes(1)
    expect(s.toast).toHaveBeenCalledTimes(1)
  })
  it('超过 10 秒再刷新', () => {
    const s = setup()
    s.run(100_000)
    expect(s.run(100_000 + RELOAD_GUARD_MS)).toBe('reloaded')
    expect(s.reload).toHaveBeenCalledTimes(2)
  })
  it('sessionStorage 读、写、访问抛错：不刷新，只提示', () => {
    for (const throwOn of ['get', 'set', 'access'] as const) {
      const s = setup({ throwOn })
      expect(s.run(1)).toBe('toasted')
      expect(s.reload).not.toHaveBeenCalled()
      expect(s.toast).toHaveBeenCalledTimes(1)
    }
  })
})
