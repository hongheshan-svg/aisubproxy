import { describe, expect, it, vi } from 'vitest'
import { RELOAD_MARK_KEY, clearReloadMark, isChunkLoadError, recoverFromChunkError } from '../chunkReload'

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

describe('recoverFromChunkError（标记 + 换页成功后清掉）', () => {
  function setup(opts: { throwOn?: 'get' | 'set' | 'access' | 'remove'; online?: boolean } = {}) {
    const data: Record<string, string> = {}
    const reload = vi.fn()
    const toast = vi.fn()
    const storage = () => {
      if (opts.throwOn === 'access') throw new Error('denied')
      return {
        getItem: (k: string) => { if (opts.throwOn === 'get') throw new Error('x'); return k in data ? data[k] : null },
        setItem: (k: string, v: string) => { if (opts.throwOn === 'set') throw new Error('x'); data[k] = v },
        removeItem: (k: string) => { if (opts.throwOn === 'remove') throw new Error('x'); delete data[k] },
      }
    }
    const run = (target = '/ops/audit') => recoverFromChunkError({ storage, reload, toast, target, isOnline: () => opts.online !== false })
    return { data, reload, toast, run, storage }
  }

  it('第一次：写标记（目标地址）并刷新', () => {
    const s = setup()
    expect(s.run('/ops/audit')).toBe('reloaded')
    expect(s.reload).toHaveBeenCalledTimes(1)
    expect(s.toast).not.toHaveBeenCalled()
    expect(s.data[RELOAD_MARK_KEY]).toBe('/ops/audit')
  })
  it('标记还在（不管隔了多久）：不刷新，只提示', () => {
    const s = setup()
    s.run()
    expect(s.run()).toBe('toasted')
    expect(s.run()).toBe('toasted')
    expect(s.reload).toHaveBeenCalledTimes(1)
    expect(s.toast).toHaveBeenCalledTimes(2)
  })
  it('换页成功清掉标记后，下次失败又能刷新', () => {
    const s = setup()
    s.run()
    clearReloadMark(s.storage)
    expect(s.data[RELOAD_MARK_KEY]).toBeUndefined()
    expect(s.run()).toBe('reloaded')
    expect(s.reload).toHaveBeenCalledTimes(2)
  })
  it('sessionStorage 读、写、访问抛错：不刷新，只提示；清标记抛错不外泄', () => {
    for (const throwOn of ['get', 'set', 'access'] as const) {
      const s = setup({ throwOn })
      expect(s.run()).toBe('toasted')
      expect(s.reload).not.toHaveBeenCalled()
      expect(s.toast).toHaveBeenCalledTimes(1)
    }
    const s = setup({ throwOn: 'remove' })
    expect(() => clearReloadMark(s.storage)).not.toThrow()
  })
  it('断网：不刷新、不写标记，只提示', () => {
    const s = setup({ online: false })
    expect(s.run()).toBe('toasted')
    expect(s.reload).not.toHaveBeenCalled()
    expect(s.data[RELOAD_MARK_KEY]).toBeUndefined()
    expect(s.toast).toHaveBeenCalledTimes(1)
  })
})
