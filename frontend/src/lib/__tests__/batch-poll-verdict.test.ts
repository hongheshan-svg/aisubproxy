// 批量兑换页只读查询查到记录之后怎么处理（FE2 审查重要 1）。数据都是假的，结构照后端 writeLocalResult 写出的样子
// （backend/internal/handler/local_cdk.go：localOrderPublic、writeLocalResult）。
import { describe, expect, it } from 'vitest'
import { UNKNOWN_QUERY_MS, pollVerdict, readPolledOrder, unknownExpired } from '../batch-redeem'

/** 后端按令牌查结果的回包：码最新一张单的公开视图。码还没用掉（干净失败）时带 can_retry、attempts_left。 */
function result(status: string, extra: Record<string, unknown> = {}) {
  const order = { status, stage: '', message: '提示文字', plan: 'plus', region: 'US', account_email: 'bu***@example.com', ...extra }
  return { provider: 'marswallex', provider_kind: 'card_supply', status, stage: '', message: '提示文字', order, events: [], redemption_token: 'tok' }
}
// 以前那次干净失败留下的记录：码仍未使用，可以重兑
const oldCleanFailure = result('failed_precharge', { message: '本次未能开通，也没有发起支付，可以稍后重新提交', can_retry: true, attempts_left: 3 })

describe('读出查询结果里的单据（FE2 审查重要 1）', () => {
  it('取状态、文字、邮箱和「可以重新提交」', () => {
    const p = readPolledOrder(oldCleanFailure)
    expect(p.status).toBe('failed_precharge')
    expect(p.message).toBe('本次未能开通，也没有发起支付，可以稍后重新提交')
    expect(p.email).toBe('bu***@example.com')
    expect(p.canRetry).toBe(true)
  })
  it('没有 can_retry 或者不是 true：不算可以重新提交', () => {
    expect(readPolledOrder(result('declined')).canRetry).toBe(false)
    expect(readPolledOrder(result('failed_precharge', { can_retry: 'true' })).canRetry).toBe(false)
    expect(readPolledOrder(null).canRetry).toBe(false)
  })
})

describe('结果未知的条目不采信「失败、可以重新提交」（FE2 审查重要 1）', () => {
  it('结果未知时查到旧的干净失败：当作还没查到这次的记录，不下结论', () => {
    expect(pollVerdict(200, readPolledOrder(oldCleanFailure), true)).toBe('pending')
  })
  it('没带 can_retry 的 failed_precharge 也一样：这个状态只出现在码仍未使用时', () => {
    expect(pollVerdict(200, readPolledOrder(result('failed_precharge')), true)).toBe('pending')
    expect(pollVerdict(200, readPolledOrder(result('FAILED_PRECHARGE')), true)).toBe('pending')
  })
  it('结果未知时查到别的失败（码已被占用、有了定论）：照常判失败', () => {
    for (const st of ['declined', 'failed', 'cancelled']) {
      expect(pollVerdict(200, readPolledOrder(result(st)), true), st).toBe('failed')
    }
  })
  it('结果未知时查到成功、处理中：照常', () => {
    expect(pollVerdict(200, readPolledOrder(result('completed')), true)).toBe('success')
    for (const st of ['queued', 'running', 'review']) {
      expect(pollVerdict(200, readPolledOrder(result(st)), true), st).toBe('processing')
    }
  })
  it('正常受理的条目（不是结果未知）：兑换回包已经证明建了这次的单，查到干净失败就是这次的，判失败', () => {
    expect(pollVerdict(200, readPolledOrder(oldCleanFailure), false)).toBe('failed')
  })
  it('没查到（404）、查询出错：都不下结论', () => {
    for (const s of [0, 404, 429, 500, 503]) {
      expect(pollVerdict(s, readPolledOrder({ error: '未找到该卡密的兑换记录' }), true), String(s)).toBe('pending')
      expect(pollVerdict(s, readPolledOrder({ error: 'x' }), false), String(s)).toBe('pending')
    }
  })
})

describe('审查描述的场景：这张卡密以前干净失败过，这次兑换结果未知（FE2 审查重要 1）', () => {
  const since = 1_000_000
  it('第一次查询比后端建单早到，拿到旧记录：不标失败；后端随后建单，查到处理中、再到成功', () => {
    // 标完结果未知立刻查：后端还在就绪检查、互斥检查，没建单，回的是上一次的失败
    expect(pollVerdict(200, readPolledOrder(oldCleanFailure), true)).toBe('pending')
    // 下一轮：这次的单建好了
    expect(pollVerdict(200, readPolledOrder(result('queued')), true)).toBe('processing')
    // 之后按正常受理的条目查（unknownSince 已清掉）
    expect(pollVerdict(200, readPolledOrder(result('completed')), false)).toBe('success')
  })
  it('这次请求其实没到后端，一直只能查到旧记录：到 90 秒时限标「结果未知」，不显示「可以重新提交」', () => {
    for (let t = since; t < since + UNKNOWN_QUERY_MS; t += 3_000) {
      expect(pollVerdict(200, readPolledOrder(oldCleanFailure), true)).toBe('pending')
      expect(unknownExpired(since, t)).toBe(false)
    }
    expect(unknownExpired(since, since + UNKNOWN_QUERY_MS)).toBe(true)
  })
})

describe('查到 2xx 但认不出状态：结果未知的不当作查到（FE2 审查次要 5）', () => {
  const since = 1_000_000
  it('结果未知的条目：没有状态、没见过的状态都当作还没查到，继续查到时限', () => {
    expect(pollVerdict(200, readPolledOrder({}), true)).toBe('pending')
    expect(pollVerdict(200, readPolledOrder({ raw: '<html>ok</html>' }), true)).toBe('pending')
    expect(pollVerdict(200, readPolledOrder(result('something_new')), true)).toBe('pending')
  })
  it('正常受理的条目：兑换回包已经证明建了单，认不出的状态照旧显示处理中', () => {
    expect(pollVerdict(200, readPolledOrder({}), false)).toBe('processing')
    expect(pollVerdict(200, readPolledOrder(result('something_new')), false)).toBe('processing')
  })
  it('兑换回 200 却认不出状态、之后一直查不到认得的状态：到 90 秒时限标「结果未知」', () => {
    for (let t = since; t < since + UNKNOWN_QUERY_MS; t += 3_000) {
      expect(pollVerdict(200, readPolledOrder({ raw: '' }), true)).toBe('pending')
      expect(unknownExpired(since, t)).toBe(false)
    }
    expect(unknownExpired(since, since + UNKNOWN_QUERY_MS)).toBe(true)
  })
})
