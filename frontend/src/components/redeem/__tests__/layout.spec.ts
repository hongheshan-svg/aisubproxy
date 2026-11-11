// 用户端电脑布局零件：布局状态、导航条、两栏、共用常见问题。无 DOM，用 vue/server-renderer 渲染并断言 HTML。
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { renderToString } from 'vue/server-renderer'
import { createI18n } from 'vue-i18n'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import zh from '../../../i18n/locales/zh'
import en from '../../../i18n/locales/en'

// 语言/明暗切换按钮会碰 document，换成替身
vi.mock('../../LanguageToggle.vue', () => ({ default: defineComponent({ setup: () => () => h('i', { class: 'lang-stub' }) }) }))
vi.mock('../../ThemeToggle.vue', () => ({ default: defineComponent({ setup: () => () => h('i', { class: 'theme-stub' }) }) }))
vi.mock('../../../i18n', () => ({ i18n: { global: { t: (k: string) => k } } }))

import UserTopNav from '../UserTopNav.vue'
import FaqList from '../FaqList.vue'
import UserTwoColumn from '../UserTwoColumn.vue'
import { DESKTOP_QUERY, initLayoutMode, isDesktop } from '../../../lib/layoutMode'

const read = (p: string) => readFileSync(resolve(__dirname, '../../..', p), 'utf8')

async function render(comp: any, path = '/', props: Record<string, unknown> = {}, slots: Record<string, () => any> = {}, locale: 'zh' | 'en' = 'zh') {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { render: () => null } }] })
  router.push(path)
  await router.isReady()
  const app = createSSRApp(defineComponent({ render: () => h(comp, props, slots) }))
  app.use(router)
  app.use(createI18n({ legacy: false, globalInjection: true, locale, fallbackLocale: 'zh', messages: { zh, en } }))
  return renderToString(app)
}

// 取出带指定 href 的 <a ...> 开始标签
const anchor = (html: string, href: string) => (html.slice(html.indexOf('<nav')).match(new RegExp(`<a [^>]*href="${href}"[^>]*>`)) || [''])[0]

beforeEach(() => { isDesktop.value = false })

describe('UserTopNav', () => {
  it('电脑布局在 /batch：四个链接，批量兑换是当前页，不显示返回首页', async () => {
    isDesktop.value = true
    const html = await render(UserTopNav, '/batch', { back: true })
    for (const h of ['/', '/batch', '/billing', '/#help']) expect(html).toContain(`href="${h}"`)
    expect(html).toContain('<nav')
    expect(anchor(html, '/batch')).toContain('aria-current="page"')
    expect(anchor(html, '/billing')).not.toContain('aria-current')
    expect(html).not.toContain('返回首页')
  })

  it('电脑布局在 /recharge：兑换是当前页', async () => {
    isDesktop.value = true
    const html = await render(UserTopNav, '/recharge')
    expect(anchor(html, '/')).toContain('aria-current="page"')
    expect(anchor(html, '/batch')).not.toContain('aria-current')
  })

  it('home 时常见问题链接是 #help', async () => {
    isDesktop.value = true
    const html = await render(UserTopNav, '/', { home: true })
    expect(html).toContain('href="#help"')
    expect(html).not.toContain('href="/#help"')
  })

  it('窄屏：没有 <nav，back=true 时有返回首页', async () => {
    const html = await render(UserTopNav, '/batch', { back: true })
    expect(html).not.toContain('<nav')
    expect(html).toContain('返回首页')
    expect(await render(UserTopNav, '/batch')).not.toContain('返回首页')
  })

  it('有 Logo 时 img 带加载失败处理，站名过长截断', () => {
    const src = read('components/redeem/UserTopNav.vue')
    expect(src).toContain('@error')
    expect(src).toContain('/api/v1/public/site/logo?v=')
    expect(src).toMatch(/text-overflow: ellipsis/)
    expect(src).toMatch(/min-width: 0/)
  })
})

describe('FaqList', () => {
  it('id="help" 一次，7 个 <details', async () => {
    const html = await render(FaqList)
    expect((html.match(/id="help"/g) || []).length).toBe(1)
    expect((html.match(/<details/g) || []).length).toBe(7)
    expect(html).toContain('aria-labelledby="faq-title"')
  })
})

describe('UserTwoColumn', () => {
  it('没传 aside 就没有 <aside；wide 在两栏之后', async () => {
    const a = await render(UserTwoColumn, '/', {}, { default: () => 'MAIN' })
    expect(a).not.toContain('<aside')
    expect(a).not.toContain('u-wide')
    const b = await render(UserTwoColumn, '/', {}, { default: () => 'MAIN', aside: () => 'SIDE', wide: () => 'WIDE' })
    expect(b).toContain('<aside')
    expect(b.indexOf('MAIN')).toBeLessThan(b.indexOf('SIDE'))
    expect(b.indexOf('SIDE')).toBeLessThan(b.indexOf('WIDE'))
    expect(b.indexOf('u-wide')).toBeGreaterThan(b.indexOf('</aside>'))
  })
})

describe('initLayoutMode', () => {
  it('matches 真→true，change 事件→false，没有 matchMedia 不抛错', () => {
    let listener: ((e: { matches: boolean }) => void) | undefined
    const queries: string[] = []
    const win = {
      matchMedia: (q: string) => {
        queries.push(q)
        return { matches: true, addEventListener: (_: string, fn: any) => { listener = fn } } as unknown as MediaQueryList
      },
    }
    initLayoutMode(win)
    expect(queries).toEqual([DESKTOP_QUERY])
    expect(isDesktop.value).toBe(true)
    listener!({ matches: false })
    expect(isDesktop.value).toBe(false)
    listener!({ matches: true })
    expect(isDesktop.value).toBe(true)
    expect(() => initLayoutMode({})).not.toThrow()
  })
})

describe('断点', () => {
  it('脚本和样式用同一个断点', () => {
    expect(DESKTOP_QUERY).toBe('(min-width: 1024px)')
    for (const file of ['styles/user-theme.css', 'components/redeem/UserTwoColumn.vue']) {
      const qs = [...read(file).matchAll(/@media[^{]*min-width[^{]*/g)].map((m) => m[0].trim())
      expect(qs.length).toBeGreaterThan(0)
      for (const q of qs) expect(q).toBe(`@media ${DESKTOP_QUERY}`)
    }
  })
})

describe('文案', () => {
  it('中英文都有 redeemUi.nav.redeem 和 redeemUi.nav.label', () => {
    expect((zh as any).redeemUi.nav).toEqual({ redeem: '兑换', label: '主导航' })
    expect((en as any).redeemUi.nav).toEqual({ redeem: 'Redeem', label: 'Main navigation' })
  })
})
