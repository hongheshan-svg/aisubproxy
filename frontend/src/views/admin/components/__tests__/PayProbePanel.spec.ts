// 本机没有 DOM 环境，所以用服务端渲染检查面板的静态结构；提交与结果展示的逻辑在 lib/payprobe 单测里覆盖。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { ID_INJECTION_KEY } from 'element-plus/es/hooks/use-id/index.mjs'
import { ZINDEX_INJECTION_KEY } from 'element-plus/es/hooks/use-z-index/index.mjs'
import { installElement } from '../../../../element'
import PayProbePanel from '../PayProbePanel.vue'
import { PROBE_CHECKS } from '../../../../lib/payprobe'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

async function render() {
  const app = createSSRApp(PayProbePanel)
  installElement(app)
  app.provide(ID_INJECTION_KEY, { prefix: 1024, current: 0 })
  app.provide(ZINDEX_INJECTION_KEY, { current: 0 })
  return renderToString(app)
}

describe('免付款探测面板', () => {
  it('列出全部探测项，并说明不付款、不点击', async () => {
    const html = await render()
    for (const c of PROBE_CHECKS) expect(html).toContain(c.title)
    expect(html).toContain('全部不付款')
    expect(html).toContain('不点击、不填表')
    expect(html).toContain('开始探测')
  })

  it('说明只有打开 ChatGPT 那一项截图，账单门户不截图（H2-02）', async () => {
    const html = await render()
    expect(html).not.toContain('只打开、等待、截图、读文字')
    expect(html).toContain('账单门户不截图')
  })

  it('没有会话时不能开始，并给出原因', async () => {
    const html = await render()
    expect(html).toContain('请先粘贴测试账号的会话')
    expect(html).toMatch(/<button[^>]*disabled[^>]*>[\s\S]*?开始探测/)
  })
})
