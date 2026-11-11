// setLocale 在存储被禁用时也要生效（F1-05）。i18n/index.ts 加载时要读 document，所以先装假对象再动态引入。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

beforeEach(() => { vi.resetModules() })
afterEach(() => { vi.unstubAllGlobals() })

// vue 的 runtime-dom 加载时会碰 document.createElement 等，给个最小的假对象
const fakeDoc = () => ({ documentElement: { lang: '' }, createElement: () => ({}), createElementNS: () => ({}), querySelector: () => null })

describe('setLocale', () => {
  it('localStorage.setItem 抛错时不抛，语言和 html lang 照常切换', async () => {
    const doc = fakeDoc()
    vi.stubGlobal('document', doc)
    vi.stubGlobal('localStorage', {
      getItem() { throw new Error('denied') },
      setItem() { throw new Error('denied') },
    })
    const m = await import('../index')
    expect(() => m.setLocale('en')).not.toThrow()
    expect(m.i18n.global.locale.value).toBe('en')
    expect(doc.documentElement.lang).toBe('en')
  })

  it('正常时仍写入本地存储', async () => {
    const data: Record<string, string> = {}
    vi.stubGlobal('document', fakeDoc())
    vi.stubGlobal('localStorage', { getItem: (k: string) => data[k] ?? null, setItem: (k: string, v: string) => { data[k] = v } })
    const m = await import('../index')
    m.setLocale('en')
    expect(data.locale).toBe('en')
  })
})
