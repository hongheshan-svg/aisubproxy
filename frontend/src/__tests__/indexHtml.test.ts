// index.html 首屏底色和 theme-color 要与本机记住的皮肤一致（没记过时是默认皮肤深青自助）；
// 颜色值从样式表里读，不在测试里写死第二遍。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { DEFAULT_BRAND_NAME, DEFAULT_SKIN, SKINS } from '../theme'
import { effectiveTokens, parseSkinTokens, resolveHex } from '../styles/__tests__/cssTokens'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
const html = read('../../index.html')
const parsed = parseSkinTokens(read('../style.css') + '\n' + read('../styles/skins.css'))
const tokens = (skin: string, mode: 'light' | 'dark') => effectiveTokens(parsed, skin, mode)
const lc = (s: string) => s.toLowerCase()

/** 取某条 :where(...) 首屏规则的声明块 */
function ruleOf(selector: string): string | null {
  const i = html.indexOf(selector + ' {')
  if (i < 0) return null
  return html.slice(i + selector.length + 2, html.indexOf('}', i))
}

describe('index.html 首屏', () => {
  it('默认皮肤是 teal', () => expect(DEFAULT_SKIN).toBe('teal'))
  it('默认皮肤的暗色首屏底色不是曜夜的 #0c0c11', () => expect(lc(ruleOf(':where(html[data-skin="teal"].dark)')!)).not.toContain('#0c0c11'))

  it('每套皮肤的暗色首屏底色和字色等于样式表里该皮肤暗色的 --bg、--ink', () => {
    for (const s of SKINS) {
      const body = ruleOf(`:where(html[data-skin="${s.id}"].dark)`)
      expect(body, s.id).not.toBeNull()
      expect(lc(body!), s.id).toContain(`background: ${lc(resolveHex(tokens(s.id, 'dark'), '--bg'))}`)
      expect(lc(body!), s.id).toContain(`color: ${lc(resolveHex(tokens(s.id, 'dark'), '--ink'))}`)
    }
  })

  it('不是只有暗色的皮肤，还有亮色首屏底色和字色，等于样式表里该皮肤亮色的 --bg、--ink', () => {
    for (const s of SKINS.filter((x) => !x.preferDark)) {
      const body = ruleOf(`:where(html[data-skin="${s.id}"]:not(.dark))`)
      expect(body, s.id).not.toBeNull()
      expect(lc(body!), s.id).toContain(`background: ${lc(resolveHex(tokens(s.id, 'light'), '--bg'))}`)
      expect(lc(body!), s.id).toContain(`color: ${lc(resolveHex(tokens(s.id, 'light'), '--ink'))}`)
    }
  })

  it('theme-color 默认值是默认皮肤的暗色底', () => {
    expect(lc(html)).toContain(`<meta name="theme-color" content="${lc(resolveHex(tokens(DEFAULT_SKIN, 'dark'), '--bg'))}"`)
  })

  it('首屏脚本按皮肤改 theme-color：每套皮肤的亮、暗色值都等于样式表里的 --bg', () => {
    const m = html.match(/var TC = \{([^}]*)\}/)
    expect(m).not.toBeNull()
    for (const s of SKINS) {
      const e = m![1].match(new RegExp(`${s.id}:\\s*\\['(#[0-9a-fA-F]{6})',\\s*'(#[0-9a-fA-F]{6})'\\]`))
      expect(e, s.id).not.toBeNull()
      const darkBg = lc(resolveHex(tokens(s.id, 'dark'), '--bg'))
      const lightBg = s.preferDark ? darkBg : lc(resolveHex(tokens(s.id, 'light'), '--bg'))
      expect(lc(e![1]), `${s.id} 亮`).toBe(lightBg)
      expect(lc(e![2]), `${s.id} 暗`).toBe(darkBg)
    }
  })

  it('页面标题和后端默认站名一致', () => {
    expect(html).toContain(`<title>${DEFAULT_BRAND_NAME}</title>`)
    expect(DEFAULT_BRAND_NAME).toBe('Recharge Portal')
  })
})

// 真的跑一遍首屏脚本（vm），覆盖有记录 / 无记录 / 存储抛错三种情形
import { runInNewContext } from 'node:vm'
describe('index.html 首屏脚本行为', () => {
  const script = html.match(/<script>([\s\S]*?)<\/script>/)![1]
  function run(opts: { store?: Record<string, string> | 'throw'; path?: string; systemDark?: boolean }) {
    const attrs: Record<string, string> = {}
    const classes = new Set<string>()
    const meta = { content: '#0C1A18', setAttribute(k: string, v: string) { if (k === 'content') this.content = v } }
    const store = opts.store ?? {}
    runInNewContext(script, {
      location: { pathname: opts.path ?? '/' },
      window: { matchMedia: () => ({ matches: !!opts.systemDark }) },
      localStorage: store === 'throw' ? { getItem() { throw new Error('denied') } } : { getItem: (k: string) => (k in store ? store[k] : null) },
      document: {
        documentElement: { setAttribute: (k: string, v: string) => { attrs[k] = v }, classList: { add: (c: string) => classes.add(c) } },
        querySelector: () => meta,
      },
    })
    return { attrs, dark: classes.has('dark'), themeColor: meta.content }
  }
  it('无记录：teal + 跟随系统（系统亮）', () => {
    const r = run({})
    expect(r.attrs).toMatchObject({ 'data-skin': 'teal', 'data-tone': 'light', 'data-surface': 'user' })
    expect(r.dark).toBe(false)
  })
  it('有记录：访客的明暗优先于站点默认，皮肤按记录', () => {
    const r = run({ store: { 'theme-mode': 'dark', 'site-theme-mode': 'light', 'site-skin': 'forest' } })
    expect(r.attrs).toMatchObject({ 'data-skin': 'forest', 'data-tone': 'dark' })
    expect(r.dark).toBe(true)
    expect(lc(r.themeColor)).toBe(lc(resolveHex(tokens('forest', 'dark'), '--bg')))
  })
  it('没有访客选择时用站点默认；非法值回退', () => {
    expect(run({ store: { 'site-theme-mode': 'dark' } }).dark).toBe(true)
    expect(run({ store: { 'theme-mode': 'bogus', 'site-theme-mode': 'dark' } }).dark).toBe(true)
    expect(run({ store: { 'site-skin': 'toString' } }).attrs['data-skin']).toBe('teal')
  })
  it('只有暗色的皮肤即使选亮色也是暗', () => {
    expect(run({ store: { 'theme-mode': 'light', 'site-skin': 'cyber' } }).dark).toBe(true)
  })
  it('存储抛错：照样设好默认皮肤、色调和 data-surface', () => {
    const r = run({ store: 'throw', path: '/ops/login' })
    expect(r.attrs).toMatchObject({ 'data-skin': 'teal', 'data-tone': 'light', 'data-surface': 'console' })
  })
  it('data-surface 按路径', () => {
    expect(run({ path: '/recharge' }).attrs['data-surface']).toBe('user')
    expect(run({ path: '/agent/orders' }).attrs['data-surface']).toBe('console')
  })
})
