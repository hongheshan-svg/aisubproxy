// 兑换流程模块：假的 fetch（按路径返回预设响应并记下每次调用）、内存存储、手动触发的定时器。
// 不调真实 ChatGPT，不付钱。
import { afterAll, beforeAll, describe, expect, it, vi, type Mock } from 'vitest'
import type { RedeemDeps } from '../useRedeemFlow'

// 模块会引入全局 i18n，它加载时碰 document 和 localStorage；测试环境没有 DOM，先放最小替身再动态 import
let useRedeemFlow: typeof import('../useRedeemFlow').useRedeemFlow
beforeAll(async () => {
  await import('vue') // 先加载 vue（它加载时需要真的 document），再放替身
  vi.stubGlobal('document', { documentElement: {} })
  vi.stubGlobal('localStorage', memStorage())
  useRedeemFlow = (await import('../useRedeemFlow')).useRedeemFlow
})
afterAll(() => { vi.unstubAllGlobals() })

const KEY = 'cdk_redeem_progress_v2'
const PREVIEW = '/api/v1/public/cdk/preview'
const PREFLIGHT = '/api/v1/public/cdk/preflight'
const REDEEM = '/api/v1/public/cdk/redeem'
const RESULT = '/api/v1/public/cdk/result'
const BY_CODE = '/api/v1/public/cdk/result-by-code'
const LOOKUP = '/api/v1/lookup/cdk'
const NOW = Date.parse('2026-11-09T08:00:00Z')

const CODE = 'MW-PLUS-AAAA-BBBB'
const SESSION_TOKEN = 'SESSION-TOKEN-SECRET-123'
const ACCESS_TOKEN = 'eyJhbGciOiJSUzI1NiJ9.ACCESS-PART.SIGNATURE'
const SESSION_RAW = JSON.stringify({
  user: { email: 'alice@example.com' },
  expires: '2099-01-01T00:00:00Z',
  accessToken: ACCESS_TOKEN,
  sessionToken: SESSION_TOKEN,
})

const MW = { provider: 'marswallex', provider_kind: 'card_supply' }
const MW_PREVIEW = {
  ...MW, status: 'unused', product: 'chatgpt', plan: 'plus', plan_type: 'plus', plan_flow: 'direct', region: 'US',
  redemption_token: 'rt_A', attempts_left: 2, kind: 'subscription', requires_subscription: false,
  requires_no_subscription: true, auto_renew: false, currency: 'USD',
}
const MW_ELIGIBLE = {
  ...MW, email: 'alice@example.com', account_email: 'alice@example.com', preflight_token: 'pf_1', expires_in: 600,
  account_checked: true, current_plan: 'free', subscription_has_active: false,
  kind: 'subscription', requires_subscription: false, requires_no_subscription: true, auto_renew: false,
}
const MW_QUEUED = { ...MW, redemption_token: 'rt_A', status: 'queued', message: '', order: { status: 'queued', stage: 'queued' } }

// 'hang'：请求发出后一直等不到回复（模拟提交途中刷新或关页）
type Reply = { status?: number; body?: unknown } | Error | 'hang'
interface Call { url: string; path: string; init?: RequestInit; body?: any }

// 每个路径一串预设响应，按顺序用，用到最后一个后一直重复它
function fakeFetch(routes: Record<string, Reply | Reply[]> = {}) {
  const calls: Call[] = []
  const queues = new Map<string, Reply[]>()
  const reply = (path: string, ...replies: Reply[]) => { queues.set(path, replies) }
  for (const [p, r] of Object.entries(routes)) reply(p, ...(Array.isArray(r) ? r : [r]))
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const path = url.split('?')[0]
    calls.push({ url, path, init, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const q = queues.get(path)
    if (!q || q.length === 0) throw new Error('没有预设响应：' + url)
    const r = q.length > 1 ? q.shift()! : q[0]
    if (r === 'hang') return new Promise<Response>(() => {})
    if (r instanceof Error) throw r
    return new Response(JSON.stringify(r.body ?? {}), { status: r.status ?? 200 })
  })
  const count = (path: string) => calls.filter((c) => c.path === path).length
  return { fn: fn as unknown as typeof fetch, mock: fn, calls, reply, count }
}

function memStorage(init: Record<string, string> = {}) {
  const m = new Map(Object.entries(init))
  return {
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => { m.set(k, String(v)) },
    removeItem: (k: string) => { m.delete(k) },
  }
}

