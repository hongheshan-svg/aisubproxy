import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

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
    expect(() => t.setSkin('cyber')).not.toThrow()
    expect(t.siteSkin.value).toBe('cyber')
    expect(t.themeMode.value).toBe('light') // F1-04：换皮肤不再改写访客的明暗选择
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
    expect(t.skinTone(forest, false)).toEqual({ primary: '#05825b', on: '#ffffff' })
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
    expect(f.vars['--side-active']).toBe('#05825b')
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


// 带真实读写行为的假存储
function memStore(init: Record<string, string> = {}) {
  const data: Record<string, string> = { ...init }
  return Object.assign(
    {
      getItem: (k: string) => (k in data ? data[k] : null),
      setItem: (k: string, v: string) => { data[k] = v },
      removeItem: (k: string) => { delete data[k] },
    },
    { data },
  )
}

describe('theme：站点默认明暗不写成访客偏好（F1-01）', () => {
  it('空存储下 applySiteThemeMode(dark)：生效、写 site-theme-mode、不写 theme-mode', async () => {
    const st = memStore()
    installFakes(st)
    const t = await import('../theme')
    t.applySiteThemeMode('dark')
    expect(t.themeMode.value).toBe('dark')
    expect(st.data['site-theme-mode']).toBe('dark')
    expect(st.data['theme-mode']).toBeUndefined()
  })

  it('本机选过就不跟站点', async () => {
    const st = memStore({ 'theme-mode': 'light' })
    installFakes(st)
    const t = await import('../theme')
    t.applySiteThemeMode('dark')
    expect(t.themeMode.value).toBe('light')
    expect(st.data['site-theme-mode']).toBe('dark')
    expect(st.data['theme-mode']).toBe('light')
  })

  it('站点默认之后被管理员改了，没有本机偏好的访客跟着变', async () => {
    const st = memStore()
    installFakes(st)
    const t = await import('../theme')
    t.applySiteThemeMode('dark')
    t.applySiteThemeMode('light')
    expect(t.themeMode.value).toBe('light')
  })

  it('非法值忽略', async () => {
    const st = memStore()
    installFakes(st)
    const t = await import('../theme')
    t.applySiteThemeMode('purple' as never)
    expect(t.themeMode.value).toBe('auto')
    expect(st.data['site-theme-mode']).toBeUndefined()
  })

  it('没有本机偏好时启动先用上次的站点默认', async () => {
    installFakes(memStore({ 'site-theme-mode': 'dark' }))
    const t = await import('../theme')
    expect(t.themeMode.value).toBe('dark')
  })

  it('存储被禁用时 applySiteThemeMode 不抛', async () => {
    installFakes(throwingStorage)
    const t = await import('../theme')
    expect(() => t.applySiteThemeMode('dark')).not.toThrow()
    expect(t.themeMode.value).toBe('dark')
  })

  it('访客自己点切换（setTheme）仍然落盘到 theme-mode', async () => {
    const st = memStore()
    installFakes(st)
    const t = await import('../theme')
    t.setTheme('dark')
    expect(st.data['theme-mode']).toBe('dark')
  })

  it('main.ts 不再用 setTheme 写站点默认；index.html 首屏脚本回退读 site-theme-mode', () => {
    const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
    expect(read('../main.ts')).not.toMatch(/\bsetTheme\(/)
    expect(read('../main.ts')).toContain('applySiteThemeMode')
    expect(read('../../index.html')).toContain('site-theme-mode')
  })
})

describe('theme：只有暗色的皮肤一律按暗色（F1-04）', () => {
  it('setSkin 到赛博/余烬/黑白，本机的 theme-mode 不被改写', async () => {
    for (const id of ['cyber', 'ember', 'noir'] as const) {
      vi.resetModules()
      const st = memStore({ 'theme-mode': 'light' })
      installFakes(st)
      const t = await import('../theme')
      t.setSkin(id)
      expect(st.data['theme-mode']).toBe('light')
      expect(t.themeMode.value).toBe('light')
    }
  })

  it('isDark 在这三套皮肤下恒为真，themeLocked 为真；其他皮肤不锁', async () => {
    installFakes(memStore({ 'theme-mode': 'light' }))
    const t = await import('../theme')
    t.setSkin('cyber')
    expect(t.isDark()).toBe(true)
    expect(t.themeLocked.value).toBe(true)
    t.setSkin('forest')
    expect(t.isDark()).toBe(false)
    expect(t.themeLocked.value).toBe(false)
  })

  it('锁定时 toggleTheme 什么也不改', async () => {
    const st = memStore({ 'theme-mode': 'light' })
    installFakes(st)
    const t = await import('../theme')
    t.setSkin('noir')
    t.toggleTheme()
    expect(t.themeMode.value).toBe('light')
    expect(st.data['theme-mode']).toBe('light')
  })

  it('.dark 类、data-tone 三处说法一致：选亮色也是 dark', async () => {
    const classes: Array<[string, boolean | undefined]> = []
    const st = memStore({ 'theme-mode': 'light' })
    const f = installFakes(st)
    ;(document.documentElement.classList as { toggle: (c: string, on?: boolean) => void }).toggle = (c, on) => { classes.push([c, on]) }
    const t = await import('../theme')
    t.setSkin('ember')
    expect(classes[classes.length - 1]).toEqual(['dark', true])
    expect(f.attrs['data-tone']).toBe('dark')
  })

  it('ThemeToggle 在锁定时不渲染；SkinPicker 的明暗按钮和后台明暗单选在锁定时置灰', () => {
    const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
    expect(read('../components/ThemeToggle.vue')).toMatch(/v-if="!themeLocked"/)
    expect(read('../components/SkinPicker.vue')).toMatch(/:disabled="themeLocked"/)
    expect(read('../views/admin/SiteAppearance.vue')).toMatch(/:disabled="themeLocked"/)
  })
})
