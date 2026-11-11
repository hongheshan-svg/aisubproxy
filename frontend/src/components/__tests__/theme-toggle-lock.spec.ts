// F1-04：只有暗色的皮肤下，明暗切换按钮不渲染（SSR 渲染真实组件）；其他皮肤照常渲染；
// 后台和代理商侧栏里包按钮的容器也一起不渲染，免得留下空位。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { createSSRApp, defineComponent, h } from 'vue'
import { renderToString } from 'vue/server-renderer'

function fakes(skin: string) {
  vi.stubGlobal('localStorage', { getItem: (k: string) => (k === 'site-skin' ? skin : null), setItem() {}, removeItem() {} })
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false, addEventListener() {} }) })
  vi.stubGlobal('document', {
    documentElement: { classList: { toggle() {} }, setAttribute() {}, style: { setProperty() {} } },
    head: { appendChild() {} }, createElement: () => ({ dataset: {} }), querySelector: () => null,
  })
}
beforeEach(() => { vi.resetModules() })
afterEach(() => { vi.unstubAllGlobals() })

async function renderToggle() {
  const { default: ThemeToggle } = await import('../ThemeToggle.vue')
  return renderToString(createSSRApp(defineComponent({ render: () => h('div', [h(ThemeToggle)]) })))
}

describe('ThemeToggle 渲染', () => {
  it('赛博、余烬、黑白：不渲染按钮', async () => {
    for (const skin of ['cyber', 'ember', 'noir']) {
      vi.resetModules(); fakes(skin)
      expect(await renderToggle(), skin).not.toContain('<button')
    }
  })
  it('其他皮肤：渲染按钮', async () => {
    fakes('forest')
    expect(await renderToggle()).toContain('<button')
  })
})

describe('侧栏容器', () => {
  it('后台和代理商侧栏的 .sn-tools 容器随按钮一起不渲染', () => {
    for (const f of ['../../layouts/AdminLayout.vue', '../../layouts/AgentLayout.vue']) {
      const src = readFileSync(fileURLToPath(new URL(f, import.meta.url)), 'utf8')
      expect(src, f).toMatch(/<div v-if="!themeLocked" class="sn-tools"><ThemeToggle \/><\/div>/)
    }
  })
})
