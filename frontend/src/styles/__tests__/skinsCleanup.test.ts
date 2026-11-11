// F2-10：skins.css 第 1 节旧骨架（页头、标题区、步骤条、页签）改版后没人用，连同各皮肤里跟着失效的装饰规则和令牌一起删掉。
// 仍在用的只有 .u-title（UserPageHeader）。读源文件断言，防止旧类名/令牌被悄悄加回来。
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const dir = fileURLToPath(new URL('..', import.meta.url))
const strip = (s: string) => s.replace(/\/\*[\s\S]*?\*\//g, '')
const skins = strip(readFileSync(dir + 'skins.css', 'utf8'))

const DEAD_CLASSES = ['u-topbar', 'u-brand', 'u-brand-text', 'u-logo', 'u-tools', 'u-hero', 'u-hero--compact', 'u-eyebrow', 'u-sub', 'u-badges', 'u-main', 'stepper', 'stepper-dot', 'stepper-label', 'is-done', 'is-current', 'svc-card']

describe('F2-10 skins.css 旧骨架已清理', () => {
  it.each(DEAD_CLASSES)('不再含 .%s', (c) => {
    expect(skins).not.toMatch(new RegExp(`\\.${c}(?![\\w-])`))
  })
  it('仍保留 .u-title（UserPageHeader 在用）', () => {
    expect(skins).toMatch(/^\.u-title \{/m)
    expect(readFileSync(dir + '../components/UserPageHeader.vue', 'utf8')).toContain('u-title')
  })
  it('没有只服务旧装饰的动画', () => {
    expect(skins).not.toContain('cyber-caret')
    expect(skins).not.toContain('ember-pulse')
  })
  it('样式文件里不再定义 --tab-/--tabs-/--eyebrow-/--dot-/--badge-/--logo-/--hero- 令牌', () => {
    const files = [...readdirSync(dir).filter((f) => f.endsWith('.css')).map((f) => dir + f), dir + '../style.css']
    for (const f of files) {
      const css = strip(readFileSync(f, 'utf8'))
      expect(css.match(/--(tab|tabs|eyebrow|dot|badge|logo|hero)-[\w-]*\s*:/g) ?? [], f).toEqual([])
    }
  })
})

// FE1 复审留下的两条次要：用户端复位块补变量；rose/paper 的输入框装饰限定到非用户端
describe('用户端复位块与输入框装饰选择器', () => {
  const ut = strip(readFileSync(dir + 'user-theme.css', 'utf8'))
  const i = ut.indexOf('html[data-surface="user"][data-skin][data-tone] {')
  const reset = ut.slice(i, ut.indexOf('}', i))
  it.each(['--input-focus-shadow', '--btn-hover-filter'])('复位块含 %s', (v) => {
    expect(reset).toMatch(new RegExp(`${v}:\\s*[^;]+;`))
  })
  it('皮肤里用到的 --logo-shadow / --dot-current-shadow 已随旧骨架删除，不再需要复位', () => {
    expect(skins).not.toContain('--logo-shadow')
    expect(skins).not.toContain('--dot-current-shadow')
  })
  it('rose 的 .input 下划线、paper 的 textarea 虚线限定到非用户端', () => {
    const lines = skins.split('\n').filter((l) => /\[data-skin='(rose|paper)'\][^{]*(\.input|textarea)/.test(l))
    expect(lines.length).toBeGreaterThanOrEqual(4)
    for (const l of lines) expect(l, l).toMatch(/^html:not\(\[data-surface="user"\]\)\[data-skin=/)
  })
})
