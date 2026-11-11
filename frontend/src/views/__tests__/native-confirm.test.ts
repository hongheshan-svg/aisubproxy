// 确认框统一用 lib/dialog 的危险确认框（F2-18）：跟皮肤走、有危险样式和自定义按钮文字，
// 浏览器原生的 window.confirm 没有危险样式，按钮是浏览器默认的「确定/取消」。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(__dirname, '../..')
const read = (p: string) => readFileSync(resolve(SRC, p), 'utf8')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name !== '__tests__') walk(p, out)
    } else if (/\.(vue|ts)$/.test(name)) out.push(p)
  }
  return out
}

describe('不再用浏览器原生确认框（F2-18）', () => {
  it('源码里没有 window.confirm', () => {
    const hits = walk(SRC).filter((p) => readFileSync(p, 'utf8').includes('window.confirm')).map((p) => relative(SRC, p))
    expect(hits).toEqual([])
  })

  it('代理商退出：有结果未知的订单时用危险确认框', () => {
    expect(read('layouts/AgentLayout.vue')).toContain(
      "await dialog.confirm('有一笔订单结果未知，退出后这台电脑上不能再原样重试。确定退出吗？', { title: '订单结果未知', okText: '仍然退出', danger: true })",
    )
  })

  it('一键更新：用危险确认框，返回后已离开页面或已在更新就不提交', () => {
    const src = read('views/admin/SystemUpdate.vue')
    expect(src).toMatch(/const ok = await dialog\.confirm\([\s\S]*?\{ title: '一键无痕更新', okText: '开始更新', danger: true \},\s*\)/)
    expect(src).toContain('if (!ok || unmounted || !canOneClickUpdate.value) return')
  })
})
