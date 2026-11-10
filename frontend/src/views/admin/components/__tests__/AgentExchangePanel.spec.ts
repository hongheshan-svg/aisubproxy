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
import {
  DISABLING_HINT, LEGACY_DISABLING_HINT, SWAP_STATE_LABEL,
  disablingHint, issuedConfirmText, newCodeIdLabel,
} from '../../../../lib/provider'
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
  // 历史记录 = 已移除的 ZovoCard 留下的（old_cdk_id > 0），本站码记录 old_cdk_id 为 0
  const legacy = { id: 7, state: 'disabling', old_cdk_id: 12, old_code_prefix: 'ZV-abc' }
  const local = { id: 8, state: 'disabling', old_cdk_id: 0, old_code_prefix: 'MW-xyz' }

  it('历史记录的 disabling 提示是 LEGACY_DISABLING_HINT，不含「每 10 分钟」', () => {
    expect(disablingHint(legacy)).toBe(LEGACY_DISABLING_HINT)
    expect(LEGACY_DISABLING_HINT).toBe('这是已移除的 ZovoCard 留下的记录，系统不会再处理。旧码在 ZovoCard 那边可能仍能兑换，请到 ZovoCard 后台手动作废旧码。')
    expect(LEGACY_DISABLING_HINT).not.toContain('每 10 分钟')
  })

  it('本站码记录的 disabling 提示是新的 DISABLING_HINT', () => {
    expect(disablingHint(local)).toBe(DISABLING_HINT)
    expect(DISABLING_HINT).toBe('新码已发给用户，记录还没收尾。系统每 10 分钟自动补上记账，不用处理。')
  })

  it('定论 issued 的确认框：历史记录要人工作废，本站码仍由系统处理', () => {
    const a = issuedConfirmText(legacy, 99)
    expect(a).toContain('ZovoCard 后台手动作废')
    expect(a).not.toContain('由系统继续处理')
    expect(a).toContain('#99')
    expect(a).toContain('#12 ZV-abc…')
    const b = issuedConfirmText(local, 99)
    expect(b).toContain('由系统继续处理')
    expect(b).not.toContain('ZovoCard')
  })

  it('新码 ID 输入框标签区分历史记录和本站码', () => {
    expect(newCodeIdLabel(legacy)).toContain('ZovoCard 那边的卡密 ID')
    expect(newCodeIdLabel(local)).toContain('本站卡密 ID')
  })

  it('模板用这几个函数，页首说明是新文案', () => {
    const src = readFileSync(fileURLToPath(new URL('../AgentExchangePanel.vue', import.meta.url)), 'utf8')
    expect(src).toContain('disablingHint(row)')
    expect(src).toContain('newCodeIdLabel(cur)')
    expect(src).toContain('issuedConfirmText(row, form.newId)')
    expect(src).toContain('旧码带 #ID 的是已移除的 ZovoCard 留下的记录，要到 ZovoCard 后台查')
  })
})
