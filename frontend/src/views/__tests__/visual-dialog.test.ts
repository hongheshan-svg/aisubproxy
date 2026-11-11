// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('弹窗在用户端页面用用户端配色', () => {
  const src = read('components/DialogHost.vue')
  it('非后台页面给弹窗套 user-ui，并用 u-card / u-btn', () => {
    expect(src).toContain("surfaceOf(route.path) === 'user'")
    expect(src).not.toContain("startsWith('/agent')")
    expect(src).toContain('user-ui')
    expect(src).toContain('u-btn-primary')
    expect(src).toContain('u-btn-secondary')
    expect(src).toMatch(/@media \(max-width: 480px\)[^}]*\{[^}]*dlg-actions[^}]*column-reverse/)
    expect(src).toMatch(/\.dlg-actions > \.dlg-danger/)
  })
})
