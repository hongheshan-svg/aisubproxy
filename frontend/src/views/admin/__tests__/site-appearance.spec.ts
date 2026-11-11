// 后台「外观与品牌」页：Logo、皮肤。本机没有 DOM 环境，用 vue/server-renderer 渲染真实组件并断言 HTML。
import { readFileSync } from 'node:fs'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

vi.mock('../../../lib/api', () => ({ authFetch: async () => ({ ok: false, status: 404, json: async () => ({}) }) }))

import { installElement } from '../../../element'
import { setSiteBrand } from '../../../theme'
import SiteAppearance from '../SiteAppearance.vue'

async function render() {
  const app = createSSRApp(defineComponent({ render: () => h(SiteAppearance) }))
  installElement(app)
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

beforeEach(() => { setSiteBrand({ logoVersion: '' }) })

describe('后台外观页', () => {
  it('皮肤标签和页面说明用新文案，没有主色一项', async () => {
    const html = await render()
    expect(html).toContain('站点名称、副标题、Logo、默认亮暗模式和皮肤。保存后用户端、后台和代理商后台一起生效；没保存的皮肤只是预览，刷新或离开本页会还原。')
    expect(html).toContain('皮肤（用户端和后台一起换）')
    expect(html).not.toContain('只影响后台页面')
    expect(html).not.toContain('主色')
    expect(html).not.toContain('aria-pressed')
  })

  it('亮暗三项都在', async () => {
    const html = await render()
    for (const s of ['亮色', '暗色', '跟随系统']) expect(html).toContain(s)
  })

  it('有 logo_version 时显示预览和「删除 Logo」', async () => {
    setSiteBrand({ logoVersion: 'abc123' })
    const html = await render()
    expect(html).toContain('删除 Logo')
    expect(html).toContain('src="/api/v1/public/site/logo?v=abc123"')
  })

  it('logo_version 为空时没有「删除 Logo」和预览图', async () => {
    setSiteBrand({ logoVersion: '' })
    const html = await render()
    expect(html).not.toContain('删除 Logo')
    expect(html).not.toContain('/api/v1/public/site/logo')
  })

  it('文件输入限定 PNG、JPEG、WebP', async () => {
    const html = await render()
    expect(html).toContain('accept="image/png,image/jpeg,image/webp"')
  })
})

describe('SiteAppearance.vue 源文件', () => {
  const src = readFileSync(new URL('../SiteAppearance.vue', import.meta.url), 'utf8')
  it('不再含主色相关标识', () => {
    for (const w of ['primary_color', 'PRESET_COLORS', 'checkPrimary']) expect(src).not.toContain(w)
  })
  it('保存用 buildSavePayload', () => {
    expect(src).toContain('buildSavePayload(')
  })
  it('离开页面时仍还原已保存皮肤', () => {
    expect(src).toMatch(/onBeforeUnmount\(\(\) => \{\s*if \(dirty\.value\) setSkin\(savedSkin\.value\)/)
  })
})
