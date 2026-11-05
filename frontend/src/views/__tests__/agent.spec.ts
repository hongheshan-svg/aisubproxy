// 代理商登录页：沿用 home.spec.ts 的 SSR 渲染写法，首屏只有中文的「登录名」「密码」，没有 i18n 键。
import { describe, expect, it } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import AgentLogin from '../agent/AgentLogin.vue'
import AgentOverview from '../agent/AgentOverview.vue'
import AgentOrder from '../agent/AgentOrder.vue'
import AgentCodes from '../agent/AgentCodes.vue'
import AgentOrders from '../agent/AgentOrders.vue'
import AgentOrderDetail from '../agent/AgentOrderDetail.vue'
import AgentLedger from '../agent/AgentLedger.vue'
import AgentAccount from '../agent/AgentAccount.vue'

describe('AgentLogin', () => {
  it('首屏有登录名和密码输入框，没有英文文案键', async () => {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
    router.push('/agent/login')
    await router.isReady()
    const app = createSSRApp(defineComponent({ render: () => h(AgentLogin) }))
    app.use(router)
    const html = await renderToString(app)
    expect(html).toContain('登录名')
    expect(html).toContain('密码')
    expect(html).toContain('type="password"')
    for (const t of ['代理商登录', '登录名', '密码', '登录']) expect(html).toContain(t)
  })
})

describe('代理商后台页面', () => {
  const pages: [string, any, string[]][] = [
    ['概览', AgentOverview, ['余额', '最近订单']],
    ['下单', AgentOrder, ['数量', '备注']],
    ['我的码', AgentCodes, ['导出']],
    ['订单', AgentOrders, ['订单']],
    ['订单详情', AgentOrderDetail, ['订单详情', '返回订单列表']],
    ['流水', AgentLedger, ['导出 CSV']],
    ['账号', AgentAccount, ['当前密码', '动态码']],
  ]
  for (const [name, comp, texts] of pages) {
    it(`${name}页 SSR 渲染不报错，没有文案键`, async () => {
      const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
      router.push('/agent/orders/1')
      await router.isReady()
      const app = createSSRApp(defineComponent({ render: () => h(comp) }))
      app.use(router)
      const html = await renderToString(app)
      for (const t of texts) expect(html).toContain(t)
      expect(html).not.toMatch(/\bagent\.[a-z]/)
    })
  }
})
