// 代理商登录页：沿用 home.spec.ts 的 SSR 渲染写法，首屏只有中文的「登录名」「密码」，没有 i18n 键。
import { describe, expect, it } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import AgentLogin from '../agent/AgentLogin.vue'

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
    expect(html).not.toMatch(/\b[a-z]+\.[a-z]+\.[a-zA-Z]+\b(?![^<]*>)/)
  })
})
