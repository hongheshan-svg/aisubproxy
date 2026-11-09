// 用户端兑换流程：五屏（输卡密、粘贴登录信息、核对账号、开通中、结果）的状态和接口调用都在这里，
// 各屏组件只负责显示（规格 §7、§8）。接口路径、请求体和设备头沿用旧 RechargeView。
//
// 资金安全：
// - 同一张码同一时间只发一次兑换请求：redeem() 在第一个 await 之前同步把 submitState 置为 submitting。
// - 兑换请求发出后没收到回复（fetch 抛错），不知道服务器收没收到：submitState 置为 unknown，
//   之后只能 checkResult() 按卡密查结果，redeem()、reset()、preview() 都不动作。
//   发请求前就把「提交中」写进缓存：提交途中刷新或关页，回来也只能查结果。
// - 429 不自动重试；任何失败都不会自动再兑换一次。
// - 刷新后恢复到第 ③ 屏用缓存的预检票开通，票过期回第 ② 屏，不自动重提。
import { computed, getCurrentScope, onScopeDispose, ref, type ComputedRef, type Ref } from 'vue'
import { i18n } from '../i18n'
import type { LookupResult } from '../lib/aftersale'
import { lookupAftersale, type LookupMessages } from '../lib/aftersaleClient'
import { claudeRedeemBody, isClaudePlan } from '../lib/claude'
import { isGrokPlan } from '../lib/grok'
import { isCardSupply, isCreditCode, redeemRejectedBeforeOrder, withCreditTerms } from '../lib/mwredeem'
import { planSatisfied } from '../lib/plan'
import { accountFromPreflight, confirmState, targetPlanOf, type AccountSummary, type ConfirmState } from '../lib/redeemAccount'
import { isXPremiumPlan } from '../lib/x-premium'

export type Screen = 'code' | 'credential' | 'confirm' | 'progress' | 'result'
export type FlowErrorKind = 'network' | 'rate_limited' | 'ticket_expired' | 'rejected' | 'submit_unknown' | 'invalid'
export interface FlowError { kind: FlowErrorKind; message?: string }
export interface RedeemDeps {
  fetchFn?: typeof fetch
  storage?: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'> | null // 默认 sessionStorage，取不到为 null
  now?: () => number
  every?: (fn: () => void, ms: number) => () => void // 默认 setInterval，返回取消函数
  deviceId?: string
}
export interface FlowResult { status: string; stage: string; message: string; attemptsLeft?: number }
type SubmitState = 'idle' | 'submitting' | 'unknown'

export interface RedeemFlow {
  screen: Ref<Screen>
  code: Ref<string>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  previewInfo: Ref<any | null>
  account: Ref<AccountSummary | null>
  blockingCode: Ref<string>
  blockingReason: Ref<string>
  extra: Ref<{ accountCurrency?: string; codeCurrency?: string }>
  result: Ref<FlowResult>
  /** 预览报码不能用、开通到终态后取的售后查询结果 */
  lookup: Ref<LookupResult | null>
  error: Ref<FlowError | null>
  busy: Ref<boolean>
  submitState: Ref<SubmitState>
  progressStartedAt: Ref<number | null>
  /** 有可用的预检票且账号要先恢复订阅：为真显示「恢复订阅」，为假显示「重新核对」 */
  canRecover: Readonly<Ref<boolean>>
  /** 第 ③ 屏的状态，和 redeem() 的拦截用同一套判断（含当前套餐已满足） */
  confirmView: ComputedRef<ConfirmState>
  preview(code: string): Promise<void>
  preflight(credential: { mode: 'session'; session: string }): Promise<void>
  redeem(opts?: { agreeCreditTerms?: boolean; claudeOrgId?: string; grokSession?: string }): Promise<void>
  checkResult(): Promise<void>
  /** 调用前由页面弹二次确认（沿用 grace.confirm 文案），这里不弹框 */
  recoverGrace(): Promise<void>
  /** 回到第 ② 屏：清预检票、账号摘要、放弃码、出错，卡密和预览保留 */
  changeAccount(): void
  reset(opts?: { keepCode?: boolean }): void
  restore(queryCode: string): boolean
}

const PROGRESS_KEY = 'cdk_redeem_progress_v2'
const OLD_PROGRESS_KEY = 'cdk_redeem_progress_v1'
const MAX_AGE_MS = 7 * 24 * 3600 * 1000
const POLL_MS = 3000

