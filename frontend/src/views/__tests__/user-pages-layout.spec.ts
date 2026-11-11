// 批量兑换、账单查询、卡密查询、代理换码：电脑布局左边操作、右边常见问题；窄屏不变。测试环境没有 DOM，用服务端渲染。
import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

// 页头会引入主题和切换语言（碰 document / localStorage），这里把宽度参数一并写进输出，方便断言
vi.mock('../../components/UserPageHeader.vue', () => ({
  default: defineComponent({
    props: ['title', 'subtitle', 'maxWidth'],
    setup: (p: { title: string; subtitle: string; maxWidth?: number }) => () =>
      h('header', { 'data-max-width': String(p.maxWidth ?? '') }, [p.title, p.subtitle]),
  }),
}))
vi.mock('../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import BatchRedeemView from '../user/BatchRedeemView.vue'
import BillingCheckView from '../user/BillingCheckView.vue'
import CDKStatusView from '../user/CDKStatusView.vue'
import AgentSwapView from '../user/AgentSwapView.vue'
import { isDesktop } from '../../lib/layoutMode'

const SRC = join(__dirname, '..', '..')

// 批量兑换页在加载时读 localStorage 取设备号；测试环境没有，给一个内存版
beforeAll(() => {
  const mem = new Map<string, string>()
  vi.stubGlobal('localStorage', {
    getItem: (k: string) => mem.get(k) ?? null,
    setItem: (k: string, v: string) => void mem.set(k, v),
    removeItem: (k: string) => void mem.delete(k),
  })
})
afterAll(() => vi.unstubAllGlobals())

async function render(comp: unknown, desktop: boolean) {
  const before = isDesktop.value
  isDesktop.value = desktop
  try {
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
    router.push('/')
    await router.isReady()
    const app = createSSRApp(defineComponent({ render: () => h(comp as never) }))
    app.use(router)
    app.use(createI18n({ legacy: false, globalInjection: true, locale: 'zh', fallbackLocale: 'zh', messages: { zh, en } }))
    return await renderToString(app)
  } finally {
    isDesktop.value = before
  }
}

const count = (html: string, s: string) => html.split(s).length - 1
const asideOf = (html: string) => html.slice(html.indexOf('<aside'), html.indexOf('</aside>'))

// 每页取一个现有的表单区标志，确认原有内容还在
const pages: Array<[string, unknown, string]> = [
  ['批量兑换', BatchRedeemView, '批量兑换'],
  ['账单查询', BillingCheckView, 'bill-modes'],
  ['卡密查询', CDKStatusView, 'lk-modes'],
  ['代理换码', AgentSwapView, 'swap-pw'],
]

describe.each(pages)('%s页', (_name, comp, marker) => {
  it('电脑布局：常见问题只出现一次且在 <aside 里，原有表单还在', async () => {
    const html = await render(comp, true)
    expect(count(html, 'id="help"')).toBe(1)
    expect(html).toContain('<aside')
    expect(asideOf(html)).toContain('id="help"')
    expect(html).toContain('u-page--split')
    expect(html).toContain(marker)
  })
  it('窄屏：没有常见问题，表单还在', async () => {
    const html = await render(comp, false)
    expect(html).not.toContain('id="help"')
    expect(html).not.toContain('<aside')
    expect(html).toContain(marker)
  })
})

describe('页头宽度', () => {
  it('批量兑换：电脑布局传 0，窄屏保持 768', async () => {
    expect(await render(BatchRedeemView, true)).toContain('data-max-width="0"')
    expect(await render(BatchRedeemView, false)).toContain('data-max-width="768"')
  })
  it('卡密查询：电脑布局传 0', async () => {
    expect(await render(CDKStatusView, true)).toContain('data-max-width="0"')
  })
})

describe('源文件', () => {
  const read = (p: string) => readFileSync(join(SRC, p), 'utf8')
  it('CDKStatusView：批量结果放在 #wide 插槽里，且电脑布局不再限宽 960', () => {
    const src = read('views/user/CDKStatusView.vue')
    expect(src).toMatch(/<template #wide>[\s\S]*?cdkLookup\.resultTitle[\s\S]*?<\/template>/)
    expect(src).toMatch(/@media \(max-width: 1023px\) \{[^}]*\.lk-wide \{ max-width: 960px; \}/)
  })
  it('BatchRedeemView：768 限宽只在窄屏生效', () => {
    const src = read('views/user/BatchRedeemView.vue')
    expect(src).toMatch(/@media \(max-width: 1023px\) \{[^}]*\.user-ui \.batch-page \{ max-width: 768px; \}/)
  })
})
