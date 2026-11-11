// F2-02：用户端 <main class="u-page"> 不能再带整屏高度（min-height: 100vh），内容少时页面不该出现滚动条和底部空白。
// 手机/电脑布局靠 .user-ui .u-page 的 max-width、padding、居中，这里一并锁住，防止顺手改坏。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')
const skins = read('../skins.css')
const userTheme = read('../user-theme.css')

/** 取出某个选择器（完整匹配）的所有声明块 */
function bodies(css: string, selector: string): string[] {
  return [...css.matchAll(/([^{}]+)\{([^{}]*)\}/g)]
    .filter((m) => m[1].split(',').some((s) => s.trim() === selector))
    .map((m) => m[2])
}

describe('F2-02 .u-page 不带整屏高度', () => {
  it('skins.css 里的 .u-page 没有 min-height / height: 100vh', () => {
    for (const b of bodies(skins, '.u-page')) expect(b).not.toMatch(/(min-)?height\s*:\s*100(d|s|l)?vh/)
  })
  it('所有样式文件里 .u-page 都不设整屏高度', () => {
    for (const css of [skins, userTheme, read('../../style.css')]) {
      for (const b of css.match(/[^{}]*\.u-page[^{}]*\{[^{}]*\}/g) ?? []) expect(b).not.toMatch(/min-height\s*:\s*100/)
    }
  })
  it('用户端布局规则保持：520px 单栏居中、手机 16px 边距、电脑宽页 1120px', () => {
    const b = bodies(userTheme, '.user-ui .u-page').join('\n')
    expect(b).toMatch(/max-width:\s*520px/)
    expect(b).toMatch(/margin:\s*0 auto/)
    expect(b).toMatch(/padding:\s*24px 16px/)
    expect(b).toMatch(/width:\s*100%/)
    expect(bodies(userTheme, '.user-ui .u-page--split').join('')).toMatch(/max-width:\s*1120px/)
  })
  it('外层 UserShell 仍保证至少一屏高', () => {
    const shell = readFileSync(fileURLToPath(new URL('../../components/redeem/UserShell.vue', import.meta.url)), 'utf8')
    expect(shell).toContain('min-h-screen')
  })
})
