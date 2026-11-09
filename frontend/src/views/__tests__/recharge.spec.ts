// 兑换页按 useRedeemFlow 的 screen 切换五屏。流程换成替身，只看页面渲染出什么；点击测不了（没有 jsdom），分支见 lib/redeemPage 的单测。数据都是假的。
import { describe, expect, it, vi } from 'vitest'
import { computed, createSSRApp, defineComponent, h, ref } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

const hoisted = vi.hoisted(() => ({ flow: null as unknown }))

// 页头会引入主题和切换语言（碰 document / localStorage），这里只关心正文
vi.mock('../../components/UserPageHeader.vue', () => ({
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  default: defineComponent({ props: ['title', 'subtitle'], setup: (p: any) => () => h('header', [p.title, p.subtitle]) }),
}))
vi.mock('../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))
vi.mock('../../composables/useRedeemFlow', () => ({ useRedeemFlow: () => hoisted.flow }))

import RechargeView from '../user/RechargeView.vue'

const noop = () => undefined
function fakeFlow(o: Record<string, unknown> = {}) {
  const s = {
    screen: 'code', code: '', previewInfo: null, account: null, blockingCode: '', blockingReason: '', extra: {},
    result: { status: '', stage: '', message: '' }, lookup: null, error: null, busy: false, submitState: 'idle',
    progressStartedAt: null, canRecover: false, confirmView: 'ok', ...o,
  }
  return {
    screen: ref(s.screen), code: ref(s.code), previewInfo: ref(s.previewInfo), account: ref(s.account),
    blockingCode: ref(s.blockingCode), blockingReason: ref(s.blockingReason), extra: ref(s.extra), result: ref(s.result),
    lookup: ref(s.lookup), error: ref(s.error), busy: ref(s.busy), submitState: ref(s.submitState),
    progressStartedAt: ref(s.progressStartedAt), canRecover: computed(() => s.canRecover), confirmView: computed(() => s.confirmView),
    preview: noop, preflight: noop, redeem: noop, checkResult: noop, refreshProgress: noop, refreshLookup: noop, recoverGrace: async () => false,
    changeAccount: noop, reset: noop, restore: () => false,
  }
}

async function render(o: Record<string, unknown>, locale: 'zh' | 'en' = 'zh') {
  hoisted.flow = fakeFlow(o)
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push('/recharge')
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(RechargeView) }))
  app.use(router)
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

const text = (html: string) => html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ')
const BANNED = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token', 'preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']
const plus = { plan: 'plus', provider_kind: 'card_supply', requires_no_subscription: true, kind: 'subscription' }
const account = { maskedEmail: 'bu***@example.com', currentPlan: 'free', subscriptionHasActive: false, recoveryRequired: false, delinquent: false, checked: true }

