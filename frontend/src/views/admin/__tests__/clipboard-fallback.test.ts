// 复制都走 lib/clipboard 的 copyToClipboard（F2-09）：用 http://IP:端口 打开后台时不是安全上下文，
// navigator.clipboard 不存在，直接调它会抛异常；copyToClipboard 会退到 execCommand。
// 安装向导的初始密码只显示一次，复制失败时要明说，让管理员手动选中复制。
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const SRC = resolve(__dirname, '../../..')
const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name !== '__tests__' && name !== 'node_modules') walk(p, out)
    } else if (/\.(vue|ts)$/.test(name)) out.push(p)
  }
  return out
}

describe('复制走带回退的 copyToClipboard（F2-09）', () => {
  it('除 lib/clipboard.ts 外，源码里没有直接调 navigator.clipboard 的地方', () => {
    const hits = walk(SRC)
      .filter((p) => relative(SRC, p) !== join('lib', 'clipboard.ts'))
      .filter((p) => readFileSync(p, 'utf8').includes('navigator.clipboard'))
      .map((p) => relative(SRC, p))
    expect(hits).toEqual([])
  })

  for (const p of ['../SetupView.vue', '../MarsWallexIntegration.vue', '../components/PayProbePanel.vue']) {
    it(`${p} 引入并使用 copyToClipboard`, () => {
      const src = read(p)
      expect(src).toMatch(/import \{ copyToClipboard \} from '[./]+\/lib\/clipboard'/)
      expect(src).toContain('await copyToClipboard(')
    })
  }

  it('安装向导：复制失败时按钮写明「复制失败，请手动选中上面的密码复制」', () => {
    const src = read('../SetupView.vue')
    expect(src).toContain('const copyFailed = ref(false)')
    expect(src).toContain("{{ copied ? '已复制' : copyFailed ? '复制失败，请手动选中上面的密码复制' : '复制账号密码' }}")
  })

  it('对接页、探测记录保持原来的成功和失败提示文字', () => {
    expect(read('../MarsWallexIntegration.vue')).toMatch(/dialog\.toast\('已复制', 'ok'\)[\s\S]*dialog\.toast\('复制失败', 'err'\)/)
    expect(read('../components/PayProbePanel.vue')).toMatch(/dialog\.toast\('已复制', 'ok'\)[\s\S]*dialog\.toast\('复制失败，请手动选中文字复制', 'warn'\)/)
  })
})
