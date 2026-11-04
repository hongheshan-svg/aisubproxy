import { describe, expect, it } from 'vitest'
import {
  ACCOUNT_TYPE_LABEL, envProxyWarning, WEBHOOK_UNREACHABLE_HINT, accountTypeLabel, outcomeLabel, showTestReason, toOneLineBase64, webhookUnreachable,
} from '../mwconfig'

describe('mwconfig lib', () => {
  it('toOneLineBase64: 去掉首尾行和所有换行、空白', () => {
    const pem = '-----BEGIN PUBLIC KEY-----\nMIIBIjANBg\r\nkqhkiG9w0B\n  AQEFAAOC \n-----END PUBLIC KEY-----\n'
    expect(toOneLineBase64(pem)).toBe('MIIBIjANBgkqhkiG9w0BAQEFAAOC')
  })
  it('toOneLineBase64: 已经是一行 Base64 就原样返回；空串返回空串', () => {
    expect(toOneLineBase64('MIIBIj')).toBe('MIIBIj')
    expect(toOneLineBase64('')).toBe('')
  })

  it('outcomeLabel: 四个已知值翻成中文，其他值照原样', () => {
    expect(outcomeLabel('success')).toBe('连接成功')
    expect(outcomeLabel('rejected')).toBe('被拒')
    expect(outcomeLabel('unknown')).toBe('结果未知')
    expect(outcomeLabel('not_sent')).toBe('未发出')
    expect(outcomeLabel('weird')).toBe('weird')
    expect(outcomeLabel('')).toBe('')
  })

  it('accountTypeLabel: MAIN、SUB_ACCOUNT 翻成中文，其他值照原样', () => {
    expect(accountTypeLabel('MAIN')).toBe('主钱包')
    expect(accountTypeLabel('SUB_ACCOUNT')).toBe('子账户')
    expect(accountTypeLabel('OTHER')).toBe('OTHER')
    expect(accountTypeLabel(undefined)).toBe('—')
    expect(Object.keys(ACCOUNT_TYPE_LABEL).sort()).toEqual(['MAIN', 'SUB_ACCOUNT'])
  })

  it('showTestReason: 401 / 403 不显示 reason，其他情况显示', () => {
    expect(showTestReason(401)).toBe(false)
    expect(showTestReason(403)).toBe(false)
    expect(showTestReason(404)).toBe(true)
    expect(showTestReason(0)).toBe(true)
    expect(showTestReason(undefined)).toBe(true)
  })

  describe('webhookUnreachable', () => {
    const bad = [
      'http://example.com/api/v1/webhooks/marswallex', // http
      'https://127.0.0.1:8080/x',
      'https://localhost/x',
      'https://LOCALHOST:3000/x',
      'https://10.0.0.5/x',
      'https://10.255.255.255/x',
      'https://172.16.0.1/x',
      'https://172.31.255.1/x',
      'https://192.168.1.10/x',
      'https://[::1]/x',
      'https://[fc00::1]/x',
      'https://[fd12:3456::1]/x',
    ]
    const good = [
      'https://example.com/x',
      'https://pay.example.com:8443/x',
      'https://8.8.8.8/x',
      'https://172.15.0.1/x',
      'https://172.32.0.1/x',
      'https://192.169.0.1/x',
      'https://11.0.0.1/x',
      'https://1-2-3-4.sslip.io/x',
      'https://[2001:db8::1]/x',
      'https://[fe80::1]/x',
    ]
    it.each(bad)('不可达：%s', (u) => expect(webhookUnreachable(u)).toBe(true))
    it.each(good)('可达：%s', (u) => expect(webhookUnreachable(u)).toBe(false))
    it('空串和无法解析的地址不提示', () => {
      expect(webhookUnreachable('')).toBe(false)
      expect(webhookUnreachable(undefined)).toBe(false)
      expect(webhookUnreachable('not a url')).toBe(false)
    })
    it('提示文字原样', () => {
      expect(WEBHOOK_UNREACHABLE_HINT).toBe('这个地址 MarsWallex 访问不到。请用公网域名、通过 HTTPS 打开后台，再复制这里的地址；没有域名可以用 <IP 改横杠>.sslip.io。')
    })
  })

  it('envProxyWarning: 有环境代理才提示，文字原样', () => {
    expect(envProxyWarning('')).toBe('')
    expect(envProxyWarning(undefined)).toBe('')
    expect(envProxyWarning('proxy.example:8080')).toBe(
      '服务器设置了环境代理（proxy.example:8080），访问 MarsWallex 会经过它，白名单要填这个代理的出口 IP，上面两行不是平台看到的地址。',
    )
  })
})
