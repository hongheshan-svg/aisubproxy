// 代理商后台的订单、码、流水三张表在手机上曾经被挤成一字一行：备注、邮箱列用 break-all 能缩到一个字宽，
// 流水的来源列整格不换行，外部单号把表格撑得很宽。现在 < 1024px 用卡片列表，>= 1024px 用表格。
import { describe, expect, it } from 'vitest'
import { createSSRApp, defineComponent, h, type Component } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import AgentOrders from '../agent/AgentOrders.vue'
import AgentCodes from '../agent/AgentCodes.vue'
import AgentLedger from '../agent/AgentLedger.vue'

const templateOf = (name: string) => {
  const src = readFileSync(fileURLToPath(new URL(`../agent/${name}.vue`, import.meta.url)), 'utf8')
  return src.slice(0, src.indexOf('<script'))
}

const PAGES: [string, Component, string][] = [
  ['AgentOrders', AgentOrders, '暂无订单'],
  ['AgentCodes', AgentCodes, '没有码'],
  ['AgentLedger', AgentLedger, '没有流水'],
]

async function render(comp: Component) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push('/agent')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp) }))
  app.use(router)
  return renderToString(app)
}

describe.each(PAGES)('%s 布局', (name, comp, emptyText) => {
  const t = templateOf(name)

  it('手机和平板用卡片列表，电脑用表格，分界是 1024px（lg）', () => {
    expect(t).toMatch(/class="[^"]*\blg:hidden\b[^"]*" data-agent-list/)
    expect(t).toMatch(/<div class="card p-2 overflow-x-auto hidden lg:block">\s*<table/)
  })

  it('不用 break-all，自由文字列有最小宽度并只在本列折行', () => {
    expect(t).not.toContain('break-all')
    expect(t).toMatch(/<td class="p-2 min-w-\[1[02]rem\][^"]*\[overflow-wrap:anywhere\]/)
  })

  it('空状态在卡片列表和表格里都显示', async () => {
    const html = await render(comp)
    expect(html.split(emptyText).length - 1).toBe(2)
  })
})

describe('AgentCodes 手机卡片', () => {
  it('列表上方有「全选本页」，整张卡片是 label，点哪里都能勾选', () => {
    const t = templateOf('AgentCodes')
    const list = t.slice(t.indexOf('data-agent-list'), t.indexOf('hidden lg:block'))
    expect(list).toContain('@change="toggleAll" />全选本页</label>')
    expect(list).toMatch(/<label class="flex items-start[^"]*">\s*<input type="checkbox"[^>]*@change="toggle\(c\.id\)"/)
  })
})

describe('AgentLedger 来源列', () => {
  it('表格里来源名不换行，外部单号可以折行（不再整格 whitespace-nowrap）', () => {
    const t = templateOf('AgentLedger')
    const table = t.slice(t.indexOf('hidden lg:block'))
    expect(table).toContain('<span class="whitespace-nowrap">{{ sourceLabel(l.source) }}</span>')
    expect(table).toMatch(/class="opacity-70 \[overflow-wrap:anywhere\]"> \{\{ l\.external_ref \}\}/)
  })
})
