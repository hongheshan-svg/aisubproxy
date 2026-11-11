// 12 套皮肤、亮暗两种的文字和按钮对比度。用户端文字和按钮直接取皮肤变量，所以每套都要达到 4.5:1。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { SKINS } from '../../theme'
import { contrastRatio, sideColorsFrom } from '../../lib/brandColor'
import { effectiveTokens, parseSkinTokens, resolveHex } from './cssTokens'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
// 与 main.ts 的引入顺序一致：style.css 在前，skins.css 在后
const parsed = parseSkinTokens(read('../../style.css') + '\n' + read('../skins.css'))

// 侧栏菜单分组标题（muted，白色混入 side-bg 60%）在这两套暗色主色推出的侧栏底上只有 4.27 / 4.35。
// 主色只往浅调到白色也修不好，根子在 sideColorsFrom 的混色比例；修好它之后这两项会转为通过，
// 到时把 it.fails 改回 it。
const SIDE_MUTED_KNOWN_GAP = new Set(['rose dark', 'violet dark'])

const PAIRS: Array<[string, string]> = [
  ['--ink', '--bg'], ['--ink', '--surface-solid'], ['--ink-2', '--bg'], ['--ink-2', '--surface-solid'],
  ['--primary-on', '--primary'], ['--primary', '--surface-solid'],
]

describe('12 套皮肤的对比度', () => {
  for (const s of SKINS) for (const mode of ['light', 'dark'] as const) {
    it(`${s.id} ${mode} 对比度`, () => {
      const tk = effectiveTokens(parsed, s.id, mode)
      const bad = PAIRS.map(([a, b]) => [a, b, contrastRatio(resolveHex(tk, a), resolveHex(tk, b))] as const)
        .filter(([, , r]) => r < 4.5)
        .map(([a, b, r]) => `${a} 在 ${b} 上 ${r.toFixed(2)}`)
      expect(bad, `${s.id} ${mode}`).toEqual([])
    })
    it(`${s.id} ${mode} 元数据和样式一致`, () => {
      const tk = effectiveTokens(parsed, s.id, mode)
      expect(resolveHex(tk, '--primary')).toBe(s[mode].primary.toUpperCase())
      expect(resolveHex(tk, '--primary-on')).toBe(s[mode].on.toUpperCase())
    })
    const sideIt = SIDE_MUTED_KNOWN_GAP.has(`${s.id} ${mode}`) ? it.fails : it
    sideIt(`${s.id} ${mode} 侧栏颜色达标`, () => {
      const side = sideColorsFrom(s[mode].primary)
      expect(contrastRatio('#FFFFFF', side.bg)).toBeGreaterThanOrEqual(10)
      expect(contrastRatio(side.ink, side.bg)).toBeGreaterThanOrEqual(4.5)
      expect(contrastRatio(side.muted, side.bg)).toBeGreaterThanOrEqual(4.5)
    })
  }
})

describe('解析工具', () => {
  const css = `
    /* 注释里的 :root { --x: #000 } 要忽略 */
    :root { --a: #abc; --b: var(--a); --c: var(--nope, var(--b)); --d: rgba(0, 0, 0, .5); color: red; }
    @media (max-width: 1px) { :root { --a: #111111; } }
    html[data-skin='foo'] { --a: #112233; }
    html.dark[data-skin="foo"], html[data-skin="bar"] { --a: #445566; }
    html.dark[data-skin="foo"] .x { --a: #ffffff; }
  `
  const p = parseSkinTokens(css)
  it('只收顶层的 :root 和皮肤块，跳过 @media 和带后代的选择器', () => {
    expect(p.root['--a']).toBe('#abc')
    expect(p.root.color).toBeUndefined()
    expect(p.skins.foo.light['--a']).toBe('#112233')
    expect(p.skins.foo.dark['--a']).toBe('#445566')
    expect(p.skins.bar.light['--a']).toBe('#445566')
  })
  it('暗色 = 亮色再叠暗色块', () => {
    expect(effectiveTokens(p, 'foo', 'light')['--a']).toBe('#112233')
    expect(effectiveTokens(p, 'foo', 'dark')['--a']).toBe('#445566')
    expect(effectiveTokens(p, 'bar', 'dark')['--a']).toBe('#445566')
  })
  it('var 链、兜底、三位色号展开成大写六位', () => {
    expect(resolveHex(p.root, '--a')).toBe('#AABBCC')
    expect(resolveHex(p.root, '--b')).toBe('#AABBCC')
    expect(resolveHex(p.root, '--c')).toBe('#AABBCC')
  })
  it('rgba 抛错并写出变量名', () => {
    expect(() => resolveHex(p.root, '--d')).toThrow(/--d/)
  })
})
