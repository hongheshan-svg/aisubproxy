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