interface Timer { fn: () => void; ms: number; cancel: Mock }
function manualEvery() {
  const timers: Timer[] = []
  const every = (fn: () => void, ms: number) => {
    const cancel = vi.fn()
    timers.push({ fn, ms, cancel })
    return cancel
  }
  return { every, timers }
}
// 手动触发一次查进度，等它跑完
const runTick = async (t: Timer) => { await (t.fn() as unknown as Promise<void>) }

function makeFlow(net: ReturnType<typeof fakeFetch>, extra: Partial<RedeemDeps> = {}) {
  const storage = memStorage()
  const timer = manualEvery()
  const flow = useRedeemFlow({ fetchFn: net.fn, storage, now: () => NOW, every: timer.every, deviceId: 'web-test-device', ...extra })
  return { flow, storage: (extra.storage ?? storage) as ReturnType<typeof memStorage>, timer }
}

// 走到第 ③ 屏：MarsWallex 订阅码，账号符合
async function toConfirm(preflightReply: Reply = { body: MW_ELIGIBLE }) {
  const net = fakeFetch({ [PREVIEW]: { body: MW_PREVIEW }, [PREFLIGHT]: preflightReply })
  const { flow, storage, timer } = makeFlow(net)
  await flow.preview(CODE)
  expect(flow.screen.value).toBe('credential')
  await flow.preflight({ mode: 'session', session: SESSION_RAW })
  expect(flow.screen.value).toBe('confirm')
  return { flow, net, storage, timer }
}

