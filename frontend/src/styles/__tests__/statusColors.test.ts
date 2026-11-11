// 后台的状态色（F2-17，用户选 1）：警告、错误、成功文字统一用全局类，读皮肤变量，不再各页写死一份。
// 状态色直接当文字用时多数皮肤达不到 4.5:1，所以文字用 --*-ink（状态色往正文色方向混一些）。
// 12 套皮肤 × 亮暗，在页面底色、面板底色、次级底色（--surface-2：斑马行、zovo 的状态标签底），
// 以及提示框/标签的浅色底（-soft 叠在面板上）上都不低于 4.5:1。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import { SKINS } from '../../theme'
import { contrastRatio } from '../../lib/brandColor'
import { composite, effectiveTokens, parseSkinTokens, resolveHex, resolveRgba } from './cssTokens'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
const styleCss = read('../../style.css')
// 与 main.ts 的引入顺序一致：style.css 在前，skins.css 在后
const parsed = parseSkinTokens(styleCss + '\n' + read('../skins.css'))
const STATUS = ['good', 'warn', 'err', 'info'] as const

describe('状态文字色的对比度（F2-17）', () => {
  for (const s of SKINS) for (const mode of ['light', 'dark'] as const) {
    it(`${s.id} ${mode}：--*-ink 在页面底、面板底、次级底、浅色状态底上都不低于 4.5`, () => {
      const tk = effectiveTokens(parsed, s.id, mode)
      const surface = resolveHex(tk, '--surface-solid')
      const bad: string[] = []
      for (const t of STATUS) {
        const fg = resolveHex(tk, `--${t}-ink`)
        const backs: Array<[string, string]> = [
          ['--bg', resolveHex(tk, '--bg')],
          ['--surface-solid', surface],
          ['--surface-2 叠在面板上', composite(resolveRgba(tk, '--surface-2'), surface)],
          [`--${t}-soft 叠在面板上`, composite(resolveRgba(tk, `--${t}-soft`), surface)],
        ]
        for (const [label, bg] of backs) {
          const r = contrastRatio(fg, bg)
          if (r < 4.5) bad.push(`--${t}-ink 在 ${label} 上 ${r.toFixed(2)}`)
        }
      }
      expect(bad, `${s.id} ${mode}`).toEqual([])
    })
  }
})

describe('全局状态类读 --*-ink（F2-17）', () => {
  it(':root 定义 --*-ink：状态色往正文色方向混，跟着皮肤变', () => {
    const pct = { good: 60, warn: 60, err: 45, info: 50 }
    for (const t of STATUS) expect(parsed.root[`--${t}-ink`]).toBe(`color-mix(in srgb, var(--${t}) ${pct[t]}%, var(--ink))`)
  })
  it('.text-ok / .text-warn / .text-err / .field-error', () => {
    expect(styleCss).toMatch(/\.text-ok\s*\{\s*color: var\(--good-ink\);\s*\}/)
    expect(styleCss).toMatch(/\.text-warn\s*\{\s*color: var\(--warn-ink\);\s*\}/)
    expect(styleCss).toMatch(/\.text-err,\s*\.field-error\s*\{\s*color: var\(--err-ink\);\s*\}/)
  })
  it('提示框、状态标签的文字也用 --*-ink（底色和边框仍是状态色）', () => {
    expect(styleCss).toContain('.pill-good { background: var(--good-soft); color: var(--good-ink); border-color: transparent; }')
    expect(styleCss).toContain('.pill-warn { background: var(--warn-soft); color: var(--warn-ink); border-color: transparent; }')
    expect(styleCss).toContain('.pill-err  { background: var(--err-soft);  color: var(--err-ink);  border-color: transparent; }')
    expect(styleCss).toContain('.pill-info { background: var(--info-soft); color: var(--info-ink); border-color: transparent; }')
    expect(styleCss).toContain('.alert-error   { background: var(--err-soft);  color: var(--err-ink);  border-color: var(--err); }')
    expect(styleCss).toContain('.alert-success { background: var(--good-soft); color: var(--good-ink); border-color: var(--good); }')
    expect(styleCss).toContain('.alert-info    { background: var(--info-soft); color: var(--info-ink); border-color: var(--info); }')
    expect(styleCss).toContain('.alert-warning { background: var(--warn-soft); color: var(--warn-ink); border-color: var(--warn); }')
  })
  it('分步标题（对接页、Telegram 告警页共用）提到全局', () => {
    expect(styleCss).toMatch(/\.ops-step-title\s*\{[^}]*color: var\(--ink\)/)
    expect(styleCss).toMatch(/\.ops-step-no\s*\{[^}]*background: var\(--primary-soft\)[^}]*color: var\(--primary\)/)
  })
})

// ---------- 后台各页不再自己写一份 ----------
const SRC = resolve(fileURLToPath(new URL('../..', import.meta.url)))
function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) { if (name !== '__tests__') walk(p, out) } else if (name.endsWith('.vue')) out.push(p)
  }
  return out
}
const adminFiles = [...walk(join(SRC, 'views/admin')), join(SRC, 'layouts/AdminLayout.vue')]
const styleOf = (src: string) => [...src.matchAll(/<style[^>]*>([\s\S]*?)<\/style>/g)].map((m) => m[1]).join('\n')
const templateOf = (src: string) => src.replace(/<script[\s\S]*?<\/script>/g, '').replace(/<style[\s\S]*?<\/style>/g, '')

describe('后台页面不再写死状态色（F2-17）', () => {
  it('作用域样式里没有 .text-warn / .text-err / .text-ok / .warn-box / .step-title / .step-no / .mono 的重复定义', () => {
    const hits = adminFiles.filter((p) => /(^|[\s,}])\.(text-warn|text-err|text-ok|warn-box|step-title|step-no|mono)\b[^{]*\{/m.test(styleOf(readFileSync(p, 'utf8'))))
    expect(hits.map((p) => relative(SRC, p))).toEqual([])
  })
  it('作用域样式里没有写死的红、橙、绿、蓝和浅粉', () => {
    const fixed = /#(d97706|dc2626|16a34a|15803d|b91c1c|fef2f2|fecaca|3b82f6)\b|rgba\(\s*(220,\s*38,\s*38|22,\s*163,\s*74)\b/i
    const hits = adminFiles.filter((p) => fixed.test(styleOf(readFileSync(p, 'utf8'))))
    expect(hits.map((p) => relative(SRC, p))).toEqual([])
  })
  it('模板里不用 Tailwind 的固定红、橙色，也不把 --warn / --err / --good 直接写成文字色', () => {
    const hits: string[] = []
    for (const p of adminFiles) {
      const t = templateOf(readFileSync(p, 'utf8'))
      if (/\btext-(red|amber|green|orange|yellow)-\d{3}\b/.test(t)) hits.push(relative(SRC, p) + '：Tailwind 固定色')
      if (/color:\s*var\(--(warn|err|good|info)\)/.test(t)) hits.push(relative(SRC, p) + '：直接用状态色当文字色')
    }
    expect(hits).toEqual([])
  })
  it('深色皮肤下的浅粉提示框换成全局的错误提示框', () => {
    for (const f of ['views/admin/TelegramAlerts.vue', 'views/admin/MarsWallexIntegration.vue']) {
      const src = readFileSync(join(SRC, f), 'utf8')
      expect(src, f).not.toContain('warn-box')
      expect(src, f).toContain('class="alert alert-error font-semibold"')
      expect(src, f).not.toMatch(/class="step-(title|no)"/)
      expect(src, f).toContain('class="ops-step-title"><span class="ops-step-no">')
    }
  })
})
