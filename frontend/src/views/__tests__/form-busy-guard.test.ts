// F2-14：输入框绑了回车的提交函数，开头必须先看「正在请求」，否则连按回车会绕过按钮的禁用状态并发多个请求。
// 测试环境没有 DOM、点不了回车，这里按源码断言函数体第一句是这个判断。
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(resolve(__dirname, '../..', p), 'utf8')

/** 取函数体里第一条语句（到第一个换行为止），支持 `async function name()` 和 `const name = async () =>` 两种写法。 */
function firstStatement(src: string, name: string): string {
  const re = new RegExp(`(?:async\\s+function\\s+${name}\\s*\\([^)]*\\)|const\\s+${name}\\s*=\\s*async\\s*\\([^)]*\\)\\s*=>)\\s*\\{\\s*\\n\\s*([^\\n]*)`)
  const m = src.match(re)
  if (!m) throw new Error(`找不到函数 ${name}`)
  return m[1].trim()
}

describe('回车不能绕过按钮禁用（F2-14）', () => {
  it('后台登录页：submit 开头判断 loading', () => {
    const src = read('views/auth/LoginView.vue')
    expect(src).toContain('@keyup.enter="submit"')
    expect(firstStatement(src, 'submit')).toBe('if (loading.value) return')
  })
  it('账单查询页：query 开头判断 loading', () => {
    const src = read('views/user/BillingCheckView.vue')
    expect(src).toContain('@keyup.enter="query"')
    expect(firstStatement(src, 'query')).toBe('if (loading.value) return')
  })
  it('卡密查询页：单张和批量查询开头都判断 querying', () => {
    const src = read('views/user/CDKStatusView.vue')
    expect(src).toContain('@keyup.enter="query"')
    expect(firstStatement(src, 'query')).toBe('if (querying.value) return')
    expect(firstStatement(src, 'queryBatch')).toBe('if (querying.value) return')
  })
  it('批量兑换页：开始验证在验证中、提交中都不动作', () => {
    const src = read('views/user/BatchRedeemView.vue')
    expect(firstStatement(src, 'handleVerifyBatch')).toBe('if (verifying.value || submitting.value || autoSubmitting.value) return')
  })
})
