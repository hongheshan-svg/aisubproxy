// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('Claude 组织 ID 的占位符在 375 宽不被截断', () => {
  const src = read('components/redeem/CredentialStep.vue')
  it('占位符字号缩小', () => {
    expect(src).toMatch(/claude-org[^{]*::placeholder[^{]*\{[^}]*font-size: 1[0-3]px/)
  })
})
