type TFn = (key: string, params?: Record<string, unknown>) => string
type TeFn = (key: string) => boolean

/** data 里的数字字段（如 retry_after_minutes、limit），原样当作文案参数。 */
function numericParams(data: unknown): Record<string, unknown> {
  const out: Record<string, unknown> = {}
  if (data && typeof data === 'object' && !Array.isArray(data)) {
    for (const [k, v] of Object.entries(data as Record<string, unknown>)) {
      if (typeof v === 'number' && Number.isFinite(v)) out[k] = v
    }
  }
  return out
}

/** 接口报错转成当前语言的文字：有 error_code 且语言包里有 `${prefix}.${code}` 就用它，否则用后端原文 error，再否则用 fallbackKey。 */
export function apiErrorText(t: TFn, te: TeFn, prefix: string, data: unknown, fallbackKey: string): string {
  const obj = data && typeof data === 'object' && !Array.isArray(data) ? (data as Record<string, unknown>) : {}
  const params = numericParams(obj)
  const code = typeof obj.error_code === 'string' ? obj.error_code.trim() : ''
  if (code && te(`${prefix}.${code}`)) return t(`${prefix}.${code}`, params)
  const raw = typeof obj.error === 'string' ? obj.error.trim() : ''
  if (raw) return raw
  return t(fallbackKey, params)
}

/**
 * 代理换码失败后的提示和是否锁住提交。锁定规则与 lib/provider.ts 的 swapFailure 完全一致
 * （结果不明、网络异常、409 处理中 / 待人工核对一律锁住），只是文字走语言包。
 * 换码接口返回 conflict 时按 state 选文案（pending / unknown 各有一句，其余用「已换过」）。
 */
export function swapFailureT(status: number, d: any, t: TFn, te: TeFn): { locked: boolean; message: string } {
  const ns = 'agentSwap'
  const withStatus = { ...(d && typeof d === 'object' ? d : {}), status }
  if (status === 502 && d?.outcome === 'unknown') {
    return { locked: true, message: apiErrorText(t, te, `${ns}.errors`, withStatus, `${ns}.fallback.outcomeUnknown`) + t(`${ns}.lock.unknown`) }
  }
  if (status === 0) {
    return { locked: true, message: t(`${ns}.networkError`) }
  }
  if (status === 409 && (d?.state === 'pending' || d?.state === 'unknown')) {
    const data = d?.error_code === 'conflict' ? { ...withStatus, error_code: `conflict_${d.state}` } : withStatus
    return { locked: true, message: apiErrorText(t, te, `${ns}.errors`, data, `${ns}.fallback.busy`) + t(`${ns}.lock.busy`) }
  }
  const msg = typeof d?.msg === 'string' && !String(d?.error || '').trim() && !d?.error_code ? { ...withStatus, error: d.msg } : withStatus
  return { locked: false, message: apiErrorText(t, te, `${ns}.errors`, msg, `${ns}.errors.failed`) }
}
