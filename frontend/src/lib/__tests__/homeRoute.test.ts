// 首页分流表（规格 §6.1）：每一行一条测试。
import { describe, expect, it } from 'vitest'
import { homeRoute } from '../homeRoute'
import type { LookupResult } from '../aftersale'

const r = (o: Partial<LookupResult>): LookupResult => ({ cdk_code: 'SXC-AAAA', used: false, status: 'unused', ...o })

describe('homeRoute', () => {
  it('unused 不可重提 → 跳兑换页', () => {
    expect(homeRoute(r({ status: 'unused' }))).toEqual({ kind: 'goto', to: '/recharge?code=SXC-AAAA' })
  })
  it('unused 可重提 → 首页结果卡', () => {
    expect(homeRoute(r({ status: 'unused', can_resubmit: true, guide: 'retry_later' }))).toEqual({ kind: 'card' })
    expect(homeRoute(r({ status: 'unused', can_resubmit: true }))).toEqual({ kind: 'card' })
  })
  it('failed 可重提 → 首页结果卡', () => {
    expect(homeRoute(r({ status: 'failed', can_resubmit: true, guide: 'provider_retry' }))).toEqual({ kind: 'card' })
  })
  it('processing → 跳兑换页', () => {
    expect(homeRoute(r({ status: 'processing' }))).toEqual({ kind: 'goto', to: '/recharge?code=SXC-AAAA' })
  })
  it.each(['used', 'replaced', 'unsupported', 'disabled', 'expired', 'unknown'])('%s → 首页结果卡', (status) => {
    expect(homeRoute(r({ status, used: status === 'used' }))).toEqual({ kind: 'card' })
  })
  it('failed 不可重提 → 首页结果卡', () => {
    expect(homeRoute(r({ status: 'failed', can_resubmit: false }))).toEqual({ kind: 'card' })
  })
  it('不认得的状态 → 首页结果卡', () => {
    expect(homeRoute(r({ status: 'weird' }))).toEqual({ kind: 'card' })
  })
  it('码里有空格和 + 时正确编码', () => {
    expect(homeRoute(r({ cdk_code: 'a b+c', status: 'unused' }))).toEqual({ kind: 'goto', to: '/recharge?code=a%20b%2Bc' })
  })
})
