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
 * provider 是用哪家供卡型服务商的卡池；空串时不传，服务端按 MarsWallex 处理。
 */
export function buildPayBody(mode: PayMode, plan: string, region: string, session: string, provider = '') {
  const body: Record<string, unknown> = { plan_key: plan, region, session, dry_run: mode !== 'real' }
  if (mode === 'dry_fill') body.dry_run_fill = true
  const p = provider.trim()
  if (p) body.provider = p
  return body
}

// ---------- 服务商选择（只列供卡型） ----------

export const DEFAULT_PAY_PROVIDER = 'marswallex'

interface ProviderLike { name: string; display_name?: string; kind: string }

/** 发起付款能选的服务商：注册表里 kind 为 card_supply 的那几家，顺序沿用注册表。 */
export function cardSupplyProviders<T extends ProviderLike>(reg: { providers: T[] } | null | undefined): T[] {
  return (reg?.providers || []).filter((p) => p.kind === 'card_supply')
}

/**
 * 预选服务商：链接带来的 ?provider= 是供卡型就用它；否则选 MarsWallex；MarsWallex 不在列表里时选第一家；
 * 列表为空返回空串（提交时服务端按 MarsWallex 处理）。
 */
export function pickPayProvider(list: ProviderLike[], wanted?: unknown): string {
  const w = typeof wanted === 'string' ? wanted.trim() : Array.isArray(wanted) ? String(wanted[0] ?? '').trim() : ''
  if (w && list.some((p) => p.name === w)) return w
  if (list.some((p) => p.name === DEFAULT_PAY_PROVIDER)) return DEFAULT_PAY_PROVIDER
  return list[0]?.name || ''
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
  provider_unavailable: '该服务商没有可用的卡池',
  checkout_url_untrusted: '支付链接的域名不在白名单里，已放弃（没有注资）',
  custom_checkout_unsupported: '点数结账返回的是嵌入式支付页，暂不支持，已放弃（没有注资）',
  save_payment_unconfirmed: '没能确认「保存付款信息」未勾选，已放弃，没有点支付',
  credit_account_unknown: '读不出账号 ID，不能买点数',
  credit_no_subscription: '账号没有生效中的订阅，点数只卖给已订阅 Plus 或 Pro 的账号',
  credit_plan_ineligible: '账号套餐不是 Plus 或 Pro，不能买点数',
  credit_not_personal: '不是个人账号（或读不出账号类型），不能买点数',
  credit_delinquent: '账号有欠费，不能买点数',
  credit_delinquency_unknown: '没能确认账号是否欠费，不能买点数',
  credit_currency_mismatch: '账号计费币种与码的地区币种不一致',
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

// ---------- 单据种类、关闭续费、点数单核对 ----------

interface KindLike { kind?: string; credit_quantity?: number; plan_key?: string }

/** 单据种类：credit 显示点数数量；空或 subscription 都是订阅开通（迁移前的旧单没有种类）。 */
export function orderKindLabel(o: KindLike): string {
  if (o.kind === 'credit') return o.credit_quantity ? `Codex 点数 ${o.credit_quantity}` : 'Codex 点数'
  return '订阅开通'
}

const RENEWAL_LABEL: Record<string, string> = {
  pending: '正在关闭续费',
  cancelled: '已关闭续费',
  failed: '关闭续费失败',
  skipped: '没有处理（会话已清空）',
}

/** 续费进度；空串表示不适用（点数单、试运行单、没成功的单）。 */
export function renewalLabel(state: string): string {
  if (!state) return '不适用'
  return RENEWAL_LABEL[state] ?? state
}

export function renewalTagType(state: string): 'success' | 'danger' | 'warning' | 'info' {
  if (state === 'cancelled') return 'success'
  if (state === 'failed') return 'danger'
  if (state === 'pending' || state === 'skipped') return 'warning'
  return 'info'
}

interface RenewalLike { state: string; dry_run?: boolean; kind?: string; renewal_state: string; has_session?: boolean }

/** 这张单是不是要关闭续费：成功的真实订阅开通单（与后端 wantsRenewalCancel 一致）。 */
export function wantsRenewalCancel(o: Pick<RenewalLike, 'state' | 'dry_run' | 'kind'>): boolean {
  return o.state === 'succeeded' && !o.dry_run && (o.kind === 'subscription' || !o.kind)
}

/** 「重试关闭续费」能不能点：与后端 RetryRenewal 的条件一致，返回不能点的原因，能点返回空串。 */
export function renewalRetryBlock(o: RenewalLike): string {
  if (!wantsRenewalCancel(o)) return '只有成功的真实开通单才需要关闭续费'
  if (o.renewal_state === 'cancelled') return '续费已经关闭并核实过'
  if ((o.renewal_state !== 'pending' && o.renewal_state !== 'failed') || !o.has_session) {
    return '会话已清空，系统没法再关，' + RENEWAL_MANUAL_HINT
  }
  return ''
}

export const RENEWAL_MANUAL_HINT = '请让用户自行在 ChatGPT 设置里关闭续费'

/** 续费没关掉、系统也没法再关时，要提醒管理员联系用户。 */
export function renewalNeedsUser(o: RenewalLike): boolean {
  if (!wantsRenewalCancel(o)) return false
  return o.renewal_state === 'failed' || o.renewal_state === 'skipped' || (o.renewal_state === 'pending' && !o.has_session)
}

const CARD_SAVED_LABEL: Record<string, string> = {
  not_saved: '没有存进用户账号',
  saved: '我们的卡被存进了用户账号，请联系用户删除',
  unknown: '没能核对（读不出用户的已存卡）',
}

/** 点数单成功后「我们的卡有没有被存进用户账号」的核对结果；空串表示没核对（没成功或人工定论的单）。 */
export function cardSavedLabel(v: string): string {
  if (!v) return '没有核对'
  return CARD_SAVED_LABEL[v] ?? v
}

/**
 * 单据详情里授权交易读不出时的提示。没有卡池的服务商是固定情况（单据本身的时间线照常可看），
 * 其余是暂时读不出，不能据此判定未扣款。
 */
export function evidenceNotice(err: string): { title: string; description: string } {
  if (err.includes('没有卡池')) {
    return { title: '这家服务商没有卡池', description: err + '。上面的单据信息和时间线照常可看，扣款情况请到该服务商自己的后台核对。' }
  }
  return { title: '授权交易暂时读不出', description: '这不代表没有授权交易，请稍后刷新再看，不要据此判定未扣款。' }
}

/** 试运行填表的字段命中事件（时间线里以「试运行填表 字段」开头）。 */
export function isFillFieldEvent(message: string): boolean {
  return /^(试运行)?填表 字段「/.test(message)
}
