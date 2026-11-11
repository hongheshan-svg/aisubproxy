// F2-03：批量兑换页兑换请求发出后断线或 5xx，条目标「处理中」并只读地查真实结果，绝不重新提交。
// 页面没有组件测试，这里按源码断言接线；归类、等待时间、统计和导出在 lib/__tests__/batch-redeem.test.ts 单测。
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

describe('兑换请求结果未知时只查不重发（F2-03）', () => {
  it('整页只有一处会发兑换请求', () => {
    expect((src.match(/\/api\/v1\/public\/cdk\/redeem/g) || []).length).toBe(1)
  })
  it('发兑换请求前记下「已发出」；之后抛错的走结果未知，之前的仍算失败', () => {
    const b = body('runPreflightRedeem')
    const sent = b.indexOf('redeemSent = true')
    expect(sent).toBeGreaterThan(b.indexOf('if (!live(item.batch)) return'))
    expect(sent).toBeLessThan(b.indexOf("'/api/v1/public/cdk/redeem'"))
    const katch = b.slice(b.lastIndexOf('} catch {'))
    expect(katch).toMatch(/if \(redeemSent\) \{\s*markResultUnknown\(/)
    expect(katch).toContain("status: 'failed'")
  })
  it('回复按 redeemOutcome 归类，5xx 走结果未知', () => {
    const b = body('runPreflightRedeem')
    expect(b).toContain('redeemOutcome(redeem.r.status, st)')
    expect(b).toMatch(/outcome === 'unknown'\) \{(\s*\/\/[^\n]*)?\s*markResultUnknown\(/)
    expect(b).not.toContain('redeem.r.status !== 202')
  })
  it('结果未知：标处理中、写明正在查询，并带上开始时间去查', () => {
    const b = body('markResultUnknown')
    expect(b).toContain("status: 'processing'")
    expect(b).toContain("t('batch.resultUnknown')")
    expect(b).toMatch(/startPoll\(item\.id, token, item\.batch, Date\.now\(\)\)/)
    expect(b).not.toMatch(/cdk\/redeem|cdk\/preflight|submitOne|runPreflightRedeem/)
  })
  it('查询只走只读的批量结果接口', () => {
    const b = body('pollBatch')
    expect(b).toContain("'/api/v1/lookup/cdk/results'")
    expect(b).not.toMatch(/cdk\/redeem|cdk\/preflight/)
  })
  it('一直查不到兑换记录：按 unknownLookupVerdict 停下，标失败并提示去卡密查询页核对', () => {
    const b = body('applyPollResult')
    expect(b).toContain('unknownLookupVerdict(status, target.unknownSince, Date.now())')
    expect(b).toContain("t('batch.resultNotFound')")
    expect(b).not.toMatch(/submitOne|runPreflightRedeem|cdk\/redeem/)
  })
  it('统计和导出用 lib 里的 batchStats、successRows', () => {
    expect(src).toMatch(/const stats = computed\(\(\) => batchStats\(items\.value\)\)/)
    expect(body('exportBatchSuccess')).toContain('successRows(items.value)')
  })
})
