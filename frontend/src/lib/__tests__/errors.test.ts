import { describe, expect, it } from 'vitest'
import { auditActionLabel, auditDetailLabel, mapApiError } from '../errors'

describe('mapApiError', () => {
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

  it('付款单错误码翻译，原文保留', () => {
    const m = mapApiError('cf_challenge')
    expect(m.mapped).toBe(true)
    expect(m.text).toContain('Cloudflare')
    expect(m.raw).toBe('cf_challenge')
    expect(mapApiError('some_unknown_code').mapped).toBe(false)
  })
})
