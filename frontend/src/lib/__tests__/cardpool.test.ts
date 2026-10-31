import { describe, expect, it } from 'vitest'
import { canAbandonSubmission, submissionLabel, submissionTagType, emptyCardholderForm, toCardholderPayload, validateCardholderForm, FLAG_LABEL, fmtTs, hasEmoji, isUncertain, isUnknownOutcome, kindLabel, ledgerPagerTotal, mainSellCurrencies, orderStateText, parseAmountCents, submitOutcome } from '../cardpool'

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

  it('fmtTs: 空、0 与负数（零值时间）显示 —', () => {
    expect(fmtTs(null)).toBe('—')
    expect(fmtTs(0)).toBe('—')
    expect(fmtTs(-62135596800)).toBe('—')
    expect(fmtTs(1700000000)).not.toBe('—')
  })

  it('orderStateText', () => {
    expect(orderStateText({ state: 'success', last_error: '' })).toBe('成功')
    expect(orderStateText({ state: 'failed', last_error: '余额不足' })).toBe('失败（余额不足）')
    expect(orderStateText({ state: 'processing', last_error: 'x' })).toBe('处理中')
    expect(orderStateText({ state: 'weird' })).toBe('weird')
  })

  it('submitOutcome: 资金单提交结果跟随单据状态', () => {
    const ok200 = (state: string, last_error = '') => submitOutcome({ ok: true, status: 200, data: { state, last_error } }, '兑换')
    for (const st of ['processing', 'success']) {
      const o = ok200(st)
      expect(o.kind).toBe('ok')
      expect(o.clearForm).toBe(true)
      expect(o.message).toContain('兑换已提交')
    }
    for (const st of ['unknown', 'needs_review']) {
      const o = ok200(st)
      expect(o.kind).toBe('uncertain')
      expect(o.clearForm).toBe(false)
      expect(o.message).toContain('结果未知，切勿重复提交，请查看下方单据')
    }
    for (const st of ['failed', 'not_sent']) {
      const o = ok200(st, '平台返回 FAIL')
      expect(o.kind).toBe('failed')
      expect(o.clearForm).toBe(false)
      expect(o.message).toContain('平台返回 FAIL')
    }
    // 502 只来自发送前的账户同步：没有建单
    const sync = submitOutcome({ ok: false, status: 502, data: { error: '同步超时', outcome: 'unknown' } }, '兑换')
    expect(sync).toEqual({ kind: 'not_submitted', clearForm: false, message: '未提交：同步超时' })
    // 网络异常：结果未知
    const net = submitOutcome({ ok: false, status: 0, data: { error: '网络错误' } }, '转账')
    expect(net.kind).toBe('uncertain')
    expect(net.clearForm).toBe(false)
    expect(net.message).toContain('切勿重复提交')
    // 500 可能是发出后本地记录失败：按结果未知
    expect(submitOutcome({ ok: false, status: 500, data: { error: '操作失败' } }, '兑换').kind).toBe('uncertain')
    // 校验失败、已有在途单：明确未执行
    for (const status of [400, 409]) {
      const o = submitOutcome({ ok: false, status, data: { error: 'E' + status } }, '转账')
      expect(o).toEqual({ kind: 'failed', clearForm: false, message: 'E' + status })
    }
  })

  it('ledgerPagerTotal: 总数未知时满页才有下一页', () => {
    expect(ledgerPagerTotal(41, 3, 20, 1)).toBe(41)
    expect(ledgerPagerTotal(-1, 1, 20, 20)).toBe(21) // 满页 → 可翻到第 2 页
    expect(ledgerPagerTotal(-1, 2, 20, 20)).toBe(41) // 第 2 页满页 → 可翻到第 3 页
    expect(ledgerPagerTotal(-1, 2, 20, 7)).toBe(27) // 短页 → 本页即最后一页
    expect(ledgerPagerTotal(-1, 1, 20, 0)).toBe(0)
    expect(ledgerPagerTotal(-1, 2, 20, 0)).toBe(21) // 翻到空页：停在第 2 页，不再有下一页
    const pages = (t: number) => Math.max(1, Math.ceil(t / 20))
    expect(pages(ledgerPagerTotal(-1, 2, 20, 20))).toBe(3)
    expect(pages(ledgerPagerTotal(-1, 2, 20, 7))).toBe(2)
    expect(pages(ledgerPagerTotal(-1, 2, 20, 0))).toBe(2)
  })

  it('mainSellCurrencies: 汇总全部未消失主钱包的币种', () => {
    const acct = (account_type: string, gone: boolean, ...cs: string[]) => ({
      account_type, gone, balances: cs.map((currency) => ({ currency })),
    })
    expect(mainSellCurrencies([acct('MAIN', false, 'USD'), acct('MAIN', false, 'USDT'), acct('SUB_ACCOUNT', false, 'HKD')]))
      .toEqual(['USD', 'USDT'])
    expect(mainSellCurrencies([acct('MAIN', false, 'USD'), acct('MAIN', true, 'EUR')])).toEqual(['USDT', 'USD'])
    expect(mainSellCurrencies([{ account_type: 'MAIN', gone: false, balances: null }])).toEqual(['USDT'])
  })
})
