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

// 真实页头要引入语言/明暗切换按钮（碰 document），换成替身，页头本身用 importActual 取回
vi.mock('../../components/LanguageToggle.vue', () => ({ default: defineComponent({ setup: () => () => h('i', { class: 'lang-stub' }) }) }))
vi.mock('../../components/ThemeToggle.vue', () => ({ default: defineComponent({ setup: () => () => h('i', { class: 'theme-stub' }) }) }))

// 查询模块引入全局 i18n（加载时碰 document），首页渲染用不到它，换成替身
vi.mock('../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import HomeView from '../HomeView.vue'
import { isDesktop } from '../../lib/layoutMode'
import CodeStatusCard from '../../components/redeem/CodeStatusCard.vue'
import CodeEntry from '../../components/redeem/CodeEntry.vue'
import ProgressTicks from '../../components/redeem/ProgressTicks.vue'
import SessionHelp from '../../components/SessionHelp.vue'
import AfterSaleResult from '../../components/AfterSaleResult.vue'
import { lookupAftersale } from '../../lib/aftersaleClient'
import { batchMessage, csvRow } from '../../lib/aftersale'

const SESSION_TEXT = '先在浏览器中登录需要操作的 ChatGPT 账号，再打开 chatgpt.com/api/auth/session，复制完整 JSON。登录信息包含账号凭据，请仅在你确认可信的对应服务页使用，并按页面提示核对目标账号。'
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
  it('只有一个卡密输入框和「继续」，大标题和说明来自新文案', async () => {
    const html = await render(HomeView)
    const s = text(html)
    expect((html.match(/<input/g) || []).length).toBe(1)
    expect(html).toContain('data-testid="code-input"')
    expect(s).toContain('把卡密粘贴到这里')
    expect(s).toContain('继续')
    expect(s).toContain('卡密在哪？')
  })

  it('没有旧的入口卡片、快速三步、「兑换后怎么查」和独立的售后查询区块', async () => {
    const html = await render(HomeView)
    for (const id of ['quick-steps', 'query-guide', 'aftersale-lookup', 'aftersale-batch-link']) expect(html).not.toContain(`data-testid="${id}"`)
    expect(html).not.toContain('svc-card')
    expect(html).not.toContain('href="/recharge"')
  })

  it('页脚三个链接：常见问题（页内）、批量兑换、账单查询', async () => {
    const html = await render(HomeView)
    expect(html).toContain('href="#help"')
    expect(html).toContain('href="/batch"')
    expect(html).toContain('href="/billing"')
    const s = text(html)
    for (const w of ['常见问题', '批量兑换', '账单查询']) expect(s).toContain(w)
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

describe('首页：电脑两栏与窄屏顺序', () => {
  const aside = (html: string) => html.slice(html.indexOf('<aside'), html.indexOf('</aside>'))

  it('电脑布局：有步骤条且第 1 格是当前步；常见问题只出现一次且在 aside 里', async () => {
    isDesktop.value = true
    try {
      const html = await render(HomeView)
      expect(html).toContain('data-testid="progress-ticks"')
      expect(html).toMatch(/<li[^>]*aria-current="step"/)
      expect(html.match(/<li[^>]*aria-current="step"/g)!.length).toBe(1)
      expect(html.indexOf('aria-current="step"')).toBeLessThan(html.indexOf('data-testid="progress-ticks"') + 400)
      expect((html.match(/id="help"/g) || []).length).toBe(1)
      expect(html).toContain('<aside')
      expect(aside(html)).toContain('id="help"')
    } finally { isDesktop.value = false }
  })

  it('窄屏：没有步骤条；常见问题只出现一次，排在「卡密在哪」之后', async () => {
    isDesktop.value = false
    const html = await render(HomeView)
    expect(html).not.toContain('data-testid="progress-ticks"')
    expect((html.match(/id="help"/g) || []).length).toBe(1)
    expect(html.indexOf('id="help"')).toBeGreaterThan(html.indexOf('data-testid="code-where"'))
  })
})

describe('售后查询结果展示', () => {
  const base = { cdk_code: 'SXC-AAAA', used: false }

  it('已使用：显示打码邮箱和开通时间，不显示到期时间', async () => {
    const html = await render(AfterSaleResult, 'zh', {
      result: { ...base, status: 'used', used: true, plan: 'Plus', account_email: 'a***@x.com', used_at: '2026-11-04T09:22:37Z', expires_at: '2026-12-04T09:22:37Z' },
    })
    const s = text(html)
    expect(s).toContain('a***@x.com')
    expect(s).toContain('2026-11-04 17:22:37')
    expect(s).not.toContain('到期时间')
    expect(s).not.toContain('2026-12-04')
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

  it('服务商失败没有原因原文：指引不说「原因如下」，下面也没有空的原因行', async () => {
    const html = await render(AfterSaleResult, 'zh', {
      result: { ...base, status: 'failed', can_resubmit: true, guide: 'provider_retry' },
    })
    expect(text(html)).not.toContain('如下')
    expect(html).not.toContain('data-testid="aftersale-note"')
    const en = text(await render(AfterSaleResult, 'en', { result: { ...base, status: 'failed', guide: 'provider_wait' } }))
    expect(en).not.toMatch(/below/i)
    expect(en).not.toMatch(/\baftersale\./)
  })

  it('换码链停在又换过码的码上：不显示那张码，没有复制、去兑换按钮', async () => {
    const html = await render(AfterSaleResult, 'zh', {
      result: { ...base, status: 'replaced', replacement: { code: 'SXC-VOID2', status: 'replaced', depth: 1 } },
    })
    expect(html).not.toContain('SXC-VOID2')
    expect(html).not.toContain('data-testid="copy-replacement"')
    expect(html).not.toContain('data-testid="redeem-replacement"')
    expect(text(html)).toContain('新码也已换发')
  })

  it('英文界面不露出文案键', async () => {
    const html = await render(AfterSaleResult, 'en', {
      result: { ...base, status: 'used', used: true, plan: 'Plus', plan_kind: 'subscription', renewal: 'off', account_email: 'a***@x.com' },
    })
    expect(text(html)).not.toMatch(/\baftersale\./)
    expect(text(html)).toContain('Auto-renewal is off')
  })
})

describe('首页新组件', () => {
  const base = { cdk_code: 'SXC-AAAA', used: false }

  it('首页页面上没有用户端禁用词（中英文）', async () => {
    const banned = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token', 'preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']
    for (const loc of ['zh', 'en'] as const) {
      const s = text(await render(HomeView, loc)).toLowerCase()
      for (const w of banned) expect(s).not.toContain(w.toLowerCase())
    }
  })

  it('结果卡 home：沿用 afterSaleView 标题，有「换一张卡密」，转人工没有复制卡密', async () => {
    const html = await render(CodeStatusCard, 'zh', { context: 'home', result: { ...base, status: 'failed', can_resubmit: false } })
    const s = text(html)
    expect(s).toContain('已转人工核对')
    expect(s).toContain('换一张卡密')
    expect(html).not.toContain('data-testid="copy-code"')
  })

  it('结果卡 result：可重提换标题和按钮；attemptsLeft 为 1 才加次数提醒', async () => {
    const result = { ...base, status: 'failed', can_resubmit: true, guide: 'provider_retry' }
    const one = await render(CodeStatusCard, 'zh', { context: 'result', result, attemptsLeft: 1 })
    expect(text(one)).toContain('没开通成功，没有扣款，这张码还能用')
    expect(text(one)).toContain('换个账号再试')
    expect(text(one)).toContain('如果付款后再失败一次，这张码会转人工处理。')
    const two = await render(CodeStatusCard, 'zh', { context: 'result', result, attemptsLeft: 2 })
    expect(text(two)).not.toContain('再失败一次')
  })

  it('结果卡 result：转人工换标题，有说明和「复制卡密」', async () => {
    const html = await render(CodeStatusCard, 'zh', { context: 'result', result: { ...base, status: 'failed', can_resubmit: false } })
    const s = text(html)
    expect(s).toContain('没开通成功，已转人工处理')
    expect(s).toContain('请联系卖家，把卡密发给他')
    expect(html).toContain('data-testid="copy-code"')
  })

  it('输入框忙时用 readonly 不用 disabled（不丢焦点）', async () => {
    const html = await render(CodeEntry, 'zh', { modelValue: 'A', busy: true, error: '' })
    const input = html.match(/<input[^>]*>/)![0]
    expect(input).toContain('readonly')
    expect(input).not.toContain('disabled')
  })

  it('顶部三格：认出码前叫核对卡密，认出后用套餐名，完成后叫已开通', async () => {
    const a = text(await render(ProgressTicks, 'zh', { codeLabel: null, current: 1, done: false }))
    expect(a).toContain('核对卡密')
    expect(a).toContain('确认账号')
    expect(a).toContain('开通')
    const b = await render(ProgressTicks, 'zh', { codeLabel: 'Plus 码', current: 3, done: true })
    expect(text(b)).toContain('Plus 码')
    expect(text(b)).toContain('已开通')
    expect((b.match(/tick-check/g) || []).length).toBe(3)
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

  it('404 一律用语言包的找不到文案，不用后端的中文 message（英文界面不露中文）', async () => {
    expect((await lookupAftersale('x', msgs, json(404, { error: '未找到该卡密记录', message: '未找到该卡密。请确认输入完整卡密。' }))).error).toBe('找不到')
    expect((await lookupAftersale('x', { notFound: 'CDK not found', network: 'Network error' }, json(404, { message: '未找到该卡密。请确认输入完整卡密。' }))).error).toBe('CDK not found')
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

describe('用户端页头', () => {
  async function header(props: Record<string, unknown> = {}) {
    const Real = (await vi.importActual<any>('../../components/UserPageHeader.vue')).default
    return render(Real, 'zh', { title: '标题', ...props })
  }

  it('有 logoVersion 时渲染 Logo 图片，没有时不渲染 img', async () => {
    const { siteBrand } = await import('../../theme')
    const old = siteBrand.value
    try {
      siteBrand.value = { ...old, logoVersion: 'v123' }
      expect(await header()).toContain('<img src="/api/v1/public/site/logo?v=v123"')
      siteBrand.value = { ...old, logoVersion: '' }
      expect(await header()).not.toContain('<img')
    } finally {
      siteBrand.value = old
    }
  })

  it('back 为真时有返回首页链接，默认没有', async () => {
    const withBack = await header({ back: true })
    expect(withBack).toContain('href="/"')
    expect(text(withBack)).toContain('返回首页')
    expect(text(await header())).not.toContain('返回首页')
  })

  it('页头不含皮肤选择（皮肤选择只在后台，这条防回退）', async () => {
    const html = await header({ back: true })
    expect(html).not.toMatch(/skin-picker|SkinPicker|data-skin-option/i)
  })
})

describe('用户端页脚', () => {
  it('三个链接：常见问题、批量兑换、账单查询；首页内用 #help，其他页用 /#help', async () => {
    const UserFooter = (await import('../../components/redeem/UserFooter.vue')).default
    const other = await render(UserFooter)
    expect(other).toContain('href="/#help"')
    expect(other).toContain('href="/batch"')
    expect(other).toContain('href="/billing"')
    expect(text(other)).toContain('常见问题')
    const home = await render(UserFooter, 'zh', { home: true })
    expect(home).toContain('href="#help"')
  })
})
