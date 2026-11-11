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
  it('发兑换请求前在「本批已发出」集合里登记，登记过的不再发；集合随批次号重建（FE2 审查重要 3）', () => {
    const b = body('runPreflightRedeem')
    const claim = b.indexOf("if (!claimRedeem(redeemClaimed, item.cardKey)) return 'fail'")
    expect(claim).toBeGreaterThan(b.indexOf('if (!live(item.batch)) return'))
    expect(claim).toBeLessThan(b.indexOf('redeemSent = true'))
    expect(claim).toBeLessThan(b.indexOf("'/api/v1/public/cdk/redeem'"))
    expect(src).toContain('let redeemClaimed = new Set<string>()')
    const v = body('handleVerifyBatch')
    expect(v.indexOf('redeemClaimed = new Set<string>()')).toBeGreaterThan(v.indexOf('const gen = ++batchSeq'))
    const r = body('resetAll')
    expect(r.indexOf('redeemClaimed = new Set<string>()')).toBeGreaterThan(r.indexOf('batchSeq++'))
    expect((src.match(/redeemClaimed = new Set<string>\(\)/g) || []).length).toBe(3)
  })
  it('回复按 redeemOutcome 归类，5xx 走结果未知', () => {
    const b = body('runPreflightRedeem')
    expect(b).toContain('redeemOutcome(redeem.r.status, st, redeem.data)')
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
  it('只读查询带 15 秒超时，超时按这一次查询失败处理；兑换请求不加超时（F2-03 裁定）', () => {
    const b = body('pollBatch')
    expect(b).toMatch(/await withAbortTimeout\(POLL_REQUEST_TIMEOUT_MS, \(signal\) =>\s*api\('\/api\/v1\/lookup\/cdk\/results', \{[\s\S]*?signal,\s*\}\)/)
    // 超时抛错落到原来的 catch：算整轮失败，下一轮再查，之后照常检查 90 秒时限
    expect(b).toMatch(/\} catch \{\s*outcome = parsePollResponse\(0, null\)/)
    const r = body('runPreflightRedeem')
    expect(r).not.toMatch(/withAbortTimeout|signal/)
  })
  it('到时限还没查到兑换记录：停止查询，标「结果未知」（不是失败），提示去卡密查询页核对、不要重复提交（裁定 1）', () => {
    const e = body('expireUnknownTargets')
    expect(e).toContain('unknownExpired(target.unknownSince ?? 0, now)')
    expect(e).toContain("status: 'unknown'")
    expect(e).toContain("t('batch.resultUnknownStopped')")
    expect(e).toContain('terminal: true')
    expect(e).not.toMatch(/'failed'|submitOne|runPreflightRedeem|cdk\/redeem/)
    // 每轮查询之后都检查一次，不管这一轮是查到 404、5xx 还是整体失败
    const p = body('pollBatch')
    expect(p.indexOf('expireUnknownTargets(Date.now())')).toBeGreaterThan(p.indexOf('} finally {'))
    // 单个令牌查不到时不再判失败
    const a = body('applyPollResult')
    expect(a).not.toMatch(/resultNotFound|unknownLookupVerdict/)
  })
  it('查询结果按 lib 的 pollVerdict 归类：结果未知的条目带上 unknownSince，「还没查到」时什么都不改（FE2 审查重要 1）', () => {
    const a = body('applyPollResult')
    expect(a).toContain('const polled = readPolledOrder(data)')
    expect(a).toContain('const verdict = pollVerdict(status, polled, (target.unknownSince ?? 0) > 0)')
    expect(a).toMatch(/if \(verdict === 'pending'\) return/)
    expect(a.indexOf("if (verdict === 'pending') return")).toBeLessThan(a.indexOf('updateItem('))
    // 不再自己按状态表判成功、失败
    expect(a).not.toMatch(/isFailStatus\(|isSuccessStatus\(/)
  })
  it('90 秒时限另排计时器、按墙钟算，不跟着查询的间隔和 429 退避往后拖（FE2 审查次要 1）', () => {
    // 新的结果未知一进来就排时限计时器
    const s = body('startPoll')
    expect(s).toMatch(/if \(unknownSince > 0\) scheduleUnknownExpiry\(\)/)
    // 计时器按最早一张的时限排，到点检查时限、再排下一张
    const sch = body('scheduleUnknownExpiry')
    expect(sch.trim().split('\n')[0].trim()).toBe('if (unmounted) return')
    expect(sch).toContain('msUntilUnknownDeadline(pollTargets.value, Date.now())')
    expect(sch).toContain('setTimeout(onUnknownDeadline, wait)')
    const on = body('onUnknownDeadline')
    expect(on).toContain('if (unmounted) return')
    expect(on).toContain('expireUnknownTargets(Date.now())')
    expect(on).toContain('scheduleUnknownExpiry()')
    // 全部到终态时，排着的下一轮（可能是退避中）不用再等，提示也清掉
    expect(on).toMatch(/if \(pollTimer\) clearTimeout\(pollTimer\)/)
    expect(on).toContain("pollNotice.value = ''")
    // 停查（重置、换批次、离开页面）时一起清掉
    expect(body('stopAllPolls')).toMatch(/if \(expiryTimer\) clearTimeout\(expiryTimer\)\s*expiryTimer = null/)
  })
  it('统计和导出用 lib 里的 batchStats、successRows、unknownRows', () => {
    expect(src).toMatch(/const stats = computed\(\(\) => batchStats\(items\.value\)\)/)
    const x = body('exportBatchSuccess')
    expect(x).toContain('successRows(items.value)')
    expect(x).toContain("unknownRows(items.value, t('batch.resultUnknownStopped'))")
  })
  it('顶部统计单独显示「结果未知」', () => {
    expect(src).toContain('{{ stats.unknown }}')
    expect(src).toContain("t('batch.statUnknown')")
  })
  it('结果未知的有自己的状态标签和颜色', () => {
    expect(body('statusLabel')).toContain("unknown: t('batch.status.unknown')")
    expect(src).toContain(".status-pill[data-st='unknown']")
  })
})

describe('结果未知的卡密不会再被提交（裁定 1）', () => {
  it('待提交队列（手动提交的当前一张、一键自动提交的快照）都只认 isSubmittable', () => {
    expect(src).toMatch(/const pendingSessionItems = computed\(\(\) => items\.value\.filter\(\(i\) => isSubmittable\(i\.status\)\)\)/)
    expect(body('handleAutoSubmitAll')).toContain('items.value.filter((i) => isSubmittable(i.status))')
    expect(body('handleSubmitSession')).toContain('currentItem.value')
    expect(src).toMatch(/const currentItem = computed\(\(\) => pendingSessionItems\.value\[0\]/)
  })
  it('页面没有「重试失败项」这类会再次提交的入口', () => {
    expect(src).not.toMatch(/@click="[^"]*(retry|Retry|resubmit)/)
    expect(src).not.toMatch(/function\s+\w*(retry|Retry|resubmit)\w*\s*\(/)
  })
})