describe('useRedeemFlow', () => {
  it('确认开通连点只发一次请求', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, { body: MW_QUEUED })
    const p1 = flow.redeem()
    expect(flow.submitState.value).toBe('submitting')
    const p2 = flow.redeem()
    await Promise.all([p1, p2])
    expect(net.count(REDEEM)).toBe(1)
    const call = net.calls.find((c) => c.path === REDEEM)!
    expect(call.body).toMatchObject({ redemption_token: 'rt_A', preflight_token: 'pf_1' })
    expect(new Headers(call.init?.headers).get('X-Redemption-Device')).toBe('web-test-device')
    expect(flow.screen.value).toBe('progress')
  })

  it('兑换请求网络失败后只查结果，不再兑换', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, new TypeError('Failed to fetch'))
    await flow.redeem()
    expect(flow.submitState.value).toBe('unknown')
    expect(flow.error.value?.kind).toBe('submit_unknown')
    expect(flow.screen.value).toBe('confirm')

    await flow.redeem()
    expect(net.count(REDEEM)).toBe(1)

    const before = net.calls.length
    net.reply(BY_CODE, { status: 404, body: { ...MW, error: '未找到该卡密的兑换记录' } })
    await flow.checkResult()
    expect(net.calls.slice(before).map((c) => c.path)).toEqual([BY_CODE])
    expect(net.calls[net.calls.length - 1].url).toContain('code=' + encodeURIComponent(CODE))
    expect(flow.screen.value).toBe('confirm')
    expect(flow.submitState.value).toBe('idle')
    expect(net.count(REDEEM)).toBe(1)
  })

  it('没收到回复后刷新页面，仍然只能查结果', async () => {
    const { flow, net, storage } = await toConfirm()
    net.reply(REDEEM, new TypeError('Failed to fetch'))
    await flow.redeem()
    expect(flow.submitState.value).toBe('unknown')

    const net2 = fakeFetch({ [REDEEM]: { body: MW_QUEUED } })
    const flow2 = useRedeemFlow({ fetchFn: net2.fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'web-test-device' })
    expect(flow2.restore(CODE)).toBe(true)
    expect(flow2.screen.value).toBe('confirm')
    expect(flow2.submitState.value).toBe('unknown')
    expect(flow2.error.value?.kind).toBe('submit_unknown')
    await flow2.redeem()
    flow2.changeAccount()
    expect(flow2.screen.value).toBe('confirm')
    expect(net2.count(REDEEM)).toBe(0)
  })

  it('查一下结果查到单就进第 ④ 屏，同样不调兑换接口', async () => {
    const { flow, net, timer } = await toConfirm()
    net.reply(REDEEM, new TypeError('Failed to fetch'))
    await flow.redeem()
    net.reply(BY_CODE, { body: { ...MW, cdk_code: CODE, redemption_token: 'rt_A', status: 'running', stage: 'payment', order: { status: 'running', stage: 'payment' } } })
    await flow.checkResult()
    expect(flow.screen.value).toBe('progress')
    expect(flow.result.value).toMatchObject({ status: 'running', stage: 'payment' })
    expect(timer.timers).toHaveLength(1)
    expect(net.count(REDEEM)).toBe(1)
  })

  it('预检票过期回到第 ② 屏', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, { status: 400, body: { ...MW, error: '预检已失效，请重新检测账号' } })
    await flow.redeem()
    expect(flow.screen.value).toBe('credential')
    expect(flow.error.value?.kind).toBe('ticket_expired')
    expect(flow.error.value?.message).toBe('核对已过期，请重新粘贴登录信息')
    expect(flow.code.value).toBe(CODE)
    expect(flow.previewInfo.value).not.toBeNull()
    expect(flow.submitState.value).toBe('idle')
    // 票已清掉，在第 ② 屏再点也不会发兑换
    await flow.redeem()
    expect(net.count(REDEEM)).toBe(1)
  })

  it('MarsWallex 其他被拒（没建单）停在第 ③ 屏显示原因', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, { status: 503, body: { ...MW, error: '服务暂时不可用，请稍后再试' } })
    await flow.redeem()
    expect(flow.screen.value).toBe('confirm')
    expect(flow.error.value).toEqual({ kind: 'rejected', message: '服务暂时不可用，请稍后再试' })
    expect(flow.submitState.value).toBe('idle')
    expect(net.count(RESULT) + net.count(BY_CODE)).toBe(0)
  })

  it('429 不自动重试', async () => {
    const net = fakeFetch({ [PREVIEW]: { status: 429, body: { error: 'too many requests' } } })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    expect(flow.error.value?.kind).toBe('rate_limited')
    expect(flow.error.value?.message).toBe('操作太频繁，请过几分钟再试')
    expect(net.mock).toHaveBeenCalledTimes(1)
    expect(flow.screen.value).toBe('code')
    expect(flow.code.value).toBe(CODE)
  })

  it('兑换遇到 429 停在第 ③ 屏，不自动重试', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, { status: 429, body: { error: 'too many requests' } })
    await flow.redeem()
    expect(flow.error.value?.kind).toBe('rate_limited')
    expect(flow.screen.value).toBe('confirm')
    expect(flow.submitState.value).toBe('idle')
    expect(net.count(REDEEM)).toBe(1)
  })

  it('服务器 5xx 或网络断开算没连上，已填内容不清空', async () => {
    const net = fakeFetch({ [PREVIEW]: { body: MW_PREVIEW }, [PREFLIGHT]: { status: 502, body: {} } })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    await flow.preflight({ mode: 'session', session: SESSION_RAW })
    expect(flow.error.value).toEqual({ kind: 'network', message: '没连上，请再试一次' })
    expect(flow.screen.value).toBe('credential')
    expect(flow.code.value).toBe(CODE)
    expect(flow.previewInfo.value).not.toBeNull()

    net.reply(PREFLIGHT, new TypeError('Failed to fetch'))
    await flow.preflight({ mode: 'session', session: SESSION_RAW })
    expect(flow.error.value?.kind).toBe('network')
    expect(flow.screen.value).toBe('credential')
    expect(net.count(PREFLIGHT)).toBe(2)
  })

  it('登录信息无效停在第 ② 屏', async () => {
    const net = fakeFetch({
      [PREVIEW]: { body: MW_PREVIEW },
      [PREFLIGHT]: { status: 400, body: { ...MW, error_code: 'session_invalid', error: '会话解析失败：缺少 sessionToken' } },
    })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    await flow.preflight({ mode: 'session', session: SESSION_RAW })
    expect(flow.screen.value).toBe('credential')
    // 用语言包文案（规格 §5.4 原句），不用后端原文
    expect(flow.error.value).toEqual({ kind: 'invalid', message: '登录信息已失效，请回到 ChatGPT 刷新那个页面，再重新复制' })
    expect(flow.account.value).toBeNull()
  })

  it('账号不符合时进第 ③ 屏，带放弃码和币种，开通不发请求', async () => {
    const { flow, net } = await toConfirm({
      body: {
        ...MW, email: 'alice@example.com', account_email: 'alice@example.com', account_checked: true, current_plan: 'plus',
        subscription_has_active: true, blocking_code: 'credit_currency_mismatch', blocking_reason: '币种不一致',
        account_currency: 'USD', code_currency: 'PHP',
      },
    })
    expect(flow.blockingCode.value).toBe('credit_currency_mismatch')
    expect(flow.blockingReason.value).toBe('币种不一致')
    expect(flow.extra.value).toEqual({ accountCurrency: 'USD', codeCurrency: 'PHP' })
    expect(flow.account.value?.maskedEmail).toBe('al***@example.com')
    await flow.redeem()
    expect(net.count(REDEEM)).toBe(0)
    expect(flow.submitState.value).toBe('idle')
  })

  it('点数码没勾须知不发兑换，勾了带 agree_credit_terms', async () => {
    const credit = { kind: 'credit', credit_quantity: 250, requires_subscription: true, requires_no_subscription: false, requires_credit_terms: true }
    const net = fakeFetch({
      [PREVIEW]: { body: { ...MW_PREVIEW, plan: 'credit250', plan_type: 'credit250', auto_renew: undefined, ...credit } },
      [PREFLIGHT]: { body: { ...MW_ELIGIBLE, current_plan: 'plus', subscription_has_active: true, ...credit } },
      [REDEEM]: { body: MW_QUEUED },
    })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    await flow.preflight({ mode: 'session', session: SESSION_RAW })
    await flow.redeem()
    expect(net.count(REDEEM)).toBe(0)
    await flow.redeem({ agreeCreditTerms: true })
    expect(net.count(REDEEM)).toBe(1)
    expect(net.calls.find((c) => c.path === REDEEM)!.body).toMatchObject({ preflight_token: 'pf_1', agree_credit_terms: true })
  })

  it('进度缓存不存登录信息原文', async () => {
    const { storage } = await toConfirm()
    const saved = storage.getItem(KEY)
    expect(saved).not.toBeNull()
    expect(saved).not.toContain(SESSION_TOKEN)
    expect(saved).not.toContain(ACCESS_TOKEN)
    expect(saved).not.toContain('alice@example.com')
    expect(saved).not.toMatch(/password/i)
    expect(saved).toContain('al***@example.com')
    expect(saved).toContain('pf_1')
  })

  it('Grok 登录态不进缓存，开通时由页面带上', async () => {
    const grokSession = 'sso=GROK-SSO-SECRET-0123456789'
    const net = fakeFetch({
      [PREVIEW]: { body: { status: 'unused', product: 'grok', plan: 'premium_monthly', redemption_token: 'rt_G' } },
      [PREFLIGHT]: { body: { code: 0, data: { email: 'gina@example.com', currentPlan: 'free' } } },
      [REDEEM]: { status: 202, body: { status: 'queued' } },
    })
    const { flow, storage } = makeFlow(net)
    await flow.preview('GROK-CODE-1')
    await flow.preflight({ mode: 'session', session: grokSession })
    expect(flow.screen.value).toBe('confirm')
    expect(storage.getItem(KEY)).not.toContain('GROK-SSO-SECRET')
    await flow.redeem({ grokSession })
    expect(net.calls.find((c) => c.path === REDEEM)!.body).toMatchObject({
      redemption_token: 'rt_G', credential: { mode: 'session', session: grokSession },
    })
    expect(flow.screen.value).toBe('progress')
    expect(storage.getItem(KEY)).not.toContain('GROK-SSO-SECRET')
  })

  it('Claude 没有预检票时带 Organization ID 开通，ID 不进缓存', async () => {
    const orgId = '0f4c2b8e-1d2a-4c3b-9e8f-7a6b5c4d3e2f'
    const net = fakeFetch({
      [PREVIEW]: { body: { status: 'unused', product: 'claude', plan: 'pro', redemption_token: 'rt_C' } },
      [PREFLIGHT]: { body: { code: 0, data: {} } },
      [REDEEM]: { body: { status: 'queued' } },
    })
    const { flow, storage } = makeFlow(net)
    await flow.preview('CLAUDE-CODE-1')
    await flow.preflight({ mode: 'session', session: orgId })
    expect(flow.screen.value).toBe('confirm')
    expect(flow.account.value?.checked).toBeNull()
    expect(storage.getItem(KEY)).not.toContain(orgId)
    await flow.redeem({ claudeOrgId: orgId })
    expect(net.calls.find((c) => c.path === REDEEM)!.body).toMatchObject({
      redemption_token: 'rt_C', credential: { mode: 'session', session: orgId },
    })
    expect(storage.getItem(KEY)).not.toContain(orgId)
  })

  it('在第 ③ 屏刷新能恢复，票过期回第 ② 屏', async () => {
    const { storage } = await toConfirm()
    const net2 = fakeFetch({ [REDEEM]: { status: 400, body: { ...MW, error: '预检已失效，请重新检测账号' } } })
    const flow2 = useRedeemFlow({ fetchFn: net2.fn, storage, now: () => NOW + 60_000, every: manualEvery().every, deviceId: 'web-test-device' })
    expect(flow2.restore('')).toBe(true)
    expect(flow2.screen.value).toBe('confirm')
    expect(flow2.account.value?.maskedEmail).toBe('al***@example.com')
    expect(flow2.code.value).toBe(CODE)
    await flow2.redeem()
    expect(net2.count(REDEEM)).toBe(1)
    expect(net2.calls[0].body).toMatchObject({ redemption_token: 'rt_A', preflight_token: 'pf_1' })
    expect(flow2.screen.value).toBe('credential')
    expect(flow2.error.value?.kind).toBe('ticket_expired')
    expect(storage.getItem(KEY)).not.toContain('pf_1')
  })

  it('在第 ④ 屏刷新接着查进度', async () => {
    const { flow, net, storage } = await toConfirm()
    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    expect(flow.progressStartedAt.value).toBe(NOW)

    const net2 = fakeFetch({ [RESULT]: { body: { ...MW, status: 'running', stage: 'card_funding', order: { status: 'running', stage: 'card_funding' } } } })
    const timer2 = manualEvery()
    const flow2 = useRedeemFlow({ fetchFn: net2.fn, storage, now: () => NOW + 5 * 60_000, every: timer2.every, deviceId: 'web-test-device' })
    expect(flow2.restore(CODE)).toBe(true)
    expect(flow2.screen.value).toBe('progress')
    expect(flow2.progressStartedAt.value).toBe(NOW)
    expect(timer2.timers).toHaveLength(1)
    await runTick(timer2.timers[0])
    expect(flow2.result.value.stage).toBe('card_funding')
    expect(net2.calls[0].url).toContain('token=rt_A')
    expect(net2.count(REDEEM)).toBe(0)
  })

  it('带了别的卡密就丢掉旧进度', () => {
    const storage = memStorage({ [KEY]: JSON.stringify({ screen: 'credential', code: 'CODE-A', redemptionToken: 'rt_A', previewInfo: MW_PREVIEW, savedAt: NOW }) })
    const flow = useRedeemFlow({ fetchFn: fakeFetch().fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'd' })
    expect(flow.restore('CODE-B')).toBe(false)
    expect(storage.getItem(KEY)).toBeNull()
    expect(flow.code.value).not.toBe('CODE-A')
    expect(flow.screen.value).toBe('code')
  })

  it('超过 7 天的进度丢掉', () => {
    const old = NOW - 8 * 24 * 3600 * 1000
    const storage = memStorage({ [KEY]: JSON.stringify({ screen: 'credential', code: 'CODE-A', redemptionToken: 'rt_A', previewInfo: MW_PREVIEW, savedAt: old }) })
    const flow = useRedeemFlow({ fetchFn: fakeFetch().fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'd' })
    expect(flow.restore('')).toBe(false)
    expect(storage.getItem(KEY)).toBeNull()
  })

  it('换一个账号保留卡密', async () => {
    const { flow, storage } = await toConfirm()
    const preview = flow.previewInfo.value
    flow.changeAccount()
    expect(flow.screen.value).toBe('credential')
    expect(flow.code.value).toBe(CODE)
    expect(flow.previewInfo.value).toEqual(preview)
    expect(flow.account.value).toBeNull()
    expect(flow.blockingCode.value).toBe('')
    expect(flow.error.value).toBeNull()
    expect(storage.getItem(KEY)).not.toContain('pf_1')
  })

  it('恢复订阅后用新票开通，旧票不再使用', async () => {
    const { flow, net } = await toConfirm({ body: { ...MW_ELIGIBLE, subscription_recovery_required: true } })
    expect(flow.account.value?.recoveryRequired).toBe(true)
    await flow.redeem()
    expect(net.count(REDEEM)).toBe(0)

    net.reply('/api/v1/public/cdk/recover-subscription', {
      body: { status: 'cleared', preflight: { ...MW_ELIGIBLE, preflight_token: 'pf_2', subscription_recovery_required: false } },
    })
    await flow.recoverGrace()
    const call = net.calls.find((c) => c.path === '/api/v1/public/cdk/recover-subscription')!
    expect(call.body).toEqual({ redemption_token: 'rt_A', preflight_token: 'pf_1', confirmed: true })
    expect(flow.account.value?.recoveryRequired).toBe(false)
    expect(flow.account.value?.maskedEmail).toBe('al***@example.com')

    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    expect(net.calls.find((c) => c.path === REDEEM)!.body).toMatchObject({ preflight_token: 'pf_2' })
  })

  it('恢复订阅没确认解除：仍要先恢复，不能开通', async () => {
    const { flow, net, storage } = await toConfirm({ body: { ...MW_ELIGIBLE, subscription_is_delinquent: true } })
    expect(flow.canRecover.value).toBe(true)
    net.reply('/api/v1/public/cdk/recover-subscription', { body: { status: 'pending', preflight: { ...MW_ELIGIBLE, preflight_token: 'pf_2', subscription_is_delinquent: true } } })
    await flow.recoverGrace()
    expect(flow.account.value?.delinquent).toBe(true)
    expect(flow.account.value?.recoveryRequired).toBe(true)
    expect(flow.canRecover.value).toBe(false)
    expect(flow.confirmView.value).toBe('recover')
    expect(storage.getItem(KEY)).not.toContain('pf_1')
    expect(storage.getItem(KEY)).not.toContain('pf_2')
    // 旧票已交出去，再点恢复也不发
    await flow.recoverGrace()
    expect(net.count('/api/v1/public/cdk/recover-subscription')).toBe(1)
    await flow.redeem()
    expect(net.count(REDEEM)).toBe(0)
  })

  it('按卡密恢复的单失败后换账号，会重新预览再到第 ② 屏', async () => {
    const net = fakeFetch({
      [PREVIEW]: [
        { status: 409, body: { ...MW, error: '该卡密正在处理中，请勿重复提交（已使用，可查询进度）', status: 'reserved' } },
        { body: MW_PREVIEW },
      ],
      [BY_CODE]: { body: { ...MW, cdk_code: CODE, redemption_token: 'rt_A', status: 'failed_precharge', order: { status: 'failed_precharge', can_retry: true, attempts_left: 1 } } },
      [LOOKUP]: { body: { cdk_code: CODE, status: 'unused', used: false, can_resubmit: true, guide: 'retry_later' } },
    })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    expect(flow.screen.value).toBe('result')
    expect(flow.previewInfo.value).toBeNull()
    flow.changeAccount()
    await vi.waitFor(() => expect(flow.screen.value).toBe('credential'))
    expect(flow.previewInfo.value).toMatchObject({ plan: 'plus' })
    expect(net.count(PREVIEW)).toBe(2)
    expect(net.count(REDEEM)).toBe(0)
  })

  it('删掉 v1 旧缓存', () => {
    const storage = memStorage({ cdk_redeem_progress_v1: '{"step":3,"code":"OLD"}' })
    useRedeemFlow({ fetchFn: fakeFetch().fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'd' })
    expect(storage.getItem('cdk_redeem_progress_v1')).toBeNull()
  })

  it('预览报码不能用：取售后结果，停在第 ① 屏', async () => {
    const net = fakeFetch({
      [PREVIEW]: { status: 400, body: { ...MW, error: '卡密已禁用', status: 'disabled' } },
      [LOOKUP]: { body: { cdk_code: CODE, status: 'disabled', used: false } },
    })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    expect(flow.screen.value).toBe('code')
    expect(flow.lookup.value?.status).toBe('disabled')
    expect(flow.error.value).toBeNull()
    expect(net.count(BY_CODE)).toBe(0)
  })

  it('预览报已使用：按卡密查到单就进第 ④ 屏', async () => {
    const net = fakeFetch({
      [PREVIEW]: { status: 409, body: { ...MW, error: '该卡密已使用，无法再次兑换', status: 'consumed' } },
      [BY_CODE]: { body: { ...MW, cdk_code: CODE, redemption_token: 'rt_A', status: 'running', stage: 'checkout', order: { status: 'running', stage: 'checkout' } } },
    })
    const { flow, timer } = makeFlow(net)
    await flow.preview(CODE)
    expect(flow.screen.value).toBe('progress')
    expect(flow.result.value).toMatchObject({ status: 'running', stage: 'checkout' })
    expect(timer.timers).toHaveLength(1)
    expect(net.count(LOOKUP)).toBe(0)
  })

  it('到终态停止查询并取售后结果', async () => {
    const { flow, net, timer } = await toConfirm()
    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    expect(flow.screen.value).toBe('progress')
    expect(timer.timers).toHaveLength(1)
    expect(timer.timers[0].ms).toBe(3000)

    net.reply(RESULT,
      { body: { ...MW, status: 'running', stage: 'payment', order: { status: 'running', stage: 'payment' } } },
      { body: { ...MW, status: 'completed', stage: '', order: { status: 'completed', stage: '', account_email: 'al***@example.com' } } })
    net.reply(LOOKUP, { body: { cdk_code: CODE, status: 'used', used: true, plan: 'plus', renewal: 'off' } })
    await runTick(timer.timers[0])
    expect(flow.screen.value).toBe('progress')
    expect(flow.result.value.stage).toBe('payment')
    expect(timer.timers[0].cancel).not.toHaveBeenCalled()

    await runTick(timer.timers[0])
    expect(timer.timers[0].cancel).toHaveBeenCalled()
    expect(flow.lookup.value?.renewal).toBe('off')
    expect(flow.screen.value).toBe('result')
    expect(flow.result.value.status).toBe('completed')
    expect(net.count(LOOKUP)).toBe(1)
  })

  it('MarsWallex 的 review 也是终态，带上剩余次数', async () => {
    const { flow, net, timer } = await toConfirm()
    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    net.reply(RESULT, { body: { ...MW, status: 'review', stage: 'payment', order: { status: 'review', stage: 'payment', attempts_left: 1 } } })
    net.reply(LOOKUP, { body: { cdk_code: CODE, status: 'processing', used: false, guide: 'manual_review' } })
    await runTick(timer.timers[0])
    expect(timer.timers[0].cancel).toHaveBeenCalled()
    expect(flow.screen.value).toBe('result')
    expect(flow.result.value).toMatchObject({ status: 'review', attemptsLeft: 1 })
  })

  it('查进度遇到 404 停止查询', async () => {
    const { flow, net, timer } = await toConfirm()
    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    net.reply(RESULT, { status: 404, body: { ...MW, error: '未找到该卡密的兑换记录' } })
    await runTick(timer.timers[0])
    expect(timer.timers[0].cancel).toHaveBeenCalled()
    expect(flow.error.value?.kind).toBe('rejected')
    expect(net.count(REDEEM)).toBe(1)
  })
})

