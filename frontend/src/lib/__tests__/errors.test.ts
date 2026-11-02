import { describe, expect, it } from 'vitest'
import { auditActionLabel, auditDetailLabel, mapApiError } from '../errors'

describe('mapApiError', () => {
  it('卡台未配置 Key：翻译成人话，带去配置链接，保留原文', () => {
    const m = mapApiError('card_api_key not configured（请先配置）')
    expect(m.mapped).toBe(true)
    expect(m.config).toBe(true)
    expect(m.text).toContain('卡台 API Key')
    expect(m.link?.to).toBe('/ops/integration')
    expect(m.raw).toBe('card_api_key not configured（请先配置）')
  })

  it('MarsWallex 未配置', () => {
    const m = mapApiError('marswallex: not_sent/config message=未配置 developerId')
    expect(m.config).toBe(true)
    expect(m.link?.to).toBe('/ops/marswallex')
    expect(m.raw).toContain('not_sent/config')
  })

  it('未识别的错误原样返回', () => {
    const m = mapApiError('something odd')
    expect(m.mapped).toBe(false)
    expect(m.text).toBe('something odd')
  })

  it('Error 对象取 message，空值用 fallback', () => {
    expect(mapApiError(new Error('boom')).text).toBe('boom')
    expect(mapApiError('', '加载失败').text).toBe('加载失败')
  })
})

describe('审计字典', () => {
  it('动作键翻译，cardpool. 前缀去掉，未知键原样', () => {
    expect(auditActionLabel('install_bootstrap')).toBe('完成安装')
    expect(auditActionLabel('cardpool.wallet_transfer')).toBe('钱包转账')
    expect(auditActionLabel('zzz_unknown')).toBe('zzz_unknown')
  })
  it('详情', () => {
    expect(auditDetailLabel('setup completed')).toBe('安装完成')
    expect(auditDetailLabel('x')).toBe('x')
  })
})
