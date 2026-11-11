// 下单页「放弃并重新下单」（F2-18，用户选 1）：确认框换成统一的危险确认框（异步）。
// 确认框返回后重新检查一次待重试状态：等待期间已在别处处理完、别的标签页锁定了另一笔、这页又发起了重试，
// 都不放弃；放弃本身只清锁定，不发任何下单请求。
// 审查修复：弹确认框前记下这笔的请求号，确认后按请求号核对（别的标签页放弃后又下了内容相同的一笔也认得出）；
// 页面这一层「显示的还是不是这一笔」的核对单独有用例盯住。
// 用 headless 挂载真正执行 onMounted（读商品和 /me、核对身份、恢复待重试），通过 setupState 调页面里的 abandon。
import { beforeEach, describe, expect, it, vi } from 'vitest'

const env = vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = String(v) },
    removeItem: (k: string) => { delete data[k] },
    get length() { return Object.keys(data).length },
    key: (i: number) => Object.keys(data)[i] ?? null,
  }
  // 页面在 onMounted 里监听 storage 事件
  ;(globalThis as any).window = { addEventListener() {}, removeEventListener() {} }
  return { data }
})

const calls: { path: string; method: string }[] = []
vi.mock('../../lib/agentApi', () => ({
  agentApi: vi.fn(async (path: string, method = 'GET') => {
    calls.push({ path, method })
    if (path === '/products') return { ok: true, status: 200, data: { items: [{ product_code: 'gpt-plus', name: 'Plus', unit_price_cents: 1200, ready: true }] } }
    if (path === '/me') return { ok: true, status: 200, data: { username: 'alice', balance_cents: 100000 } }
    if (path.startsWith('/orders?')) return { ok: true, status: 200, data: { items: [] } }
    return { ok: false, status: 500, data: {} }
  }),
  agentErrorText: () => '出错了',
}))

let answer: (v: boolean) => void = () => {}
const confirmCalls: unknown[][] = []
vi.mock('../../lib/dialog', () => ({
  dialog: {
    confirm: vi.fn((...args: unknown[]) => { confirmCalls.push(args); return new Promise<boolean>((r) => { answer = r }) }),
    toast: vi.fn(),
  },
}))

import { ORDER_RETRY_KEY } from '../../lib/retryLock'
import { OrderRetry, orderRetry, type PendingOrder } from '../../lib/agentViews'
import AgentOrder from '../agent/AgentOrder.vue'
import { mountHeadless, settle } from '../admin/__tests__/headless'

const P: PendingOrder = { product_code: 'gpt-plus', quantity: 3, note: '客户 A', expected_unit_price_cents: 1200 }
const posts = () => calls.filter((c) => c.method === 'POST')

/** 挂载下单页，返回页面的 setupState（script setup 里的变量和函数）。 */
async function mountOrder() {
  let state: any = null
  const unmount = mountHeadless(AgentOrder, (app) => {
    // 只挂了这一个组件（headless 的 render 为空，不渲染子组件）；取它的 setupState
    app.mixin({ created() { const st = (this as any).$.setupState; if (st && 'abandon' in st) state = st } })
  })
  await settle()
  return { state, unmount }
}

/** 另一个标签页（共用同一份 localStorage）。 */
function otherTab() {
  const t = new OrderRetry(globalThis.localStorage as any)
  t.bind('alice')
  return t
}

beforeEach(() => {
  for (const k of Object.keys(env.data)) delete env.data[k]
  orderRetry.clear()
  calls.length = 0
  confirmCalls.length = 0
  // 上一次下单结果未知：锁定落盘（别的标签页写的也一样）
  otherTab().begin(P)
})

