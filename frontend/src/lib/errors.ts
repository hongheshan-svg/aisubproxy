// 把后端返回的原始错误码/英文键名翻译成管理员看得懂的话。
// 原始文本一定要保留（raw），排障时从详情/悬浮提示里看。

import { payErrorText } from './payui'

export interface MappedError {
  /** 给人看的文案；未识别时等于原文 */
  text: string
  /** 原始错误文本（trim 后） */
  raw: string
  /** 是否识别并翻译过 */
  mapped: boolean
  /** 属于「未配置凭证」类：页面应显示带「去配置」的空状态，而不是红色错误 */
  config?: boolean
  /** 去配置的跳转 */
  link?: { to: string; label: string }
}

interface Rule {
  test: RegExp
  text: string
  config?: boolean
  link?: { to: string; label: string }
}

const MW_CFG = { to: '/ops/marswallex', label: '去配置 MarsWallex' }

const RULES: Rule[] = [
  { test: /marswallex:.*(not_sent\/config|未配置)/i, text: '尚未配置 MarsWallex 凭证（Developer ID / API Token / 私钥）', config: true, link: MW_CFG },
  { test: /master.?key/i, text: '服务器未配置主密钥（APP_MASTER_KEY），无法使用密钥类功能' },
  { test: /ip.*(whitelist|白名单)|whitelist/i, text: '出口 IP 不在白名单内，请把本机出口 IP 加到服务商后台的白名单' },
  { test: /unauthorized|invalid.*token|token.*invalid/i, text: '登录已失效或凭证无效，请重新登录或检查凭证' },
  { test: /timeout|timed out|deadline exceeded/i, text: '请求超时，请稍后重试；资金类操作请先到单据列表确认结果，不要直接重复提交' },
  { test: /^(failed to fetch|network ?error|load failed)/i, text: '网络异常，请检查网络后重试' },
]

export function mapApiError(input: unknown, fallback = ''): MappedError {
  const raw = String((input as any)?.message ?? input ?? '').trim() || fallback
  // 付款单错误码（cf_challenge、amount_changed 等）单独出现时翻译成人话，原文仍在 raw 里
  if (/^[a-z][a-z0-9_]*$/.test(raw)) {
    const t = payErrorText(raw)
    if (t) return { text: t, raw, mapped: true }
  }
  for (const r of RULES) {
    if (r.test.test(raw)) return { text: r.text, raw, mapped: true, config: r.config, link: r.link }
  }
  return { text: raw, raw, mapped: false }
}

