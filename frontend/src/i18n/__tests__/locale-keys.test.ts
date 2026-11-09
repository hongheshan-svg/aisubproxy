import { afterAll, beforeAll, describe, expect, it, vi } from 'vitest'
import zh from '../locales/zh'
import en from '../locales/en'

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function keys(o: any, p = ''): string[] { return Object.entries(o).flatMap(([k, v]) => v && typeof v === 'object' ? keys(v, p + k + '.') : [p + k]) }
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function get(o: any, path: string): string { return path.split('.').reduce((a, k) => a[k], o) }

// 用户端禁用词（规格 §6.7），不区分大小写
const BANNED = ['预检', '通道', '轮询', '阶段', '上游', '卡台', 'Session JSON', 'sessionToken', 'Access Token',
  'preflight', 'upstream', 'polling', 'stage', 'channel', 'session token', 'access token']

// 全局 i18n 在加载时会碰 document，测试环境没有 DOM，先放一个最小替身
let i18n: typeof import('../index').i18n
beforeAll(async () => {
  await import('vue') // 先加载 vue（它加载时需要真的 document），再放替身
  vi.stubGlobal('document', { documentElement: {} })
  i18n = (await import('../index')).i18n
})

afterAll(() => { vi.unstubAllGlobals() })

const redeemKeys = keys(zh).filter((k) => k.startsWith('redeemUi.'))

describe('语言包', () => {
  it('中英文键完全一致', () => { expect(keys(en).sort()).toEqual(keys(zh).sort()) })
  it('redeemUi 必备的键都在', () => { for (const k of ['redeemUi.home.title', 'redeemUi.paste.expired', 'redeemUi.blocking.already_subscribed', 'redeemUi.errors.submitUnknown', 'redeemUi.result.manualTitle']) expect(keys(zh)).toContain(k) })
  it('X 只要 auth_token', () => { expect(JSON.stringify(zh.xPremium)).not.toMatch(/ct0|billing_email/); expect(JSON.stringify(en.xPremium)).not.toMatch(/ct0|billing_email/) })

  it('X 的输入提示只是 auth_token，批量提示不再指向逐张兑换', () => {
    for (const x of [zh.xPremium, en.xPremium]) {
      expect(x.placeholder).toBe('auth_token')
      expect(x.batchHint).not.toMatch(/逐张|one at a time/i)
    }
  })

  it('redeemUi 下每个键都能取出，不抛错、不为空、没有残留花括号', () => {
    expect(redeemKeys.length).toBeGreaterThan(80)
    for (const loc of ['zh', 'en'] as const) {
      const src = loc === 'zh' ? zh : en
      for (const k of redeemKeys) {
        const raw = get(src, k)
        const params: Record<string, string> = {}
        for (const m of raw.matchAll(/\{(\w+)\}/g)) params[m[1]] = 'X1'
        const out = i18n.global.t(k, params, { locale: loc })
        expect(out, `${loc} ${k}`).not.toBe('')
        expect(out, `${loc} ${k}`).not.toBe(k)
        expect(out, `${loc} ${k}`).not.toMatch(/[{}]/)
      }
    }
  })

  it('redeemUi 中英文文案都不含用户端禁用词', () => {
    for (const src of [zh, en]) {
      for (const k of redeemKeys) {
        const v = get(src, k).toLowerCase()
        for (const w of BANNED) expect(v.includes(w.toLowerCase()), `${k} 含禁用词 ${w}`).toBe(false)
      }
    }
  })

  it('中英文的占位符一致', () => {
    for (const k of redeemKeys) {
      const ph = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort()
      expect(ph(get(en, k)), k).toEqual(ph(get(zh, k)))
    }
  })
})
