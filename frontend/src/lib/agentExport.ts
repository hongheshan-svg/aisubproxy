// 代理商导出：.txt 一行一张码，.csv 带 BOM。完整的码只在内存里，不写日志。
import { fmtBeijing } from './mwwebhook'

export type ExportedCode = {
  id: number
  code: string
  unavailable?: boolean
  product_code?: string
  plan?: string
  region?: string
  status?: string
  created_at?: number
  order_id?: number
  note?: string
}

export const UNAVAILABLE_TEXT = '无法取回，请联系管理员'

export function codesTxt(items: ExportedCode[]): { text: string; skipped: number } {
  let skipped = 0
  const lines: string[] = []
  for (const it of items) {
    if (it.unavailable) { skipped++; continue }
    lines.push(it.code)
  }
  return { text: lines.length ? lines.join('\n') + '\n' : '', skipped }
}

// 文本列以公式字符开头时前面加单引号，防止 Excel 当公式执行（CSV 公式注入）
function csvText(v: unknown): string {
  const s = v === undefined || v === null ? '' : String(v)
  return /^[=+\-@\t\r]/.test(s) ? `'${s}` : s
}

function csvField(v: unknown): string {
  const s = v === undefined || v === null ? '' : String(v)
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function codesCsv(items: ExportedCode[]): string {
  const rows = ['code,product_code,plan,region,status,purchased_at,order_id,note']
  for (const it of items) {
    rows.push([
      it.unavailable ? UNAVAILABLE_TEXT : it.code,
      csvText(it.product_code),
      csvText(it.plan),
      csvText(it.region),
      csvText(it.status),
      it.created_at ? fmtBeijing(it.created_at) : '',
      it.order_id,
      csvText(it.note),
    ].map(csvField).join(','))
  }
  return '\uFEFF' + rows.join('\n') + '\n'
}

export function downloadText(name: string, text: string, mime: string): void {
  const blob = new Blob([text], { type: `${mime};charset=utf-8` })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