describe('放弃并重新下单（F2-18）', () => {
  it('用统一的危险确认框；确认后清掉锁定，不发下单请求', async () => {
    const { state, unmount } = await mountOrder()
    expect(state.pending).toEqual(P)
    const run = state.abandon()
    await settle()
    expect(confirmCalls).toHaveLength(1)
    expect(confirmCalls[0][0]).toBe('放弃后将用新请求号重新下单；如果上一笔其实已成功，会被重复扣款。确定放弃吗？')
    expect(confirmCalls[0][1]).toMatchObject({ danger: true, okText: '放弃并重新下单' })
    answer(true)
    await run
    expect(state.pending).toBeNull()
    expect(env.data[ORDER_RETRY_KEY]).toBeUndefined()
    expect(posts()).toHaveLength(0)
    unmount()
  })

  it('点取消：什么都不动', async () => {
    const { state, unmount } = await mountOrder()
    const run = state.abandon()
    await settle()
    answer(false)
    await run
    expect(state.pending).toEqual(P)
    expect(env.data[ORDER_RETRY_KEY]).toBeTruthy()
    unmount()
  })

  it('确认框打开期间别的标签页原样重试成功：确认后不放弃，提示已在别处处理完', async () => {
    const { state, unmount } = await mountOrder()
    const run = state.abandon()
    await settle()
    const t = otherTab()
    t.settle(t.lock.current('alice')!.key)
    answer(true)
    await run
    expect(state.pending).toBeNull()
    expect(state.notice).toContain('已在别的页面处理完')
    expect(posts()).toHaveLength(0)
    unmount()
  })

  it('确认框打开期间别的标签页放弃并新下了一笔（结果未知）：不清那一笔，改显示它', async () => {
    const { state, unmount } = await mountOrder()
    const run = state.abandon()
    await settle()
    const t = otherTab()
    t.abandon()
    const other = { ...P, quantity: 9 }
    t.begin(other)
    const stored = env.data[ORDER_RETRY_KEY]
    answer(true)
    await run
    expect(env.data[ORDER_RETRY_KEY]).toBe(stored) // 别的标签页的锁定原样保留
    expect(state.pending).toEqual(other)
    expect(state.err).toContain('有一笔订单结果未知')
    expect(posts()).toHaveLength(0)
    unmount()
  })

  it('确认框打开期间别的标签页放弃后又下了内容完全相同的一笔（结果未知）：不清那一笔的请求号，提示状态已变', async () => {
    const { state, unmount } = await mountOrder()
    const run = state.abandon()
    await settle()
    const t = otherTab()
    t.abandon()
    const k2 = t.begin({ ...P })
    const stored = env.data[ORDER_RETRY_KEY]
    answer(true)
    await run
    expect(env.data[ORDER_RETRY_KEY]).toBe(stored) // 新那笔的锁定原样保留
    expect(otherTab().begin(P)).toBe(k2) // 还能用同一个请求号原样重试
    expect(state.pending).toEqual(P)
    expect(state.err).toContain('状态在别的页面变了，这次没有放弃')
    expect(posts()).toHaveLength(0)
    unmount()
  })

  it('确认框打开期间页面上的待重试被清空、存储里仍是同一笔：页面这一层的核对拦住，不清存储', async () => {
    const { state, unmount } = await mountOrder()
    const before = env.data[ORDER_RETRY_KEY]
    const run = state.abandon()
    await settle()
    state.pending = null
    answer(true)
    await run
    expect(env.data[ORDER_RETRY_KEY]).toBe(before)
    expect(posts()).toHaveLength(0)
    unmount()
  })

  it('点放弃时存储里已经没有这笔：不弹确认框，提示已在别处处理完', async () => {
    const { state, unmount } = await mountOrder()
    const t = otherTab()
    t.settle(t.lock.current('alice')!.key)
    await state.abandon()
    expect(confirmCalls).toHaveLength(0)
    expect(state.pending).toBeNull()
    expect(state.notice).toContain('已在别的页面处理完')
    expect(posts()).toHaveLength(0)
    unmount()
  })

  it('确认框打开期间这页发起了原样重试（请求在路上）：不放弃，锁定保留', async () => {
    const { state, unmount } = await mountOrder()
    const run = state.abandon()
    await settle()
    state.busy = true
    answer(true)
    await run
    expect(env.data[ORDER_RETRY_KEY]).toBeTruthy()
    expect(state.pending).toEqual(P)
    unmount()
  })

  it('确认框打开期间页面上的待重试已被刷新掉（storage 事件）：不放弃存储里新出现的那笔', async () => {
    const { state, unmount } = await mountOrder()
    const run = state.abandon()
    await settle()
    state.pending = null // onStorage 显示「已在别处处理完」
    const t = otherTab()
    t.abandon()
    t.begin({ ...P, quantity: 7 })
    answer(true)
    await run
    expect(orderRetry.refresh()).toEqual({ ...P, quantity: 7 })
    expect(posts()).toHaveLength(0)
    unmount()
  })
})
