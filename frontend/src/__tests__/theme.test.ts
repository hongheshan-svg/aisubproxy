import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// theme.ts 加载时就会读 localStorage，测试环境没有 DOM，所以先装假对象再动态引入
function installFakes(storage: unknown) {
  const appended: Array<{ href?: string }> = []
  const attrs: Record<string, string> = {}
  const vars: Record<string, string> = {}
  vi.stubGlobal('localStorage', storage)
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false, addEventListener() {} }) })
  vi.stubGlobal('document', {
    documentElement: {
      classList: { toggle() {} },
      setAttribute: (k: string, v: string) => { attrs[k] = v },
      style: { setProperty: (k: string, v: string) => { vars[k] = v } },
    },
    head: { appendChild: (el: { href?: string }) => appended.push(el) },
    createElement: () => ({ dataset: {} }),
    querySelector: () => null,
  })
  return Object.assign(appended, { attrs, vars })
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
    const appended = installFakes({ getItem: (k: string) => (k === 'site-skin' ? 'zovo' : null), setItem() {} })
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

describe('theme：深青自助与亮暗配色', () => {
  const store = (skin?: string) => ({
    getItem: (k: string) => (k === 'site-skin' ? skin ?? null : null),
    setItem() {},
    removeItem() {},
  })

  it('皮肤列表第一项是 teal，没记过皮肤时用 teal', async () => {
    installFakes(store())
    const t = await import('../theme')
    expect(t.SKINS[0].id).toBe('teal')
    expect(t.DEFAULT_SKIN).toBe('teal')
    expect(t.siteSkin.value).toBe('teal')
  })

  it('记了不存在的皮肤编号时用 teal', async () => {
    installFakes(store('bogus'))
    const t = await import('../theme')
    expect(t.siteSkin.value).toBe('teal')
  })

  it('记了 zovo 就用 zovo', async () => {
    installFakes(store('zovo'))
    const t = await import('../theme')
    expect(t.siteSkin.value).toBe('zovo')
  })

  it('每套皮肤都有亮暗两组主色，旧字段已去掉', async () => {
    installFakes(store())
    const t = await import('../theme')
    for (const s of t.SKINS) {
      expect(s.light.primary).toMatch(/^#[0-9a-fA-F]{6}$/)
      expect(s.dark.on).toMatch(/^#[0-9a-fA-F]{6}$/)
      expect(s).not.toHaveProperty('primary')
      expect(s).not.toHaveProperty('primaryLight')
    }
    const forest = t.SKINS.find((s) => s.id === 'forest')!
    expect(t.skinTone(forest, false)).toEqual({ primary: '#059669', on: '#ffffff' })
    expect(t.skinTone(forest, true)).toEqual({ primary: '#34d399', on: '#0c1612' })
  })

  it('effectiveDark 四种情况', async () => {
    installFakes(store())
    const t = await import('../theme')
    const get = (id: string) => t.SKINS.find((s) => s.id === id)!
    expect(t.effectiveDark('light', get('forest'), false)).toBe(false)
    expect(t.effectiveDark('dark', get('forest'), false)).toBe(true)
    expect(t.effectiveDark('light', get('cyber'), false)).toBe(true)
    expect(t.effectiveDark('auto', get('forest'), true)).toBe(true)
  })

  it('套用 teal 亮色：写 data-tone 和侧栏变量', async () => {
    const f = installFakes(store())
    const t = await import('../theme')
    t.setTheme('light')
    expect(f.attrs['data-skin']).toBe('teal')
    expect(f.attrs['data-tone']).toBe('light')
    expect(f.vars['--side-bg']).toBe('#0A473F')
    expect(f.vars['--side-ink']).toBe('#C9D7D5')
    expect(f.vars['--side-muted']).toBe('#9DB5B2')
    expect(f.vars['--side-active']).toBe('#0F6B5F')
    expect(f.vars['--side-active-on']).toBe('#FFFFFF')
  })

  it('切到暗色模式和切换皮肤时 data-tone、侧栏变量都跟着重写', async () => {
    const f = installFakes(store())
    const t = await import('../theme')
    t.setTheme('light')
    t.setTheme('dark')
    expect(f.attrs['data-tone']).toBe('dark')
    expect(f.vars['--side-active']).toBe('#3FB8A5')
    t.setTheme('light')
    t.setSkin('forest')
    expect(f.attrs['data-tone']).toBe('light')
    expect(f.vars['--side-active']).toBe('#059669')
  })

  it('默认暗色皮肤选亮色时 data-tone 仍是 dark', async () => {
    const f = installFakes(store())
    const t = await import('../theme')
    t.setTheme('light')
    t.setSkin('cyber')
    expect(f.attrs['data-tone']).toBe('dark')
    expect(f.vars['--side-active']).toBe('#22d3ee')
  })

  it('teal 不加载任何 Google 字体', async () => {
    const appended = installFakes(store())
    const t = await import('../theme')
    t.setSkinFontsAllowed(true)
    expect(appended.length).toBe(0)
  })
})
