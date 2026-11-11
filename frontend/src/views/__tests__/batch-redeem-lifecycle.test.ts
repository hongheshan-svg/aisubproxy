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

describe('重置、离开页面、换批次（F2-04b）', () => {
  it('提交中（手动或自动）「重置」按钮禁用，resetAll 自己也拦', () => {
    expect(src).toMatch(/<button[^>]*:disabled="submitting \|\| autoSubmitting"[^>]*@click="resetAll"/)
    expect(firstLine('resetAll')).toBe('if (submitting.value || autoSubmitting.value) return')
    // 重置不再把 autoSubmitting 硬置回假：自动提交时根本不能重置
    expect(body('resetAll')).not.toContain('autoSubmitting.value = false')
  })
  it('开始验证、重置都换批次号；条目 id 带批次号，旧批次的写回对不上新条目', () => {
    expect(body('handleVerifyBatch')).toContain('const gen = ++batchSeq')
    expect(body('resetAll')).toContain('batchSeq++')
    expect(body('handleVerifyBatch')).toContain('id: `${gen}-${i}-${k}`')
    expect(body('handleVerifyBatch')).toContain('batch: gen')
  })
  it('验证结果回来时批次已换：不写回', () => {
    const b = body('handleVerifyBatch')
    expect(b.indexOf('if (!live(gen)) return')).toBeGreaterThan(b.indexOf('await mapPool'))
    expect(b.indexOf('if (!live(gen)) return')).toBeLessThan(b.indexOf('items.value = results'))
  })
  it('自动提交用 runSubmitPool，每取一张前看批次还在不在', () => {
    const b = body('handleAutoSubmitAll')
    expect((b.match(/runSubmitPool\(/g) || []).length).toBe(2)
    expect((b.match(/\(\) => live\(gen\)/g) || []).length).toBe(2)
    expect(b).not.toMatch(/while \(true\)/)
  })
  it('兑换请求发出前最后看一次：页面已离开就不再提交这一张', () => {
    const b = body('runPreflightRedeem')
    const guard = b.indexOf('if (!live(item.batch)) return')
    expect(guard).toBeGreaterThan(b.indexOf("'/api/v1/public/cdk/preflight'"))
    expect(guard).toBeLessThan(b.indexOf("'/api/v1/public/cdk/redeem'"))
  })
  it('开始查进度和每轮查询回来都比对批次号', () => {
    expect(body('startPoll')).toContain('if (gen !== batchSeq) return')
    const p = body('pollBatch')
    expect(p).toContain('const gen = batchSeq')
    expect(p).toMatch(/await api\([\s\S]*?if \(!live\(gen\)\) return/)
    expect(p).toContain('if (gen === batchSeq) pollInFlight = false')
  })
  it('live() 同时看页面还在、批次没换', () => {
    expect(body('live').trim()).toBe('return !unmounted && gen === batchSeq')
  })
})
