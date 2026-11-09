// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('批量兑换页把旧变量接到 --u-*', () => {
  const src = read('views/user/BatchRedeemView.vue')
  it('--primary / --brd / --soft 等指向用户端变量', () => {
    expect(src).toMatch(/\.user-ui \.batch-page \{[^}]*--primary: var\(--u-primary\)/)
    expect(src).toMatch(/\.user-ui \.batch-page \{[^}]*--brd: var\(--u-line\)/)
    expect(src).toMatch(/\.user-ui \.batch-page \{[^}]*--soft: var\(--u-tint\)/)
    expect(src).toMatch(/\.user-ui \.batch-page \{[^}]*--ink: var\(--u-ink\)/)
  })
  it('导入 Excel 的按钮不低于 48px', () => {
    expect(src).toMatch(/min-h-\[48px\][^"]*border-dashed/)
  })
})
