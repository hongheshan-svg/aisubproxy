import { describe, expect, it } from 'vitest'
import { secretErrorText, secretPlaceholder } from '../secretError'

describe('secretErrorText', () => {
  it('取出 <键>_error', () => {
    expect(secretErrorText({ card_api_key_error: ' 解不开 ' }, 'card_api_key')).toBe('解不开')
  })
  it('没有错误或类型不对返回空串', () => {
    expect(secretErrorText({}, 'card_api_key')).toBe('')
    expect(secretErrorText(null, 'card_api_key')).toBe('')
    expect(secretErrorText({ webhook_secret_error: 5 }, 'webhook_secret')).toBe('')
  })
})

describe('secretPlaceholder', () => {
  it('解不开时提示重新填写，不显示已配置', () => {
    expect(secretPlaceholder({ card_api_key_configured: true, card_api_key_error: 'x' }, 'card_api_key', '粘贴')).toBe('需要重新填写')
  })
  it('已配置显示尾号，未配置显示默认提示', () => {
    expect(secretPlaceholder({ card_api_key_configured: true, card_api_key_hint: '****abcd' }, 'card_api_key', '粘贴')).toBe('已配置 ****abcd')
    expect(secretPlaceholder({}, 'card_api_key', '粘贴')).toBe('粘贴')
    expect(secretPlaceholder({ webhook_secret_set: true, webhook_secret_hint: '****1234' }, 'webhook_secret', '未配置')).toBe('已配置 ****1234')
  })
})
