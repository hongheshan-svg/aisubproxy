// 卡池 / 钱包后台页面的公共函数。
import { authFetch } from './api'

export interface Resp<T = any> { ok: boolean; status: number; data: T }

/** 调用 /api/v1/admin/cardpool 下的接口；FormData 原样传，其余 JSON.stringify。 */
export async function poolApi<T = any>(path: string, method = 'GET', body?: unknown): Promise<Resp<T>> {
  try {
    const r = await authFetch('/api/v1/admin/cardpool' + path, {
      method,
      body: body === undefined ? undefined : body instanceof FormData ? body : JSON.stringify(body),
    })
    const data = await r.json().catch(() => ({}))
    return { ok: r.ok, status: r.status, data }
  } catch {
    return { ok: false, status: 0, data: { error: '网络错误' } as any }
  }
}

export function errText(r: Resp, fallback: string): string {
  return (r.data && r.data.error) || fallback
}

/** 502 且 outcome=unknown：平台是否已执行不确定，需稍后刷新确认。 */
export function isUnknownOutcome(r: Resp): boolean {
  return r.status === 502 && r.data?.outcome === 'unknown'
}

/** 结果不确定：网络异常（status 0）或平台明确返回 outcome=unknown。资金单此时绝不能让用户重试。 */
export function isUncertain(r: Resp): boolean {
  return r.status === 0 || isUnknownOutcome(r)
}

/** 账户流水收支方向（平台：DEBIT=出账，CREDIT=入账）。 */
export const FLAG_LABEL: Record<string, string> = {
  DEBIT: '出账', CREDIT: '入账', MAIN_TO_SUB_ACCOUNT: '主钱包转子账户', SUB_ACCOUNT_TO_MAIN: '子账户转主钱包',
}

export const TXN_LABEL: Record<string, string> = {
  CONSUME: '消费', CHARGE: '充值', REFUND: '退款', EXCHANGE: '汇兑', REVERSE: '撤销',
  FEE: '手续费', LIMIT_ADJUST: '限额调整', TRANSFER: '转账',
}

export function money(cents?: number | null): string {
  return ((cents || 0) / 100).toFixed(2)
}

export function fmtTs(ts?: number | null): string {
  if (!ts) return '—'
  return new Date(ts * 1000).toLocaleString()
}

export const ORDER_KINDS: { value: string; label: string }[] = [
  { value: 'apply', label: '开卡' },
  { value: 'recharge', label: '转入' },
  { value: 'withdraw', label: '转出' },
  { value: 'status', label: '改状态' },
  { value: 'conversion', label: '兑换' },
  { value: 'wallet_transfer', label: '钱包转账' },
]

export function kindLabel(k: string): string {
  return ORDER_KINDS.find((x) => x.value === k)?.label || k
}

/** 金额字符串（最多 2 位小数、整数部分最多 12 位、必须大于 0）转分；不合法返回 null。 */
export function parseAmountCents(s: string): number | null {
  const t = String(s ?? '').trim()
  const m = /^(\d{1,12})(?:\.(\d{1,2}))?$/.exec(t)
  if (!m) return null
  const cents = Number(m[1]) * 100 + Number((m[2] || '').padEnd(2, '0'))
  return cents > 0 ? cents : null
}

export function hasEmoji(s: string): boolean {
  return /\p{Extended_Pictographic}/u.test(s)
}

// ---------- 持卡人资料表单（规则与后端 validateProfile 一致） ----------

export interface AddressValue { country: string; state: string; city: string; address: string; post_code: string }

export interface CardholderFormValue {
  first_name: string
  last_name: string
  chinese_first_name: string
  chinese_last_name: string
  date_of_birth: string
  zone_number: string
  mobile: string
  email: string
  country_code: string
  id_type: string
  id_no: string
  id_period_begin: string
  id_period_end: string
  id_photo_file_ids: string[]
  residential_address: AddressValue
  billing_address: AddressValue
  billing_same: boolean
  ssn: string
}

function emptyAddress(): AddressValue {
  return { country: '', state: '', city: '', address: '', post_code: '' }
}

export function emptyCardholderForm(): CardholderFormValue {
  return {
    first_name: '', last_name: '', chinese_first_name: '', chinese_last_name: '',
    date_of_birth: '', zone_number: '', mobile: '', email: '', country_code: '',
    id_type: 'PASSPORT', id_no: '', id_period_begin: '', id_period_end: '',
    id_photo_file_ids: [], residential_address: emptyAddress(), billing_address: emptyAddress(),
    billing_same: true, ssn: '',
  }
}

function runeLen(s: string, lo: number, hi: number): boolean {
  const n = Array.from(s).length
  return n >= lo && n <= hi
}

function asciiName(s: string): boolean {
  return s.length >= 1 && s.length <= 32 && /^[\x20-\x7e]+$/.test(s)
}

function validDate(s: string): boolean {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s)
  if (!m) return false
  const y = Number(m[1]), mo = Number(m[2]), d = Number(m[3])
  if (y < 1 || mo < 1 || mo > 12 || d < 1) return false
  return d <= new Date(Date.UTC(y, mo, 0)).getUTCDate()
}

