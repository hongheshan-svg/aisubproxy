/** 取账单查询返回里的说明文字（凭卡密查询不提供账单链接时，后端用 note 说明原因）。只认非空字符串。 */
export function pickBillingNote(d: unknown): string {
  const n = d && typeof d === 'object' ? (d as { note?: unknown }).note : undefined
  return typeof n === 'string' ? n.trim() : ''
}