describe('修复第 1 轮', () => {
  it('提交途中刷新或关页，回来只能查结果', async () => {
    const { flow, net, storage } = await toConfirm()
    net.reply(REDEEM, 'hang')
    void flow.redeem()
    expect(flow.submitState.value).toBe('submitting')
    expect(storage.getItem(KEY)).toContain('"submitUnknown":true')

    const net2 = fakeFetch({ [REDEEM]: { body: MW_QUEUED } })
    const flow2 = useRedeemFlow({ fetchFn: net2.fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'web-test-device' })
    expect(flow2.restore(CODE)).toBe(true)
    expect(flow2.screen.value).toBe('confirm')
    expect(flow2.submitState.value).toBe('unknown')
    expect(flow2.error.value?.kind).toBe('submit_unknown')
    await flow2.redeem()
    expect(net2.count(REDEEM)).toBe(0)
  })

  it('有明确结果后清掉提交标记：429 和其他被拒', async () => {
    const { flow, net, storage } = await toConfirm()
    net.reply(REDEEM, { status: 429, body: { error: 'too many requests' } })
    await flow.redeem()
    expect(storage.getItem(KEY)).toContain('"submitUnknown":false')
    net.reply(REDEEM, { status: 409, body: { ...MW, error: '该账号正在其他服务商处理中' } })
    await flow.redeem()
    expect(flow.error.value?.kind).toBe('rejected')
    expect(storage.getItem(KEY)).toContain('"submitUnknown":false')
  })

  it('不确定是否已提交时，重来和重新预览都不动作', async () => {
    const { flow, net, storage } = await toConfirm()
    net.reply(REDEEM, new TypeError('Failed to fetch'))
    await flow.redeem()
    expect(flow.submitState.value).toBe('unknown')
    const before = net.calls.length
    flow.reset()
    await flow.preview('ANOTHER-CODE')
    expect(flow.submitState.value).toBe('unknown')
    expect(flow.screen.value).toBe('confirm')
    expect(flow.code.value).toBe(CODE)
    expect(storage.getItem(KEY)).toContain('"submitUnknown":true')
    expect(net.calls.length).toBe(before)
  })

  it('提交中调 restore 直接返回 false，不改状态', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, 'hang')
    void flow.redeem()
    expect(flow.restore('')).toBe(false)
    expect(flow.submitState.value).toBe('submitting')
    expect(flow.screen.value).toBe('confirm')
    expect(flow.code.value).toBe(CODE)
  })

  it('恢复订阅失败后刷新，不能拿旧票再发', async () => {
    const RECOVER = '/api/v1/public/cdk/recover-subscription'
    const { flow, net, storage } = await toConfirm({ body: { ...MW_ELIGIBLE, subscription_recovery_required: true } })
    net.reply(RECOVER, new TypeError('Failed to fetch'))
    await flow.recoverGrace()
    expect(flow.canRecover.value).toBe(false)
    expect(storage.getItem(KEY)).not.toContain('pf_1')

    const net2 = fakeFetch({ [RECOVER]: { body: { status: 'cleared' } } })
    const flow2 = useRedeemFlow({ fetchFn: net2.fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'web-test-device' })
    flow2.restore(CODE)
    expect(flow2.canRecover.value).toBe(false)
    await flow2.recoverGrace()
    expect(net2.count(RECOVER)).toBe(0)
  })

  it('恢复订阅遇到 429 把票还回去，还能再恢复', async () => {
    const RECOVER = '/api/v1/public/cdk/recover-subscription'
    const { flow, net, storage } = await toConfirm({ body: { ...MW_ELIGIBLE, subscription_recovery_required: true } })
    net.reply(RECOVER, { status: 429, body: {} })
    await flow.recoverGrace()
    expect(flow.error.value?.kind).toBe('rate_limited')
    expect(flow.canRecover.value).toBe(true)
    expect(storage.getItem(KEY)).toContain('pf_1')
  })

  it('恢复订阅接口报错：清票，用恢复失败的文案', async () => {
    const RECOVER = '/api/v1/public/cdk/recover-subscription'
    const { flow, net, storage } = await toConfirm({ body: { ...MW_ELIGIBLE, subscription_recovery_required: true } })
    net.reply(RECOVER, { status: 400, body: { error: 'bad' } })
    await flow.recoverGrace()
    expect(flow.canRecover.value).toBe(false)
    expect(flow.error.value).toEqual({ kind: 'rejected', message: '暂时无法确认处理结果，请重新检测账号，不要重复取消。' })
    expect(storage.getItem(KEY)).not.toContain('pf_1')
  })

  it('服务器没给原因的被拒用兜底文案', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, { status: 403, body: { ...MW } })
    await flow.redeem()
    expect(flow.error.value).toEqual({ kind: 'rejected', message: '这张卡密暂时不能开通。请稍后再试，或联系卖家。' })
  })

  it('服务器回复不完整时不说「没连上」', async () => {
    const BAD = { kind: 'network', message: '服务器的回复不完整，请再试一次。' }
    const net = fakeFetch({ [PREVIEW]: { body: { ...MW_PREVIEW, redemption_token: '' } } })
    const { flow } = makeFlow(net)
    await flow.preview(CODE)
    expect(flow.error.value).toEqual(BAD)

    const net2 = fakeFetch({ [PREVIEW]: { body: MW_PREVIEW }, [PREFLIGHT]: { body: { ...MW, account_checked: true, current_plan: 'free' } } })
    const flow2 = makeFlow(net2).flow
    await flow2.preview(CODE)
    await flow2.preflight({ mode: 'session', session: SESSION_RAW })
    expect(flow2.error.value).toEqual(BAD)
    expect(flow2.screen.value).toBe('credential')
  })

  it('核对账号只在第 ②、③ 屏执行', async () => {
    const { flow, net } = await toConfirm()
    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    expect(flow.screen.value).toBe('progress')
    await flow.preflight({ mode: 'session', session: SESSION_RAW })
    expect(net.count(PREFLIGHT)).toBe(1)
    expect(flow.screen.value).toBe('progress')
  })

  it('confirmView 和开通的判断一致：当前套餐已满足算不能用', async () => {
    const ok = await toConfirm()
    expect(ok.flow.confirmView.value).toBe('ok')

    const unchecked = await toConfirm({ body: { ...MW_ELIGIBLE, account_checked: false, current_plan: undefined, subscription_has_active: undefined } })
    expect(unchecked.flow.confirmView.value).toBe('self_check')

    const satisfied = await toConfirm({ body: { ...MW_ELIGIBLE, current_plan: 'plus', subscription_has_active: true } })
    expect(satisfied.flow.blockingCode.value).toBe('')
    expect(satisfied.flow.confirmView.value).toBe('blocked')
    satisfied.net.reply(REDEEM, { body: MW_QUEUED })
    await satisfied.flow.redeem()
    expect(satisfied.net.count(REDEEM)).toBe(0)
  })

  it('restore 比对卡密时去掉首尾空白、不区分大小写', async () => {
    const { storage } = await toConfirm()
    const flow2 = useRedeemFlow({ fetchFn: fakeFetch().fn, storage, now: () => NOW, every: manualEvery().every, deviceId: 'd' })
    expect(flow2.restore('  mw-plus-aaaa-bbbb ')).toBe(true)
    expect(flow2.screen.value).toBe('confirm')
  })

  it('进第 ④ 屏后缓存里不再有预检票', async () => {
    const { flow, net, storage } = await toConfirm()
    expect(storage.getItem(KEY)).toContain('pf_1')
    net.reply(REDEEM, { body: MW_QUEUED })
    await flow.redeem()
    expect(flow.screen.value).toBe('progress')
    expect(storage.getItem(KEY)).not.toContain('pf_1')
  })
})
