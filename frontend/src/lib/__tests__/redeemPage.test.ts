// 兑换页的分支判断（纯函数）：页面本身没有 jsdom 点不动，分支都在这里测。
import { describe, expect, it, vi } from 'vitest'
import {
  canSwitchCode, codeAction, makeRecoverAction, nextProgress, productOf, queryCode, recheckAction, showCheckButton, showGracePending, switchCodeEntry, ticksState, waitedLong,
} from '../redeemPage'
import type { ProgressView } from '../redeemProgress'

describe('queryCode：带码链接', () => {
  it('?code= 优先，其次 ?cdk=，去掉首尾空白', () => {
    expect(queryCode({ code: ' SXC-A ' })).toBe('SXC-A')
    expect(queryCode({ cdk: 'SXC-B' })).toBe('SXC-B')
    expect(queryCode({ code: 'SXC-A', cdk: 'SXC-B' })).toBe('SXC-A')
  })
  it('没带、空串、数组取第一个', () => {
    expect(queryCode({})).toBe('')
    expect(queryCode({ code: '', cdk: '' })).toBe('')
    expect(queryCode({ code: ['SXC-C', 'SXC-D'] })).toBe('SXC-C')
    expect(queryCode({ code: [null] })).toBe('')
  })
})

describe('ticksState：顶部三格', () => {
  it('第 ① 屏在第一格，②③ 在第二格，④ 在第三格', () => {
    expect(ticksState('code', '')).toEqual({ current: 1, done: false })
    expect(ticksState('credential', '')).toEqual({ current: 2, done: false })
    expect(ticksState('confirm', '')).toEqual({ current: 2, done: false })
    expect(ticksState('progress', 'running')).toEqual({ current: 3, done: false })
  })
  it('第 ⑤ 屏：成功才算全部完成', () => {
    expect(ticksState('result', 'completed')).toEqual({ current: 3, done: true })
    expect(ticksState('result', 'failed')).toEqual({ current: 3, done: false })
  })
})

describe('productOf：打开哪个产品', () => {
  it('Claude、X、Grok、其余是 ChatGPT', () => {
    expect(productOf('claude_max_5x')).toBe('claude')
    expect(productOf('x_premium_monthly')).toBe('x')
    expect(productOf('grok_premium_monthly')).toBe('grok')
    expect(productOf('plus')).toBe('chatgpt')
    expect(productOf('credit250')).toBe('chatgpt')
    expect(productOf('')).toBe('chatgpt')
  })
})

describe('codeAction：结果卡的「换个账号再试」和「用新码兑换」', () => {
  it('和当前卡密相同（不区分大小写、去空白）：重试', () => {
    expect(codeAction('SXC-AAAA', 'SXC-AAAA')).toBe('retry')
    expect(codeAction('sxc-aaaa', ' SXC-AAAA ')).toBe('retry')
  })
  it('不同（换来的新码）：用新码', () => {
    expect(codeAction('SXC-NEW1', 'SXC-AAAA')).toBe('useCode')
    expect(codeAction('SXC-NEW1', '')).toBe('useCode')
  })
})

describe('recheckAction：重新核对', () => {
  it('内存里还有登录信息就重新核对，没有就回第 ② 屏', () => {
    expect(recheckAction(true)).toBe('preflight')
    expect(recheckAction(false)).toBe('changeAccount')
  })
})

describe('换码入口：不确定是否已提交时锁住', () => {
  it('canSwitchCode 只在空闲时为真', () => {
    expect(canSwitchCode('idle')).toBe(true)
    expect(canSwitchCode('submitting')).toBe(false)
    expect(canSwitchCode('unknown')).toBe(false)
  })
  it('unknown：任何一屏都不给换码入口，只给提示', () => {
    for (const s of ['code', 'credential', 'confirm', 'progress', 'result'] as const) expect(switchCodeEntry(s, 'unknown')).toBe('locked')
  })
  it('提交中：不给入口也不提示', () => {
    expect(switchCodeEntry('confirm', 'submitting')).toBe('none')
  })
  it('空闲：第 ②③ 屏给「换一张卡密」，其余屏由各自的卡片处理', () => {
    expect(switchCodeEntry('credential', 'idle')).toBe('show')
    expect(switchCodeEntry('confirm', 'idle')).toBe('show')
    for (const s of ['code', 'progress', 'result'] as const) expect(switchCodeEntry(s, 'idle')).toBe('none')
  })
})