const API = '/api/v1/public/cdk'
const TERMINAL = new Set(['completed', 'failed_precharge', 'failed', 'declined', 'cancelled'])
// 预览报这类原因时码已经兑换过，先按卡密查进度（旧 RechargeView.doPreview 的判断）
const USED_TEXT = /已兑换|已使用|used|redeemed|consumed|已消耗/i

/** 进度缓存的内容。不存：登录信息原文、邮箱密码、Claude Organization ID、Grok 登录态、须知勾选、最近付款和支付方式（规格 §7）。 */
interface SavedProgress {
  screen: Screen
  code: string
  redemptionToken: string
  preflightToken: string
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  previewInfo: any
  account: AccountSummary | null
  result: { status: string; stage: string }
  progressStartedAt: number | null
  submitUnknown: boolean
  savedAt: number
}

const t = (key: string) => i18n.global.t(key)
const MESSAGE_KEY: Partial<Record<FlowErrorKind, string>> = {
  network: 'redeemUi.errors.network',
  rate_limited: 'redeemUi.errors.rateLimited',
  ticket_expired: 'redeemUi.errors.previewExpired',
  submit_unknown: 'redeemUi.errors.submitUnknown',
  rejected: 'redeemUi.errors.rejected', // 服务器没给原因时的兜底
}
/** 出错：有固定文案的种类用语言包；rejected 优先用服务器给的原因，invalid 由调用处传文案。 */
function flowError(kind: FlowErrorKind, message?: string): FlowError {
  const key = MESSAGE_KEY[kind]
  const text = message || (key ? t(key) : '')
  return text ? { kind, message: text } : { kind }
}
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function serverText(data: any): string | undefined {
  const s = String(data?.error || data?.msg || data?.message || '').trim()
  return s || undefined
}
/** 429 和 5xx 的统一分类；其余返回 null，由调用处按接口处理。 */
function httpError(status: number): FlowError | null {
  if (status === 429) return flowError('rate_limited')
  if (status >= 500) return flowError('network')
  return null
}

function defaultStorage(): RedeemDeps['storage'] {
  try {
    return typeof sessionStorage === 'undefined' ? null : sessionStorage
  } catch {
    return null
  }
}

// 与旧 RechargeView 同一个设备号：兑换接口按它做限流和风控
function defaultDeviceId(): string {
  const make = () => 'web-' + Math.random().toString(36).slice(2) + Date.now().toString(36)
  try {
    let v = localStorage.getItem('cdk_device_id')
    if (!v) {
      v = make()
      localStorage.setItem('cdk_device_id', v)
    }
    return v
  } catch {
    return make()
  }
}

function defaultEvery(fn: () => void, ms: number): () => void {
  const id = setInterval(fn, ms)
  return () => clearInterval(id)
}

const emptyResult = (): FlowResult => ({ status: '', stage: '', message: '' })

