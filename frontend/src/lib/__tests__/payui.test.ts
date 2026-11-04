import { describe, expect, it } from 'vitest'
import { breakerReason, buildPayBody, orderModeLabel, payErrorText, sourceLabel } from '../payui'

describe('buildPayBody', () => {
  it('试运行到填表同时传 dry_run:true 和 dry_run_fill:true', () => {
    expect(buildPayBody('dry_fill', 'plus', 'PH', 's')).toEqual({ plan_key: 'plus', region: 'PH', session: 's', dry_run: true, dry_run_fill: true })
  })
  it('读金额试运行只传 dry_run:true', () => {
    const b = buildPayBody('dry_amount', 'plus', 'PH', 's')
    expect(b.dry_run).toBe(true)
    expect('dry_run_fill' in b).toBe(false)
  })
  it('真实付款 dry_run:false 且没有 dry_run_fill', () => {
    const b = buildPayBody('real', 'plus', 'PH', 's')
    expect(b.dry_run).toBe(false)
    expect('dry_run_fill' in b).toBe(false)
  })
})

describe('展示映射', () => {
  it('订单运行方式', () => {
    expect(orderModeLabel({ dry_run: true, dry_run_fill: true })).toBe('试运行到填表')
    expect(orderModeLabel({ dry_run: true })).toBe('读金额试运行')
    expect(orderModeLabel({ dry_run: false })).toBe('')
  })
  it('订单来源', () => {
    expect(sourceLabel('admin_test', '')).toBe('后台测试')
    expect(sourceLabel('cdk', '12')).toBe('CDK #12')
    expect(sourceLabel('', '')).toBe('—')
  })
  it('新错误码有翻译，未知码返回空串', () => {
    for (const c of ['cf_challenge', 'amount_changed', 'submit_not_clicked', 'dry_run_fill_failed']) expect(payErrorText(c)).not.toBe('')
    expect(payErrorText('amount_below_floor')).toContain('最低金额')
    expect(payErrorText('amount_below_floor')).toContain('没有注资')
    expect(payErrorText('something_new')).toBe('')
  })
  it('稳定性相关错误码有说明', () => {
    expect(payErrorText('verify_unreachable')).toContain('查不到账号状态')
    expect(payErrorText('verify_unreachable')).not.toContain('未开通')
    expect(payErrorText('stale_order')).toContain('没有点支付')
    // internal_error 既用于提交前放弃，也用于确认阶段转人工，说明要两种都涵盖
    expect(payErrorText('internal_error')).toContain('没点支付')
    expect(payErrorText('internal_error')).toContain('人工核对')
  })
  it('熔断原因优先用后端文案', () => {
    expect(breakerReason({ reason: 'submit_failures', reason_text: '连续 3 单' })).toBe('连续 3 单')
    expect(breakerReason({ reason: 'fill_failures', reason_text: '' })).toContain('填表')
  })
})
