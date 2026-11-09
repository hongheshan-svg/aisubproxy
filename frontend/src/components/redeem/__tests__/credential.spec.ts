// 第 ② 屏「粘贴登录信息」：用 vue/server-renderer 渲染真实组件并断言 HTML。数据都是假的。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../../i18n/locales/zh'
import en from '../../../i18n/locales/en'

// 组件可能间接引入全局 i18n（加载时碰 document），渲染用不到，换成替身
vi.mock('../../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import CredentialStep from '../CredentialStep.vue'
import DeviceGuide from '../DeviceGuide.vue'
import SessionPasteBox from '../SessionPasteBox.vue'

async function render(comp: any, locale: 'zh' | 'en', props: Record<string, unknown>) {
  const app = createSSRApp(defineComponent({ render: () => h(comp, props) }))
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

// 渲染测试里没法输入：组件 setup 完成后（created 钩子）直接给粘贴内容这个状态赋值
async function renderWithRaw(comp: any, props: Record<string, unknown>, raw: string) {
  const app = createSSRApp(defineComponent({ render: () => h(comp, props) }))
  app.mixin({ created() { if ((this as any).$.type === comp) (this as any).$.setupState.raw = raw } })
  app.use(createI18n({ legacy: false, globalInjection: true, locale: 'zh', fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

const text = (html: string) => html.replace(/<[^>]*>/g, ' ')
const BANNED = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token', 'preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']

const mwNoSub = { plan: 'plus', provider_kind: 'card_supply', requires_no_subscription: true }
const mwSub = { plan: 'plus', provider_kind: 'card_supply', requires_subscription: true }
const grok = { plan: 'premium_monthly', product: 'grok' }
const xcode = { plan: 'premium_monthly', product: 'x' }
const claude = { plan: 'max_5x', product: 'claude' }
const props = (preview: any, extra: Record<string, unknown> = {}) => ({ preview, busy: false, error: null, ...extra })

describe('第 ② 屏：ChatGPT 码', () => {
  it('没有订阅的条件提醒、三个设备标签、登录信息页链接；没有邮箱密码入口', async () => {
    const html = await render(CredentialStep, 'zh', props(mwNoSub))
    expect(html).toContain('这张码只能用在没有订阅的账号上')
    for (const tab of ['iPhone', '安卓', '电脑']) expect(html).toContain(tab)
    expect(html).toContain('href="https://chatgpt.com/api/auth/session"')
    expect(html).toContain('rel="noopener noreferrer"')
    expect(html).not.toContain('改用邮箱和密码')
  })
  it('订阅码显示另一条条件提醒', async () => {
    const html = await render(CredentialStep, 'zh', props(mwSub))
    expect(html).toContain('已订阅 Plus 或 Pro 的个人账号')
  })
  it('空输入时下一步带 disabled', async () => {
    const html = await render(CredentialStep, 'zh', props(mwNoSub))
    expect(html).toMatch(/data-testid="credential-next"[^>]*disabled/)
  })
  it('粘贴 eyJ 三段式时下一步带 disabled，并提示只是其中一段', async () => {
    const html = await renderWithRaw(CredentialStep, props(mwNoSub), 'eyJhbGciOi.eyJzdWIiOi.c2ln')
    expect(html).toMatch(/data-testid="credential-next"[^>]*disabled/)
    expect(html).toContain('只是其中一段')
  })
  it('粘贴合格的登录信息时下一步可点', async () => {
    const sample = JSON.stringify({ user: { email: 'bun@example.com' }, sessionToken: 'fake-token', expires: new Date(Date.now() + 86400000).toISOString() })
    const html = await renderWithRaw(CredentialStep, props(mwNoSub), sample)
    expect(html).not.toMatch(/data-testid="credential-next"[^>]*disabled/)
    expect(html).toContain('认出账号')
  })
  it('英文界面没有中文和禁用词', async () => {
    const html = text(await render(CredentialStep, 'en', props(mwNoSub, { error: { kind: 'network', message: 'No connection' } })))
    expect(html).not.toMatch(/[一-鿿]/)
    for (const w of BANNED) expect(html.toLowerCase()).not.toContain(w.toLowerCase())
  })
  it('中文界面没有禁用词', async () => {
    const html = text(await render(CredentialStep, 'zh', props(mwNoSub)))
    for (const w of BANNED) expect(html.toLowerCase()).not.toContain(w.toLowerCase())
  })
})

describe('第 ② 屏：busy 与出错', () => {
  it('busy 时按钮是「正在核对账号…」且带 disabled', async () => {
    const html = await render(CredentialStep, 'zh', props(mwNoSub, { busy: true }))
    expect(html).toMatch(/data-testid="credential-next"[^>]*disabled[^>]*>[\s\S]*正在核对账号…/)
  })
  it('出错时直接显示流程给的文字', async () => {
    const html = await render(CredentialStep, 'zh', props(mwNoSub, { error: { kind: 'network', message: '没连上，请再试一次' } }))
    expect(html).toMatch(/class="u-error" role="alert"[^>]*>[^<]*没连上，请再试一次/)
  })
})

describe('第 ② 屏：其他码种', () => {
  it('Grok、Claude、X 码没有设备教程和登录信息页链接', async () => {
    for (const p of [grok, claude, xcode]) {
      const html = await render(CredentialStep, 'zh', props(p))
      expect(html).not.toContain('iPhone')
      expect(html).not.toContain('chatgpt.com/api/auth/session')
    }
  })
  it('Claude 码有两次 Organization ID 和免费账号确认', async () => {
    const html = await render(CredentialStep, 'zh', props(claude))
    expect(html).toContain('data-testid="claude-org"')
    expect(html).toContain('data-testid="claude-org-confirm"')
    expect(html).toContain('data-testid="claude-free"')
  })
  it('Grok、X 码有各自的输入框', async () => {
    expect(await render(CredentialStep, 'zh', props(grok))).toContain('sso=')
    expect(await render(CredentialStep, 'zh', props(xcode))).toContain('auth_token')
  })
})

describe('DeviceGuide', () => {
  it('initial 决定默认显示哪个设备的步骤', async () => {
    const html = await render(DeviceGuide, 'zh', { initial: 'ios' })
    expect(html).toContain('用 Safari 登录')
    expect(html).toContain('登录信息相当于账号钥匙')
  })
})
