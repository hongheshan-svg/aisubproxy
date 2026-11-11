// 用户端配色全部取自皮肤变量；提醒类颜色按 data-tone 分亮暗。样式无法在无浏览器的测试里渲染，这里读源文件断言，SSR 用例检查外壳。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import UserShell from '../../components/redeem/UserShell.vue'

const css = readFileSync(fileURLToPath(new URL('../user-theme.css', import.meta.url)), 'utf8')

function block(selector: string): string {
  const start = css.indexOf(selector + ' {')
  expect(start, `找不到 ${selector} 块`).toBeGreaterThanOrEqual(0)
  return css.slice(start, css.indexOf('}', start))
}

describe('user-theme.css', () => {
  const light = block('.user-ui')
  const MAP: Array<[string, string]> = [
    ['--u-bg', 'var(--bg)'], ['--u-surface', 'var(--surface-solid)'], ['--u-ink', 'var(--ink)'],
    ['--u-muted', 'var(--ink-2)'], ['--u-line', 'var(--brd)'], ['--u-primary', 'var(--primary)'],
    ['--u-on-primary', 'var(--primary-on)'], ['--u-tint', 'var(--primary-soft)'],
    ['--u-radius', 'var(--radius-md)'], ['--u-radius-card', 'var(--radius-lg)'],
  ]
  it('.user-ui 把颜色和圆角映射到皮肤变量', () => {
    for (const [k, v] of MAP) expect(light).toContain(`${k}: ${v};`)
  })
  it('8 个颜色变量里没有写死的色号', () => {
    for (const [k] of MAP.slice(0, 8)) {
      const line = light.split('\n').find((l) => l.trim().startsWith(k + ':'))!
      expect(line).not.toContain('#')
    }
  })
  it('暗色提醒类颜色放在 html[data-tone="dark"] .user-ui', () => {
    const dark = block('html[data-tone="dark"] .user-ui')
    expect(dark).toContain('--u-warn-bg: #2E2410')
    expect(light).toContain('--u-warn-bg: #FBF3E4')
  })
  it('用户端标题一律继承系统字体，不跟皮肤的展示字体（规格 §3.5）', () => {
    expect(css).toMatch(/\.user-ui :is\(h1, h2, h3, h4, h5, h6\) \{ font-family: inherit; \}/)
  })
  it('不再有 prefers-color-scheme、[data-theme、自带底色', () => {
    expect(css).not.toContain('prefers-color-scheme')
    expect(css).not.toContain('[data-theme')
    expect(light).not.toContain('background: var(--u-bg)')
  })
  it('字体固定为系统字体', () => {
    expect(light).toContain('font-family: -apple-system, "PingFang SC", "Microsoft YaHei", "Noto Sans SC", sans-serif;')
  })
  it('圆角不再写死 14px', () => {
    expect(css).not.toContain('border-radius: 14px')
  })
})

describe('UserShell', () => {
  it('不带 data-theme 和 --brand-primary', async () => {
    const html = await renderToString(createSSRApp({ components: { UserShell }, template: '<UserShell><p>正文</p></UserShell>' }))
    expect(html).toContain('user-ui')
    expect(html).toContain('正文')
    expect(html).not.toContain('data-theme')
    expect(html).not.toContain('--brand-primary')
  })
})
