// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('卡密批量查询的结果表在窄屏横向滚动', () => {
  const src = read('views/user/CDKStatusView.vue')
  it('表有最小宽度，卡密和邮箱不逐字换行', () => {
    expect(src).toMatch(/\.lk-table \{[^}]*min-width: \d+px/)
    expect(src).toMatch(/\.lk-break \{[^}]*white-space: nowrap/)
  })
})
