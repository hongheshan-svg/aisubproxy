// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('页头的语言和明暗按钮跟用户端配色', () => {
  // 顶部一行（含语言、明暗按钮）已搬到 UserTopNav，页头只引用它
  const src = read('components/redeem/UserTopNav.vue')
  it('按钮高度不小于 48px，颜色走 --u-*', () => {
    const style = src.slice(src.indexOf('<style'))
    expect(style).toMatch(/tn-tools[^{]*:deep\(\.btn-ghost\)[^{]*\{[^}]*min-height: 48px/)
    expect(style).toMatch(/tn-tools[^{]*:deep\(\.btn-ghost\)[^{]*\{[^}]*var\(--u-surface\)/)
  })
})
