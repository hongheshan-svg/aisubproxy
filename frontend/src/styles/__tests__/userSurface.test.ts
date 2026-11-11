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
