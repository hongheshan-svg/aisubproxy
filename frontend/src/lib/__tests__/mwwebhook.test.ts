import { describe, expect, it } from 'vitest'
import { WEBHOOK_HINT, fmtBeijing, lastWebhookText, showWebhookHint } from '../mwwebhook'

const DAY = 86400

describe('mwwebhook lib', () => {
  it('fmtBeijing: 按北京时间（UTC+8）显示，与浏览器时区无关', () => {
    expect(fmtBeijing(1793694600)).toBe('2026-11-03 16:30:00') // 2026-11-03 08:30:00 UTC
    expect(fmtBeijing(1793736309)).toBe('2026-11-04 04:05:09') // 跨日
    expect(fmtBeijing(1798732800)).toBe('2027-01-01 00:00:00') // 跨年
  })

  it('lastWebhookText: 没收到过显示「从未」，否则显示北京时间', () => {
    expect(lastWebhookText(null)).toBe('从未')
    expect(lastWebhookText(undefined)).toBe('从未')
    expect(lastWebhookText(0)).toBe('从未')
    expect(lastWebhookText(1793694600)).toBe('2026-11-03 16:30:00（北京时间）')
  })

  it('showWebhookHint: 从未收到或超过 7 天时显示说明行', () => {
    const now = 1793694600
    expect(showWebhookHint(null, now)).toBe(true)
    expect(showWebhookHint(undefined, now)).toBe(true)
    expect(showWebhookHint(now - 6 * DAY, now)).toBe(false)
    expect(showWebhookHint(now - 7 * DAY, now)).toBe(false)
    expect(showWebhookHint(now - 7 * DAY - 1, now)).toBe(true)
    expect(showWebhookHint(now - 30 * DAY, now)).toBe(true)
  })

  it('说明行文字', () => {
    expect(WEBHOOK_HINT).toBe('没配置通知地址时，收不到 3DS 验证码；外部冻结、持卡人审核结果已改为每 10 分钟定时查询。')
  })
})
