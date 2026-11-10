// 代理换码核对面板：接口路径已从 /cardplatform/agent-exchanges 改到 /agent-exchanges，界面不再出现「卡台」。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

const calls: Array<{ url: string; method: string }> = []
vi.mock('../../../../lib/api', () => ({
  authFetch: async (url: string, init?: { method?: string }) => {
    calls.push({ url: String(url), method: init?.method || 'GET' })
    return { ok: true, status: 200, json: async () => ({ list: [] }) }
  },
}))

import { installElement } from '../../../../element'
import { DISABLING_HINT, SWAP_STATE_LABEL } from '../../../../lib/provider'
import AgentExchangePanel from '../AgentExchangePanel.vue'
import { mountHeadless, settle } from '../../__tests__/headless'

describe('AgentExchangePanel', () => {
  it('挂载后请求 /api/v1/admin/agent-exchanges，不再走 /cardplatform/ 路径', async () => {
    calls.length = 0
    const unmount = mountHeadless(AgentExchangePanel)
    await settle()
    unmount()
    expect(calls.map((c) => c.url).sort()).toEqual([
      '/api/v1/admin/agent-exchanges?state=disabling',
      '/api/v1/admin/agent-exchanges?state=unknown',
    ])
    expect(calls.some((c) => c.url.includes('cardplatform'))).toBe(false)
  })

  it('界面文字里没有「卡台」，也没有去外部手动禁用旧码的说明', async () => {
    const app = createSSRApp(AgentExchangePanel)
    installElement(app)
    app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
    app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
    const html = await renderToString(app)
    expect(html).toContain('代理换码核对')
    expect(html).not.toContain('卡台')
    expect(DISABLING_HINT).not.toContain('卡台')
    expect(DISABLING_HINT).not.toContain('手动禁用')
    expect(Object.values(SWAP_STATE_LABEL).join('')).not.toContain('卡台')
    // 定论表单在弹窗里，SSR 不渲染，直接检查源文件：不含卡台字样，也不再读 old_disabled
    const src = readFileSync(fileURLToPath(new URL('../AgentExchangePanel.vue', import.meta.url)), 'utf8')
    expect(src).not.toContain('卡台')
    expect(src).not.toContain('cardplatform')
    expect(src).not.toContain('old_disabled')
    expect(src).toContain('/agent-exchanges/')
  })
})