describe('showGracePending：恢复订阅没确认解除', () => {
  const base = { tried: true, screen: 'confirm' as const, confirmView: 'recover' as const, error: null }
  it('点过恢复、仍要恢复、没出错：显示', () => {
    expect(showGracePending(base)).toBe(true)
  })
  it('没点过、已解除、出错了、不在第 ③ 屏：不显示', () => {
    expect(showGracePending({ ...base, tried: false })).toBe(false)
    expect(showGracePending({ ...base, confirmView: 'ok' })).toBe(false)
    expect(showGracePending({ ...base, error: { kind: 'network', message: '暂时无法确认' } })).toBe(false)
    expect(showGracePending({ ...base, screen: 'credential' })).toBe(false)
  })
})

describe('waitedLong：满 10 分钟', () => {
  const t0 = 1_800_000_000_000
  it('没开始、不到 10 分钟为假，满 10 分钟为真', () => {
    expect(waitedLong(null, t0)).toBe(false)
    expect(waitedLong(t0, t0 + 9 * 60_000 + 59_999)).toBe(false)
    expect(waitedLong(t0, t0 + 10 * 60_000)).toBe(true)
  })
})

describe('showCheckButton：「查一下结果」', () => {
  it('第 ④ 屏出错时给', () => {
    expect(showCheckButton({ screen: 'progress', error: { kind: 'network' }, status: 'running', hasLookup: false })).toBe(true)
    expect(showCheckButton({ screen: 'progress', error: null, status: 'running', hasLookup: false })).toBe(false)
  })
  it('第 ⑤ 屏没成功又没拿到售后结果时给', () => {
    expect(showCheckButton({ screen: 'result', error: null, status: 'failed', hasLookup: false })).toBe(true)
    expect(showCheckButton({ screen: 'result', error: null, status: 'failed', hasLookup: true })).toBe(false)
    expect(showCheckButton({ screen: 'result', error: null, status: 'completed', hasLookup: false })).toBe(false)
  })
  it('其他屏不给（第 ③ 屏的「查一下结果」在 ConfirmStep 里）', () => {
    expect(showCheckButton({ screen: 'confirm', error: { kind: 'submit_unknown' }, status: '', hasLookup: false })).toBe(false)
  })
})

describe('nextProgress：开通中的三行，每次带上一次的行', () => {
  const R1: ProgressView['rows'] = ['current', 'todo', 'todo']
  const R2: ProgressView['rows'] = ['done', 'current', 'todo']
  const R3: ProgressView['rows'] = ['done', 'done', 'current']
  it('第 ④ 屏按状态算', () => {
    expect(nextProgress('progress', 'running', 'payment', R1).rows).toEqual(R2)
    expect(nextProgress('progress', 'running', 'subscription', R2).rows).toEqual(R3)
  })
  it('认不出的状态保持上一次的行，不倒退', () => {
    expect(nextProgress('progress', 'running', 'something_new', R3).rows).toEqual(R3)
    expect(nextProgress('result', 'failed', '', R2)).toEqual({ rows: R2, phase: 'failed' })
  })
  it('离开第 ④⑤ 屏回到初始，下一张码从第 1 行开始', () => {
    expect(nextProgress('confirm', 'running', 'payment', R3).rows).toEqual(R1)
    expect(nextProgress('code', '', '', R3).rows).toEqual(R1)
  })
})

// 以下三条由旧的 scripts/test-grace-recovery.mjs（测旧页面内部）改测到新模块
describe('makeRecoverAction：恢复订阅先确认', () => {
  it('买家取消：不调恢复', async () => {
    const recover = vi.fn(async () => undefined)
    const run = makeRecoverAction(async () => false, recover)
    expect(await run()).toBe(false)
    expect(recover).not.toHaveBeenCalled()
  })
  it('买家确认：调一次恢复', async () => {
    const recover = vi.fn(async () => undefined)
    const run = makeRecoverAction(async () => true, recover)
    expect(await run()).toBe(true)
    expect(recover).toHaveBeenCalledTimes(1)
  })
  it('确认框开着或恢复在途时再点：不再弹框，也不再调恢复', async () => {
    let answer!: (v: boolean) => void
    const confirm = vi.fn(() => new Promise<boolean>((r) => { answer = r }))
    let finish!: () => void
    const recover = vi.fn(() => new Promise<void>((r) => { finish = r }))
    const run = makeRecoverAction(confirm, recover)
    const first = run()
    expect(await run()).toBe(false) // 确认框还开着
    answer(true)
    await Promise.resolve()
    await Promise.resolve()
    expect(recover).toHaveBeenCalledTimes(1)
    expect(await run()).toBe(false) // 恢复请求还在途
    finish()
    expect(await first).toBe(true)
    expect(confirm).toHaveBeenCalledTimes(1)
    expect(recover).toHaveBeenCalledTimes(1)
    // 结束后可以再点
    const again = run()
    answer(false)
    expect(await again).toBe(false)
    expect(confirm).toHaveBeenCalledTimes(2)
  })
})
