import { describe, expect, it } from 'vitest'
import { canAbandonSubmission, submissionLabel, submissionTagType, emptyCardholderForm, toCardholderPayload, validateCardholderForm, FLAG_LABEL, hasEmoji, isUncertain, isUnknownOutcome, kindLabel, parseAmountCents } from '../cardpool'

describe('cardpool lib', () => {
  it('parseAmountCents', () => {
    expect(parseAmountCents('12.5')).toBe(1250)
    expect(parseAmountCents('0.01')).toBe(1)
    expect(parseAmountCents(' 3 ')).toBe(300)
    expect(parseAmountCents('0')).toBeNull()
    expect(parseAmountCents('1.005')).toBeNull()
    expect(parseAmountCents('-1')).toBeNull()
    expect(parseAmountCents('abc')).toBeNull()
    expect(parseAmountCents('1234567890123')).toBeNull()
  })

  it('kindLabel covers wallet kinds', () => {
    expect(kindLabel('conversion')).toBe('兑换')
    expect(kindLabel('wallet_transfer')).toBe('钱包转账')
    expect(kindLabel('apply')).toBe('开卡')
    expect(kindLabel('x')).toBe('x')
  })

  it('isUnknownOutcome', () => {
    expect(isUnknownOutcome({ ok: false, status: 502, data: { outcome: 'unknown' } })).toBe(true)
    expect(isUnknownOutcome({ ok: false, status: 502, data: { outcome: 'rejected' } })).toBe(false)
    expect(isUnknownOutcome({ ok: true, status: 200, data: {} })).toBe(false)
  })

  it('hasEmoji', () => {
    expect(hasEmoji('停用😀')).toBe(true)
    expect(hasEmoji('停用原因')).toBe(false)
  })

  it('isUncertain', () => {
    expect(isUncertain({ ok: false, status: 0, data: {} })).toBe(true)
    expect(isUncertain({ ok: false, status: 502, data: { outcome: 'unknown' } })).toBe(true)
    expect(isUncertain({ ok: false, status: 502, data: { outcome: 'rejected' } })).toBe(false)
    expect(isUncertain({ ok: false, status: 409, data: {} })).toBe(false)
    expect(isUncertain({ ok: true, status: 200, data: {} })).toBe(false)
  })

  it('FLAG_LABEL direction', () => {
    expect(FLAG_LABEL.DEBIT).toBe('出账')
    expect(FLAG_LABEL.CREDIT).toBe('入账')
  })

  const TODAY = '2026-10-31'
  function validUS() {
    const f = emptyCardholderForm()
    Object.assign(f, {
      first_name: 'John', last_name: 'Smith', date_of_birth: '1990-05-06', zone_number: '1', mobile: '4155550100',
      email: 'john@example.com', country_code: 'US', id_type: 'PASSPORT', id_no: 'E1234567',
      id_period_begin: '2020-01-01', id_period_end: '2030-01-01', id_photo_file_ids: ['f1'], ssn: '123-45-6789',
    })
    const a = { country: 'US', state: 'CA', city: 'SF', address: '1 Main St', post_code: '94100' }
    f.residential_address = { ...a }
    f.billing_same = true
    return f
  }

  it('validateCardholderForm', () => {
    expect(validateCardholderForm(validUS(), TODAY)).toEqual([])
    const bad = (mut: (f: ReturnType<typeof validUS>) => void) => {
      const f = validUS(); mut(f)
      expect(validateCardholderForm(f, TODAY).length).toBeGreaterThan(0)
    }
    bad((f) => { f.ssn = '' })
    bad((f) => { f.ssn = '123456789' })
    bad((f) => { f.first_name = '张三' })
    bad((f) => { f.date_of_birth = TODAY })
    bad((f) => { f.date_of_birth = '1990-02-30' })
    bad((f) => { f.id_period_end = TODAY })
    bad((f) => { f.id_period_begin = '2026-11-01' })
    bad((f) => { f.id_photo_file_ids = [] })
    bad((f) => { f.id_photo_file_ids = ['a', 'b', 'c'] })
    bad((f) => { f.country_code = 'U' })
    bad((f) => { f.email = 'Joe <a@b.com>' })
    bad((f) => { f.billing_same = false; f.billing_address = { country: 'US', state: '', city: '', address: '', post_code: '' } })
    // 非 US 不要求 SSN；+ 前缀区号合法；长期有效
    const g = validUS(); g.country_code = 'cn'; g.ssn = ''; g.zone_number = '+86'; g.id_period_end = '9999-12-31'
    expect(validateCardholderForm(g, TODAY)).toEqual([])
  })

  it('toCardholderPayload', () => {
    const f = validUS()
    f.country_code = 'cn'
    const p = toCardholderPayload(f)
    expect(p.billing_address).toEqual(f.residential_address)
    expect(p.country_code).toBe('CN')
    expect('ssn' in p).toBe(false)
    expect('billing_same' in p).toBe(false)
    const u = toCardholderPayload(validUS())
    expect(u.ssn).toBe('123-45-6789')
    const z = validUS(); z.zone_number = '+86'
    expect(toCardholderPayload(z).zone_number).toBe('86')
  })

  it('submission label / tag / abandon', () => {
    const broken = { state: 'submitted', card_holder_id: '' }
    expect(submissionLabel(broken)).toBe('提交中断，结果未知')
    expect(submissionTagType(broken)).toBe('warning')
    expect(canAbandonSubmission(broken)).toBe(true)
    const ok = { state: 'submitted', card_holder_id: 'h1' }
    expect(submissionLabel(ok)).toBe('已提交')
    expect(submissionTagType(ok)).toBe('success')
    expect(canAbandonSubmission(ok)).toBe(false)
    expect(submissionLabel({ state: 'unknown' })).toBe('结果未知')
    expect(canAbandonSubmission({ state: 'unknown' })).toBe(true)
    expect(submissionLabel({ state: 'abandoned' })).toBe('已放弃')
    expect(submissionTagType({ state: 'abandoned' })).toBe('info')
    expect(canAbandonSubmission({ state: 'abandoned' })).toBe(false)
  })
})
