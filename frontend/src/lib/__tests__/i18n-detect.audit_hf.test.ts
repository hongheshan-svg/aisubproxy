import { describe, expect, it } from 'vitest'
import { pickLocale } from '../../i18n/detect'

describe('pickLocale：默认语言保持中文', () => {
  it('没有保存过语言时一律中文，不跟随浏览器', () => {
    expect(pickLocale(null)).toBe('zh')
    expect(pickLocale(undefined)).toBe('zh')
    expect(pickLocale('')).toBe('zh')
  })
  it('用户手动保存过的语言优先，非法值忽略', () => {
    expect(pickLocale('en')).toBe('en')
    expect(pickLocale('zh')).toBe('zh')
    expect(pickLocale('fr')).toBe('zh')
  })
})
