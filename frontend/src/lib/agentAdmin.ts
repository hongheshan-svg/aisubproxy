// 管理员后台「代理商」相关页面的纯逻辑：价格改动、调账结果判断、错误文案。
import { AGENT_ERROR_TEXT } from './agentApi'
import { fmtUsd } from './agentMoney'
import { usdToCents } from './provider'
import { ADJUST_RETRY_PREFIX, clearStoredPrefix, createLeaveGuard, localStore, needsLeaveGuard, RetryLock, type LeaveGuard, type RetryStore } from './retryLock'

export type PriceCell = { tier_id: number; product_id: number; price_cents: number }
export type OverrideCell = { product_id: number; price_cents: number }

const FALLBACK = '操作失败，请稍后再试'

/** 空串或 0 表示删除（价格 0）；其余按美元解析。 */
function parsePrice(text: string): { ok: true; cents: number } | { ok: false; error: string } {
  const s = String(text ?? '').trim()
  if (s === '' || /^0+(\.0+)?$/.test(s)) return { ok: true, cents: 0 }
  const r = usdToCents(s)
  return r.ok ? { ok: true, cents: r.cents } : { ok: false, error: r.error }
}

/** 价格矩阵：edited 的键为 "<tier_id>:<product_id>"，只返回和 original 不同的格子；价格 0 = 这个等级不卖。 */
export function matrixChanges(original: PriceCell[], edited: Record<string, string>): { cells: PriceCell[]; errors: string[] } {
  const cur = new Map<string, number>()
  for (const c of original) cur.set(`${c.tier_id}:${c.product_id}`, c.price_cents)
  const cells: PriceCell[] = []
  const errors: string[] = []
  for (const [key, text] of Object.entries(edited)) {
    const m = /^(\d+):(\d+)$/.exec(key)
    if (!m) { errors.push(`格子 ${key} 不正确`); continue }
    const p = parsePrice(text)
    if (!p.ok) { errors.push(`格子 ${key}：${p.error}`); continue }
    if (p.cents !== (cur.get(key) ?? 0)) cells.push({ tier_id: Number(m[1]), product_id: Number(m[2]), price_cents: p.cents })
  }
  return { cells, errors }
}

/** 单个代理商的覆盖价：键为 product_id；价格 0 = 取消覆盖。 */
export function overrideChanges(original: { product_id: number; price_cents: number }[], edited: Record<string, string>): { cells: OverrideCell[]; errors: string[] } {
  const cur = new Map<string, number>()
  for (const c of original) cur.set(String(c.product_id), c.price_cents)
  const cells: OverrideCell[] = []
  const errors: string[] = []
  for (const [key, text] of Object.entries(edited)) {
    if (!/^\d+$/.test(key)) { errors.push(`商品 ${key} 不正确`); continue }
    const p = parsePrice(text)
    if (!p.ok) { errors.push(`商品 ${key}：${p.error}`); continue }
    if (p.cents !== (cur.get(key) ?? 0)) cells.push({ product_id: Number(key), price_cents: p.cents })
  }
  return { cells, errors }
}

/** 商品编码规则，和后端一致。 */
export function validProductCode(code: string): boolean {
  return /^[a-z0-9-]{2,48}$/.test(code)
}

/** 管理员侧的编号文案，覆盖或补充代理商侧的。 */
export const ADMIN_ERROR_TEXT: Record<string, string> = {
  ...AGENT_ERROR_TEXT,
  insufficient_balance: '余额不足，不能扣成负数',
  username_taken: '登录名已被使用',
  idempotency_conflict: '同一个幂等键已用于别的代理商或别的金额，请关闭对话框后重新填写',
  tier_in_use: '还有代理商在用这个等级，不能删除',
  name_taken: '名称已存在',
  product_code_taken: '商品编码已存在',
}

/** 管理员页面的错误显示：优先 message，其次编号表，再其次旧格式的中文文本，最后兜底。 */
export function adminErrorText(data: any, status?: number): string {
  if (data && typeof data.message === 'string' && data.message) return data.message
  const code = data && typeof data.error === 'string' ? data.error : ''
  if (ADMIN_ERROR_TEXT[code]) return ADMIN_ERROR_TEXT[code]
  if (code && !/^[a-z0-9_]+$/.test(code)) return code
  if (status === 429) return ADMIN_ERROR_TEXT.rate_limited
  if (code) return `操作失败（${code}）`
  return FALLBACK
}

