// 第 ④ 屏「开通中」、第 ⑤ 屏「结果」：用 vue/server-renderer 渲染真实组件并断言 HTML。数据都是假的。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../../i18n/locales/zh'
import en from '../../../i18n/locales/en'

// 组件可能间接引入全局 i18n（加载时碰 document），渲染用不到，换成替身
vi.mock('../../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import ProgressStep from '../ProgressStep.vue'
import ResultStep from '../ResultStep.vue'

async function render(comp: unknown, props: Record<string, unknown>, locale: 'zh' | 'en' = 'zh') {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const app = createSSRApp(defineComponent({ render: () => h(comp as any, props) }))
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

const text = (html: string) => html.replace(/<[^>]*>/g, ' ').replace(/\s+/g, ' ')
const count = (html: string, s: string) => html.split(s).length - 1
const BANNED = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token', 'preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']

const progress = (o: Record<string, unknown> = {}) => ({
  planText: 'Plus', view: { rows: ['done', 'current', 'todo'], phase: 'running' }, waitedLong: false, ...o,
})

const lookup = (o: Record<string, unknown> = {}) => ({ cdk_code: 'SXC-AAAA-BBBB', used: true, status: 'used', ...o })
const result = (o: Record<string, unknown> = {}) => ({
  planText: 'Plus', status: 'completed', kind: 'card_supply', product: 'chatgpt',
  maskedEmail: 'bu***@example.com', lookup: lookup({ renewal: 'off' }), ...o,
})

describe('第 ④ 屏：开通中', () => {
  it('三行：1 个勾、1 个转圈、1 个空心圆，当前行标 aria-current', async () => {
    const html = await render(ProgressStep, progress())
    expect(count(html, 'data-testid="prog-check"')).toBe(1)
    expect(count(html, 'data-testid="prog-spin"')).toBe(1)
    expect(count(html, 'data-testid="prog-hollow"')).toBe(1)
    expect(count(html, 'aria-current="step"')).toBe(1)
    const t = text(html)
    expect(t).toContain('正在开通 Plus')
    for (const w of ['核对账号', '付款', '确认开通']) expect(t).toContain(w)
  })
  it('白卡写「可以关掉这个页面」，没等满 10 分钟时没有 10 分钟那句', async () => {
    const html = await render(ProgressStep, progress())
    expect(text(html)).toContain('可以关掉这个页面。之后回首页粘贴同一张卡密，就能看到结果。')
    expect(text(html)).not.toContain('已经等了 10 分钟')
  })
  it('waitedLong 时出现 10 分钟那句', async () => {
    const t = text(await render(ProgressStep, progress({ waitedLong: true })))
    expect(t).toContain('已经等了 10 分钟，还在处理。可以关掉页面，稍后回首页粘贴卡密查看。')
  })
  it('全部完成：3 个勾', async () => {
    const html = await render(ProgressStep, progress({ view: { rows: ['done', 'done', 'done'], phase: 'done' } }))
    expect(count(html, 'data-testid="prog-check"')).toBe(3)
    expect(count(html, 'data-testid="prog-spin"')).toBe(0)
  })
  it('英文界面不含中文和禁用词', async () => {
    const t = text(await render(ProgressStep, progress({ waitedLong: true }), 'en'))
    expect(t).toContain('Activating Plus')
    expect(t).not.toMatch(/[一-鿿]/)
    for (const w of BANNED) expect(t.toLowerCase()).not.toContain(w.toLowerCase())
  })
})

describe('第 ⑤ 屏：成功', () => {
  it('ChatGPT 订阅码、renewal=off：大勾、标题、账号、已关闭，按钮打开 chatgpt.com', async () => {
    const html = await render(ResultStep, result())
    const t = text(html)
    expect(html).toContain('data-testid="result-success"')
    expect(t).toContain('已开通 Plus')
    expect(t).toContain('bu***@example.com')
    expect(t).toContain('自动续费')
    expect(t).toContain('已关闭')
    expect(t).toContain('回到 ChatGPT 刷新页面就能用了。看不到的话，退出再重新登录。')
    expect(t).toContain('打开 ChatGPT')
    expect(html).toContain('href="https://chatgpt.com"')
    expect(html).toContain('rel="noopener noreferrer"')
  })
  it('renewal=closing：正在关闭，不用你操作', async () => {
    const t = text(await render(ResultStep, result({ lookup: lookup({ renewal: 'closing' }) })))
    expect(t).toContain('正在关闭，不用你操作')
  })
  it('Claude 码：按钮打开 claude.ai，说明里是 Claude', async () => {
    const html = await render(ResultStep, result({ product: 'claude', planText: 'Claude Max 5x', kind: 'upstream' }))
    expect(html).toContain('href="https://claude.ai"')
    expect(text(html)).toContain('打开 Claude')
    expect(text(html)).toContain('回到 Claude 刷新页面')
    expect(text(html)).not.toContain('ChatGPT')
  })
  it('Grok、X 码：分别打开 grok.com、x.com', async () => {
    const g = await render(ResultStep, result({ product: 'grok', kind: 'upstream' }))
    expect(g).toContain('href="https://grok.com"')
    expect(text(g)).toContain('打开 Grok')
    const x = await render(ResultStep, result({ product: 'x', kind: 'upstream' }))
    expect(x).toContain('href="https://x.com"')
    expect(text(x)).toContain('打开 X')
  })
  it('查不到 renewal：不显示续费行', async () => {
    for (const l of [lookup(), null]) {
      const html = await render(ResultStep, result({ lookup: l }))
      expect(html).toContain('data-testid="result-success"')
      expect(text(html)).not.toContain('自动续费')
    }
  })
  it('点数码：有「点数：N」，没有续费行', async () => {
    const t = text(await render(ResultStep, result({ planText: 'Codex 点数 250', creditQuantity: 250, lookup: lookup({ plan_kind: 'credit', renewal: 'off' }) })))
    expect(t).toContain('已开通 Codex 点数 250')
    expect(t).toContain('点数：250')
    expect(t).not.toContain('自动续费')
  })
  it('有「换一张卡密」', async () => {
    const html = await render(ResultStep, result())
    expect(html).toContain('data-testid="result-reset"')
    expect(text(html)).toContain('换一张卡密')
  })
  it('英文界面不含中文和禁用词', async () => {
    const t = text(await render(ResultStep, result({ creditQuantity: 250 }), 'en'))
    expect(t).toContain('Plus activated')
    expect(t).toContain('Open ChatGPT')
    expect(t).not.toMatch(/[一-鿿]/)
    for (const w of BANNED) expect(t.toLowerCase()).not.toContain(w.toLowerCase())
  })
})

describe('第 ⑤ 屏：失败或转人工（结果卡和首页同一个组件）', () => {
  const failed = (o: Record<string, unknown> = {}) => lookup({ used: false, status: 'failed', ...o })

  it('可重提：标题和「换个账号再试」，没有成功屏', async () => {
    const html = await render(ResultStep, result({ status: 'failed_precharge', lookup: failed({ can_resubmit: true, guide: 'retry_later' }) }))
    const t = text(html)
    expect(t).toContain('没开通成功，没有扣款，这张码还能用')
    expect(t).toContain('换个账号再试')
    expect(html).not.toContain('data-testid="result-success"')
  })
  it('可重提且 attemptsLeft 为 1：有次数提醒', async () => {
    const t = text(await render(ResultStep, result({ status: 'failed_precharge', attemptsLeft: 1, lookup: failed({ can_resubmit: true, guide: 'retry_later' }) })))
    expect(t).toContain('如果付款后再失败一次，这张码会转人工处理。')
  })
  it('次数提醒只给本站付款的码：上游发码型即使带了 1 也不提醒', async () => {
    const t = text(await render(ResultStep, result({ kind: 'upstream', status: 'failed', attemptsLeft: 1, lookup: failed({ can_resubmit: true, guide: 'retry_later' }) })))
    expect(t).toContain('没开通成功，没有扣款，这张码还能用')
    expect(t).not.toContain('再失败一次')
  })
  it('attemptsLeft 为 2 时没有次数提醒', async () => {
    const t = text(await render(ResultStep, result({ status: 'failed_precharge', attemptsLeft: 2, lookup: failed({ can_resubmit: true, guide: 'retry_later' }) })))
    expect(t).not.toContain('再失败一次')
  })
  it('转人工：标题、说明和「复制卡密」', async () => {
    const html = await render(ResultStep, result({ status: 'review', lookup: failed({ can_resubmit: false }) }))
    const t = text(html)
    expect(t).toContain('没开通成功，已转人工处理')
    expect(t).toContain('请联系卖家，把卡密发给他')
    expect(html).toContain('data-testid="copy-code"')
    expect(t).toContain('复制卡密')
  })
  it('已换新码：新码和「用新码兑换」', async () => {
    const html = await render(ResultStep, result({ status: 'failed', lookup: lookup({ status: 'replaced', replacement: { code: 'SXC-NEW1', status: 'unused', depth: 1 } }) }))
    expect(text(html)).toContain('SXC-NEW1')
    expect(text(html)).toContain('用新码兑换')
  })
  it('没开通成功又查不到售后结果：显示「暂时查不到」，不显示成功屏', async () => {
    const html = await render(ResultStep, result({ status: 'failed', lookup: null }))
    expect(html).toContain('data-testid="result-unknown"')
    expect(text(html)).toContain('暂时查不到，请稍后再试')
    expect(html).not.toContain('data-testid="result-success"')
  })
})
