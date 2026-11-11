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
  it('后台改密码弹窗：回车提交开头判断 pwdLoading（FE2 审查次要 9）', () => {
    const src = read('layouts/AdminLayout.vue')
    expect(src).toContain('@keyup.enter="submitPwd"')
    expect(firstStatement(src, 'submitPwd')).toBe('if (pwdLoading.value) return')
  })
  it('批量兑换页：开始验证在验证中、提交中都不动作', () => {
    const src = read('views/user/BatchRedeemView.vue')
    expect(firstStatement(src, 'handleVerifyBatch')).toBe('if (verifying.value || submitting.value || autoSubmitting.value) return')
  })
})

/** 取 `async function name(...) {` 的函数体（按花括号配对）。 */
function fnBody(src: string, name: string): string {
  const m = new RegExp(`async\\s+function\\s+${name}\\s*\\([^)]*\\)[^{]*\\{`).exec(src)
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

describe('批量兑换页的提交不会进两次（FE2 审查重要 3）', () => {
  const src = read('views/user/BatchRedeemView.vue')
  it('手动提交：开头先看手动、自动两个在途标志', () => {
    expect(firstStatement(src, 'handleSubmitSession')).toBe('if (!currentItem.value || submitting.value || autoSubmitting.value) return')
  })
  it('一键自动提交：开头先看自动、手动、验证三个在途标志', () => {
    // 第一次调用只同步取走前几张，剩下的还是「待凭证」；没有这一句，再进一次就会把后面的卡密再排一遍、各提交一次
    expect(firstStatement(src, 'handleAutoSubmitAll')).toBe('if (autoSubmitting.value || submitting.value || verifying.value) return')
  })
  it('「提交下一张」「一键自动提交」两个按钮在提交中、自动提交中都禁用，整页只有这两个入口（FE2 审查次要 7）', () => {
    expect(src).toMatch(/:disabled="!sessionInput\.trim\(\) \|\| submitting \|\| autoSubmitting"\s*@click="handleSubmitSession"/)
    expect(src).toMatch(/:disabled="submitting \|\| autoSubmitting"\s*@click="handleAutoSubmitAll"/)
    expect((src.match(/handleSubmitSession\b/g) || []).length).toBe(2) // 模板一处、定义一处
    expect((src.match(/handleAutoSubmitAll\b/g) || []).length).toBe(2)
  })
  it('置在途标志都在第一个 await 之前：两次调用之间没有空档', () => {
    const auto = fnBody(src, 'handleAutoSubmitAll')
    const firstAwait = auto.indexOf('await ')
    expect(firstAwait).toBeGreaterThan(0)
    // 邮箱和登录信息两条分支各置一次，都在各自的 runSubmitPool 之前
    const sets = [...auto.matchAll(/autoSubmitting\.value = true/g)].map((m) => m.index!)
    const pools = [...auto.matchAll(/await runSubmitPool\(/g)].map((m) => m.index!)
    expect(sets.length).toBe(2)
    expect(pools.length).toBe(2)
    expect(sets[0]).toBeLessThan(pools[0])
    expect(sets[1]).toBeGreaterThan(pools[0])
    expect(sets[1]).toBeLessThan(pools[1])
    const manual = fnBody(src, 'handleSubmitSession')
    const mSets = [...manual.matchAll(/submitting\.value = true/g)].map((m) => m.index!)
    const mAwaits = [...manual.matchAll(/await submitOne\(/g)].map((m) => m.index!)
    expect(mSets.length).toBe(2)
    expect(mAwaits.length).toBe(2)
    expect(mSets[0]).toBeLessThan(mAwaits[0])
    expect(mSets[1]).toBeGreaterThan(mAwaits[0])
    expect(mSets[1]).toBeLessThan(mAwaits[1])
  })
})
