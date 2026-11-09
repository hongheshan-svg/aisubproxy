// planLabelT：按语言包取套餐名。中文必须与 planLabel 逐字相同，英文不能带中文。
import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { planLabel, planLabelT } from '../plan'

const tOf = (locale: 'zh' | 'en') => {
  const i = createI18n({ legacy: false, locale, messages: { zh, en } })
  return (k: string, p?: Record<string, unknown>) => String(i.global.t(k, p as never))
}
const PLANS = ['free', '', 'plus', 'plus_half', 'pro_5x', 'pro_5x_renew', 'pro_20x_renew', 'credit250', 'claude_pro',
  'grok_plus_monthly', 'grok_super_yearly', 'grok_premium_monthly_half', 'x_premium_monthly', 'team']

describe('planLabelT', () => {
  it('中文与 planLabel 逐字相同', () => {
    const t = tOf('zh')
    for (const p of PLANS) expect(planLabelT(p, t), p).toBe(planLabel(p))
  })
  it('英文不含中文', () => {
    const t = tOf('en')
    for (const p of PLANS) expect(planLabelT(p, t), p).not.toMatch(/[一-鿿]/)
  })
})
