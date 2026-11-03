// Claude 卡密：档位识别 + Organization ID 凭证。
//
// 卡台预览回裸档位（pro / max_5x / max_20x），新版卡台另回 product=claude。本站统一补成 claude_ 前缀再判，
// 免得裸 pro 落进 GPT 的「Pro 20x」。没有 product 时只认 Claude 独有的 max_5x / max_20x：裸 pro 旧 GPT 码也用过。
//
// 兑换时填目标组织的 Organization ID（UUID），两次一致，并由持码人确认目标账号目前是免费账号。
// 卡台公开兑换接口没有写 Claude 的凭证字段，和 Grok / X 一样放进 credential.session（用户已确认，2026-11-03）。
// 预检给了 preflight_token 就带票兑换；没给（与 Grok 一样）就兑换时直接带凭证。

const CLAUDE_PLAN = /^claude_(?:pro|max_5x|max_20x)$/
const CLAUDE_ONLY_BARE = /^(?:max_5x|max_20x)$/
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

const LABELS: Record<string, string> = {
  claude_pro: 'Claude Pro',
  claude_max_5x: 'Claude Max 5x',
  claude_max_20x: 'Claude Max 20x',
}

export function isClaudePlan(value: string): boolean {
  return CLAUDE_PLAN.test(String(value || '').trim().toLowerCase())
}

/** 预览给的档位与产品 → 带 claude_ 前缀的档位；不是 Claude 码返回空串。 */
export function claudeTargetPlan(plan: string, product: string): string {
  const p = String(plan || '').trim().toLowerCase()
  const prod = String(product || '').trim().toLowerCase()
  const full = p.startsWith('claude_') ? p : (prod === 'claude' || CLAUDE_ONLY_BARE.test(p)) ? 'claude_' + p : ''
  return isClaudePlan(full) ? full : ''
}

/** 预览响应是不是 Claude 码。 */
export function isClaudePreview(body: any): boolean {
  if (!body || typeof body !== 'object') return false
  return !!claudeTargetPlan(body.plan || body.plan_type || '', body.product || '')
}

export function claudePlanLabel(value: string): string {
  return LABELS[String(value || '').trim().toLowerCase()] || String(value || '')
}

/** 去掉首尾空白并转小写；不是完整 UUID 返回空串。 */
export function normalizeOrgId(raw: string): string {
  const s = String(raw || '').trim().toLowerCase()
  return UUID.test(s) ? s : ''
}

/** 填写检查：'' 表示通过；format 不是完整 UUID，mismatch 两次不一致，free 没确认免费账号。 */
export function claudeOrgCheck(first: string, second: string, freeConfirmed: boolean): '' | 'format' | 'mismatch' | 'free' {
  const a = normalizeOrgId(first)
  if (!a) return 'format'
  if (a !== String(second || '').trim().toLowerCase()) return 'mismatch'
  if (!freeConfirmed) return 'free'
  return ''
}

export function claudeCredential(orgId: string): { mode: 'session'; session: string } {
  return { mode: 'session', session: normalizeOrgId(orgId) }
}

export function claudeRedeemBody(redemptionToken: string, preflightToken: string, orgId: string, clientRequestId: string) {
  if (preflightToken) return { redemption_token: redemptionToken, preflight_token: preflightToken, client_request_id: clientRequestId }
  return { redemption_token: redemptionToken, credential: claudeCredential(orgId), client_request_id: clientRequestId }
}
