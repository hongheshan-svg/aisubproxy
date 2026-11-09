// 首页查询：连点只发一次请求；去首尾空白；出错给买家能看懂的文字。
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'

let useCodeLookup: typeof import('../useCodeLookup').useCodeLookup
let i18n: typeof import('../../i18n').i18n
beforeAll(async () => {
  await import('vue') // 先加载 vue（它加载时需要真的 document），再放替身
  vi.stubGlobal('document', { documentElement: {} })
  i18n = (await import('../../i18n')).i18n
  useCodeLookup = (await import('../useCodeLookup')).useCodeLookup
})
afterAll(() => { vi.unstubAllGlobals() })

const res = (status: number, body: unknown) => ({ ok: status >= 200 && status < 300, status, json: async () => body }) as Response

describe('useCodeLookup', () => {
  it('连调两次 submit（回车加点按钮、慢网连点）只请求一次', async () => {
    let release!: (r: Response) => void
    const f = vi.fn(() => new Promise<Response>((ok) => { release = ok }))
    const l = useCodeLookup({ fetchFn: f as any })
    const p1 = l.submit('SXC-AAAA')
    const p2 = l.submit('SXC-AAAA')
    expect(l.busy.value).toBe(true)
    await p2
    expect(f).toHaveBeenCalledTimes(1)
    release(res(200, { cdk_code: 'SXC-AAAA', status: 'used', used: true }))
    await p1
    expect(f).toHaveBeenCalledTimes(1)
    expect(l.busy.value).toBe(false)
    expect(l.result.value?.status).toBe('used')
    // 结束后可以再查
    f.mockImplementation(async () => res(200, { cdk_code: 'SXC-AAAA', status: 'used', used: true }))
    await l.submit('SXC-AAAA')
    expect(f).toHaveBeenCalledTimes(2)
  })

  it('去掉首尾空白再查；空输入不请求', async () => {
    const f = vi.fn(async () => res(200, { cdk_code: 'x y', status: 'unused', used: false }))
    const l = useCodeLookup({ fetchFn: f as any })
    await l.submit('  x y \n')
    expect(f).toHaveBeenCalledWith('/api/v1/lookup/cdk?code=x%20y')
    f.mockClear()
    await l.submit('   ')
    expect(f).not.toHaveBeenCalled()
    expect(l.busy.value).toBe(false)
  })

  it('网络错误：error 有值、result 为 null，busy 复位', async () => {
    const l = useCodeLookup({ fetchFn: (async () => { throw new Error('boom') }) as any })
    await l.submit('SXC-AAAA')
    expect(l.error.value).toBe(i18n.global.t('redeemUi.errors.homeNetwork'))
    expect(l.result.value).toBeNull()
    expect(l.busy.value).toBe(false)
  })

  it('404 显示查不到；400 显示格式不对', async () => {
    const l = useCodeLookup({ fetchFn: (async () => res(404, { message: '未找到' })) as any })
    await l.submit('SXC-AAAA')
    expect(l.error.value).toBe(i18n.global.t('redeemUi.errors.homeNotFound'))
    const l2 = useCodeLookup({ fetchFn: (async () => res(400, { error: 'bad' })) as any })
    await l2.submit('!!')
    expect(l2.error.value).toBe(i18n.global.t('redeemUi.errors.homeFormat'))
  })

  it.each([
    [429, 'rateLimited'],
    [500, 'homeNetwork'],
    [503, 'homeNetwork'],
    [403, 'badReply'],
  ])('HTTP %i 显示语言包文字，不用后端返回的中文', async (status, key) => {
    const l = useCodeLookup({ fetchFn: (async () => res(status, { error: '后端中文错误', message: '后端中文消息' })) as any })
    await l.submit('SXC-AAAA')
    expect(l.error.value).toBe(i18n.global.t(`redeemUi.errors.${key}`))
    expect(l.error.value).not.toContain('后端')
    expect(l.result.value).toBeNull()
  })

  it('请求挂起时 clear()，旧请求结束后不会把结果写回，也能马上再查', async () => {
    let release!: (r: Response) => void
    const f = vi.fn(() => new Promise<Response>((ok) => { release = ok }))
    const l = useCodeLookup({ fetchFn: f as any })
    const p = l.submit('SXC-AAAA')
    l.clear()
    expect(l.busy.value).toBe(false)
    release(res(200, { cdk_code: 'SXC-AAAA', status: 'used', used: true }))
    await p
    expect(l.result.value).toBeNull()
    expect(l.error.value).toBe('')
    expect(l.busy.value).toBe(false)
  })

  it('查第二张码时，发出请求的同时旧结果已清空', async () => {
    const f = vi.fn(async () => res(200, { cdk_code: 'A', status: 'used', used: true }))
    const l = useCodeLookup({ fetchFn: f as any })
    await l.submit('A')
    expect(l.result.value).not.toBeNull()
    let release!: (r: Response) => void
    f.mockImplementation((() => new Promise<Response>((ok) => { release = ok })) as any)
    const p = l.submit('B')
    expect(l.result.value).toBeNull()
    release(res(200, { cdk_code: 'B', status: 'unused', used: false }))
    await p
    expect(l.result.value?.cdk_code).toBe('B')
  })

  it('clear 清空结果和错误', async () => {
    const l = useCodeLookup({ fetchFn: (async () => res(200, { cdk_code: 'A', status: 'used', used: true })) as any })
    await l.submit('A')
    l.clear()
    expect(l.result.value).toBeNull()
    expect(l.error.value).toBe('')
  })
})
