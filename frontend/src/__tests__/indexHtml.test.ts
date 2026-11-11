// index.html 首屏底色和 theme-color 要与默认皮肤（深青自助）一致；颜色值从样式表里读，不在测试里写死第二遍。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { DEFAULT_SKIN } from '../theme'
import { effectiveTokens, parseSkinTokens, resolveHex } from '../styles/__tests__/cssTokens'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
const html = read('../../index.html')
const parsed = parseSkinTokens(read('../style.css') + '\n' + read('../styles/skins.css'))
const dark = effectiveTokens(parsed, DEFAULT_SKIN, 'dark')
const light = effectiveTokens(parsed, DEFAULT_SKIN, 'light')
const lc = (s: string) => s.toLowerCase()

describe('index.html 首屏', () => {
  it('默认皮肤是 teal', () => expect(DEFAULT_SKIN).toBe('teal'))
  it('不再有曜夜雾金的 #0c0c11', () => expect(lc(html)).not.toContain('#0c0c11'))
  it('首屏暗色底色和字色等于样式表里深青暗色的 --bg、--ink', () => {
    const m = html.match(/:where\(html\.dark\) \{([^}]*)\}/)
    expect(m).not.toBeNull()
    expect(lc(m![1])).toContain(`background: ${lc(resolveHex(dark, '--bg'))}`)
    expect(lc(m![1])).toContain(`color: ${lc(resolveHex(dark, '--ink'))}`)
  })
  it('theme-color 默认值是深青暗色底', () => {
    expect(lc(html)).toContain(`<meta name="theme-color" content="${lc(resolveHex(dark, '--bg'))}"`)
  })
  it('首屏脚本在 teal 且非暗色时把 theme-color 设成深青亮色 --bg', () => {
    expect(lc(html)).toContain(lc(resolveHex(light, '--bg')))
    expect(html).toMatch(/skin === 'teal' && !dark/)
    expect(html).toContain("meta[name=\"theme-color\"]")
  })
})
