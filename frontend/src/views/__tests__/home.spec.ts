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
import AfterSaleResult from '../../components/AfterSaleResult.vue'
import { lookupAftersale } from '../../lib/aftersaleClient'
import { batchMessage, csvRow } from '../../lib/aftersale'

const SESSION_TEXT = '先在浏览器中登录需要操作的 ChatGPT 账号，再打开 chatgpt.com/api/auth/session，复制完整 JSON。Session 包含账号凭据，请仅在你确认可信的对应服务页使用，并按页面提示核对目标账号。'
const CLAUDE_TEXT = '填写目标组织的 Organization ID，格式为 UUID。两次输入必须一致，并确认目标账号目前为免费账号。该流程使用 Organization ID。'

async function render(comp: any, locale: 'zh' | 'en' = 'zh', props: Record<string, unknown> = {}) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push('/')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp, props) }))
  app.use(router)
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

// 逐字符跳过 <...>（含 Vue SSR 的 <!--[--> 注释）取正文。Vue 已把正文和属性里的 < > 转义，不会误删正文。
function text(html: string) {
  let out = ''
  let inTag = false
  for (const ch of html) {
    if (ch === '<') inTag = true
    else if (ch === '>') inTag = false
    else if (!inTag) out += ch
  }
  return out.replace(/\s+/g, ' ')
}

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

describe('首页售后查询', () => {
  const base = { cdk_code: 'SXC-AAAA', used: false }

  it('首页有售后查询区块，并链接到批量查询', async () => {
    const html = await render(HomeView)
    expect(html).toContain('data-testid="aftersale-lookup"')
    expect(html).toContain('data-testid="aftersale-batch-link"')
    expect(html).toContain('href="/history"')
    expect(text(html)).toContain('售后查询')
  })

  it('已使用：显示打码邮箱和到期时间', async () => {
    const html = await render(AfterSaleResult, 'zh', {
      result: { ...base, status: 'used', used: true, plan: 'Plus', account_email: 'a***@x.com', used_at: '2026-11-04T09:22:37Z', expires_at: '2026-12-04T09:22:37Z' },
    })
    const s = text(html)
    expect(s).toContain('a***@x.com')
    expect(s).toContain('到期时间')
    expect(s).toContain('2026-12-04 17:22:37')
  })

  it('已换发：显示新码、复制按钮；新码未使用时有去兑换按钮', async () => {
    const html = await render(AfterSaleResult, 'zh', {
      result: { ...base, status: 'replaced', replacement: { code: 'SXC-NEW1', status: 'unused', depth: 1 } },
    })
    expect(text(html)).toContain('SXC-NEW1')
    expect(html).toContain('data-testid="copy-replacement"')
    expect(html).toContain('data-testid="redeem-replacement"')
  })

  it('失败指引后紧跟服务商原因，原因按文本输出不当成 HTML', async () => {
    const html = await render(AfterSaleResult, 'zh', {
      result: { ...base, status: 'unused', can_resubmit: true, guide: 'provider_retry', provider_note: '<img src=x onerror=alert(1)>' },
    })
    expect(html).not.toContain('<img')
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt;')
    expect(text(html)).toContain('服务商返回的原因如下')
  })

  it('英文界面不露出文案键', async () => {
    const html = await render(AfterSaleResult, 'en', {
      result: { ...base, status: 'used', used: true, plan: 'Plus', plan_kind: 'subscription', renewal: 'off', account_email: 'a***@x.com' },
    })
    expect(text(html)).not.toMatch(/\baftersale\./)
    expect(text(html)).toContain('Auto-renewal is off')
  })
})

describe('首页售后查询的请求', () => {
  const msgs = { notFound: '找不到', network: '网络错误' }
  const json = (status: number, body: unknown) => async () => ({ ok: status >= 200 && status < 300, status, json: async () => body }) as Response

  it('空输入不发请求', async () => {
    const f = vi.fn()
    expect(await lookupAftersale('  ', msgs, f as any)).toEqual({ error: '' })
    expect(f).not.toHaveBeenCalled()
  })

  it('200 返回结果，码做 URL 编码', async () => {
    const f = vi.fn(json(200, { cdk_code: 'a b', status: 'used', used: true }))
    const r = await lookupAftersale(' a b ', msgs, f as any)
    expect(f).toHaveBeenCalledWith('/api/v1/lookup/cdk?code=a%20b')
    expect(r.result?.status).toBe('used')
  })

  it('404 用响应里的 message，没有就用默认文案', async () => {
    expect((await lookupAftersale('x', msgs, json(404, { message: '没有这张卡密' }))).error).toBe('没有这张卡密')
    expect((await lookupAftersale('x', msgs, json(404, {}))).error).toBe('找不到')
  })

  it('其他非 2xx 显示响应里的 error 或 message，没有就用网络错误', async () => {
    expect((await lookupAftersale('x', msgs, json(429, { error: '太频繁了' }))).error).toBe('太频繁了')
    expect((await lookupAftersale('x', msgs, json(400, { message: '参数错' }))).error).toBe('参数错')
    expect((await lookupAftersale('x', msgs, json(500, {}))).error).toBe('网络错误')
  })

  it('fetch 抛错显示网络错误', async () => {
    const f = async () => { throw new Error('boom') }
    expect((await lookupAftersale('x', msgs, f as any)).error).toBe('网络错误')
  })
})

describe('批量结果的失败原因', () => {
  const row = { cdk_code: 'C1', status: 'failed', used: false, message: '使用失败', provider_note: 'bad, "quoted"' }

  it('有 provider_note 时 message 追加原因，没有时原样', () => {
    expect(batchMessage(row)).toBe('使用失败（原因：bad, "quoted"）')
    expect(batchMessage({ ...row, provider_note: undefined })).toBe('使用失败')
    expect(batchMessage({ cdk_code: 'C2', status: 'failed', used: false, provider_note: '原因X' })).toBe('（原因：原因X）')
  })

  it('CSV 行列不变，message 列带原因，引号转义', () => {
    expect(csvRow(row)).toBe('"C1","failed","0","","","","使用失败（原因：bad, ""quoted""）"')
  })
})
