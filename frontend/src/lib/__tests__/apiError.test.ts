import { describe, expect, it } from 'vitest'
import { apiErrorText, swapFailureT } from '../apiError'
import { swapFailure } from '../provider'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

// 用真实语言包做 t / te，免得测试里的假数据和实际文案对不上
function makeT(messages: Record<string, any>) {
  const get = (key: string): unknown => key.split('.').reduce<any>((a, k) => (a == null ? a : a[k]), messages)
  const te = (key: string) => typeof get(key) === 'string'
  const t = (key: string, params: Record<string, unknown> = {}) => {
    const v = get(key)
    if (typeof v !== 'string') return key
    return v.replace(/\{(\w+)\}/g, (_, k) => String(params[k] ?? ''))
  }
  return { t, te }
}

const fake = makeT({
  p: { errors: { known: '已知错误', mins: '{retry_after_minutes} 分钟后再试' }, fallback: '通用文案 {status}' },
})

describe('apiErrorText', () => {
  it('有 error_code 且语言包有这个键：用语言包的文字', () => {
    expect(apiErrorText(fake.t, fake.te, 'p.errors', { error_code: 'known', error: '后端原文' }, 'p.fallback')).toBe('已知错误')
  })
  it('有 error_code 但语言包没有：用后端原文', () => {
    expect(apiErrorText(fake.t, fake.te, 'p.errors', { error_code: 'new_code', error: '后端原文' }, 'p.fallback')).toBe('后端原文')
  })
  it('没有 error_code 但有原文：用原文', () => {
    expect(apiErrorText(fake.t, fake.te, 'p.errors', { error: '后端原文' }, 'p.fallback')).toBe('后端原文')
  })
  it('什么都没有（空对象、非对象、原文为空白）：用 fallbackKey', () => {
    for (const d of [{}, null, undefined, 'oops', 5, [], { error: '   ' }, { error_code: 7 }]) {
      expect(apiErrorText(fake.t, fake.te, 'p.errors', d, 'p.fallback')).toBe('通用文案 ')
    }
  })
  it('data 里的数字字段原样传给 t', () => {
    expect(apiErrorText(fake.t, fake.te, 'p.errors', { error_code: 'mins', retry_after_minutes: 12 }, 'p.fallback')).toBe('12 分钟后再试')
    expect(apiErrorText(fake.t, fake.te, 'p.errors', { status: 502 }, 'p.fallback')).toBe('通用文案 502')
  })
  it('只传数字字段：字符串字段不会被当成参数', () => {
    const seen: Record<string, unknown>[] = []
    const t = (k: string, p: Record<string, unknown> = {}) => { seen.push(p); return k }
    apiErrorText(t, fake.te, 'p.errors', { error_code: 'known', retry_after_minutes: 3, state: 'done', error: 'x' }, 'p.fallback')
    expect(seen[0]).toEqual({ retry_after_minutes: 3 })
  })
})

describe('swapFailureT 与旧的 swapFailure 在中文下逐字相同', () => {
  const { t, te } = makeT(zh)
  const cases: Array<[number, any]> = [
    [502, { outcome: 'unknown', error: '发码结果不明，已转人工核对，请勿重试', error_code: 'outcome_unknown' }],
    [0, {}],
    [409, { state: 'pending', error: '该卡密正在换码，请勿重复提交', error_code: 'conflict' }],
    [409, { state: 'unknown', error: '该卡密上次换码结果不明，已转人工核对，请勿重试', error_code: 'conflict' }],
    [409, { state: 'done', error: '该卡密已换过新码，不可重复兑换', error_code: 'conflict' }],
    [400, { error: '该卡密尚无兑换失败记录，不能换新码', error_code: 'no_failed_record' }],
    [401, { error: '密码错误', error_code: 'wrong_password' }],
    [429, { error: '换码口令错误次数过多，已暂停换码，请 37 分钟后再试', error_code: 'locked', retry_after_minutes: 37 }],
    [429, { error: '代理换码已达今日上限（20 张），请联系管理员', error_code: 'daily_limit', limit: 20 }],
    [502, { error: '卡台不可用' }],
    [500, { msg: '内部错误' }],
    [500, {}],
    [502, { outcome: 'unknown' }],
    [409, { state: 'pending' }],
  ]
  for (const [status, d] of cases) {
    it(`${status} ${JSON.stringify(d)}`, () => {
      expect(swapFailureT(status, d, t, te)).toEqual(swapFailure(status, d))
    })
  }
})

describe('swapFailureT 英文', () => {
  const { t, te } = makeT(en)
  const noZh = (s: string) => expect(s).not.toMatch(/[一-鿿]/)
  it('结果不明：锁住，文字全英文', () => {
    const f = swapFailureT(502, { outcome: 'unknown', error: '发码结果不明，已转人工核对，请勿重试', error_code: 'outcome_unknown' }, t, te)
    expect(f.locked).toBe(true)
    noZh(f.message)
    expect(f.message).toMatch(/Do not retry/)
  })
  it('网络断开：锁住，文字全英文', () => {
    const f = swapFailureT(0, {}, t, te)
    expect(f.locked).toBe(true)
    noZh(f.message)
  })
  it('409 处理中 / 结果不明：锁住；已换过：不锁', () => {
    const pending = swapFailureT(409, { state: 'pending', error: '该卡密正在换码，请勿重复提交', error_code: 'conflict' }, t, te)
    expect(pending.locked).toBe(true)
    noZh(pending.message)
    const unknown = swapFailureT(409, { state: 'unknown', error: '中文', error_code: 'conflict' }, t, te)
    expect(unknown.locked).toBe(true)
    noZh(unknown.message)
    const done = swapFailureT(409, { state: 'done', error: '该卡密已换过新码，不可重复兑换', error_code: 'conflict' }, t, te)
    expect(done.locked).toBe(false)
    noZh(done.message)
  })
  it('锁住时的提示：报错与后缀拼起来没有「..」，「Do not retry」只出现一次', () => {
    const cases = [
      swapFailureT(502, { outcome: 'unknown', error_code: 'outcome_unknown' }, t, te),
      swapFailureT(502, { outcome: 'unknown' }, t, te),
      swapFailureT(409, { state: 'pending', error_code: 'conflict' }, t, te),
      swapFailureT(409, { state: 'unknown', error_code: 'conflict' }, t, te),
      swapFailureT(409, { state: 'pending' }, t, te),
    ]
    for (const f of cases) {
      expect(f.locked).toBe(true)
      expect(f.message).not.toContain('..')
      expect(f.message.match(/Do not retry/g)?.length).toBe(1)
    }
  })
  it('口令锁定带分钟数', () => {
    const f = swapFailureT(429, { error: '中文', error_code: 'locked', retry_after_minutes: 42 }, t, te)
    expect(f.locked).toBe(false)
    expect(f.message).toContain('42')
    noZh(f.message)
  })
  it('认不出的码照旧显示后端原文；什么都没有时显示带状态码的通用文案', () => {
    expect(swapFailureT(502, { error: 'upstream says no', error_code: 'cardplatform_unauthorized' }, t, te).message).toBe('upstream says no')
    const f = swapFailureT(500, {}, t, te)
    expect(f.message).toContain('500')
    noZh(f.message)
  })
})
