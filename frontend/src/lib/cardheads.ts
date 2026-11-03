// 各服务商的卡头（卡产品），对应 /api/v1/admin/providers/:name/card-heads。
// 3DS、Apple Pay 是管理员手动标注的，只用于显示，不参与选卡。

export type TriState = '' | 'yes' | 'no'

export interface CardHead {
  provider: string
  product_id: string
  source: 'api' | 'manual'
  name: string
  card_heads: string[]
  network: string
  country: string
  currency: string
  listed: boolean
  three_ds: TriState
  apple_pay: TriState
  note: string
  synced_at: number
  updated_at: number
  updated_by: string
}

export interface CardHeadList {
  provider: string
  auto: boolean
  last_sync_at: number
  last_sync_error: string
  items: CardHead[]
}

export const TRI_OPTIONS: { value: TriState; label: string }[] = [
  { value: '', label: '未标注' },
  { value: 'yes', label: '支持' },
  { value: 'no', label: '不支持' },
]

export function triLabel(v: TriState): string {
  return v === 'yes' ? '支持' : v === 'no' ? '不支持' : '未标注'
}

// 卡头用「/」连接；没有卡头时用产品名，再没有用产品 ID
export function cardHeadTitle(h: CardHead): string {
  if (h.card_heads?.length) return h.card_heads.join('/')
  return h.name || h.product_id
}

// 只有标注部分，如「3DS 支持 · Apple Pay 支持」，给已经显示了卡头的地方用；未标注的项不写
export function cardHeadMarks(h: CardHead): string {
  const parts: string[] = []
  if (h.three_ds) parts.push(`3DS ${triLabel(h.three_ds)}`)
  if (h.apple_pay) parts.push(`Apple Pay ${triLabel(h.apple_pay)}`)
  return parts.join(' · ') + (h.listed ? '' : '（接口已不再返回）')
}

// 下拉框和列表里的说明，如「537100 · 3DS 支持 · Apple Pay 支持」
export function cardHeadOptionLabel(h: CardHead): string {
  const marks = cardHeadMarks(h)
  if (!marks) return cardHeadTitle(h)
  return marks.startsWith('（') ? cardHeadTitle(h) + marks : `${cardHeadTitle(h)} · ${marks}`
}

export function cardHeadsByProduct(items: CardHead[]): Record<string, CardHead> {
  const out: Record<string, CardHead> = {}
  for (const h of items) out[h.product_id] = h
  return out
}
