// 皮肤的页面底纹（--bg-tint 渐变、网格、横线）和 zovo 的氛围层只画在后台、代理商后台和登录页；
// 用户端页面保持纯色背景。用 html 上的 data-surface 区分：user = 用户端，console = 其余。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { surfaceOf } from '../../router/routes'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
const css = read('../../style.css')

describe('surfaceOf：按路径分用户端和后台', () => {
  it('用户端页面', () => {
    for (const p of ['/', '/recharge', '/batch', '/history', '/billing', '/lookup', '/partner/swap', '/auth/register', '/nope', '/opsx', '/agents']) {
      expect(surfaceOf(p), p).toBe('user')
    }
  })
  it('后台、登录页、安装页、代理商后台', () => {
    for (const p of ['/ops', '/ops/login', '/ops/setup', '/ops/cardpool', '/agent', '/agent/login', '/agent/orders/3']) {
      expect(surfaceOf(p), p).toBe('console')
    }
  })
})

describe('样式：用户端不画皮肤的页面底纹', () => {
  it('用户端把 html 的底纹关掉', () => {
    expect(css).toMatch(/html\[data-surface=["']user["']\]\s*\{[^}]*background-image:\s*none/)
  })
  it('用户端把 zovo 的 body::before / body::after 氛围层关掉，且优先级盖过皮肤自己的规则', () => {
    expect(css).toMatch(/html\[data-surface=["']user["']\]\[data-skin\]\s+body::before,\s*html\[data-surface=["']user["']\]\[data-skin\]\s+body::after\s*\{[^}]*display:\s*none/)
  })
  it('页面底纹只有 html 一处在用 --bg-tint', () => {
    const uses = [...(css + read('../skins.css')).matchAll(/background(?:-image)?:\s*var\(--bg-tint\)/g)]
    expect(uses).toHaveLength(1)
  })
})

describe('data-surface 的设置', () => {
  it('首屏脚本按地址设置，避免先画出底纹再去掉', () => {
    expect(read('../../../index.html')).toMatch(/setAttribute\('data-surface'/)
  })
  it('每次路由切换后更新', () => {
    expect(read('../../router/index.ts')).toMatch(/data-surface|surfaceOf/)
  })
})

// ---- 元素级装饰：用户端一律不要，只留在后台和登录页（规格 B §3.5） ----
describe('皮肤的元素级装饰不命中用户端', () => {
  const skins = read('../skins.css').split('@media (prefers-reduced-motion')[0] // 末尾 reduced-motion 复位块只关动画，不算装饰
  const rules = [...skins.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim(), body: m[2] }))
  const selectors = rules.flatMap((r) => r.sel.split(',').map((s) => ({ sel: s.trim(), body: r.body })))
  const OUT = 'html:not([data-surface="user"])'
  const startsOut = (s: string) => s.startsWith(OUT) || s.startsWith('html.dark:not([data-surface="user"])')

  it('带 .u-title / .u-eyebrow / .u-badges / .u-main 的皮肤规则都限定在非用户端', () => {
    const hit = selectors.filter((x) => /\.u-(title|eyebrow|badges|main)\b/.test(x.sel) && /\[data-skin=/.test(x.sel))
    expect(hit.length).toBeGreaterThan(20)
    expect(hit.filter((x) => !startsOut(x.sel)).map((x) => x.sel)).toEqual([])
  })
  it('按钮的掠光伪元素和切角 clip-path 限定在非用户端', () => {
    const hit = selectors.filter((x) => /\.btn-(primary|secondary)/.test(x.sel) && (/::(before|after)/.test(x.sel) || /clip-path/.test(x.body)))
    expect(hit.length).toBeGreaterThanOrEqual(3)
    expect(hit.filter((x) => !startsOut(x.sel)).map((x) => x.sel)).toEqual([])
  })
  it('用户端把按钮的渐变、硬投影、位移变量复位成纯色', () => {
    const ut = read('../user-theme.css')
    const i = ut.indexOf('html[data-surface="user"][data-skin][data-tone] {')
    expect(i).toBeGreaterThan(-1)
    const body = ut.slice(i, ut.indexOf('}', i))
    for (const v of ['--btn-img', '--btn-shadow', '--btn-shadow-hover', '--btn-shadow-active', '--btn2-shadow', '--btn-hover-move', '--btn-active-move']) {
      expect(body, v).toMatch(new RegExp(`${v}:\\s*none;`))
    }
  })
  it('皮肤里用过的按钮变量，复位块都覆盖到', () => {
    const ut = read('../user-theme.css')
    const body = ut.slice(ut.indexOf('html[data-surface="user"][data-skin][data-tone] {'))
    const used = new Set([...skins.matchAll(/(--btn2?-(?:img|shadow(?:-hover|-active)?|border|hover-move|active-move|case|track)):/g)].map((m) => m[1]))
    for (const v of used) expect(body, v).toContain(v + ':')
  })
})
