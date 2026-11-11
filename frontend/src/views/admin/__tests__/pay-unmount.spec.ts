// 自动付款页：进页面后很快离开，不能留下定时器（F2-06）。
// 用 headless 挂载真正执行 onMounted / onUnmounted：/config 还没返回就卸载，之后返回也不能再建 30 秒的熔断轮询。
// 详情抽屉的 5 秒轮询要先打开抽屉才会排，headless 下点不到，按源码断言接线。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { createMemoryHistory, createRouter } from 'vue-router'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

const calls: string[] = []
let releaseConfig: (() => void) | null = null
vi.mock('../../../lib/api', () => ({
  authFetch: async (url: string) => {
    calls.push(String(url))
    if (String(url).startsWith('/api/v1/admin/pay/config')) await new Promise<void>((r) => { releaseConfig = r })
    return { ok: false, status: 404, json: async () => ({ error: 'nf' }) }
  },
}))

import MarsWallexPay from '../MarsWallexPay.vue'
import { mountHeadless, settle } from './headless'

const src = readFileSync(fileURLToPath(new URL('../MarsWallexPay.vue', import.meta.url)), 'utf8')

/** 取 `function name(...) {` 的函数体（按花括号配对）。 */
function body(name: string): string {
  const m = new RegExp(`function\\s+${name}\\s*\\([^)]*\\)[^{]*\\{`).exec(src)
  if (!m) throw new Error(`找不到函数 ${name}`)
  let depth = 1
  let i = m.index + m[0].length
  const start = i
  for (; i < src.length && depth > 0; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}') depth--
  }
  return src.slice(start, i - 1)
}

afterEach(() => { vi.restoreAllMocks() })

describe('自动付款页离开后不留定时器（F2-06）', () => {
  it('/config 返回前就离开页面：之后不再建熔断轮询，也不再请求 /breaker', async () => {
    calls.length = 0
    releaseConfig = null
    const intervals = vi.spyOn(globalThis, 'setInterval')
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { template: '<div />' } }] })
    router.push('/ops/marswallex-pay')
    await router.isReady()
    const unmount = mountHeadless(MarsWallexPay, (app) => app.use(router))
    await settle()
    expect(releaseConfig, '挂载后应在等 /config').not.toBeNull()
    unmount()
    releaseConfig!()
    await settle()
    expect(intervals.mock.calls.filter((c) => c[1] === 30000)).toHaveLength(0)
    expect(calls.some((u) => u.includes('/pay/breaker'))).toBe(false)
  })

  it('详情轮询：schedulePoll 第一句就看 unmounted，卸载时先置真', () => {
    expect(body('schedulePoll').trim().split('\n')[0].trim()).toBe('if (unmounted) return')
    const m = src.match(/onUnmounted\(\(\) => \{([\s\S]*?)\n\}\)/)
    expect(m, 'onUnmounted 要写成多行回调').not.toBeNull()
    expect(m![1].trim().split('\n')[0].trim()).toBe('unmounted = true')
  })
})
