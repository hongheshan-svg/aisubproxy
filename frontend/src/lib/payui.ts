// 自动付款页面的展示逻辑：试运行三选一、订单来源、错误码翻译、熔断状态。

export type PayMode = 'dry_amount' | 'dry_fill' | 'real'

export const PAY_MODE_LABEL: Record<PayMode, string> = {
  dry_amount: '读金额试运行', dry_fill: '试运行到填表', real: '真实付款',
}

export const PAY_MODE_DESC: Record<PayMode, string> = {
  dry_amount: '只打开支付页读取应付金额，不选卡、不付款。',
  dry_fill: '会真的打开支付页并填测试卡，但绝不点支付、不碰卡池。',
  real: '从卡池取一张卡注资并真实付款，会动钱。',
}

/**
 * 提交付款单的请求体。试运行到填表必须同时传 dry_run:true：
 * 没打补丁的服务端会忽略 dry_run_fill，只传它就会变成会动钱的真实付款单。
 */
export function buildPayBody(mode: PayMode, plan: string, region: string, session: string) {
  const body: Record<string, unknown> = { plan_key: plan, region, session, dry_run: mode !== 'real' }
  if (mode === 'dry_fill') body.dry_run_fill = true
  return body
}

/** 订单的运行方式标签；空串表示真实付款。 */
export function orderModeLabel(o: { dry_run?: boolean; dry_run_fill?: boolean }): string {
  if (o.dry_run_fill) return PAY_MODE_LABEL.dry_fill
  if (o.dry_run) return PAY_MODE_LABEL.dry_amount
  return ''
}

/** 来源列：admin_test = 后台测试，cdk = 用户用 CDK 兑换（source_ref 是本地码 ID）。 */
export function sourceLabel(source: string, ref: string): string {
  if (source === 'admin_test') return '后台测试'
  if (source === 'cdk') return ref ? `CDK #${ref}` : 'CDK'
  return source || '—'
}

const PAY_ERROR_LABEL: Record<string, string> = {
  cf_challenge: '被 Cloudflare 拦截，请检查该地区代理的出口 IP',
  amount_changed: '填表后应付金额变了，已放弃提交',
  amount_over_cap: '应付金额超过上限，已放弃',
  amount_unreadable: '读不出应付金额，已放弃',
  submit_not_clicked: '支付按钮没有被点击，未扣款',
  dry_run_fill_failed: '试运行填表失败（没有点支付，没有动卡池）',
  dry_run_refused: '试运行单不允许真实付款，已拒绝',
  fill_failed: '填写支付表单失败',
  form_rejected: '支付页表单校验没通过',
  card_declined: '卡被拒',
  fund_failed: '给卡注资失败',
  fund_timeout: '注资等待超时',
  no_card: '卡池里没有可用的卡',
  acquire_failed: '取卡失败',
  card_info_failed: '读取卡信息失败',
  card_sensitive_failed: '读取卡敏感信息失败',
  card_expiry_invalid: '卡有效期无效',
  card_currency_unsupported: '这张卡不支持该币种',
  currency_mismatch: '支付页币种与配置不一致',
  session_invalid: 'ChatGPT 会话无效或已过期',
  session_unreadable: '读不出 ChatGPT 会话',
  already_subscribed: '该账号已有订阅',
  plan_unavailable: '该账号当前不能开通这个套餐',
  checkout_failed: '生成支付链接失败',
  chatgpt_failed: '访问 ChatGPT 失败',
  proxy_unavailable: '该地区代理不可用',
  browser_failed: '浏览器启动失败',
  page_failed: '支付页打开失败',
  verify_timeout: '确认超时，需要人工核对',
  evidence_unavailable: '拿不到交易证据，需要人工核对',
  evidence_conflict: '交易证据互相矛盾，需要人工核对',
  service_restart: '服务重启打断了这一单，需要人工核对',
  internal_error: '内部错误',
}

/** 错误码翻译；没收录的返回空串，页面仍展示原始错误码。 */
export function payErrorText(code: string): string {
  return PAY_ERROR_LABEL[code] ?? ''
}

export interface Breaker {
  paused: boolean; reason: string; reason_text: string; threshold: number
  submitted_failure_streak: number; fill_failure_streak: number
  tripped_at: number | null; order_nos: string[]; last_resume_at: number | null; last_resume_by: string
}

/** 暂停原因：优先用后端给的中文说明。 */
export function breakerReason(b: Pick<Breaker, 'reason' | 'reason_text'>): string {
  if (b.reason_text) return b.reason_text
  switch (b.reason) {
    case 'submit_failures': return '连续多单提交后被拒或待人工'
    case 'fill_failures': return '连续多单填表失败'
    case 'check_failed': return '熔断状态查询失败，按暂停处理'
    default: return b.reason || ''
  }
}

/** 试运行填表的字段命中事件（时间线里以「试运行填表 字段」开头）。 */
export function isFillFieldEvent(message: string): boolean {
  return /^(试运行)?填表 字段「/.test(message)
}
