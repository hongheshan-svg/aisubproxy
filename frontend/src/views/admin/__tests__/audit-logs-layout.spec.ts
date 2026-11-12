// 审计日志在手机上曾经被挤成一字一行：详情列 break-all 能缩到一个字宽，操作标签也跟着竖排。
// 现在 < 1024px 用卡片列表，>= 1024px 用表格；表格里操作标签不换行，详情列有最小宽度。
import { describe, expect, it, vi } from 'vitest'
import { createSSRApp } from 'vue'
import { renderToString } from 'vue/server-renderer'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

vi.mock('../../../lib/api', () => ({
  authFetch: async () => ({ ok: false, status: 500, json: async () => ({}) }),
}))

import AuditLogs from '../AuditLogs.vue'

const src = readFileSync(fileURLToPath(new URL('../AuditLogs.vue', import.meta.url)), 'utf8')
const template = src.slice(0, src.indexOf('<script'))

describe('审计日志布局', () => {
  it('手机和平板用卡片列表，电脑用表格，分界是 1024px（lg）', () => {
    expect(template).toMatch(/<ul class="lg:hidden" data-audit-list>/)
    expect(template).toMatch(/<div class="hidden lg:block overflow-x-auto">\s*<table class="data-table">/)
  })

  it('不用 break-all：它让列宽能缩到一个字', () => {
    expect(template).not.toContain('break-all')
  })

  it('操作标签不换行，详情列和 IP 列有最小宽度', () => {
    const tds = [...template.matchAll(/<td class="([^"]*)"/g)].map((m) => m[1])
    expect(tds.some((c) => c.includes('whitespace-nowrap') && !c.includes('text-muted'))).toBe(true)
    expect(tds.some((c) => /min-w-\[16rem\]/.test(c) && c.includes('[overflow-wrap:anywhere]'))).toBe(true)
    expect(tds.some((c) => /min-w-\[8\.5rem\]/.test(c))).toBe(true)
    // 卡片里的标签也不换行
    expect(template).toMatch(/<span :class="actionClass\(item\.action\)" class="whitespace-nowrap">/)
  })

  it('空状态在卡片列表和表格里都显示', async () => {
    const html = await renderToString(createSSRApp(AuditLogs))
    expect(html.match(/暂无审计记录/g)?.length).toBe(2)
  })
})
