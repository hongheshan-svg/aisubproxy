// 列表请求防乱序（F2-22）：先发的请求后返回时，不能把旧条件（或上一个代理商）的数据写到页面上。
// 卡池：每个列表一个递增序号，返回后比对；旧响应也不能把新请求的「加载中」关掉。
// 代理商详情：路由参数换了（组件复用）时，上一个代理商在途的请求回来不再写；同一列表连续翻页也按序号丢旧响应。
// 页面没有组件测试，按源码断言接线。
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

describe('卡池列表防乱序（F2-22）', () => {
  const src = read('../CardPool.vue')
  for (const [fn, seq, loading] of [
    ['loadCards', 'cardsSeq', 'cardsLoading'],
    ['loadOrders', 'ordersSeq', 'ordersLoading'],
    ['loadTxns', 'txnsSeq', 'txnsLoading'],
  ]) {
    it(`${fn}：请求前取序号，返回后不是最新就丢弃，finally 里只有最新请求才关加载中`, () => {
      expect(src).toContain(`let ${seq} = 0`)
      const b = body(src, fn)
      expect(b.trim().split('\n')[0].trim()).toBe(`const my = ++${seq}`)
      const awaitAt = b.indexOf('await api')
      const checkAt = b.indexOf(`if (my !== ${seq}) return`)
      expect(awaitAt).toBeGreaterThan(-1)
      expect(checkAt).toBeGreaterThan(awaitAt)
      // 比对要在写表格和弹错之前
      expect(checkAt).toBeLessThan(b.indexOf('if (!r.ok)'))
      expect(b).toMatch(new RegExp(`finally \\{\\s*if \\(my === ${seq}\\) ${loading}\\.value = false`))
    })
  }
})

describe('代理商详情列表防乱序（F2-22）', () => {
  const src = read('../AgentDetail.vue')
  for (const fn of ['loadAgent', 'loadLedger', 'loadOrders', 'loadOverrides', 'loadCodes']) {
    it(`${fn}：返回时已换到别的代理商（或已有更新的同类请求）就不写页面`, () => {
      const b = body(src, fn)
      expect(b).toContain('const cur = id.value')
      const awaitAt = b.indexOf('await adminApi')
      const guard = /if \(my !== \w+Seq \|\| cur !== id\.value\) return/.exec(b)
      expect(guard, `${fn} 缺少返回后的比对`).not.toBeNull()
      expect(guard!.index).toBeGreaterThan(awaitAt)
      expect(guard!.index).toBeLessThan(b.indexOf('if (!r.ok)'))
      // 请求地址用发请求时的 id
      expect(b.slice(0, awaitAt + 200)).toContain('${cur}')
    })
  }
})
