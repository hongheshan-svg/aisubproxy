// F1-10：删掉的无人引用导出不要再冒出来
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const read = (p: string) => readFileSync(fileURLToPath(new URL(p, import.meta.url)), 'utf8')

describe('无人引用的导出已删除', () => {
  it('mwredeem.ts 的导出只剩在用的 5 个', () => {
    const names = [...read('../mwredeem.ts').matchAll(/^export\s+(?:async\s+)?(?:function|const|type|interface|class)\s+(\w+)/gm)].map((m) => m[1]).sort()
    expect(names).toEqual(['isCardSupply', 'isCreditCode', 'isOneMonthCode', 'redeemRejectedBeforeOrder', 'withCreditTerms'])
  })
  it('其余死导出', () => {
    expect(read('../features.ts')).not.toContain('isAgentDisabled')
    expect(read('../payprobe.ts')).not.toContain('probeOrderChecks')
    expect(read('../provider.ts')).not.toMatch(/\bProviderId\b|\beffectiveProvider\b/)
    expect(read('../agentAdmin.ts')).not.toMatch(/export const (adjustPendings|adjustInflight|adjustFinish)\b/)
    expect(read('../../router/routes.ts')).not.toContain('requiresAuth')
    expect(read('../../router/index.ts')).not.toContain("'/auth/login'")
  })
})
