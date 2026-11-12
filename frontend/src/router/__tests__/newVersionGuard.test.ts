// 新版本整页跳转守卫必须排在鉴权守卫之前；组件的离开守卫先于全局守卫，被拦下的导航不会被刷新。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const src = readFileSync(fileURLToPath(new URL('../index.ts', import.meta.url)), 'utf8')

describe('路由守卫顺序与接线', () => {
  it('整页跳转守卫是第一个 beforeEach，在鉴权守卫之前', () => {
    const first = src.indexOf('router.beforeEach(')
    const second = src.indexOf('router.beforeEach(', first + 1)
    expect(first).toBeGreaterThan(0)
    expect(second).toBeGreaterThan(first)
    const head = src.slice(first, second)
    expect(head).toContain('shouldFullReload')
    expect(head).toContain('assignPage(')
    expect(head).toContain('return false')
    expect(head).toContain('START_LOCATION')
    expect(src.slice(second, second + 120)).toContain('async')
  })
  it('用 router.resolve(to).href 跳转，并注释了 onBeforeRouteLeave 的先后', () => {
    expect(src).toContain('router.resolve(to).href')
    expect(src).toContain('onBeforeRouteLeave')
  })
  it('分包失败兜底：onError 与 vite:preloadError 都接上', () => {
    expect(src).toMatch(/router\.onError\(\(err, to\) =>/)
    expect(src).toContain("'vite:preloadError'")
    expect(src).toContain('e.preventDefault()')
  })
})
