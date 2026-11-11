import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { auditActionClass, auditActionLabel } from '../errors'

// 草稿表 1：后端现在会写、必须有中文名的动作（卡池的带 cardpool. 前缀，这里不带，两种写法都要能查到）
const CURRENT = [
  'install_rollback',
  'update_site_logo',
  'delete_site_logo',
  'admin_feature_toggle',
  'update_telegram',
  'telegram_clear',
  'runtime_fallback',
  'provider_config_update',
  'provider_default_update',
  'card_heads_sync',
  'card_head_add',
  'card_head_update',
  'card_head_delete',
  'local_cdk_issue',
  'local_cdk_disable',
  'local_cdk_enable',
  'local_cdk_redeem',
  'local_cdk_exchange',
  'pay_renewal_retry',
  'pay_probe',
  'pay_breaker_resume',
  'agent_create',
  'agent_update',
  'agent_disable',
  'agent_enable',
  'agent_reset_password',
  'agent_reset_totp',
  'agent_adjust',
  'agent_override_update',
  'agent_tier_create',
  'agent_tier_update',
  'agent_tier_delete',
  'agent_product_create',
  'agent_product_update',
  'agent_price_update',
  'agent_exchange_resolve',
  'admin_agent_shared_enable',
  'admin_agent_shared_disable',
  'agent_exchange_dedupe',
  'agent_login',
  'agent_login_failed',
  'agent_password_change',
  'agent_password_change_failed',
  'agent_totp_bind',
  'agent_order',
  'agent_codes_export',
  'agent_shared_key_create',
  'agent_shared_key_reset',
  'agent_shared_disable',
  'agent_shared_allowlist',
  'refresh_card',
  'enable_card',
  'freeze_card',
  'unfreeze_card',
  'cancel_card',
  'sweep_card',
  'lease_card',
  'release_card',
  'set_card_limit',
  'set_card_limit_failed',
  'selection',
]
// 草稿表 2：后端已不再写，名字后带「（历史）」
const HISTORY = [
  'cdk_generate',
  'cdk_disable',
  'cdk_delete',
  'cardplatform_cdk_note_set',
  'cardplatform_cdk_note_clear',
  'cardplatform_batch_note_set',
  'cardplatform_batch_note_clear',
  'cardplatform_issue_cdk',
  'cardplatform_store_cdk',
  'cardplatform_sync_cdk',
  'cardplatform_disable_cdk',
  'cardplatform_enable_cdk',
  'cardplatform_batch_disable_cdk',
  'cardplatform_batch_enable_cdk',
  'cardplatform_delete_card',
  'task_approve',
  'task_reject',
  'checkout_generate',
  'sync_plan_status',
  'update_card_selection_rules',
  'card_health.policy',
  'card_health.unblock',
  'view_sensitive',
]
// 后端线新增：平台结果不明时记 <动作>_failed
const POOL_FAILED = [
  'apply_cards',
  'cancel_card',
  'freeze_card',
  'fund_card',
  'holder_create',
  'holder_status',
  'holder_update',
  'sweep_card',
  'unfreeze_card',
  'wallet_account_status',
  'wallet_conversion',
  'wallet_create_account',
  'wallet_transfer',
]