export type AdjustOutcome = {
  kind: 'ok' | 'replayed' | 'failed' | 'unknown'
  /** 是否可以清掉幂等键（成功或明确失败）；false 表示必须原样重试 */
  clearKey: boolean
  message: string
}

export const REPLAYED_TEXT = '这是之前同一次调账的结果，没有重复入账'
export const UNKNOWN_TEXT = '结果未知，请原样重试（金额和原因已锁定，沿用同一个幂等键，不会重复入账）'

/** 调账请求的结果归类：网络异常（0）和 5xx 不知道有没有入账，保留键；其余都是明确结果。 */
export function classifyAdjust(status: number, data: any): AdjustOutcome {
  if (status >= 200 && status < 300) {
    if (data && data.replayed === true) return { kind: 'replayed', clearKey: true, message: REPLAYED_TEXT }
    return { kind: 'ok', clearKey: true, message: '调账成功' }
  }
  if (status === 0 || status >= 500) return { kind: 'unknown', clearKey: false, message: UNKNOWN_TEXT }
  return { kind: 'failed', clearKey: true, message: adminErrorText(data, status) }
}

export const USERNAME_TAKEN_HINT = '如果刚才请求中断，账号可能已经建好，请在列表里确认，需要密码时用「重置密码」。'

// ---------- 调账：结果未知的那笔按代理商 ID 分开落 localStorage（见 lib/retryLock.ts） ----------
export type PendingAdjust = { agent_id: number; amount_cents: number; note: string }

/** 存储里读出的未决调账：字段不对就不认。 */
export function parsePendingAdjust(v: unknown): PendingAdjust | null {
  if (!v || typeof v !== 'object') return null
  const o = v as Record<string, unknown>
  const id = o.agent_id, cents = o.amount_cents
  if (typeof id !== 'number' || !Number.isInteger(id) || id <= 0) return null
  if (typeof cents !== 'number' || !Number.isInteger(cents) || cents === 0) return null
  if (typeof o.note !== 'string' || !o.note || o.note.length > 200) return null
  return { agent_id: id, amount_cents: cents, note: o.note }
}

