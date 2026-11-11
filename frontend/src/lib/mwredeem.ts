// 用户兑换页上供卡型服务商的码（本站付款、本地码）的状态展示。上游发码型的码不走这里，保持原样。

/** 响应里的 provider_kind 为 card_supply 才启用本站付款相关界面；没有 provider_kind（上游原样转发）一律按上游处理。 */
export function isCardSupply(kind: unknown): boolean {
  return kind === 'card_supply'
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

/** 兑换请求体：点数码且已勾选时带上 agree_credit_terms:true，其余码保持原样。 */
export function withCreditTerms<T extends Record<string, unknown>>(body: T, info: KindInfo | null | undefined, agreed: boolean): T & { agree_credit_terms?: true } {
  if (isCreditCode(info) && agreed) return { ...body, agree_credit_terms: true as const }
  return body
}

/** 409 里带这些 status 说明已有单（码在途、已使用、已转人工），去结果页按码查进度。 */
const EXISTING_ORDER_STATUS = new Set(['reserved', 'consumed', 'failed'])

/**
 * 兑换请求被拒、没有建单时应停在确认页显示原因，而不是跳去结果页轮询一个不存在的单。
 * 有 order、成功响应，或 409 带进度状态（已有单）时去结果页。
 */
export function redeemRejectedBeforeOrder(status: number, data: any): boolean {
  if (status >= 200 && status < 300) return false
  if (data?.order) return false
  if (status === 409 && EXISTING_ORDER_STATUS.has(String(data?.status || ''))) return false
  return status === 400 || status === 403 || status === 404 || status === 409 || status === 429 || status === 503
}