describe('审计动作中文名', () => {
  it('表 1 的每个动作都有中文名，卡池前缀也能查到', () => {
    expect(CURRENT).toHaveLength(61)
    for (const k of CURRENT) {
      expect(auditActionLabel(k), k).not.toBe(k)
      expect(auditActionLabel('cardpool.' + k), k).toBe(auditActionLabel(k))
    }
  })
  it('表 2 的旧动作都带（历史）', () => {
    for (const k of HISTORY) expect(auditActionLabel(k), k).toMatch(/（历史）$/)
  })
  it('现行动作不带（历史）', () => {
    for (const k of CURRENT) expect(auditActionLabel(k), k).not.toContain('（历史）')
  })
  it('改名生效', () => {
    expect(auditActionLabel('login')).toBe('管理员登录')
    expect(auditActionLabel('change_password')).toBe('管理员改密码')
    expect(auditActionLabel('update_marswallex_config')).toBe('修改 MarsWallex 接入配置')
    expect(auditActionLabel('pay_config_update')).toBe('修改自动付款配置')
    expect(auditActionLabel('pay_proxies_update')).toBe('修改地区代理')
    expect(auditActionLabel('system_update_start')).toBe('开始版本更新')
    expect(auditActionLabel('system_update_applied')).toBe('版本更新完成')
    expect(auditActionLabel('cardpool.disable_card')).toBe('停用卡')
    expect(auditActionLabel('cardpool.cancel_card')).toBe('注销卡')
    expect(auditActionLabel('cardpool.apply_cards')).toBe('批量开卡')
    expect(auditActionLabel('update_settings')).toBe('修改站点设置')
    expect(auditActionLabel('cardpool.policy')).toBe('修改卡池策略')
    expect(auditActionLabel('cardpool.view_sensitive')).toBe('查看卡号（历史）')
  })
  it('卡池 *_failed 叫「xx（结果不明）」，另有安装回滚失败和环境变量建管理员', () => {
    for (const k of POOL_FAILED) {
      expect(auditActionLabel('cardpool.' + k + '_failed'), k).toBe(auditActionLabel(k) + '（结果不明）')
    }
    expect(auditActionLabel('install_rollback_failed')).toBe('安装回滚失败')
    expect(auditActionLabel('admin_bootstrap_env')).toBe('环境变量建管理员')
  })
  it('未知动作原样显示', () => {
    expect(auditActionLabel('brand_new_action')).toBe('brand_new_action')
  })
})

describe('审计动作配色', () => {
  it('资金类橙、高危和 *_failed 红、敏感橙、登录蓝、新增绿、其余灰', () => {
    expect(auditActionClass('agent_adjust')).toBe('pill pill-warn')
    expect(auditActionClass('cardpool.sweep_card')).toBe('pill pill-warn')
    expect(auditActionClass('cardpool.fund_card')).toBe('pill pill-warn')
    expect(auditActionClass('cardpool.cancel_card')).toBe('pill pill-err')
    expect(auditActionClass('cardpool.freeze_card')).toBe('pill pill-err')
    expect(auditActionClass('agent_reset_totp')).toBe('pill pill-err')
    expect(auditActionClass('runtime_fallback')).toBe('pill pill-err')
    expect(auditActionClass('install_rollback')).toBe('pill pill-err')
    expect(auditActionClass('install_rollback_failed')).toBe('pill pill-err')
    expect(auditActionClass('admin_bootstrap_env')).toBe('pill pill-err')
    expect(auditActionClass('cardpool.fund_card_failed')).toBe('pill pill-err')
    expect(auditActionClass('agent_login_failed')).toBe('pill pill-err')
    expect(auditActionClass('view_otp')).toBe('pill pill-warn')
    expect(auditActionClass('agent_password_change')).toBe('pill pill-warn')
    expect(auditActionClass('login')).toBe('pill pill-info')
    expect(auditActionClass('agent_login')).toBe('pill pill-info')
    expect(auditActionClass('local_cdk_issue')).toBe('pill pill-good')
    expect(auditActionClass('agent_product_create')).toBe('pill pill-good')
    expect(auditActionClass('agent_update')).toBe('pill')
  })
  it('审计页用统一配色函数和新页头文案，不再写死旧动作', () => {
    const src = readFileSync(new URL('../../views/admin/AuditLogs.vue', import.meta.url), 'utf8')
    expect(src).toContain('const actionClass = auditActionClass')
    expect(src).toContain('记录管理员和代理商的登录、改密、资金与卡池、付款配置、代理商、系统更新等敏感操作。')
    expect(src).not.toContain('checkout_generate')
  })
})
