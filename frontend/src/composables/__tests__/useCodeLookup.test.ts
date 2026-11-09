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

  it('再次查询先清掉上一次的结果和错误；clear 全部清空', async () => {
    const l = useCodeLookup({ fetchFn: (async () => res(200, { cdk_code: 'A', status: 'used', used: true })) as any })
    await l.submit('A')
    expect(l.result.value).not.toBeNull()
    l.clear()
    expect(l.result.value).toBeNull()
    expect(l.error.value).toBe('')
  })
})
