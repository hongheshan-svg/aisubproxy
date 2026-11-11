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

describe('UserPageHeader 样式（读源文件）', () => {
  const src = read('components/UserPageHeader.vue')
  const rule = (sel: string) => {
    const i = src.indexOf(sel + ' {')
    expect(i, `找不到 ${sel}`).toBeGreaterThanOrEqual(0)
    return src.slice(i, src.indexOf('}', i))
  }
  it('电脑布局的 .uh--desktop 不限宽、不加左右内边距，导航条才是整宽一条', () => {
    const r = rule('.uh--desktop')
    expect(r).not.toContain('1120')
    expect(r).not.toContain('24px')
  })
  it('电脑布局的标题区 .uh-hero 才是 1120px 居中并留 24px', () => {
    const r = rule('.uh--desktop .uh-hero')
    expect(r).toContain('max-width: 1120px')
    expect(r).toContain('margin: 0 auto')
    expect(r).toContain('24px')
  })
})

describe('FaqList 根元素不带 margin-top（两栏顶端对齐；窄屏间距由 .u-col-aside 给）', () => {
  it('.faq 没有 margin-top', () => {
    const src = read('components/redeem/FaqList.vue')
    const i = src.indexOf('.faq {')
    expect(src.slice(i, src.indexOf('}', i))).not.toContain('margin-top')
  })
  // idx 处是否还在 UserTwoColumn 的 <template #aside> 里：最近的 #aside 在最近的 <UserTwoColumn 之后，且这段里 </template> 没有多于 <template（aside 没被关掉）
  const inAside = (src: string, idx: number) => {
    const before = src.slice(0, idx)
    const a = before.lastIndexOf('#aside')
    if (a < 0 || a < before.lastIndexOf('<UserTwoColumn')) return false
    const seg = before.slice(a)
    return (seg.match(/<\/template>/g) || []).length <= (seg.match(/<template\b/g) || []).length
  }
  it('inAside 能认出已关掉的 aside', () => {
    const ok = '<UserTwoColumn><template #aside><div><FaqList /></div></template></UserTwoColumn>'
    const bad = '<UserTwoColumn><template #aside><div /></template><FaqList /></UserTwoColumn>'
    const none = '<UserTwoColumn><FaqList /></UserTwoColumn>'
    expect(inAside(ok, ok.indexOf('<FaqList'))).toBe(true)
    expect(inAside(bad, bad.indexOf('<FaqList'))).toBe(false)
    expect(inAside(none, none.indexOf('<FaqList'))).toBe(false)
  })
  it('FaqList 只用在 UserTwoColumn 的 aside 里（窄屏靠 .u-col-aside 的 32px）', () => {
    expect(read('components/redeem/UserTwoColumn.vue')).toMatch(/\.u-col-aside \{[^}]*margin-top: 32px/)
    let n = 0
    for (const f of ['views/HomeView.vue', 'views/user/AgentSwapView.vue', 'views/user/BatchRedeemView.vue', 'views/user/BillingCheckView.vue', 'views/user/CDKStatusView.vue', 'views/user/RechargeView.vue']) {
      const src = read(f)
      for (const m of src.matchAll(/<FaqList/g)) {
        n++
        expect(inAside(src, m.index!), `${f} 的 FaqList 不在 #aside 里`).toBe(true)
      }
    }
    expect(n).toBeGreaterThan(0)
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
