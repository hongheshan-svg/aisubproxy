// F2-11：没人引用的组件和全局类已删除。读文件断言，防止被加回来。
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const path = (p: string) => fileURLToPath(new URL(p, import.meta.url))
const css = readFileSync(path('../../style.css'), 'utf8').replace(/\/\*[\s\S]*?\*\//g, '')

describe('F2-11 没人引用的组件已删除', () => {
  it.each(['../../components/AfterSaleLookup.vue', '../../components/AfterSaleResult.vue', '../../views/admin/components/Modal.vue'])('%s 不存在', (f) => {
    expect(existsSync(path(f))).toBe(false)
  })
  it('只被已删组件用的语言键 aftersale.title / sub / batchLink 已删', () => {
    for (const l of ['zh', 'en']) {
      const src = readFileSync(path(`../../i18n/locales/${l}.ts`), 'utf8')
      const block = src.slice(src.indexOf('  aftersale: {'), src.indexOf('    state: {', src.indexOf('  aftersale: {')))
      for (const k of ['title', 'sub', 'batchLink']) expect(block, `${l}.${k}`).not.toMatch(new RegExp(`^\\s{4}${k}:`, 'm'))
    }
  })
})

describe('F2-11 style.css 没人用的全局类已删除', () => {
  it.each(['page-title', 'stat-value', 'brand-title', 'brand-text', 'tabular', 'card-hover', 'bg-surface', 'bd-2', 'divide-line', 'animate-fadeIn', 'animate-slideInUp', 'result-grid', 'result-tile'])('不含 .%s', (c) => {
    expect(css).not.toMatch(new RegExp(`\\.${c}(?![\\w-])`))
  })
  it('只被它们用的 @keyframes fadeIn / slideInUp 也删了', () => {
    expect(css).not.toMatch(/@keyframes (fadeIn|slideInUp)\b/)
  })
  it('并列选择器里还在用的项保留', () => {
    expect(css).toMatch(/\.mono, table \.cell, code \{/)
    expect(css).toMatch(/a\.card:hover/)
    expect(css).toMatch(/\.bg-soft \{/)
    expect(css).toMatch(/\.bd \{/)
  })
})
