import { describe, it, expect } from 'vitest'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

function make(locale: 'zh' | 'en') {
  return createI18n({
    legacy: false,
    globalInjection: true,
    locale,
    fallbackLocale: 'zh',
    messages: { zh, en },
  })
}

describe('vue-i18n 11 插值', () => {
  it('命名参数在中英文下渲染正确', () => {
    const zhT = make('zh').global.t
    const enT = make('en').global.t
    expect(zhT('batch.cdkRecognized', { n: 3 })).toBe('已识别 3 个卡密')
    expect(zhT('batch.currentOrdinal', { n: 2, total: 5 })).toBe('第 2 / 5 张')
    expect(zhT('batch.cdkListLabel', { max: 50 })).toBe('卡密列表（每行一个，最多 50）')
    const e = enT('batch.cdkRecognized', { n: 3 })
    expect(e).toContain('3')
    expect(e).not.toContain('{')
  })

  it('切换语言后输出随之变化', () => {
    const i18n = make('zh')
    i18n.global.locale.value = 'en'
    expect(i18n.global.t('batch.remaining', { n: 4 })).not.toBe('剩余 4 待贴')
  })
})
