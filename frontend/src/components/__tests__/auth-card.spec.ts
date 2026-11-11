// 登录类页面的居中卡片：AuthCard 本身的结构，以及四个页面都改用它。沿用其他页面测试的写法，用 vue/server-renderer 渲染真实组件。
import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { createPinia } from 'pinia'

// 明暗切换按钮会碰 document，换成替身
vi.mock('../ThemeToggle.vue', () => ({ default: defineComponent({ setup: () => () => h('i', { class: 'theme-stub' }) }) }))

import AuthCard from '../AuthCard.vue'
import LoginView from '../../views/auth/LoginView.vue'
import RegisterView from '../../views/auth/RegisterView.vue'
import SetupView from '../../views/admin/SetupView.vue'
import AgentLogin from '../../views/agent/AgentLogin.vue'
import { siteBrand } from '../../theme'

// SetupView 加载时清 sessionStorage；Node 环境没有，给个空桩
vi.stubGlobal('sessionStorage', { getItem: () => null, setItem: () => {}, removeItem: () => {}, clear: () => {} })

async function render(comp: any, props: Record<string, unknown> = {}, slots?: any) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push('/')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp, props, slots) }))
  app.use(router)
  app.use(createPinia())
  return renderToString(app)
}

describe('AuthCard', () => {
  it('渲染 auth-card、站名、标题、副标题和插槽内容', async () => {
    siteBrand.value = { ...siteBrand.value, name: '测试站', logoVersion: '' }
    const html = await render(AuthCard, { title: '标题X', subtitle: '副标题Y' }, { default: () => h('p', '插槽内容Z') })
    expect(html).toContain('auth-card')
    expect(html).toContain('测试站')
    expect(html).toContain('标题X')
    expect(html).toContain('副标题Y')
    expect(html).toContain('插槽内容Z')
    expect(html).toContain('theme-stub')
    expect(html).not.toContain('<img')
  })

  it('logoVersion 有值时渲染 Logo 图片', async () => {
    siteBrand.value = { ...siteBrand.value, name: '测试站', logoVersion: 'v9' }
    const html = await render(AuthCard, { title: 'T' })
    expect(html).toContain('<img')
    expect(html).toContain('/api/v1/public/site/logo?v=v9')
  })

  it('wide 时卡片带加宽标记', async () => {
    const html = await render(AuthCard, { title: 'T', wide: true })
    expect(html).toContain('is-wide')
  })

  it('源文件：img 带 @error、站名省略号截断、输入框和按钮只在卡片范围内覆盖', () => {
    const src = readFileSync(resolve(__dirname, '../AuthCard.vue'), 'utf8')
    expect(src).toMatch(/<img[^>]*@error/)
    expect(src).toContain('text-overflow: ellipsis')
    expect(src).toContain('min-width: 0')
    expect(src).toContain(':deep(.input)')
    expect(src).toContain(':deep(.btn-primary)')
    expect(src).toContain('border: 3px solid var(--primary)')
    expect(src).toContain('background-image: none')
    expect(src).toContain('box-shadow: none')
  })
})

describe('四个登录类页面都用 AuthCard', () => {
  it('LoginView：有用户名和密码输入框、返回首页链接', async () => {
    const html = await render(LoginView)
    expect(html).toContain('auth-card')
    expect(html).toContain('autocomplete="username"')
    expect(html).toContain('type="password"')
    expect(html).toContain('返回首页')
    expect(html).toContain('href="/"')
  })

  it('RegisterView：仍是功能开发中和前往登录', async () => {
    const html = await render(RegisterView)
    expect(html).toContain('auth-card')
    expect(html).toContain('功能开发中')
    expect(html).toContain('前往登录')
  })

  it('SetupView：用加宽卡片，仍有 Setup Token 和管理员用户名', async () => {
    const html = await render(SetupView)
    expect(html).toContain('auth-card')
    expect(html).toContain('is-wide')
    expect(html).toContain('首次安装')
    expect(html).toContain('Setup Token')
    expect(html).toContain('管理员用户名')
  })

  it('AgentLogin：有 auth-card 和登录输入框', async () => {
    const html = await render(AgentLogin)
    expect(html).toContain('auth-card')
    expect(html).toContain('代理商登录')
    expect(html).toContain('登录名')
    expect(html).toContain('type="password"')
  })
})
