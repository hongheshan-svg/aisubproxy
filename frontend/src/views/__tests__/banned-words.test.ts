// 用户端显示给买家的文字里不能出现内部用语。只查 <template> 和语言包文案，不查 <script> 里的变量名和接口字段。
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'

const SRC = join(__dirname, '..', '..')

// 中文按原样匹配
const ZH_WORDS = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token']
// 英文不区分大小写
const EN_WORDS = ['preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']

function hits(text: string): string[] {
  const lower = text.toLowerCase()
  return [
    ...ZH_WORDS.filter((w) => text.includes(w)),
    ...EN_WORDS.filter((w) => lower.includes(w.toLowerCase())),
  ]
}

function vueFiles(dir: string): string[] {
  return readdirSync(join(SRC, dir)).filter((f) => f.endsWith('.vue')).map((f) => `${dir}/${f}`)
}

const FILES = [
  'views/HomeView.vue',
  ...vueFiles('views/user'),
  ...vueFiles('components/redeem'),
  'components/UserPageHeader.vue',
  'components/SessionHelp.vue',
  'components/ExcelImportBlock.vue',
]

// 取最外层 <template>（行首的 <template> 到最后一个行首的 </template>），不含 <script>
function templateOf(src: string): string {
  const start = src.search(/^<template>/m)
  const end = src.lastIndexOf('\n</template>')
  if (start < 0 || end < 0) return ''
  return src.slice(start, end)
}

const TREES = ['redeemUi', 'home', 'batch', 'cdkLookup', 'aftersale', 'sessionHelp', 'claude', 'grok', 'xPremium', 'grace', 'hero', 'redeemTabs']

function flatten(node: unknown, path: string, out: Array<[string, string]>) {
  if (typeof node === 'string') out.push([path, node])
  else if (node && typeof node === 'object') {
    for (const [k, v] of Object.entries(node as Record<string, unknown>)) flatten(v, `${path}.${k}`, out)
  }
}

describe('买家可见文字不含内部用语', () => {
  it('模板里没有禁用词', () => {
    const found: string[] = []
    for (const f of FILES) {
      const tpl = templateOf(readFileSync(join(SRC, f), 'utf8'))
      const h = hits(tpl)
      if (h.length) found.push(`${f}: ${h.join('、')}`)
    }
    expect(found, `模板命中：\n${found.join('\n')}`).toEqual([])
  })

  for (const [name, messages] of [['zh', zh], ['en', en]] as const) {
    it(`${name} 语言包相关子树里没有禁用词`, () => {
      const entries: Array<[string, string]> = []
      for (const tree of TREES) flatten((messages as any)[tree], `${name}.${tree}`, entries)
      expect(entries.length).toBeGreaterThan(0)
      const found = entries.flatMap(([k, v]) => {
        const h = hits(v)
        return h.length ? [`${k}: ${h.join('、')}`] : []
      })
      expect(found, `语言包命中：\n${found.join('\n')}`).toEqual([])
    })
  }
})
