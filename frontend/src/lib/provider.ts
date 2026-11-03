// 卡服务商（ZovoCard / MarsWallex）后台页面的公共函数：接口、就绪检查、本地码状态、金额换算。
import { authFetch } from './api'
import type { Resp } from './cardpool'

export type ProviderId = 'zovocard' | 'marswallex'

export const PROVIDER_LABEL: Record<string, string> = { zovocard: 'ZovoCard', marswallex: 'MarsWallex' }

export function providerLabel(p: string): string {
  return PROVIDER_LABEL[p] ?? (p || '—')
}

/** 调用 /api/v1/admin 下的接口（服务商、本地码、换码核对共用），可带额外请求头。 */
export async function adminApi<T = any>(path: string, method = 'GET', body?: unknown, headers?: Record<string, string>): Promise<Resp<T>> {
  try {
    const r = await authFetch('/api/v1/admin' + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const data = await r.json().catch(() => ({}))
    return { ok: r.ok, status: r.status, data }
  } catch {
    return { ok: false, status: 0, data: { error: '网络错误' } as any }
  }
}

// ---------- 金额：界面用美元，接口用分 ----------

export type CentsResult = { ok: true; cents: number } | { ok: false; error: string }

/** 美元文本或数字转分。拒绝空、非数字、0、负数、超过两位小数，避免把「0 = 不限」之类的误操作提交上去。 */
export function usdToCents(input: string | number | null | undefined): CentsResult {
  const s = String(input ?? '').trim()
  if (s === '') return { ok: false, error: '请填写金额' }
  if (!/^-?\d+(\.\d+)?$/.test(s)) return { ok: false, error: '金额格式不对' }
  if (s.startsWith('-')) return { ok: false, error: '金额必须大于 0' }
  const [whole, frac = ''] = s.split('.')
  if (frac.length > 2) return { ok: false, error: '金额最多两位小数' }
  const cents = Number(whole) * 100 + Number(frac.padEnd(2, '0'))
  if (!Number.isSafeInteger(cents)) return { ok: false, error: '金额过大' }
  if (cents <= 0) return { ok: false, error: '金额必须大于 0' }
  return { ok: true, cents }
}

/** 分转美元文本（两位小数，不带符号）。 */
export function centsToUsd(cents: number | null | undefined): string {
  const c = Number.isFinite(cents as number) ? Math.round(cents as number) : 0
  return (c / 100).toFixed(2)
}

// ---------- 就绪检查 ----------

export interface ReadinessCheck { key: string; ok: boolean; detail: string }
export interface ProviderReadiness { provider: string; ok: boolean; problems: string[]; checks: ReadinessCheck[] }

export interface CheckHint { title: string; link?: { to: string; label: string } }

/** 检查项键名 → 中文标题与去哪里处理。 */
export const CHECK_HINTS: Record<string, CheckHint> = {
  config: { title: 'MarsWallex 凭证', link: { to: '/ops/marswallex', label: '去配置 MarsWallex' } },
  master_key: { title: '服务器主密钥' },
  engine: { title: '付款引擎' },
  breaker: { title: '付款熔断', link: { to: '/ops/marswallex-pay', label: '去自动付款查看' } },
  card_pool: { title: '卡池', link: { to: '/ops/cardpool', label: '去卡池' } },
  first_real_payment: { title: '首笔真实付款', link: { to: '/ops/marswallex-pay', label: '去自动付款做真实付款' } },
  wallet_sync: { title: '主钱包余额同步', link: { to: '/ops/marswallex-wallet', label: '去 MarsWallex 钱包' } },
  plan: { title: '已启用的套餐', link: { to: '/ops/marswallex-pay', label: '去付款配置启用套餐' } },
  api_key: { title: '卡台 API Key', link: { to: '/ops/integration', label: '去配置卡台' } },
  balance: { title: '卡台余额', link: { to: '/ops/integration', label: '去卡台页' } },
}

export function checkTitle(key: string): string {
  return CHECK_HINTS[key]?.title ?? key
}

/** 首笔真实付款这一条是否未通过：需要在界面上单独、醒目地说明。 */
export function needsFirstRealPayment(r: ProviderReadiness | undefined | null): boolean {
  return !!r?.checks?.some((c) => c.key === 'first_real_payment' && !c.ok)
}

export interface Combo {
  plan: string; label: string; region: string; currency: string
  cap_minor: number; fund_cents: number; cards: number
  plan_note?: string; ok: boolean; problems: string[]
}

export const NO_SUB_NOTE = '仅限无订阅账号'

/** pro_20x 在 MarsWallex 是「新开通」，只能给没有订阅的账号。 */
export function planNote(plan: string, note?: string): string {
  if (plan === 'pro_20x') return NO_SUB_NOTE
  return note || ''
}

// ---------- 发码失败分类 ----------

export type IssueFailure =
  | { kind: 'already_issued'; message: string; firstId?: number; lastId?: number; count?: number }
  | { kind: 'not_ready'; message: string; problems: string[] }
  | { kind: 'unsupported'; message: string }
  | { kind: 'uncertain'; message: string }
  | { kind: 'other'; message: string }

/**
 * 本地码发码失败的分类。409 + already_issued 表示同一个幂等键已经发过，码拿不回来，必须走「禁用这批后重发」。
 * 网络异常（status 0）和 5xx 结果不明：重试会沿用同一个键，不会多发。
 */
export function classifyIssueFailure(status: number, d: any): IssueFailure {
  const err = String(d?.error || '')
  if (status === 409 && d?.already_issued) {
    return {
      kind: 'already_issued', firstId: d.first_id, lastId: d.last_id, count: d.count,
      message: '这批码已经发过，但完整码只在第一次响应里返回，本站无法再显示。',
    }
  }
  if (status === 400 && Array.isArray(d?.problems) && d.problems.length) {
    return { kind: 'not_ready', message: err || 'MarsWallex 未就绪，不能发码', problems: d.problems.map(String) }
  }
  if (status === 400 && d?.plan && d?.provider) {
    return { kind: 'unsupported', message: err || '该服务商不支持这个产品或档位' }
  }
  if (status === 0 || status >= 500) {
    return { kind: 'uncertain', message: (err || '请求失败') + '（结果可能不明：直接再点一次发码即可，同一次请求不会重复发码）' }
  }
  return { kind: 'other', message: err || `发码失败（${status}）` }
}

// ---------- 本地码状态 ----------

export interface LocalCdkRow {
  id: number; provider: string; code_prefix: string; product: string; plan: string; region: string
  status: string; status_reason: string; manual_status?: string; needs_review?: boolean
  funded_failures: number; attempts_left: number
  order_no?: string; order_state?: string; order_error_code?: string; note?: string
  created_at?: string | number; fund_cents?: number
}

const LOCAL_STATUS: Record<string, string> = {
  unused: '未使用', reserved: '处理中', consumed: '已兑换', failed: '已失败', disabled: '已禁用',
}
const LOCAL_REASON: Record<string, string> = {
  needs_review: '待人工核对', declined: '付款被拒', dirty_order: '单据异常', attempts_exhausted: '重兑次数用完',
  exchanged: '已换新码', manual: '人工处理',
}

export function localCdkStatusText(status: string, reason: string): string {
  const base = LOCAL_STATUS[status] ?? status
  const r = reason ? (LOCAL_REASON[reason] ?? reason) : ''
  return r ? `${base}（${r}）` : base
}

export function localCdkTagType(status: string, reason: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' {
  if (reason === 'needs_review' || reason === 'dirty_order' || reason === 'attempts_exhausted') return 'warning'
  switch (status) {
    case 'consumed': return 'success'
    case 'failed': return 'danger'
    case 'reserved': return 'primary'
    case 'disabled': return 'info'
    default: return 'info'
  }
}

/** 能否禁用：未使用，或处理中以外的非终态。已成功、已失败、已禁用不能再禁用。 */
export function canDisableLocal(status: string): boolean {
  return status === 'unused' || status === 'reserved'
}

/** 能否启用：只有已禁用，且没有被换发新码（exchanged）。 */
export function canEnableLocal(status: string, reason: string): boolean {
  return status === 'disabled' && reason !== 'exchanged'
}

// ---------- 代理换码核对 ----------

export const SWAP_STATE_LABEL: Record<string, string> = {
  pending: '处理中', unknown: '结果不明，待核对', disabling: '新码已发，旧码待禁用', done: '已完成',
}

export function swapStateLabel(s: string): string {
  return SWAP_STATE_LABEL[s] ?? s
}

export function canResolveSwap(state: string): boolean {
  return state === 'unknown'
}

/** disabling 记录给运营的说明。 */
export const DISABLING_HINT = '新码已发给用户，旧码还没禁用成功。系统每 10 分钟自动重试，持续失败会告警，请到卡台手动禁用旧码。'

/**
 * 代理换码失败后是否要锁住提交。发码结果不明（502 + outcome=unknown）、网络异常（可能已发出）、
 * 以及服务端说记录处于 pending / unknown（409）时，再点只会多发或撞上占位，必须锁住并提示勿重试。
 */
export function swapFailure(status: number, d: any): { locked: boolean; message: string } {
  const err = String(d?.error || '')
  if ((status === 502 && d?.outcome === 'unknown')) {
    return { locked: true, message: (err || '发码结果不明，已转人工核对') + '。请勿重试，也不要重复提交，联系管理员核对后再处理。' }
  }
  if (status === 0) {
    return { locked: true, message: '网络异常，换码结果不明（新码可能已经发出）。请勿重试，联系管理员核对后再处理。' }
  }
  if (status === 409 && (d?.state === 'pending' || d?.state === 'unknown')) {
    return { locked: true, message: (err || '这张码的换码还在处理中或待人工核对') + '。请勿重试，联系管理员。' }
  }
  return { locked: false, message: err || d?.msg || `失败 (${status})` }
}