/** 审计日志动作键 → 中文。cardpool. 前缀先去掉再查。后端已不再产生的旧动作名后带「（历史）」。 */
const AUDIT_LABELS: Record<string, string> = {
  login: '管理员登录',
  install_bootstrap: '完成安装',
  change_password: '管理员改密码',
  update_settings: '修改站点设置',
  cdk_generate: '生成卡密（历史）',
  cdk_disable: '禁用卡密（历史）',
  cdk_delete: '删除卡密（历史）',
  cardplatform_cdk_note_set: '设置CDK备注（历史）',
  cardplatform_cdk_note_clear: '清除CDK备注（历史）',
  cardplatform_batch_note_set: '批量CDK备注（历史）',
  cardplatform_batch_note_clear: '批量清除CDK备注（历史）',
  cardplatform_issue_cdk: '卡台发码（历史）',
  cardplatform_store_cdk: '入库卡密（历史）',
  cardplatform_sync_cdk: '同步卡密（历史）',
  cardplatform_disable_cdk: '禁用卡台CDK（历史）',
  cardplatform_enable_cdk: '启用卡台CDK（历史）',
  cardplatform_batch_disable_cdk: '批量禁用CDK（历史）',
  cardplatform_batch_enable_cdk: '批量启用CDK（历史）',
  cardplatform_delete_card: '删除卡台卡（历史）',
  task_approve: '任务完成（历史）',
  task_reject: '任务失败（历史）',
  checkout_generate: '生成长链接（历史）',
  sync_plan_status: '同步套餐状态（历史）',
  update_card_selection_rules: '修改选卡规则（历史）',
  update_marswallex_config: '修改 MarsWallex 接入配置',
  generate_marswallex_keypair: '生成 MarsWallex 密钥对',
  pay_config_update: '修改自动付款配置',
  pay_proxies_update: '修改地区代理',
  pay_test_submit: '提交测试付款',
  pay_order_resolve: '人工定论付款单',
  system_update_start: '开始版本更新',
  system_update_applied: '版本更新完成',
  wallet_create_account: '新建钱包子账户',
  wallet_account_status: '修改子账户状态',
  wallet_conversion: '币种兑换',
  wallet_transfer: '钱包转账',
  apply_cards: '批量开卡',
  import_card: '导入卡',
  import_cardholder: '导入持卡人',
  holder_create: '新建持卡人',
  holder_update: '修改持卡人',
  holder_profile: '修改持卡人资料',
  holder_status: '修改持卡人状态',
  holder_abandon: '放弃持卡人',
  refresh_cardholder: '刷新持卡人',
  disable_card: '停用卡',
  fund_card: '给卡充值',
  view_sensitive: '查看卡号（历史）',
  view_otp: '查看验证码',
  recheck_order: '重查单据',
  confirm_failed: '确认单据失败',
  reprocess_event: '重新处理事件',
  backfill: '补拉交易',
  policy: '修改卡池策略',
  resume_product: '恢复产品',
  'card_health.policy': '修改健康度策略（历史）',
  'card_health.unblock': '解除健康度拦截（历史）',
  install_rollback: '安装回滚',
  update_site_logo: '修改站点图标',
  delete_site_logo: '删除站点图标',
  admin_feature_toggle: '切换功能开关',
  update_telegram: '修改 Telegram 告警',
  telegram_clear: '清除 Telegram 告警',
  runtime_fallback: '新版启动失败已回退',
  provider_config_update: '修改卡服务商配置',
  provider_default_update: '修改默认卡服务商',
  card_heads_sync: '同步卡头',
  card_head_add: '添加卡头',
  card_head_update: '修改卡头',
  card_head_delete: '删除卡头',
  local_cdk_issue: '本站发码',
  local_cdk_disable: '停用卡密',
  local_cdk_enable: '启用卡密',
  local_cdk_redeem: '买家兑换卡密',
  local_cdk_exchange: '代理商换码',
  pay_renewal_retry: '重试续费',
  pay_probe: '付款探测',
  pay_breaker_resume: '恢复自动付款',
  agent_create: '新建代理商',
  agent_update: '修改代理商',
  agent_disable: '停用代理商',
  agent_enable: '启用代理商',
  agent_reset_password: '重置代理商密码',
  agent_reset_totp: '重置两步验证',
  agent_adjust: '代理商调账',
  agent_override_update: '修改代理商专属价',
  agent_tier_create: '新建代理商等级',
  agent_tier_update: '修改代理商等级',
  agent_tier_delete: '删除代理商等级',
  agent_product_create: '新建批发商品',
  agent_product_update: '修改批发商品',
  agent_price_update: '修改批发价格',
  agent_exchange_resolve: '人工处理换码',
  admin_agent_shared_enable: '开启代理商对接',
  admin_agent_shared_disable: '关闭代理商对接',
  agent_exchange_dedupe: '换码记录去重',
  agent_login: '代理商登录',
  agent_login_failed: '代理商登录失败',
  agent_password_change: '代理商改密码',
  agent_password_change_failed: '代理商改密失败',
  agent_totp_bind: '绑定两步验证',
  agent_order: '代理商下单',
  agent_codes_export: '导出卡密',
  agent_shared_key_create: '创建对接密钥',
  agent_shared_key_reset: '重置对接密钥',
  agent_shared_disable: '代理商停用对接',
  agent_shared_allowlist: '修改对接白名单',
  refresh_card: '刷新卡',
  enable_card: '启用卡',
  freeze_card: '冻结卡',
  unfreeze_card: '解冻卡',
  cancel_card: '注销卡',
  sweep_card: '清零卡',
  lease_card: '租用卡',
  release_card: '释放卡',
  set_card_limit: '设置卡限额',
  set_card_limit_failed: '设置卡限额（结果不明）',
  selection: '修改选卡规则',
  apply_cards_failed: '批量开卡（结果不明）',
  cancel_card_failed: '注销卡（结果不明）',
  freeze_card_failed: '冻结卡（结果不明）',
  fund_card_failed: '给卡充值（结果不明）',
  holder_create_failed: '新建持卡人（结果不明）',
  holder_status_failed: '修改持卡人状态（结果不明）',
  holder_update_failed: '修改持卡人（结果不明）',
  sweep_card_failed: '清零卡（结果不明）',
  unfreeze_card_failed: '解冻卡（结果不明）',
  wallet_account_status_failed: '修改子账户状态（结果不明）',
  wallet_conversion_failed: '币种兑换（结果不明）',
  wallet_create_account_failed: '新建钱包子账户（结果不明）',
  wallet_transfer_failed: '钱包转账（结果不明）',
  install_rollback_failed: '安装回滚失败',
  admin_bootstrap_env: '环境变量建管理员',
}

export function auditActionLabel(action: string): string {
  const key = String(action || '').replace(/^cardpool\./, '')
  return AUDIT_LABELS[key] || AUDIT_LABELS[action] || action
}

const AUDIT_CLASS_ERR = new Set([
  'cancel_card', 'freeze_card', 'agent_disable', 'agent_reset_password', 'agent_reset_totp',
  'system_update_start', 'runtime_fallback', 'install_rollback', 'install_rollback_failed', 'admin_bootstrap_env',
])
const AUDIT_CLASS_FUNDS = new Set([
  'agent_adjust', 'agent_order', 'fund_card', 'wallet_transfer', 'wallet_conversion', 'sweep_card',
  'pay_order_resolve', 'agent_exchange_resolve',
])
const AUDIT_CLASS_WARN = new Set(['view_sensitive', 'view_otp', 'change_password', 'agent_password_change'])
const AUDIT_CLASS_INFO = new Set(['login', 'agent_login'])
const AUDIT_CLASS_GOOD = new Set(['local_cdk_issue', 'agent_create', 'agent_tier_create', 'agent_product_create'])

/** 审计日志动作标签配色（全局 pill 类）：高危和各 *_failed 红，资金类和敏感查看橙，登录蓝，新增绿，其余灰。 */
export function auditActionClass(action: string): string {
  const key = String(action || '').replace(/^cardpool\./, '')
  if (key.endsWith('_failed') || AUDIT_CLASS_ERR.has(key)) return 'pill pill-err'
  if (AUDIT_CLASS_FUNDS.has(key) || AUDIT_CLASS_WARN.has(key)) return 'pill pill-warn'
  if (AUDIT_CLASS_INFO.has(key)) return 'pill pill-info'
  if (AUDIT_CLASS_GOOD.has(key)) return 'pill pill-good'
  return 'pill'
}

const AUDIT_DETAIL: Record<string, string> = {
  'setup completed': '安装完成',
}
export function auditDetailLabel(detail: string): string {
  return AUDIT_DETAIL[String(detail || '').trim()] || detail
}
