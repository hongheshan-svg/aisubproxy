// 首页售后查询的请求：只取单条结果，错误统一换成可直接显示的文字。
import type { LookupResult } from './aftersale'

export interface LookupMessages {
  notFound: string
  network: string
}

export async function lookupAftersale(
  rawCode: string,
  msgs: LookupMessages,
  fetchFn: typeof fetch = fetch,
): Promise<{ result?: LookupResult; error: string }> {
  const code = rawCode.trim()
  if (!code) return { error: '' }
  try {
    const r = await fetchFn(`/api/v1/lookup/cdk?code=${encodeURIComponent(code)}`)
    const data = await r.json().catch(() => ({}))
    if (r.ok) return { result: data as LookupResult, error: '' }
    // 后端 404 的 message 是中文，英文界面也会照搬，所以找不到一律用语言包的文案
    if (r.status === 404) return { error: msgs.notFound }
    return { error: data.error || data.message || msgs.network }
  } catch {
    return { error: msgs.network }
  }
}