function signedUsdText(cents: number): string {
  const abs = Math.abs(cents)
  return `${cents < 0 ? '-' : ''}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
}

/** 打开调账对话框时恢复什么：有未决调账就恢复金额、原因并锁定，且不能丢键。 */
export function adjustOpenState(pending: PendingAdjust | null | undefined): { locked: boolean; amountText: string; note: string } {
  if (!pending) return { locked: false, amountText: '', note: '' }
  return { locked: true, amountText: signedUsdText(pending.amount_cents), note: pending.note }
}

export type AdjustPreview = {
  direction: 'credit' | 'debit'
  text: string
  afterCents: number
  afterText: string
  willBeNegative: boolean
  warning: string
}

/** 调账确认文案：对象、方向、金额、调账后余额；扣成负数时给出警告（后端会拒）。 */
export function adjustPreview(username: string, balanceCents: number, amountCents: number, retry = false): AdjustPreview {
  const credit = amountCents > 0
  const after = balanceCents + amountCents
  return {
    direction: credit ? 'credit' : 'debit',
    text: credit ? `将给 ${username} 充值 ${fmtUsd(amountCents)}` : `将从 ${username} 扣除 ${fmtUsd(-amountCents)}`,
    afterCents: after,
    afterText: `调账后余额 ${fmtUsd(after)}${retry ? '（上一次可能已入账，请先核对流水再确认）' : ''}`,
    willBeNegative: after < 0,
    warning: after < 0 ? '余额不足，不能扣成负数，提交会被拒绝' : '',
  }
}

/** 离开页面前的提示：有未决调账时返回文案，否则空串。 */
export const ADJUST_LEAVE_TEXT = '有一笔调账结果未知，离开后请先查流水再决定是否重调'
export const ADJUST_BUSY_TEXT = '上一笔调账还在处理，请稍候'

/** 提交前重读的结论：go 照常发；locked 有一笔结果未知的调账（可能是别的标签页的），改显示它；settled 对话框锁定的那笔已在别处处理完，不许再发。 */
export type AdjustRecheck = { kind: 'go' } | { kind: 'locked'; pending: PendingAdjust } | { kind: 'settled' }

function samePendingAdjust(a: PendingAdjust, b: PendingAdjust): boolean {
  return a.agent_id === b.agent_id && a.amount_cents === b.amount_cents && a.note === b.note
}

/**
 * 调账的未决和在途状态。结果未知的那笔（幂等键 + 金额 + 原因）按代理商 ID 分开落 localStorage：
 * 发出前就落盘，刷新、关标签页重开、新标签页都能找回同一个幂等键；多个标签页共用，每次提交前重读；管理员退出登录时清掉。
 * 存储不可用、记录只在内存里时，有在途或未决就注册 beforeunload；落进存储时不注册。
 * 调账不按管理员区分归属：换了管理员登录也能看到并处理上一位留下的未决（原样重试沿用同一个键，服务端最多入账一次）。
 */
export class AdjustRetry {
  /** 内存里的镜像，读的时候以存储为准（pendingFor 会同步它）。 */
  readonly pendings = new Map<number, PendingAdjust>()
  /** 在途：同一个代理商同时只允许一笔调账请求（只在本页面内）。 */
  readonly inflight = new Set<number>()
  private locks = new Map<number, RetryLock<PendingAdjust>>()

  constructor(private store: RetryStore | undefined = undefined, private guard: LeaveGuard | undefined = undefined, private now: () => number = Date.now) {}

  private syncGuard() {
    const ids = new Set([...this.pendings.keys(), ...this.inflight])
    this.guard?.set([...ids].some((id) => needsLeaveGuard(true, this.keeperFor(id).persistent)))
  }

  /** 每个代理商一把锁，同一笔内容沿用同一个幂等键。 */
  keeperFor(agentId: number): RetryLock<PendingAdjust> {
    let k = this.locks.get(agentId)
    if (!k) {
      k = new RetryLock(ADJUST_RETRY_PREFIX + agentId, this.store, parsePendingAdjust, this.now)
      this.locks.set(agentId, k)
    }
    return k
  }

  /** 重读存储：这个代理商结果未知的那笔调账（别的标签页、刷新前留下的也算），没有为 null。 */
  pendingFor(agentId: number): PendingAdjust | null {
    const cur = this.keeperFor(agentId).current()
    const p = cur && cur.payload.agent_id === agentId ? cur.payload : null
    if (p) this.pendings.set(agentId, p)
    else this.pendings.delete(agentId)
    this.syncGuard()
    return p
  }

  /** 提交前调用：payload 是对话框里要发的内容，shownLocked 表示对话框正处于「结果未知、原样重试」状态。 */
  recheck(agentId: number, payload: PendingAdjust, shownLocked: boolean): AdjustRecheck {
    const cur = this.pendingFor(agentId)
    if (cur) return shownLocked && samePendingAdjust(cur, payload) ? { kind: 'go' } : { kind: 'locked', pending: cur }
    return shownLocked ? { kind: 'settled' } : { kind: 'go' }
  }

  /** 要发请求前调用：已有在途返回 false（不许发）；否则标记在途返回 true。 */
  begin(agentId: number): boolean {
    if (this.inflight.has(agentId)) return false
    this.inflight.add(agentId)
    this.syncGuard()
    return true
  }

  /** 发出前：锁定落盘，返回幂等键。内容和已锁定的不同抛 IdempotencyLockedError。 */
  lock(agentId: number, payload: PendingAdjust): string {
    const key = this.keeperFor(agentId).lock(payload)
    this.pendings.set(agentId, payload)
    this.syncGuard()
    return key
  }

  /** 请求返回后调用（不管页面还在不在）：清在途；成功或明确失败清键和未决，结果未知记成未决、保留键。key 给了就只清这一笔。 */
  finish(agentId: number, payload: PendingAdjust, outcome: AdjustOutcome, key?: string): void {
    this.inflight.delete(agentId)
    if (outcome.clearKey) {
      this.keeperFor(agentId).done(key)
      this.pendings.delete(agentId)
    } else {
      try {
        this.keeperFor(agentId).lock(payload)
      } catch {
        /* 别的标签页已放弃这笔并锁定了另一笔：以那边为准 */
      }
      this.pendings.set(agentId, payload)
    }
    this.syncGuard()
  }

  /**
   * 发一笔调账，结算只做一次（Task 11）：发出前锁定落盘 → 等请求返回（请求本身抛异常算结果未知）→ finish。
   * onResult 是返回后的界面更新：它出异常照样往外抛，但不会再补记一条未决、也不会换键。
   * 调用前先 begin(agentId)。
   */
  async run(
    agentId: number,
    payload: PendingAdjust,
    send: (key: string) => Promise<{ status: number; data: any }>,
    onResult?: (o: AdjustOutcome) => void,
  ): Promise<AdjustOutcome> {
    let key: string
    try {
      key = this.lock(agentId, payload)
    } catch (e) {
      this.inflight.delete(agentId)
      this.syncGuard()
      throw e
    }
    let o: AdjustOutcome
    try {
      const r = await send(key)
      o = classifyAdjust(r.status, r.data)
    } catch {
      o = classifyAdjust(0, {}) // 请求本身出异常：不知道有没有入账
    }
    this.finish(agentId, payload, o, key)
    onResult?.(o)
    return o
  }

  /** 管理员确认放弃：清掉这个代理商的未决，下一笔用新键。 */
  abandon(agentId: number): void {
    this.keeperFor(agentId).done()
    this.pendings.delete(agentId)
    this.syncGuard()
  }

  /** 离开页面前的提示：有未决调账时返回文案，否则空串。 */
  leaveWarning(agentId: number): string {
    return this.pendingFor(agentId) ? ADJUST_LEAVE_TEXT : ''
  }

  /** 重新打开调账对话框时：有在途请求返回提示文案（只显示、不许提交），否则空串。 */
  reopenBlock(agentId: number): string {
    return this.inflight.has(agentId) ? ADJUST_BUSY_TEXT : ''
  }

  /** 管理员退出登录：清掉所有代理商的调账记录（包括本页面没打开过的）。 */
  clear(): void {
    for (const k of this.locks.values()) k.clear()
    clearStoredPrefix(ADJUST_RETRY_PREFIX, this.store)
    this.pendings.clear()
    this.syncGuard()
  }
}

/** 模块级单例：管理员后台各页面、多个标签页共用（经 localStorage）。 */
export const adjustRetry = new AdjustRetry(localStore(), createLeaveGuard())
export const adjustPendings = adjustRetry.pendings
export const adjustInflight = adjustRetry.inflight
export const adjustKeeperFor = (agentId: number) => adjustRetry.keeperFor(agentId)
export const adjustPendingFor = (agentId: number) => adjustRetry.pendingFor(agentId)
export const adjustRecheck = (agentId: number, payload: PendingAdjust, shownLocked: boolean) => adjustRetry.recheck(agentId, payload, shownLocked)
export const adjustBegin = (agentId: number) => adjustRetry.begin(agentId)
export const adjustFinish = (agentId: number, payload: PendingAdjust, outcome: AdjustOutcome) => adjustRetry.finish(agentId, payload, outcome)
export const adjustRun = (agentId: number, payload: PendingAdjust, send: (key: string) => Promise<{ status: number; data: any }>, onResult?: (o: AdjustOutcome) => void) =>
  adjustRetry.run(agentId, payload, send, onResult)
export const adjustAbandon = (agentId: number) => adjustRetry.abandon(agentId)
export const adjustLeaveWarning = (agentId: number) => adjustRetry.leaveWarning(agentId)
export const adjustReopenBlock = (agentId: number) => adjustRetry.reopenBlock(agentId)
/** 管理员退出登录时调用。 */
export const clearAdjustRetries = () => adjustRetry.clear()

export type TierRow = { id: number; name: string; note: string; agent_count: number }

/** 从 /agent-tiers 和 /agent-prices 两份响应组装：等级表用前者（有备注和代理商数），矩阵列只用后者的 {id, name}。 */
export function assemblePricing(tiersResp: any, pricesResp: any): {
  tiers: TierRow[]; matrixTiers: { id: number; name: string }[]; products: any[]; cells: PriceCell[]
} {
  const tiers: TierRow[] = (Array.isArray(tiersResp?.items) ? tiersResp.items : []).map((t: any) => ({
    id: t.id, name: t.name, note: typeof t.note === 'string' ? t.note : '', agent_count: Number(t.agent_count) || 0,
  }))
  const matrixTiers = (Array.isArray(pricesResp?.tiers) ? pricesResp.tiers : []).map((t: any) => ({ id: t.id, name: t.name }))
  return {
    tiers, matrixTiers,
    products: Array.isArray(pricesResp?.products) ? pricesResp.products : [],
    cells: Array.isArray(pricesResp?.cells) ? pricesResp.cells : [],
  }
}
