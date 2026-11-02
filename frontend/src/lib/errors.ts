// 把后端返回的原始错误码/英文键名翻译成管理员看得懂的话。
// 原始文本一定要保留（raw），排障时从详情/悬浮提示里看。

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

const CARD_CFG = { to: '/ops/integration', label: '去配置卡台' }
const MW_CFG = { to: '/ops/marswallex', label: '去配置 MarsWallex' }

const RULES: Rule[] = [
  { test: /card_api_key not configured/i, text: '尚未配置卡台 API Key', config: true, link: CARD_CFG },
  { test: /card_api_base not configured/i, text: '尚未配置卡台地址', config: true, link: CARD_CFG },
  { test: /marswallex:.*(not_sent\/config|未配置)/i, text: '尚未配置 MarsWallex 凭证（Developer ID / API Token / 私钥）', config: true, link: MW_CFG },
  { test: /master.?key/i, text: '服务器未配置主密钥（APP_MASTER_KEY），无法使用密钥类功能' },
  { test: /ip.*(whitelist|白名单)|whitelist/i, text: '出口 IP 不在白名单内，请把本机出口 IP 加到服务商后台的白名单' },
  { test: /unauthorized|invalid.*token|token.*invalid/i, text: '登录已失效或凭证无效，请重新登录或检查凭证' },
  { test: /timeout|timed out|deadline exceeded/i, text: '请求超时，请稍后重试；资金类操作请先到单据列表确认结果，不要直接重复提交' },
  { test: /^(failed to fetch|network ?error|load failed)/i, text: '网络异常，请检查网络后重试' },
]

export function mapApiError(input: unknown, fallback = ''): MappedError {
  const raw = String((input as any)?.message ?? input ?? '').trim() || fallback
  for (const r of RULES) {
    if (r.test.test(raw)) return { text: r.text, raw, mapped: true, config: r.config, link: r.link }
  }
  return { text: raw, raw, mapped: false }
}

/** 审计日志动作键 → 中文。cardpool. 前缀先去掉再查。 */
const AUDIT_LABELS: Record<string, string> = {
  login: '登录',
  install_bootstrap: '完成安装',
  change_password: '修改密码',
  update_settings: '修改站点设置',
  cdk_generate: '生成卡密',
  cdk_disable: '禁用卡密',
  cdk_delete: '删除卡密',
  cardplatform_cdk_note_set: '设置CDK备注',
  cardplatform_cdk_note_clear: '清除CDK备注',
  cardplatform_batch_note_set: '批量CDK备注',
  cardplatform_batch_note_clear: '批量清除CDK备注',
  cardplatform_issue_cdk: '卡台发码',
  cardplatform_store_cdk: '入库卡密',
  cardplatform_sync_cdk: '同步卡密',
  cardplatform_disable_cdk: '禁用卡台CDK',
  cardplatform_enable_cdk: '启用卡台CDK',
  cardplatform_batch_disable_cdk: '批量禁用CDK',
  cardplatform_batch_enable_cdk: '批量启用CDK',
  cardplatform_delete_card: '删除卡台卡',
  task_approve: '任务完成',
  task_reject: '任务失败',
  checkout_generate: '生成长链接',
  sync_plan_status: '同步套餐状态',
  update_card_selection_rules: '修改选卡规则',
  update_marswallex_config: '修改 MarsWallex 配置',
  generate_marswallex_keypair: '生成 MarsWallex 密钥对',
  pay_config_update: '修改付款配置',
  pay_proxies_update: '修改付款代理',
  pay_test_submit: '提交测试付款',
  pay_order_resolve: '人工定论付款单',
  system_update_start: '开始系统更新',
  system_update_applied: '系统更新完成',
  wallet_create_account: '新建钱包子账户',
  wallet_account_status: '修改子账户状态',
  wallet_conversion: '币种兑换',
  wallet_transfer: '钱包转账',
  apply_cards: '开卡',
  import_card: '导入卡',
  import_cardholder: '导入持卡人',
  holder_create: '新建持卡人',
  holder_update: '修改持卡人',
  holder_profile: '修改持卡人资料',
  holder_status: '修改持卡人状态',
  holder_abandon: '放弃持卡人',
  refresh_cardholder: '刷新持卡人',
  disable_card: '注销卡',
  fund_card: '给卡充值',
  view_sensitive: '查看卡号',
  view_otp: '查看验证码',
  recheck_order: '重查单据',
  confirm_failed: '确认单据失败',
  reprocess_event: '重新处理事件',
  backfill: '补拉交易',
  policy: '修改卡池策略',
  resume_product: '恢复产品',
  'card_health.policy': '修改健康度策略',
  'card_health.unblock': '解除健康度拦截',
}

export function auditActionLabel(action: string): string {
  const key = String(action || '').replace(/^cardpool\./, '')
  return AUDIT_LABELS[key] || AUDIT_LABELS[action] || action
}

const AUDIT_DETAIL: Record<string, string> = {
  'setup completed': '安装完成',
}
export function auditDetailLabel(detail: string): string {
  return AUDIT_DETAIL[String(detail || '').trim()] || detail
}
