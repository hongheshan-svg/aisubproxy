/** 后台「查看卡号」只显示前 6 后 4 的打码卡号（审计 C-3）：后端不再返回完整卡号和 CVV2，
 *  这里再兜一层——只取 card_no_masked，其余字段（即使后端误返回）一律丢弃，也不在界面上渲染。 */
export interface SensitiveView {
  cardNoMasked: string
}

export function toSensitiveView(data: unknown): SensitiveView {
  const raw = (data as { card_no_masked?: unknown } | null | undefined)?.card_no_masked
  const s = typeof raw === 'string' ? raw.trim() : ''
  // 兜底：万一给的是 13 位以上的纯数字（完整卡号），自己再打一次码
  const digits = s.replace(/\s/g, '')
  if (/^\d{13,19}$/.test(digits)) {
    return { cardNoMasked: digits.slice(0, 6) + '*'.repeat(digits.length - 10) + digits.slice(-4) }
  }
  return { cardNoMasked: s }
}
