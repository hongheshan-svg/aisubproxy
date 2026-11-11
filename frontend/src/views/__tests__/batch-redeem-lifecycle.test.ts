// 批量兑换页的生命周期：离开页面、重置、换批次之后，在途请求回来不能再排查询、不能再提交、不能写到新批次上。
// 页面没有组件测试、测试环境也不执行卸载钩子，这里按源码断言接线；判断逻辑本身在 lib/batch-redeem 里单测。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const src = readFileSync(resolve(__dirname, '../../views/user/BatchRedeemView.vue'), 'utf8')

/** 取 `function name(...) {` 的函数体（按花括号配对）。 */
function body(name: string): string {
  const m = new RegExp(`function\\s+${name}\\s*\\([^)]*\\)[^{]*\\{`).exec(src)
  if (!m) throw new Error(`找不到函数 ${name}`)
  let depth = 1
  let i = m.index + m[0].length
  const start = i
  for (; i < src.length && depth > 0; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}') depth--
  }
  return src.slice(start, i - 1)
}
const firstLine = (name: string) => body(name).trim().split('\n')[0].trim()

describe('离开页面后不再排查询（F2-04a）', () => {
  it('卸载时置 unmounted 并停掉查询', () => {
    const m = src.match(/onUnmounted\(\(\) => \{([\s\S]*?)\n\}\)/)
    expect(m, 'onUnmounted 要写成多行回调').not.toBeNull()
    expect(m![1]).toContain('unmounted = true')
    expect(m![1]).toContain('stopAllPolls()')
  })
  it('startPoll、schedulePoll 第一句就是 if (unmounted) return', () => {
    expect(firstLine('startPoll')).toBe('if (unmounted) return')
    expect(firstLine('schedulePoll')).toBe('if (unmounted) return')
  })
  it('pollBatch 开头也看 unmounted：卸载前排好的那一轮不再发', () => {
    expect(body('pollBatch')).toMatch(/if \(unmounted[^)]*\) return/)
  })
})
