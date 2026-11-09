// 第 ③ 屏「核对账号」用的账号摘要、状态判断和放弃码文案键。全部是纯函数，数据都是假的。
import { describe, expect, it } from 'vitest'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { accountFromPreflight, blockingKey, confirmState, type AccountSummary } from '../redeemAccount'

const checkedAccount: AccountSummary = {
  maskedEmail: 'al***@example.com',
  currentPlan: 'free',
  subscriptionHasActive: false,
  recoveryRequired: false,
  delinquent: false,
  checked: true,
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function lookupKey(src: any, key: string): unknown {
  return key.split('.').reduce((a, k) => (a && typeof a === 'object' ? a[k] : undefined), src)
}

describe('accountFromPreflight', () => {
  it('MarsWallex 返回 account_checked:false 时 checked === false', () => {
    const a = accountFromPreflight({
      provider_kind: 'card_supply',
      email: 'alice@example.com',
      account_email: 'alice@example.com',
      preflight_token: 'pf_1',
      expires_in: 600,
      account_checked: false,
    })
    expect(a.checked).toBe(false)
    expect(a.maskedEmail).toBe('al***@example.com')
  })

  it('不是 MarsWallex 的码、返回不带账号信息时 checked 为 null', () => {
    expect(accountFromPreflight({ preflight_token: 'pf_up' }).checked).toBeNull()
    expect(accountFromPreflight({ code: 0, data: { preflight_token: 'pf_up' } }).checked).toBeNull()
    expect(accountFromPreflight(null).checked).toBeNull()
  })

  it('邮箱已脱敏，摘要里没有完整邮箱', () => {
    const a = accountFromPreflight({
      email: 'alice@example.com',
      account_checked: true,
      current_plan: 'plus',
      subscription_has_active: true,
    })
    expect(a.maskedEmail).toBe('al***@example.com')
    expect(JSON.stringify(a)).not.toContain('alice@example.com')
    expect(a.checked).toBe(true)
    expect(a.currentPlan).toBe('plus')
    expect(a.subscriptionHasActive).toBe(true)
  })

  it('上游码的 data 包裹也能读，带了账号信息就算查过', () => {
    const a = accountFromPreflight({
      code: 0,
      data: {
        account_email: 'bob@example.org',
        currentPlan: 'free',
        subscription_has_active: false,
        subscription_recovery_required: true,
        subscription_is_delinquent: true,
      },
    })
    expect(a).toEqual({
      maskedEmail: 'bo***@example.org',
      currentPlan: 'free',
      subscriptionHasActive: false,
      recoveryRequired: true,
      delinquent: true,
      checked: true,
    })
  })
})

describe('confirmState', () => {
  const base = { account: checkedAccount, blockingCode: '', blockingReason: '', satisfied: false }

  it('拿到账号信息且没有阻塞项：可以开通', () => {
    expect(confirmState(base)).toBe('ok')
  })

  it('有放弃码、有原因或当前套餐已满足：不能用', () => {
    expect(confirmState({ ...base, blockingCode: 'already_subscribed' })).toBe('blocked')
    expect(confirmState({ ...base, blockingReason: '账号已有订阅' })).toBe('blocked')
    expect(confirmState({ ...base, satisfied: true })).toBe('blocked')
  })

  it('要恢复订阅或欠费：要先恢复订阅', () => {
    expect(confirmState({ ...base, account: { ...checkedAccount, recoveryRequired: true } })).toBe('recover')
    expect(confirmState({ ...base, account: { ...checkedAccount, delinquent: true } })).toBe('recover')
  })

  it('查不了或没返回账号信息：请自己确认', () => {
    expect(confirmState({ ...base, account: { ...checkedAccount, checked: false } })).toBe('self_check')
    expect(confirmState({ ...base, account: { ...checkedAccount, checked: null } })).toBe('self_check')
    expect(confirmState({ ...base, account: null })).toBe('self_check')
  })

  it('既要恢复订阅又有放弃码：先恢复订阅', () => {
    expect(confirmState({
      ...base,
      account: { ...checkedAccount, recoveryRequired: true },
      blockingCode: 'credit_delinquent',
    })).toBe('recover')
  })

  it('有放弃码且没查到账号：不能用', () => {
    expect(confirmState({ ...base, account: { ...checkedAccount, checked: false }, blockingCode: 'already_subscribed' })).toBe('blocked')
    expect(confirmState({ ...base, account: null, blockingReason: '登录态失效' })).toBe('blocked')
  })
})

describe('blockingKey', () => {
  const CODES = [
    'already_subscribed',
    'credit_no_subscription',
    'credit_plan_ineligible',
    'credit_not_personal',
    'credit_delinquent',
    'credit_currency_mismatch',
    'credit_account_unknown',
    'credit_delinquency_unknown',
  ]

  it('规格表里每个码都有键，中英文语言包都能取到', () => {
    for (const code of CODES) {
      const key = blockingKey(code)
      expect(key, code).not.toBeNull()
      expect(key!.startsWith('redeemUi.blocking.'), code).toBe(true)
      expect(typeof lookupKey(zh, key!), `${code} zh`).toBe('string')
      expect(typeof lookupKey(en, key!), `${code} en`).toBe('string')
    }
  })

  it('两个「暂时没法确认」的码共用一个键', () => {
    expect(blockingKey('credit_delinquency_unknown')).toBe(blockingKey('credit_account_unknown'))
  })

  it('未知码返回 null', () => {
    expect(blockingKey('')).toBeNull()
    expect(blockingKey('something_else')).toBeNull()
    expect(blockingKey('session_invalid')).toBeNull()
  })
})
