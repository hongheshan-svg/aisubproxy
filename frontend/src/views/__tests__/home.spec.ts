// 首页要让人一眼看懂：怎么充值、兑换后怎么查、常见问题。沿用其他页面测试的做法：用 vue/server-renderer 渲染真实组件并断言 HTML。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

// 页头会引入主题和切换语言（碰 document / localStorage），这里只关心正文
vi.mock('../../components/UserPageHeader.vue', () => ({
  default: defineComponent({ props: ['title', 'subtitle'], setup: (p: any) => () => h('header', [p.title, p.subtitle]) }),
}))

import HomeView from '../HomeView.vue'
import SessionHelp from '../../components/SessionHelp.vue'

const SESSION_TEXT = '先在浏览器中登录需要操作的 ChatGPT 账号，再打开 chatgpt.com/api/auth/session，复制完整 JSON。Session 包含账号凭据，请仅在你确认可信的对应服务页使用，并按页面提示核对目标账号。'
const CLAUDE_TEXT = '填写目标组织的 Organization ID，格式为 UUID。两次输入必须一致，并确认目标账号目前为免费账号。该流程使用 Organization ID。'

async function render(comp: any, locale: 'zh' | 'en' = 'zh') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push('/')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp) }))
  app.use(router)
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

const text = (html: string) => html.replace(/<!--.*?-->/g, '').replace(/<[^>]+>/g, '').replace(/\s+/g, ' ')

describe('Session 获取说明', () => {
  it('文字与指定说明一致，链接新开页且不带来源', async () => {
    const html = await render(SessionHelp)
    expect(text(html).trim()).toBe(SESSION_TEXT)
    expect(html).toContain('href="https://chatgpt.com/api/auth/session"')
    expect(html).toContain('rel="noopener noreferrer"')
    expect(html).toContain('target="_blank"')
  })
})

describe('首页', () => {
  it('三步充值：验证卡密、填写账号信息、确认兑换，并给出两个入口', async () => {
    const html = await render(HomeView)
    const s = text(html)
    expect(html).toContain('data-testid="quick-steps"')
    for (const w of ['验证卡密', '填写账号信息', '确认兑换']) expect(s).toContain(w)
    expect(s).toContain('ChatGPT')
    expect(s).toContain('Claude')
    expect(html).toContain('href="/recharge"')
    expect(html).toContain('href="/batch"')
  })

  it('兑换后怎么查：兑换进度、卡密查询、账单查询各有入口', async () => {
    const html = await render(HomeView)
    expect(html).toContain('data-testid="query-guide"')
    expect(html).toContain('href="/history"')
    expect(html).toContain('href="/billing"')
    expect(text(html)).toContain('原卡密')
  })

  it('常见问题：Session 与 Claude 用指定说明，可展开', async () => {
    const html = await render(HomeView)
    const s = text(html)
    expect(html).toContain('id="help"')
    expect((html.match(/<details/g) || []).length).toBeGreaterThanOrEqual(6)
    expect(s).toContain('Session 在哪里获取？')
    expect(s).toContain(SESSION_TEXT)
    expect(s).toContain('Claude 应该填写什么账号信息？')
    expect(s).toContain(CLAUDE_TEXT)
    expect(s).toContain('点击提交后没有收到明确结果，怎么办？')
    expect(s).toContain('卡密提示已经使用，还能查询吗？')
  })

  it('不提本站没有的服务', async () => {
    const s = text(await render(HomeView))
    for (const w of ['Telegram', 'Gemini', 'Duolingo', '多邻国']) expect(s).not.toContain(w)
  })

  it('英文界面每段都有译文，不露出文案键', async () => {
    const html = await render(HomeView, 'en')
    const s = text(html)
    expect(s).not.toMatch(/\bhome\.[a-z]/)
    expect(s).not.toMatch(/\bsessionHelp\.|\bclaude\.[a-z]/)
    expect(s).toContain('Organization ID')
    expect(s).toContain('chatgpt.com/api/auth/session')
    expect(s).not.toMatch(/[一-鿿]/)
  })
})
