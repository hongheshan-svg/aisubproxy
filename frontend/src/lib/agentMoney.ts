// 代理商金额：全程整数运算（美分），不经过浮点数。

/** 美分格式化成 $1,234.56；负数写成 -$5.00。 */
export function fmtUsd(cents: number): string {
  const n = Math.trunc(Number(cents) || 0)
  const neg = n < 0
  const abs = Math.abs(n)
  const whole = Math.floor(abs / 100)
  const frac = abs % 100
  const w = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return `${neg ? '-' : ''}$${w}.${String(frac).padStart(2, '0')}`
}

export type SignedUsdResult = { ok: true; cents: number } | { ok: false; error: string }

/** 把输入的美元金额转成美分，允许负号（调账用），不能为 0，最多两位小数。 */
export function parseSignedUsd(input: string): SignedUsdResult {
  const s = String(input ?? '').trim()
  if (s === '') return { ok: false, error: '请填写金额' }
  if (!/^-?\d+(\.\d+)?$/.test(s)) return { ok: false, error: '金额格式不对' }
  const neg = s.startsWith('-')
  const [whole, frac = ''] = (neg ? s.slice(1) : s).split('.')
  if (frac.length > 2) return { ok: false, error: '金额最多两位小数' }
  const abs = Number(whole) * 100 + Number(frac.padEnd(2, '0'))
  if (!Number.isSafeInteger(abs)) return { ok: false, error: '金额过大' }
  if (abs === 0) return { ok: false, error: '金额不能为 0' }
  return { ok: true, cents: neg ? -abs : abs }
}
