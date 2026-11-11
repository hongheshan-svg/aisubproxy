// 后台几处读取和保存在断网、超时时也要有提示（F2-07）。
// authFetch 直接 await fetch，网络异常会抛；只有 try/finally 的函数会把异常变成未处理的 Promise 拒绝，页面什么都不显示。
// 页面没有组件测试，保存按钮在 headless 下点不到，按源码断言：每个调 authFetch 的函数都有 catch，写操作的提示要说「不确定是否已生效」。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, p), 'utf8')

/** 取 `function name(...) {` 的函数体（按花括号配对）。 */
function body(src: string, name: string): string {
  const m = new RegExp(`function\\s+${name}\\s*\\([^)]*\\)[^{]*\\{`).exec(src)
  if (!m) throw new Error(`找不到函数 ${name}`)
  let depth = 1
  let i = m.index + m[0].length
  const start = i
  for (; i < src.length && depth > 0; i++) {
    if (src[i] === '{') depth++
    else if (src[i] === '}') depth--
  }
  return src.slice(start, i - 1)
}

// 语句级的 catch 块（不算 `.json().catch(() => ({}))`）
const hasCatchBlock = (b: string) => /\}\s*catch\s*(\([^)]*\)\s*)?\{/.test(b)

// 审查修复：写操作按做的事写「不确定是否已保存 / 已清除 / 已提交重新处理」，要人去核对，显示 8 秒；
// 读取类写明是哪一步失败（同文字的提示会合并，进页面时配置和事件同时失败只会弹一条）。
const SAVED = '网络错误，不确定是否已保存，请刷新页面核对'
const files: Record<string, { path: string; read: string[]; write: Record<string, string> }> = {
  MarsWallexIntegration: {
    path: '../MarsWallexIntegration.vue',
    read: ['loadConfig', 'checkEgress', 'runTest', 'loadEvents'],
    write: { save: SAVED, reprocess: '网络错误，不确定是否已提交重新处理，请刷新事件列表核对' },
  },
  TelegramAlerts: { path: '../TelegramAlerts.vue', read: ['load'], write: { save: SAVED, clearConfig: '网络错误，不确定是否已清除，请刷新页面核对' } },
  SiteAppearance: { path: '../SiteAppearance.vue', read: [], write: { saveBrand: SAVED } },
  AgentSwapSettings: { path: '../components/AgentSwapSettings.vue', read: ['load'], write: { save: SAVED } },
}

/** 读取类的网络异常提示：写明是哪一步 */
const readText: Record<string, string> = {
  'MarsWallexIntegration.loadConfig': '网络错误，读取配置失败',
  'MarsWallexIntegration.checkEgress': '网络错误，出口 IP 没有检测',
  'MarsWallexIntegration.runTest': '网络错误，没连上本站后台，测试没有做',
  'MarsWallexIntegration.loadEvents': '网络错误，读取 Webhook 事件失败',
}

describe('网络异常有提示（F2-07）', () => {
  for (const [name, f] of Object.entries(files)) {
    const src = read(f.path)
    for (const fn of [...f.read, ...Object.keys(f.write)]) {
      it(`${name}.${fn} 有 catch`, () => {
        const b = body(src, fn)
        expect(b).toContain('authFetch(')
        expect(hasCatchBlock(b), `${name}.${fn} 缺 catch`).toBe(true)
      })
    }
    for (const [fn, text] of Object.entries(f.write)) {
      it(`${name}.${fn} 网络异常时说「${text}」，显示 8 秒`, () => {
        expect(body(src, fn)).toContain(`dialog.toast('${text}', 'err', 8000)`)
      })
    }
    for (const fn of f.read) {
      it(`${name}.${fn} 网络异常时提示`, () => {
        expect(body(src, fn)).toMatch(/\}\s*catch\s*\{[^}]*dialog\.toast\('[^']*', 'err'\)/)
      })
    }
  }

  for (const [key, text] of Object.entries(readText)) {
    it(`${key} 网络异常时写明是哪一步：「${text}」`, () => {
      const [name, fn] = key.split('.')
      expect(body(read(files[name].path), fn)).toContain(`dialog.toast('${text}', 'err')`)
    })
  }

  it('这几个页面没有只写「网络错误」四个字的提示', () => {
    for (const f of Object.values(files)) expect(read(f.path), f.path).not.toContain("dialog.toast('网络错误', 'err')")
  })

  it('AgentSwapSettings.load 网络异常时 loaded 置假，和接口报错时一致（不提交每日上限）', () => {
    const b = body(read('../components/AgentSwapSettings.vue'), 'load')
    const c = b.slice(b.search(/\}\s*catch\s*\{/))
    expect(c).toContain('loaded.value = false')
  })

  it('MarsWallexIntegration.generateKeypair：网络异常时说「不确定密钥是否已更换」，提示要核对公钥并到平台更新', () => {
    const b = body(read('../MarsWallexIntegration.vue'), 'generateKeypair')
    expect(hasCatchBlock(b)).toBe(true)
    expect(b).toContain("dialog.toast('网络错误，不确定密钥是否已更换。请刷新页面核对公钥；公钥变了要到 MarsWallex 平台更新', 'err', 8000)")
  })

  it('AdminLayout.submitPwd：网络异常写进对话框里的错误行', () => {
    const b = body(read('../../../layouts/AdminLayout.vue'), 'submitPwd')
    expect(hasCatchBlock(b)).toBe(true)
    expect(b).toContain("pwdError.value = '网络错误，不确定密码是否已修改，请重新登录确认'")
  })
})
