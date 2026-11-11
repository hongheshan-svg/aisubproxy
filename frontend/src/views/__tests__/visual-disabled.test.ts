// 逐屏截图检查（任务 16）发现的外观问题：样式没法在无浏览器的测试里渲染，这里按源码断言关键规则，防止改回去。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

describe('禁用按钮不靠透明度', () => {
  const css = read('styles/user-theme.css')
  it('禁用样式用专门的 --u-disabled-* 颜色，没有 opacity', () => {
    const rule = css.slice(css.indexOf('.user-ui .u-btn-primary:disabled'), css.indexOf('.user-ui .u-link'))
    expect(rule).not.toMatch(/opacity/)
    expect(rule).toContain('--u-disabled-ink')
    expect(rule).toContain('--u-disabled-bg')
  })
  it('亮色、暗色（data-tone）两处都定义了这两个变量', () => {
    expect((css.match(/--u-disabled-bg:/g) || []).length).toBe(2)
    expect((css.match(/--u-disabled-ink:/g) || []).length).toBe(2)
  })
})