function validEmail(s: string): boolean {
  return runeLen(s, 1, 100) && /^[^\s<>@,;:"()[\]\\]+@[^\s<>@,;:"()[\]\\]+\.[^\s<>@,;:"()[\]\\]+$/.test(s)
}

const COUNTRY_RE = /^[A-Z]{2}$/
const SSN_RE = /^\d{3}-\d{2}-\d{4}$/

function cleanZone(z: string): string {
  return z.trim().replace(/^\+/, '')
}

function addressErrors(label: string, a: AddressValue): string[] {
  const t = (s: string) => (s || '').trim()
  const out: string[] = []
  if (!COUNTRY_RE.test(t(a.country).toUpperCase())) out.push(`${label}国家码须为两位字母`)
  if (!runeLen(t(a.state), 1, 64)) out.push(`${label}州/省须为 1–64 个字`)
  if (!runeLen(t(a.city), 1, 64)) out.push(`${label}城市须为 1–64 个字`)
  if (!runeLen(t(a.address), 1, 256)) out.push(`${label}详细地址须为 1–256 个字`)
  if (!runeLen(t(a.post_code), 1, 10)) out.push(`${label}邮编须为 1–10 个字`)
  return out
}

/** 返回中文错误列表（空数组即通过）。today 为管理员本地日期 YYYY-MM-DD。 */
export function validateCardholderForm(f: CardholderFormValue, today: string): string[] {
  const t = (s: string) => (s || '').trim()
  const errs: string[] = []
  const cc = t(f.country_code).toUpperCase()
  if (!asciiName(t(f.first_name))) errs.push('英文名须为 1–32 个可打印 ASCII 字符')
  if (!asciiName(t(f.last_name))) errs.push('英文姓须为 1–32 个可打印 ASCII 字符')
  if (!runeLen(t(f.chinese_first_name), 0, 32)) errs.push('中文名不能超过 32 个字')
  if (!runeLen(t(f.chinese_last_name), 0, 32)) errs.push('中文姓不能超过 32 个字')
  const dob = t(f.date_of_birth)
  if (!(validDate(dob) && dob < today)) errs.push('出生日期须为 YYYY-MM-DD 且早于今天')
  if (!/^\d{1,10}$/.test(cleanZone(f.zone_number))) errs.push('手机区号须为 1–10 位数字')
  if (!runeLen(t(f.mobile), 1, 50)) errs.push('手机号须为 1–50 个字')
  if (!validEmail(t(f.email))) errs.push('邮箱须为不超过 100 个字的邮箱地址（不含姓名）')
  if (!COUNTRY_RE.test(cc)) errs.push('国籍国家码须为两位字母')
  if (!['PASSPORT', 'IDCARD', 'OTHER_CERTIFICATE'].includes(t(f.id_type))) errs.push('证件类型不合法')
  if (!runeLen(t(f.id_no), 1, 64)) errs.push('证件号须为 1–64 个字')
  const b = t(f.id_period_begin), e = t(f.id_period_end)
  if (!(validDate(b) && b <= today)) errs.push('证件生效日须为 YYYY-MM-DD 且不晚于今天')
  if (!(validDate(e) && e > today)) errs.push('证件到期日须为 YYYY-MM-DD 且晚于今天（长期有效填 9999-12-31）')
  if (validDate(b) && validDate(e) && b > e) errs.push('证件生效日不能晚于到期日')
  const ids = f.id_photo_file_ids || []
  if (ids.length < 1 || ids.length > 2 || ids.some((x) => !t(x))) errs.push('证件照须上传 1–2 张')
  errs.push(...addressErrors('居住地址', f.residential_address))
  if (!f.billing_same) errs.push(...addressErrors('账单地址', f.billing_address))
  if (cc === 'US' && !SSN_RE.test(t(f.ssn))) errs.push('国籍为 US 时 SSN 必填，格式为 ###-##-####')
  return errs
}

function trimAddr(a: AddressValue): AddressValue {
  const t = (s: string) => (s || '').trim()
  return { country: t(a.country).toUpperCase(), state: t(a.state), city: t(a.city), address: t(a.address), post_code: t(a.post_code) }
}

/** 转成提交给后端的 JSON：billing_same 时复制居住地址；非 US 不带 ssn；国家码大写。 */
export function toCardholderPayload(f: CardholderFormValue): Record<string, unknown> {
  const t = (s: string) => (s || '').trim()
  const cc = t(f.country_code).toUpperCase()
  const residential = trimAddr(f.residential_address)
  const p: Record<string, unknown> = {
    first_name: t(f.first_name),
    last_name: t(f.last_name),
    chinese_first_name: t(f.chinese_first_name),
    chinese_last_name: t(f.chinese_last_name),
    date_of_birth: t(f.date_of_birth),
    zone_number: cleanZone(f.zone_number),
    mobile: t(f.mobile),
    email: t(f.email),
    country_code: cc,
    id_type: t(f.id_type),
    id_no: t(f.id_no),
    id_period_begin: t(f.id_period_begin),
    id_period_end: t(f.id_period_end),
    id_photo_file_ids: (f.id_photo_file_ids || []).map(t),
    residential_address: residential,
    billing_address: f.billing_same ? { ...residential } : trimAddr(f.billing_address),
  }
  if (cc === 'US') p.ssn = t(f.ssn)
  return p
}

// ---------- 持卡人提交记录 ----------

export interface SubmissionLike { state: string; card_holder_id?: string }

/** 已提交但没拿到持卡人 ID（如进程中断）的记录结果未知，不能当成功展示。 */
export function submissionLabel(s: SubmissionLike): string {
  if (s.state === 'unknown') return '结果未知'
  if (s.state === 'abandoned') return '已放弃'
  if (s.state === 'submitted') return s.card_holder_id ? '已提交' : '提交中断，结果未知'
  return s.state
}

export function submissionTagType(s: SubmissionLike): 'success' | 'warning' | 'info' {
  if (s.state === 'submitted') return s.card_holder_id ? 'success' : 'warning'
  if (s.state === 'unknown') return 'warning'
  return 'info'
}

export function canAbandonSubmission(s: SubmissionLike): boolean {
  return s.state === 'unknown' || (s.state === 'submitted' && !s.card_holder_id)
}
