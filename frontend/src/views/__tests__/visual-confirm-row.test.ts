// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('第 ③ 屏的行标签不被长内容挤成竖排', () => {
  const src = read('components/redeem/ConfirmStep.vue')
  it('dt 不收缩', () => {
    expect(src).toMatch(/\.step-row dt \{[^}]*flex: none/)
  })
})