describe('兑换页：五屏切换', () => {
  it('第 ① 屏：卡密输入框和「继续」，顶部三格第一格叫核对卡密', async () => {
    const html = await render({})
    expect(html).toContain('data-testid="code-input"')
    expect(html).toContain('data-testid="progress-ticks"')
    expect(text(html)).toContain('核对卡密')
    expect(html).not.toContain('data-testid="code-status-card"')
  })
  it('第 ① 屏预览报码不能用：输入框下显示和首页一样的结果卡', async () => {
    const html = await render({ lookup: { cdk_code: 'SXC-USED', used: true, status: 'used', plan: 'Plus' } })
    expect(html).toContain('data-testid="code-input"')
    expect(html).toContain('data-testid="code-status-card"')
    expect(text(html)).toContain('已开通成功')
  })
  it('第 ② 屏：粘贴登录信息，第一格是「Plus 码」，有「换一张卡密」', async () => {
    const html = await render({ screen: 'credential', code: 'SXC-AAAA', previewInfo: plus })
    expect(html).toContain('data-testid="credential-next"')
    expect(text(html)).toContain('Plus 码')
    expect(html).toContain('data-testid="switch-code"')
  })
  it('第 ③ 屏空闲：核对账号，有「换一张卡密」', async () => {
    const html = await render({ screen: 'confirm', code: 'SXC-AAAA', previewInfo: plus, account })
    expect(text(html)).toContain('这个账号可以开通')
    expect(html).toContain('data-testid="switch-code"')
    expect(html).not.toContain('data-testid="switch-code-locked"')
  })
  it('第 ③ 屏不确定是否已提交：只有「查一下结果」，没有换码和换账号入口，只有一句提示', async () => {
    const html = await render({ screen: 'confirm', code: 'SXC-AAAA', previewInfo: plus, account, submitState: 'unknown', error: { kind: 'submit_unknown', message: '没收到回复' } })
    const t = text(html)
    expect(html).toContain('data-testid="confirm-check-result"')
    expect(html).not.toContain('data-testid="switch-code"')
    expect(html).not.toContain('data-testid="code-input"')
    expect(html).not.toContain('data-testid="status-reset"')
    const hint = '要换一张卡密，请回首页粘贴新的卡密。'
    expect(html).toContain('data-testid="switch-code-locked"')
    expect(t).toContain(hint)
    // 「换一张卡密」只出现在这句提示里，不是按钮
    expect(t.replace(hint, '')).not.toContain('换一张卡密')
    expect(t).not.toContain('换一个账号')
  })
  it('第 ③ 屏提交中：没有换码入口，也没有锁定提示', async () => {
    const html = await render({ screen: 'confirm', code: 'SXC-AAAA', previewInfo: plus, account, submitState: 'submitting' })
    expect(html).not.toContain('data-testid="switch-code"')
    expect(html).not.toContain('data-testid="switch-code-locked"')
  })
  it('第 ④ 屏：开通中三行，第三格是当前格', async () => {
    const html = await render({ screen: 'progress', code: 'SXC-AAAA', previewInfo: plus, result: { status: 'running', stage: 'payment', message: '' }, progressStartedAt: 1 })
    const t = text(html)
    expect(t).toContain('正在开通 Plus')
    expect(html).toContain('data-testid="prog-spin"')
    expect(html).not.toContain('data-testid="switch-code"')
  })
  it('第 ④ 屏查进度出错：显示原因和「查一下结果」', async () => {
    const html = await render({ screen: 'progress', code: 'SXC-AAAA', previewInfo: plus, result: { status: 'running', stage: 'payment', message: '' }, error: { kind: 'network', message: '没连上，请再试一次' } })
    expect(text(html)).toContain('没连上，请再试一次')
    expect(html).toContain('data-testid="page-check-result"')
  })
  it('第 ⑤ 屏成功：已开通，三格都打勾', async () => {
    const html = await render({ screen: 'result', code: 'SXC-AAAA', previewInfo: plus, account, result: { status: 'completed', stage: '', message: '' }, lookup: { cdk_code: 'SXC-AAAA', used: true, status: 'used', renewal: 'off' } })
    const t = text(html)
    expect(t).toContain('已开通 Plus')
    expect(t).toContain('bu***@example.com')
    expect(t).toContain('已关闭')
    expect((html.match(/tick-check/g) || []).length).toBe(3)
  })
  it('第 ⑤ 屏失败可重提：结果卡', async () => {
    const html = await render({ screen: 'result', code: 'SXC-AAAA', previewInfo: plus, result: { status: 'failed_precharge', stage: '', message: '', attemptsLeft: 1 }, lookup: { cdk_code: 'SXC-AAAA', used: false, status: 'failed', can_resubmit: true, guide: 'retry_later' } })
    const t = text(html)
    expect(t).toContain('没开通成功，没有扣款，这张码还能用')
    expect(t).toContain('如果付款后再失败一次，这张码会转人工处理。')
  })
  it('英文界面第 ④⑤ 屏不含中文和禁用词', async () => {
    const pages = [
      await render({ screen: 'progress', code: 'SXC-AAAA', previewInfo: plus, result: { status: 'running', stage: 'checkout', message: '' } }, 'en'),
      await render({ screen: 'result', code: 'SXC-AAAA', previewInfo: plus, account, result: { status: 'completed', stage: '', message: '' }, lookup: { cdk_code: 'SXC-AAAA', used: true, status: 'used', renewal: 'closing' } }, 'en'),
    ]
    for (const html of pages) {
      const t = text(html)
      expect(t).not.toMatch(/[一-鿿]/)
      for (const w of BANNED) expect(t.toLowerCase()).not.toContain(w.toLowerCase())
    }
  })
})
