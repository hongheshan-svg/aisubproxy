// 侧栏当前项的样式只由 SideNavShell 决定；全局样式表里不能再有 .sn-item.active，
// 否则特异性更高，会把主色底清掉（violet / rose / paper）或改掉竖线颜色（cyber）。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')

describe('侧栏当前项样式只在 SideNavShell', () => {
  it('style.css 和 skins.css 里没有含 .sn-item.active 的选择器', () => {
    expect(read('../../style.css')).not.toContain('.sn-item.active')
    expect(read('../skins.css')).not.toContain('.sn-item.active')
  })
  it('style.css 里没有没人读的 --side-bg / --side-brd 默认值', () => {
    expect(read('../../style.css')).not.toMatch(/--side-(bg|brd):/)
  })
})
