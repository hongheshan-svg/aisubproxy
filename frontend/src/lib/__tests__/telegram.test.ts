import { describe, expect, it } from 'vitest'
import { telegramSourceTag } from '../telegram'

describe('telegramSourceTag', () => {
  it('按生效来源给出标签', () => {
    expect(telegramSourceTag('settings')).toEqual({ text: '后台配置', type: 'success' })
    expect(telegramSourceTag('env')).toEqual({ text: '环境变量', type: 'warning' })
    expect(telegramSourceTag('none')).toEqual({ text: '未配置', type: 'info' })
  })
  it('还没读到配置时显示读取中', () => {
    expect(telegramSourceTag(undefined).text).toBe('读取中')
  })
})
