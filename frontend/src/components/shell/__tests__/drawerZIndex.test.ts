// 窄屏抽屉的层级必须低于 Element Plus 弹出层（从 2001 起：下拉、弹窗、提示气泡），
// 否则抽屉底部的用户菜单（修改密码、退出登录）弹出来会被抽屉自己盖住，手机上点不到（v1.5.8 回归）。
// 同时要高于顶栏和页面内容，低于项目自己的确认框和提示（DialogHost）。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')
const shell = read('../SideNavShell.vue')

// 取某个选择器那一段样式里的 z-index
function zIndexOf(css: string, selector: string): number {
  const esc = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const m = css.match(new RegExp(`(?:^|\\n)${esc}\\s*\\{([^}]*)\\}`))
  if (!m) throw new Error(`没找到样式 ${selector}`)
  const z = m[1].match(/z-index:\s*(\d+)/)
  if (!z) throw new Error(`${selector} 没写 z-index`)
  return Number(z[1])
}

const EL_POPPER_START = 2000 // Element Plus 弹出层的起点，实际从 2001 起
const drawer = zIndexOf(shell, '.is-mobile .sidenav')
const mask = zIndexOf(shell, '.sn-mask')
const topbar = zIndexOf(shell, '.topbar')

describe('窄屏抽屉层级', () => {
  it('抽屉和遮罩都低于 Element Plus 弹出层', () => {
    expect(drawer).toBeLessThan(EL_POPPER_START)
    expect(mask).toBeLessThan(EL_POPPER_START)
  })
  it('遮罩在抽屉下面、顶栏上面', () => {
    expect(mask).toBeLessThan(drawer)
    expect(mask).toBeGreaterThan(topbar)
  })
  it('抽屉高于 Element Plus 的常规层（--el-index-top 是 1000）', () => {
    expect(drawer).toBeGreaterThan(1000)
  })
  it('项目自己的确认框和提示在抽屉上面', () => {
    const host = read('../../DialogHost.vue')
    const layers = [...host.matchAll(/z-\[(\d+)\]/g)].map((m) => Number(m[1]))
    expect(layers.length).toBeGreaterThan(0)
    for (const z of layers) expect(z).toBeGreaterThan(drawer)
  })
})

describe('新版本提示条层级', () => {
  const banner = read('../../NewVersionBanner.vue')
  const z = Number(banner.match(/z-index:\s*(\d+)/)?.[1])
  it('高于手机侧栏抽屉，低于 Element Plus 弹出层', () => {
    expect(z).toBeGreaterThan(drawer)
    expect(z).toBeLessThan(EL_POPPER_START)
  })
  it('低于项目自己的确认框和提示', () => {
    const host = read('../../DialogHost.vue')
    for (const m of host.matchAll(/z-\[(\d+)\]/g)) expect(Number(m[1])).toBeGreaterThan(z)
  })
})
