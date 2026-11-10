import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// theme.ts 加载时就会读 localStorage，测试环境没有 DOM，所以先装假对象再动态引入
function installFakes(storage: unknown) {
  const appended: Array<{ href?: string }> = []
  vi.stubGlobal('localStorage', storage)
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false, addEventListener() {} }) })
  vi.stubGlobal('document', {
    documentElement: { classList: { toggle() {} }, setAttribute() {}, style: { setProperty() {} } },
    head: { appendChild: (el: { href?: string }) => appended.push(el) },
    createElement: () => ({ dataset: {} }),
    querySelector: () => null,
  })
  return appended
}

const throwingStorage = {
  getItem() { throw new Error('denied') },
  setItem() { throw new Error('denied') },
  removeItem() { throw new Error('denied') },
}

beforeEach(() => { vi.resetModules() })
afterEach(() => { vi.unstubAllGlobals() })

describe('theme：存储被禁用', () => {
  it('模块能加载，setSkin 不抛，themeMode 照常变化', async () => {
    installFakes(throwingStorage)
    const t = await import('../theme')
    expect(t.themeMode.value).toBe('auto')
    t.setTheme('light')
    expect(t.themeMode.value).toBe('light')
    expect(() => t.setSkin('cyber')).not.toThrow() // preferDark 皮肤会把 light 转 dark
    expect(t.siteSkin.value).toBe('cyber')
    expect(t.themeMode.value).toBe('dark')
  })
})

describe('theme：Google 字体开关', () => {
  const googleLinks = (a: Array<{ href?: string }>) => a.filter((l) => l.href?.includes('fonts.googleapis'))

  it('默认关不插；打开立即插一个；再关再开不重复', async () => {
    const appended = installFakes({ getItem: () => null, setItem() {} })
    const t = await import('../theme')
    t.applyThemeAll()
    expect(googleLinks(appended)).toHaveLength(0)
    t.setSkinFontsAllowed(true)
    expect(googleLinks(appended)).toHaveLength(1)
    t.setSkinFontsAllowed(false)
    t.applyThemeAll()
    expect(googleLinks(appended)).toHaveLength(1)
    t.setSkinFontsAllowed(true)
    expect(googleLinks(appended)).toHaveLength(1)
  })
})

describe('theme：默认皮肤显示名', () => {
  it('编号 zovo 不变，显示名改为 曜夜雾金 / Night Gold，说明里不再提卡台', async () => {
    installFakes({ getItem: () => null, setItem() {}, removeItem() {} })
    const t = await import('../theme')
    const s = t.SKINS.find((x) => x.id === 'zovo')
    expect(s).toBeTruthy()
    expect(s?.label).toBe('曜夜雾金')
    expect(s?.labelEn).toBe('Night Gold')
    expect(s?.blurb).not.toContain('卡台')
    expect(t.SKINS.some((x) => x.label.includes('卡台') || x.labelEn.includes('ZovoCard'))).toBe(false)
  })
})
