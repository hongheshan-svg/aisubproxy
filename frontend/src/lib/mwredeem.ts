// 用户兑换页上供卡型服务商的码（本站付款、本地码）的状态展示。上游发码型的码不走这里，保持原样。

/** 响应里的 provider_kind 为 card_supply 才启用本站付款相关界面；没有 provider_kind（上游原样转发）一律按上游处理。 */
export function isCardSupply(kind: unknown): boolean {
  return kind === 'card_supply'
}

/** 通道标签：优先用 provider_display_name，缺失时退回 provider，再缺失显示「本站」。 */
export function channelLabel(...infos: Array<{ provider?: unknown; provider_display_name?: unknown } | null | undefined>): string {
  for (const i of infos) {
    const n = String(i?.provider_display_name || '').trim()
    if (n) return n
  }
  for (const i of infos) {
    const n = String(i?.provider || '').trim()
    if (n) return n
  }
  return '本站'
}

// ---------- 码的种类：订阅开通码、Codex 点数码 ----------

interface KindInfo { kind?: unknown; auto_renew?: unknown; requires_credit_terms?: unknown; credit_quantity?: unknown }

/** 点数码：预览或预检带 requires_credit_terms=true 或 kind=credit。兑换前必须勾选须知。 */
export function isCreditCode(info: KindInfo | null | undefined): boolean {
  return info?.requires_credit_terms === true || info?.kind === 'credit'
}

/** 订阅开通码：kind=subscription 或明确 auto_renew=false（点数码不算）。 */
export function isOneMonthCode(info: KindInfo | null | undefined): boolean {
  if (!info || isCreditCode(info)) return false
  return info.kind === 'subscription' || info.auto_renew === false
}

export const ONE_MONTH_TEXT = '本码开通一个月，到期不自动续费'

/** 点数兑换须知，逐条显示；用户勾选同意后才能兑换（服务端同样要求 agree_credit_terms）。 */
export const CREDIT_TERMS: readonly string[] = [
  '仅限已订阅 Plus 或 Pro 的账号',
  '按账号计费币种付款，码上的地区必须与之一致',
  '点数 12 个月过期，不退款',
]

/** 点数码没勾须知时不能兑换，返回原因；其余情况返回空串。 */
export function creditTermsBlock(info: KindInfo | null | undefined, agreed: boolean): string {
  return isCreditCode(info) && !agreed ? '请先阅读并勾选点数兑换须知' : ''
}

/** 兑换请求体：点数码且已勾选时带上 agree_credit_terms:true，其余码保持原样。 */
export function withCreditTerms<T extends Record<string, unknown>>(body: T, info: KindInfo | null | undefined, agreed: boolean): T & { agree_credit_terms?: true } {
  if (isCreditCode(info) && agreed) return { ...body, agree_credit_terms: true as const }
  return body
}

const STATUS_TEXT: Record<string, string> = {
  queued: '排队中',
  running: '处理中',
  completed: '已完成',
  failed_precharge: '未扣款，兑换失败',
  declined: '付款被拒',
  failed: '兑换失败',
  review: '待人工核对',
  cancelled: '已取消',
}

export function mwStatusText(status: string): string {
  const s = String(status || '').toLowerCase()
  return STATUS_TEXT[s] ?? (status || '—')
}

export type MwOutcomeKind = 'running' | 'success' | 'retry' | 'manual' | 'failed'
export interface MwOutcome { kind: MwOutcomeKind; text: string; attemptsLeft?: number }

interface OrderLike { can_retry?: boolean; attempts_left?: number }

/**
 * 结果页的结论：
 * - failed_precharge 且还有次数：未扣款，可以重新兑换
 * - failed_precharge 但次数用完：转人工，不要再提交
 * - declined / review：付款被拒或待核对，转人工，不能重兑
 */
export function mwOutcome(status: string, order?: OrderLike | null, credit = false): MwOutcome {
  const s = String(status || '').toLowerCase()
  const left = typeof order?.attempts_left === 'number' ? order.attempts_left : undefined
  switch (s) {
    case 'completed':
      if (credit) return { kind: 'success', text: '点数购买完成，请到 Codex 查看点数余额。' }
      return { kind: 'success', text: '开通完成，请到 ChatGPT 账号确认套餐。' }
    case 'queued':
    case 'running':
      return { kind: 'running', text: '正在处理，请不要重复提交，页面会自动刷新结果。' }
    case 'failed_precharge': {
      const canRetry = order?.can_retry !== false && (left === undefined || left > 0)
      if (canRetry) {
        return {
          kind: 'retry', attemptsLeft: left,
          text: `这次兑换没有成功，也没有扣款。可以重新兑换${left !== undefined ? `（还剩 ${left} 次机会）` : ''}。`,
        }
      }
      return { kind: 'manual', text: '重新兑换的次数已用完，已转人工处理。请联系发码方，不要再重复提交。' }
    }
    case 'declined':
      return { kind: 'manual', text: '支付被银行拒绝，已转人工处理。请联系发码方，不要再重复提交。' }
    case 'review':
      return { kind: 'manual', text: '结果需要人工核对，请联系发码方，不要再重复提交。' }
    case 'failed':
    case 'cancelled':
      return { kind: 'failed', text: '兑换没有成功。请联系发码方，不要再重复提交。' }
    default:
      return { kind: 'running', text: '' }
  }
}

/** 兑换请求被拒（没有建单）时应停在确认页显示原因，而不是跳去结果页轮询一个不存在的单。 */
export function redeemRejectedBeforeOrder(status: number, data: any): boolean {
  if (status === 409) return false // 已有在途或已完成的单，去结果页看进度
  if (status >= 200 && status < 300) return false
  if (data && (data.order || data.status)) return false
  return status === 400 || status === 403 || status === 404 || status === 429 || status === 503
}

const STAGE_TEXT: Record<string, string> = {
  queued: '排队', credential_check: '校验凭证', checkout: '生成支付页', card_funding: '给卡注资', payment: '支付', subscription: '确认订阅',
}

export function mwStageText(stage: string): string {
  return STAGE_TEXT[String(stage || '').toLowerCase()] ?? (stage || '')
}
