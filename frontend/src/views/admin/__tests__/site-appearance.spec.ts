// 后台「外观与品牌」页：主色、Logo。本机没有 DOM 环境，用 vue/server-renderer 渲染真实组件并断言 HTML。
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

// 取出保存按钮的开标签（属性顺序由 Element 决定，不按顺序匹配）
const saveTag = (html: string) => html.match(/<button[^>]*data-testid="save-brand"[^>]*>|<button[^>]*disabled[^>]*>/g)?.find((t) => t.includes('save-brand')) ?? ''

beforeEach(() => { setSiteBrand({ primary: '#0F6B5F', logoVersion: '' }) })

describe('后台外观页', () => {
  it('渲染 6 个预设色号和色名', async () => {
    const html = await render()
    for (const s of ['#0F6B5F', '#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F']) expect(html).toContain(s)
    expect(html).toContain('深青')
    expect(html).toContain('石墨')
  })

  it('皮肤改名为「后台皮肤」并说明只影响后台', async () => {
    const html = await render()
    expect(html).toContain('后台皮肤')
    expect(html).toContain('只影响后台页面')
    expect(html).not.toContain('整站皮肤')
  })

  it('亮暗三项都在', async () => {
    const html = await render()
    for (const s of ['亮色', '暗色', '跟随系统']) expect(html).toContain(s)
  })

  it('主色太浅时显示提示，保存按钮带 disabled', async () => {
    setSiteBrand({ primary: '#7FD4C4' })
    const html = await render()
    expect(html).toContain('这个颜色太浅')
    expect(saveTag(html)).toMatch(/\sdisabled(=|\s|>)/)
  })

  it('选深青时显示不低于 4.5 的对比度，保存按钮可点', async () => {
    setSiteBrand({ primary: '#0F6B5F' })
    const html = await render()
    const m = html.match(/白字对比度 (\d+\.\d{2}):1/)
    expect(m).not.toBeNull()
    expect(Number(m![1])).toBeGreaterThanOrEqual(4.5)
    expect(html).not.toContain('这个颜色太浅')
    expect(saveTag(html)).not.toMatch(/\sdisabled(=|\s|>)/)
  })

  it('选中的色块带 aria-pressed="true"，色块是 type=button', async () => {
    setSiteBrand({ primary: '#1E4FA8' })
    const html = await render()
    expect(html).toContain('aria-label="海蓝 #1E4FA8"')
    expect(html).toContain('aria-pressed="true"')
    expect(html.match(/aria-pressed="true"/g)).toHaveLength(1)
    expect(html).toMatch(/<button[^>]*type="button"[^>]*aria-label="深青 #0F6B5F"/)
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
