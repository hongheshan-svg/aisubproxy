// 第 ③ 屏「核对账号」：用 vue/server-renderer 渲染真实组件并断言 HTML。数据都是假的。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../../i18n/locales/zh'
import en from '../../../i18n/locales/en'

vi.mock('../../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import ConfirmStep from '../ConfirmStep.vue'

async function render(props: Record<string, unknown>, locale: 'zh' | 'en' = 'zh') {
  const app = createSSRApp(defineComponent({ render: () => h(ConfirmStep, base(props)) }))
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

const text = (html: string) => html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ')
const BANNED = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token', 'preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']

const account = (o: Record<string, unknown> = {}) => ({
  maskedEmail: 'bu***@example.com', currentPlan: 'free', subscriptionHasActive: false,
  recoveryRequired: false, delinquent: false, checked: true, ...o,
})
const mwNoSub = { plan: 'plus', provider_kind: 'card_supply', requires_no_subscription: true, auto_renew: false, kind: 'subscription' }
const mwCredit = { plan: 'credit250', provider_kind: 'card_supply', requires_subscription: true, requires_credit_terms: true, kind: 'credit', credit_quantity: 250 }

function base(o: Record<string, unknown>) {
  return {
    preview: mwNoSub, account: account(), blockingCode: '', blockingReason: '',
    extra: {}, submitState: 'idle', error: null, confirmView: 'ok', canRecover: false, ...o,
  }
}

describe('第 ③ 屏：可以开通', () => {
  it('标题、账号、将开通、续费、不能撤回、按钮', async () => {
    const html = await render({})
    const t = text(html)
    expect(t).toContain('这个账号可以开通')
    expect(t).toContain('bu***@example.com')
    expect(t).toContain('Plus 1 个月')
    expect(t).toContain('开通后帮你关掉')
    expect(t).toContain('点下面按钮后就开始付款开通，不能撤回。')
    expect(t).toContain('确认开通 Plus')
    expect(t).toContain('换一个账号')
    expect(html).not.toMatch(/data-testid="confirm-submit"[^>]*disabled/)
  })
  it('英文界面不含中文和禁用词', async () => {
    const t = text(await render({ account: account({ currentPlan: 'plus' }) }, 'en'))
    expect(t).toContain('This account can be activated')
    expect(t).not.toMatch(/[一-鿿]/)
    for (const w of BANNED) expect(t.toLowerCase()).not.toContain(w.toLowerCase())
  })
  it('点数码：点数一行和须知，须知没勾时按钮禁用', async () => {
    const html = await render({ preview: mwCredit })
    const t = text(html)
    expect(t).toContain('点数：250')
    expect(t).toContain('点数兑换须知')
    expect(t).toContain('点数 12 个月过期，不退款')
    expect(html).toMatch(/data-testid="confirm-submit"[^>]*disabled/)
  })
  it('当前套餐拿不到时显示暂时查不到', async () => {
    const t = text(await render({ account: account({ currentPlan: '' }) }))
    expect(t).toContain('暂时查不到')
  })
})

describe('第 ③ 屏：不能用', () => {
  it('already_subscribed：带当前套餐，没有开通按钮', async () => {
    const html = await render({ confirmView: 'blocked', blockingCode: 'already_subscribed', account: account({ currentPlan: 'plus' }) })
    const t = text(html)
    expect(t).toContain('这个账号不能用这张码')
    expect(t).toContain('这个账号已经有订阅（Plus）')
    expect(html).not.toContain('confirm-submit')
    expect(t).toContain('换一个账号')
  })
  it('credit_currency_mismatch：出现两个币种', async () => {
    const t = text(await render({ confirmView: 'blocked', blockingCode: 'credit_currency_mismatch', extra: { accountCurrency: 'USD', codeCurrency: 'PHP' } }))
    expect(t).toContain('USD')
    expect(t).toContain('PHP')
  })
  it('没有键时原样显示 blockingReason', async () => {
    const t = text(await render({ confirmView: 'blocked', blockingCode: 'x_unknown', blockingReason: '服务商给的原因 ABC' }))
    expect(t).toContain('服务商给的原因 ABC')
  })
  it('码和原因都空（套餐已满足）：用 already_subscribed 文案，plan 取当前套餐', async () => {
    const t = text(await render({ confirmView: 'blocked', account: account({ currentPlan: 'plus' }) }))
    expect(t).toContain('这个账号已经有订阅（Plus）')
  })
})

describe('第 ③ 屏：请自己确认', () => {
  it('订阅码：勾选框文字，开通按钮 disabled', async () => {
    const html = await render({ confirmView: 'self_check', account: account({ checked: null }) })
    const t = text(html)
    expect(t).toContain('请确认这个账号')
    expect(t).toContain('我确认这个账号现在没有订阅')
    expect(t).toContain('这张码只能用在没有订阅的账号上')
    expect(html).toMatch(/data-testid="confirm-submit"[^>]*disabled/)
  })
  it('点数码：勾选框是已订阅 Plus 或 Pro', async () => {
    const t = text(await render({ preview: mwCredit, confirmView: 'self_check', account: null }))
    expect(t).toContain('我确认这个账号已订阅 Plus 或 Pro')
  })
})

describe('第 ③ 屏：要先恢复订阅', () => {
  it('能恢复：恢复订阅按钮和换一个账号，没有开通按钮', async () => {
    const html = await render({ confirmView: 'recover', canRecover: true })
    const t = text(html)
    expect(t).toContain('这个账号要先恢复订阅')
    expect(t).toContain('恢复订阅')
    expect(html).toContain('data-testid="confirm-recover"')
    expect(html).not.toContain('data-testid="confirm-recheck"')
    expect(html).not.toContain('confirm-submit')
    expect(t).toContain('换一个账号')
  })
  it('不能恢复：重新核对按钮', async () => {
    const html = await render({ confirmView: 'recover', canRecover: false })
    expect(html).toContain('data-testid="confirm-recheck"')
    expect(text(html)).toContain('重新核对')
    expect(html).not.toContain('data-testid="confirm-recover"')
    expect(text(html)).toContain('换一个账号')
  })
})

describe('第 ③ 屏：提交状态', () => {
  it('submitting：按钮是正在提交且 disabled', async () => {
    const html = await render({ submitState: 'submitting' })
    expect(text(html)).toContain('正在提交…')
    expect(html).toMatch(/data-testid="confirm-submit"[^>]*disabled/)
  })
  it('unknown：只有提示和查一下结果，没有开通按钮和换账号', async () => {
    const html = await render({ submitState: 'unknown' })
    const t = text(html)
    expect(t).toContain('没收到回复，不确定是否已提交。点下面按钮查一下')
    expect(t).toContain('查一下结果')
    expect(html).toContain('data-testid="confirm-check-result"')
    expect(html).not.toContain('confirm-submit')
    expect(t).not.toContain('换一个账号')
  })
  it('出错时显示 error.message', async () => {
    const html = await render({ error: { kind: 'network', message: '没连上，请再试一次' } })
    expect(html).toContain('role="alert"')
    expect(text(html)).toContain('没连上，请再试一次')
  })
})

describe('第 ③ 屏：英文界面整屏无中文（套餐名走语言包）', () => {
  const clean = (t: string) => {
    expect(t).not.toMatch(/[\u4e00-\u9fff]/)
    for (const w of BANNED) expect(t.toLowerCase()).not.toContain(w.toLowerCase())
  }
  it('free 账号加订阅码', async () => clean(text(await render({ account: account({ currentPlan: 'free' }) }, 'en'))))
  it('点数码 credit250', async () => clean(text(await render({ preview: mwCredit, account: account({ currentPlan: 'plus' }) }, 'en'))))
  it('already_subscribed 加 plus_half', async () =>
    clean(text(await render({ confirmView: 'blocked', blockingCode: 'already_subscribed', account: account({ currentPlan: 'plus_half' }) }, 'en'))))
  it('未知 code 且无原因', async () =>
    clean(text(await render({ confirmView: 'blocked', blockingCode: 'zzz' }, 'en'))))
})

describe('第 ③ 屏：不能用的兜底与条件行', () => {
  it('未知 code 加空 reason：用通用文案，不说「已有订阅」', async () => {
    const t = text(await render({ confirmView: 'blocked', blockingCode: 'zzz', blockingReason: '' }))
    expect(t).toContain('这个账号暂时不能用这张码，请换一个账号。')
    expect(t).not.toContain('已经有订阅')
  })
  it('两个标志都没有：不显示条件行，勾选框用通用文字', async () => {
    const html = await render({ preview: { plan: 'plus', provider_kind: 'card_supply' }, confirmView: 'self_check', account: null })
    const t = text(html)
    expect(t).toContain('我确认这就是要开通的账号')
    expect(t).not.toContain('这张码的条件')
    expect(html).toMatch(/data-testid="confirm-submit"[^>]*disabled/)
  })
  it('requires_subscription 的订阅码：条件和勾选框都按已订阅', async () => {
    const t = text(await render({ preview: { plan: 'plus', requires_subscription: true }, confirmView: 'self_check' }))
    expect(t).toContain('我确认这个账号已订阅 Plus 或 Pro')
    expect(t).toContain('这张码只能用在已订阅')
  })
  it('恢复、换账号按钮在提交中禁用', async () => {
    const html = await render({ confirmView: 'recover', canRecover: true, submitState: 'submitting' })
    expect(html).toMatch(/data-testid="confirm-recover"[^>]*disabled/)
    expect(html).toMatch(/data-testid="confirm-change"[^>]*disabled/)
  })
})
