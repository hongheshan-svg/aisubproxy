import { describe, expect, it } from 'vitest'
import { pickLocale } from '../../i18n/detect'

describe('pickLocale：默认语言跟随浏览器', () => {
  it('中文浏览器用中文，非中文浏览器用英文', () => {
    expect(pickLocale(null, 'zh-CN')).toBe('zh')
    expect(pickLocale(null, 'zh-TW')).toBe('zh')
    expect(pickLocale(null, 'en-US')).toBe('en')
    expect(pickLocale(null, 'ja-JP')).toBe('en')
    expect(pickLocale(undefined, '')).toBe('en')
  })
  it('用户手动保存过的语言优先，非法值忽略', () => {
    expect(pickLocale('en', 'zh-CN')).toBe('en')
    expect(pickLocale('zh', 'en-US')).toBe('zh')
    expect(pickLocale('fr', 'zh-CN')).toBe('zh')
  })
})