export function useRedeemFlow(deps: RedeemDeps = {}): RedeemFlow {
  const fetchFn: typeof fetch = deps.fetchFn ?? ((input, init) => fetch(input, init))
  const storage = deps.storage === undefined ? defaultStorage() : deps.storage
  const now = deps.now ?? Date.now
  const every = deps.every ?? defaultEvery
  const deviceId = deps.deviceId ?? defaultDeviceId()

  const screen = ref<Screen>('code')
  const code = ref('')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const previewInfo = ref<any | null>(null)
  const account = ref<AccountSummary | null>(null)
  const blockingCode = ref('')
  const blockingReason = ref('')
  const extra = ref<{ accountCurrency?: string; codeCurrency?: string }>({})
  const result = ref<FlowResult>(emptyResult())
  const lookup = ref<LookupResult | null>(null)
  const error = ref<FlowError | null>(null)
  const busy = ref(false)
  const submitState = ref<SubmitState>('idle')
  const progressStartedAt = ref<number | null>(null)

  // 不对外的票据：页面用不到，只在这里带进请求。预检票是 ref，canRecover 要跟着它变
  let redemptionToken = ''
  const preflightTicket = ref('')
  let resultKind = '' // 结果响应里的 provider_kind；按卡密恢复时没有预览，靠它认 MarsWallex
  let stopPoll: (() => void) | null = null
  // 换卡密、换账号、重来时加一，丢掉还在路上的旧响应
  let seq = 0

  try {
    storage?.removeItem(OLD_PROGRESS_KEY)
  } catch {
    /* 存储被禁用 */
  }
  if (getCurrentScope()) onScopeDispose(() => stopPolling())

  // ---------- 码的种类（旧 RechargeView 的 targetPlan、isGrok、isClaude、isMW） ----------

  const targetPlan = () => targetPlanOf(previewInfo.value)
  const isGrok = (plan: string) => isGrokPlan(plan) && !isXPremiumPlan(plan)
  const isMW = () => isCardSupply(previewInfo.value?.provider_kind || resultKind)

  function alreadySatisfied(): boolean {
    const acc = account.value
    const plan = targetPlan()
    if (!acc || acc.checked !== true || !plan || acc.subscriptionHasActive === false) return false
    return planSatisfied(acc.currentPlan, plan, previewInfo.value?.plan_flow)
  }

  const confirmView = computed<ConfirmState>(() => confirmState({
    account: account.value,
    blockingCode: blockingCode.value,
    blockingReason: blockingReason.value,
    satisfied: alreadySatisfied(),
  }))
  const canRecover = computed(() => {
    const acc = account.value
    return !!preflightTicket.value && !!acc && (acc.recoveryRequired || acc.delinquent)
  })

  // ---------- 请求 ----------

  async function api(path: string, init: RequestInit = {}) {
    const headers = new Headers(init.headers || {})
    headers.set('X-Redemption-Device', deviceId)
    if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
    const r = await fetchFn(path, { ...init, headers, credentials: 'include' })
    const text = await r.text()
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    let data: any = null
    try {
      data = text ? JSON.parse(text) : null
    } catch {
      data = { raw: text }
    }
    return { r, data }
  }
  const post = (path: string, body: unknown) => api(path, { method: 'POST', body: JSON.stringify(body) })
  const byCodePath = (c: string) => `${API}/result-by-code?code=${encodeURIComponent(c)}`

  function lookupMessages(): LookupMessages {
    return { notFound: t('redeemUi.errors.homeNotFound'), network: t('redeemUi.errors.homeNetwork') }
  }

  // ---------- 进度缓存 ----------

  function save() {
    if (!storage || !code.value) return
    const payload: SavedProgress = {
      screen: screen.value,
      code: code.value,
      redemptionToken,
      // 不能用这个账号开通时票没有用处，不存，刷新后回第 ② 屏
      preflightToken: blockingCode.value || blockingReason.value ? '' : preflightTicket.value,
      previewInfo: previewInfo.value,
      account: account.value,
      result: { status: result.value.status, stage: result.value.stage },
      progressStartedAt: progressStartedAt.value,
      // 提交中也记下：请求在途时刷新或关页，回来不知道服务器收没收到
      submitUnknown: submitState.value !== 'idle',
      savedAt: now(),
    }
    try {
      storage.setItem(PROGRESS_KEY, JSON.stringify(payload))
    } catch {
      /* 超出配额或存储被禁用 */
    }
  }

  function removeSaved() {
    try {
      storage?.removeItem(PROGRESS_KEY)
    } catch {
      /* 存储被禁用 */
    }
  }

  function readSaved(): Partial<SavedProgress> | null {
    let raw: string | null = null
    try {
      raw = storage?.getItem(PROGRESS_KEY) ?? null
    } catch {
      return null
    }
    if (!raw) return null
    try {
      const p = JSON.parse(raw)
      if (!p || typeof p !== 'object') throw new Error('bad')
      if (!(Number(p.savedAt) > 0) || now() - Number(p.savedAt) > MAX_AGE_MS) throw new Error('old')
      return p
    } catch {
      removeSaved()
      return null
    }
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function savedAccount(a: any): AccountSummary | null {
    if (!a || typeof a !== 'object') return null
    return {
      maskedEmail: String(a.maskedEmail || ''),
      currentPlan: String(a.currentPlan || ''),
      subscriptionHasActive: typeof a.subscriptionHasActive === 'boolean' ? a.subscriptionHasActive : null,
      recoveryRequired: a.recoveryRequired === true,
      delinquent: a.delinquent === true,
      checked: typeof a.checked === 'boolean' ? a.checked : null,
    }
  }

  // ---------- 状态切换 ----------

  function stopPolling() {
    if (stopPoll) stopPoll()
    stopPoll = null
  }

  function clearAccountCheck() {
    preflightTicket.value = ''
    account.value = null
    blockingCode.value = ''
    blockingReason.value = ''
    extra.value = {}
  }

  function clearFlow() {
    seq++
    stopPolling()
    redemptionToken = ''
    resultKind = ''
    clearAccountCheck()
    previewInfo.value = null
    result.value = emptyResult()
    lookup.value = null
    error.value = null
    busy.value = false
    submitState.value = 'idle'
    progressStartedAt.value = null
  }

  /** 预检票不能用了：回第 ② 屏重新核对账号，卡密和预览保留。 */
  function backToCredential(err: FlowError | null) {
    clearAccountCheck()
    error.value = err
    screen.value = 'credential'
    save()
  }

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function applyResult(data: any) {
    // 结果接口：{ order: { status, stage, message, attempts_left }, ... }；兼容平铺和 data 包裹
    const order = data?.order || data?.data?.order || data?.data || data || {}
    const left = order.attempts_left
    result.value = {
      status: String(order.status || data?.status || data?.data?.status || ''),
      stage: String(order.stage || data?.stage || data?.data?.stage || ''),
      message: String(order.message || order.user_message || data?.message || data?.user_message || data?.data?.message || ''),
      ...(typeof left === 'number' ? { attemptsLeft: left } : {}),
    }
    if (data?.provider_kind) resultKind = String(data.provider_kind)
    const tok = data?.redemption_token || data?.data?.redemption_token
    if (tok) redemptionToken = String(tok)
  }

  function isTerminal(status: string): boolean {
    const s = status.toLowerCase()
    return TERMINAL.has(s) || (s === 'review' && isMW())
  }

  /** 已有单：进第 ④ 屏查进度；已经是终态就直接取售后结果进第 ⑤ 屏。 */
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  async function enterProgress(data: any, s: number, fallbackStatus = '') {
    applyResult(data)
    if (!result.value.status && fallbackStatus) result.value = { ...result.value, status: fallbackStatus }
    // 预检票里封着整份登录信息，查进度用不上，不再留着
    preflightTicket.value = ''
    screen.value = 'progress'
    if (progressStartedAt.value === null) progressStartedAt.value = now()
    save()
    if (isTerminal(result.value.status)) await finish(s)
    else startPoll()
  }

  async function finish(s: number) {
    stopPolling()
    const res = await lookupAftersale(code.value, lookupMessages(), fetchFn)
    if (s !== seq) return
    lookup.value = res.result ?? null
    screen.value = 'result'
    save()
  }

  function startPoll() {
    stopPolling()
    if (!redemptionToken && !code.value) return
    const s = seq
    let running = false
    const tick = async () => {
      if (running || s !== seq) return
      running = true
      try {
        await pollOnce(s)
      } finally {
        running = false
      }
    }
    stopPoll = every(tick, POLL_MS)
  }

  async function pollOnce(s: number) {
    let res: Awaited<ReturnType<typeof api>>
    try {
      res = await api(redemptionToken ? `${API}/result?token=${encodeURIComponent(redemptionToken)}` : byCodePath(code.value))
    } catch {
      if (s === seq) error.value = flowError('network') // 继续按原频率查
      return
    }
    if (s !== seq) return
    const { r, data } = res
    if (r.status === 404) {
      // 没有这笔单（例如兑换没有建单）：停止查询，不对着不存在的单一直转圈
      stopPolling()
      error.value = flowError('rejected', serverText(data))
      return
    }
    const httpErr = httpError(r.status)
    if (httpErr) {
      error.value = httpErr // 只是读进度，继续按原频率查
      return
    }
    if (!r.ok) return
    applyResult(data)
    error.value = null
    if (isTerminal(result.value.status)) await finish(s)
    else save()
  }

  // ---------- 对外操作 ----------

  async function preview(raw: string) {
    // 提交中或不确定是否已提交：只能查结果，不能换码重来
    if (busy.value || submitState.value !== 'idle') return
    const c = String(raw || '').trim()
    clearFlow()
    removeSaved()
    code.value = c
    screen.value = 'code'
    if (!c) {
      error.value = flowError('invalid', t('redeemUi.errors.homeFormat'))
      return
    }
    const s = seq
    busy.value = true
    try {
      let res: Awaited<ReturnType<typeof api>>
      try {
        res = await post(`${API}/preview`, { code: c })
      } catch {
        if (s === seq) error.value = flowError('network')
        return
      }
      if (s !== seq) return
      const { r, data } = res
      const httpErr = httpError(r.status)
      if (httpErr) {
        error.value = httpErr
        return
      }
      if (!r.ok) {
        // 已兑换过：按卡密查到单就接着显示进度
        if (USED_TEXT.test(serverText(data) || '') && (await resumeByCode(c, s))) return
        if (s !== seq) return
        // 码不能用：和首页一样显示售后查询的结果卡
        const msgs = lookupMessages()
        const found = await lookupAftersale(c, msgs, fetchFn)
        if (s !== seq) return
        if (found.result) lookup.value = found.result
        else error.value = flowError(found.error === msgs.network ? 'network' : 'invalid', found.error || serverText(data))
        return
      }
      const tok = data?.redemption_token || data?.data?.redemption_token || data?.token || ''
      if (!tok) {
        error.value = flowError('network', t('redeemUi.errors.badReply'))
        return
      }
      redemptionToken = String(tok)
      previewInfo.value = data?.data && typeof data.data === 'object' ? data.data : data
      screen.value = 'credential'
      save()
    } finally {
      if (s === seq) busy.value = false
    }
  }

  async function resumeByCode(c: string, s: number): Promise<boolean> {
    let res: Awaited<ReturnType<typeof api>>
    try {
      res = await api(byCodePath(c))
    } catch {
      return false
    }
    if (s !== seq || !res.r.ok) return false
    await enterProgress(res.data, s)
    return true
  }

  async function preflight(credential: { mode: 'session'; session: string }) {
    if (screen.value !== 'credential' && screen.value !== 'confirm') return
    if (busy.value || submitState.value !== 'idle' || !redemptionToken || !code.value) return
    // 旧的核对结果等回复到了再换掉：在第 ③ 屏重新核对时不闪；请求期间 busy 挡住开通
    error.value = null
    const s = seq
    busy.value = true
    try {
      let res: Awaited<ReturnType<typeof api>>
      try {
        res = await post(`${API}/preflight`, { code: code.value, redemption_token: redemptionToken, credential })
      } catch {
        if (s === seq) backToCredential(flowError('network'))
        return
      }
      if (s !== seq) return
      const { r, data } = res
      const httpErr = httpError(r.status)
      if (httpErr) return backToCredential(httpErr)
      if (r.status === 400 && data?.error_code === 'session_invalid') return backToCredential(flowError('invalid', t('redeemUi.errors.sessionInvalid')))
      if (!r.ok || (data && typeof data.code === 'number' && data.code !== 0)) return backToCredential(flowError('rejected', serverText(data)))

      const body = data?.data && typeof data.data === 'object' ? data.data : data || {}
      const ticket = String(body.preflight_token || data?.preflight_token || '')
      const bCode = String(body.blocking_code || '')
      const bReason = String(body.blocking_reason || '')
      const plan = targetPlan()
      // Grok 不发票；Claude 没票时开通带 Organization ID。其余码没有票又没说为什么：回复不完整
      if (!ticket && !bCode && !bReason && !isGrok(plan) && !isClaudePlan(plan)) {
        return backToCredential(flowError('network', t('redeemUi.errors.badReply')))
      }
      preflightTicket.value = ticket
      account.value = accountFromPreflight(data)
      blockingCode.value = bCode
      blockingReason.value = bReason
      extra.value = {
        ...(body.account_currency ? { accountCurrency: String(body.account_currency) } : {}),
        ...(body.code_currency ? { codeCurrency: String(body.code_currency) } : {}),
      }
      screen.value = 'confirm'
      save()
    } finally {
      if (s === seq) busy.value = false
    }
  }

  function redeemBody(opts: { agreeCreditTerms?: boolean; claudeOrgId?: string; grokSession?: string }): Record<string, unknown> | null {
    const plan = targetPlan()
    const clientRequestId = 'web-' + deviceId.slice(0, 8) + '-' + now()
    if (isClaudePlan(plan)) {
      // 有票带票；没票（与 Grok 一样）带页面传来的 Organization ID
      if (!preflightTicket.value && !opts.claudeOrgId) return null
      return claudeRedeemBody(redemptionToken, preflightTicket.value, opts.claudeOrgId || '', clientRequestId)
    }
    if (isGrok(plan)) {
      if (!opts.grokSession) return null
      return { redemption_token: redemptionToken, credential: { mode: 'session', session: opts.grokSession }, client_request_id: clientRequestId }
    }
    if (!preflightTicket.value) return null
    const body = { redemption_token: redemptionToken, preflight_token: preflightTicket.value, client_request_id: clientRequestId }
    return isMW() ? withCreditTerms(body, previewInfo.value, !!opts.agreeCreditTerms) : body
  }

  async function redeem(opts: { agreeCreditTerms?: boolean; claudeOrgId?: string; grokSession?: string } = {}) {
    // 以下检查和上锁都在第一个 await 之前完成：连点时第二次调用看到 submitting 直接返回
    if (submitState.value !== 'idle' || busy.value || screen.value !== 'confirm') return
    if (confirmView.value === 'blocked' || confirmView.value === 'recover') return
    if (isMW() && isCreditCode(previewInfo.value) && !opts.agreeCreditTerms) return
    const body = redeemBody(opts)
    if (!body) {
      backToCredential(flowError('ticket_expired'))
      return
    }
    submitState.value = 'submitting'
    busy.value = true
    error.value = null
    save() // 请求发出前就记下「提交中」
    const s = seq

    let res: Awaited<ReturnType<typeof api>>
    try {
      res = await post(`${API}/redeem`, body)
    } catch {
      // 不知道服务器收没收到：只能按卡密查结果，绝不重发
      if (s !== seq) return
      busy.value = false
      submitState.value = 'unknown'
      error.value = flowError('submit_unknown')
      save()
      return
    }
    if (s !== seq) return
    busy.value = false
    submitState.value = 'idle'
    const { r, data } = res
    if (r.status === 429) {
      error.value = flowError('rate_limited')
      save()
      return
    }
    // MarsWallex 被拒且没建单（熔断、未就绪、跨服务商冲突、票失效）：不跳去查一个不存在的单
    if (isMW() && redeemRejectedBeforeOrder(r.status, data)) {
      if (r.status === 400) {
        backToCredential(flowError('ticket_expired'))
      } else {
        error.value = flowError('rejected', serverText(data))
        save()
      }
      return
    }
    // 其余情况可能已经建单：去查进度
    if (!r.ok) error.value = r.status >= 500 ? flowError('network') : flowError('rejected', serverText(data))
    await enterProgress(data, s, r.ok ? 'queued' : '')
  }

  async function checkResult() {
    if (busy.value || !code.value) return
    const s = seq
    busy.value = true
    try {
      let res: Awaited<ReturnType<typeof api>>
      try {
        res = await api(byCodePath(code.value))
      } catch {
        if (s === seq) error.value = flowError('network')
        return
      }
      if (s !== seq) return
      const { r, data } = res
      if (r.status === 404) {
        // 码仍未使用、没有进行中的单：回第 ③ 屏，由买家自己再点
        submitState.value = 'idle'
        error.value = null
        screen.value = 'confirm'
        save()
        return
      }
      const httpErr = httpError(r.status)
      if (httpErr) {
        error.value = httpErr
        return
      }
      if (!r.ok) {
        error.value = flowError('rejected', serverText(data))
        return
      }
      submitState.value = 'idle'
      error.value = null
      await enterProgress(data, s)
    } finally {
      if (s === seq) busy.value = false
    }
  }

  async function recoverGrace() {
    if (busy.value || submitState.value !== 'idle' || !canRecover.value) return
    const token = preflightTicket.value
    // 票只用一次：交出去以后只要没确认解除就作废，页面改为「重新核对」，刷新后也不能拿它再发
    preflightTicket.value = ''
    error.value = null
    const s = seq
    busy.value = true
    // 没确认解除（待处理、出错、网络断）：票清空，仍按「要先恢复订阅」显示，并写进缓存
    const unconfirmed = (err: FlowError | null) => {
      preflightTicket.value = ''
      if (account.value) account.value = { ...account.value, recoveryRequired: true }
      error.value = err
      save()
    }
    try {
      let res: Awaited<ReturnType<typeof api>>
      try {
        res = await post(`${API}/recover-subscription`, { redemption_token: redemptionToken, preflight_token: token, confirmed: true })
      } catch {
        if (s === seq) unconfirmed(flowError('network', t('grace.refreshFailed')))
        return
      }
      if (s !== seq) return
      const { r, data } = res
      if (r.status === 429) {
        // 被限流时服务器没处理，票还能用
        preflightTicket.value = token
        error.value = flowError('rate_limited')
        save()
        return
      }
      if (!r.ok || (typeof data?.code === 'number' && data.code !== 0)) {
        unconfirmed(flowError(r.status >= 500 ? 'network' : 'rejected', t('grace.refreshFailed')))
        return
      }
      const out = data?.data || data
      const pf = out?.preflight
      if (pf) {
        account.value = accountFromPreflight(pf)
        blockingCode.value = String(pf.blocking_code || '')
        blockingReason.value = String(pf.blocking_reason || '')
      }
      const fresh = String(pf?.preflight_token || '')
      if (out?.status === 'cleared' && fresh) {
        // 确认解除并拿到新票：按新的账号信息显示，开通用新票
        preflightTicket.value = fresh
        save()
        return
      }
      unconfirmed(null)
    } finally {
      if (s === seq) busy.value = false
    }
  }

  function changeAccount() {
    // 提交中或不确定是否已提交时不能换账号再开通，只能先查结果
    if (submitState.value !== 'idle') return
    if (!previewInfo.value) {
      // 按卡密恢复的进度没有预览：重新预览一次，成功后同样停在第 ② 屏
      void preview(code.value)
      return
    }
    seq++
    stopPolling()
    clearAccountCheck()
    error.value = null
    busy.value = false
    result.value = emptyResult()
    lookup.value = null
    progressStartedAt.value = null
    resultKind = ''
    screen.value = 'credential'
    save()
  }

  function reset(opts: { keepCode?: boolean } = {}) {
    // 提交中或不确定是否已提交：只能查结果，不能清掉「提交中」的记录重来
    if (submitState.value !== 'idle') return
    const keep = opts.keepCode ? code.value : ''
    clearFlow()
    removeSaved()
    code.value = keep
    screen.value = 'code'
  }

  function restore(queryCode: string): boolean {
    // 兑换请求在途或不确定是否已提交：不改状态、不动缓存，也不丢掉在途请求的回复
    if (submitState.value !== 'idle') return false
    const saved = readSaved()
    if (!saved) return false
    const q = String(queryCode || '').trim()
    const savedCode = String(saved.code || '').trim()
    if (!savedCode || (q && savedCode.toLowerCase() !== q.toLowerCase())) {
      removeSaved()
      return false
    }
    clearFlow()
    code.value = savedCode
    redemptionToken = String(saved.redemptionToken || '')
    previewInfo.value = saved.previewInfo && typeof saved.previewInfo === 'object' ? saved.previewInfo : null
    progressStartedAt.value = typeof saved.progressStartedAt === 'number' ? saved.progressStartedAt : null
    const st = saved.screen

    if (st === 'progress' || st === 'result') {
      // 第 ⑤ 屏也回到查进度：第一次查到终态就重新取售后结果
      result.value = { ...emptyResult(), status: String(saved.result?.status || ''), stage: String(saved.result?.stage || '') }
      screen.value = 'progress'
      if (progressStartedAt.value === null) progressStartedAt.value = now()
      startPoll()
      return true
    }
    if (!redemptionToken || !previewInfo.value) {
      removeSaved()
      return false
    }
    if (st === 'confirm' && saved.submitUnknown === true) {
      // 刷新前兑换请求没收到回复：仍然只能查结果
      account.value = savedAccount(saved.account)
      preflightTicket.value = String(saved.preflightToken || '')
      submitState.value = 'unknown'
      error.value = flowError('submit_unknown')
      screen.value = 'confirm'
      return true
    }
    if (st === 'confirm' && saved.preflightToken) {
      account.value = savedAccount(saved.account)
      preflightTicket.value = String(saved.preflightToken)
      screen.value = 'confirm'
      return true
    }
    if (st === 'confirm' || st === 'credential') {
      screen.value = 'credential'
      save()
      return true
    }
    removeSaved()
    return false
  }

  return {
    screen, code, previewInfo, account, blockingCode, blockingReason, extra, result, lookup, error, busy, submitState,
    progressStartedAt, canRecover, confirmView, preview, preflight, redeem, checkResult, recoverGrace, changeAccount, reset, restore,
  }
}
