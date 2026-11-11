// F1-17：后台时间统一按北京时间显示（与浏览器所在时区无关）；用户端账单页保持访客本地时区。
import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { fmtBeijingAny } from '../mwwebhook'
import { fmtTs } from '../cardpool'
import { probeToText } from '../payprobe'

// 2023-11-14 22:13:20 UTC = 2023-11-15 06:13:20 北京时间
const TS = 1700000000
const BJ = '2023-11-15 06:13:20'

describe('后台时间显示固定为北京时间', () => {
  it('卡池 fmtTs', () => {
    expect(fmtTs(TS)).toBe(BJ)
    expect(fmtTs(0)).toBe('—')
    expect(fmtTs(-62135596800)).toBe('—')
  })
  it('探测记录文本里的时间', () => {
    expect(probeToText({ id: 1, created_at: TS, results: [] } as never)).toContain(BJ)
  })
  it('fmtBeijingAny：秒数、ISO 字符串、空值、坏值', () => {
    expect(fmtBeijingAny(TS)).toBe(BJ)
    expect(fmtBeijingAny('2023-11-14T22:13:20Z')).toBe(BJ)
    expect(fmtBeijingAny('2023-11-15T06:13:20+08:00')).toBe(BJ)
    // SQLite CURRENT_TIMESTAMP：UTC、不带时区标记（代理换码面板的 created_at/updated_at 就是这种）
    expect(fmtBeijingAny('2023-11-14 22:13:20')).toBe(BJ)
    expect(fmtBeijingAny('2023-11-14T22:13:20')).toBe(BJ)
    expect(fmtBeijingAny('')).toBe('—')
    expect(fmtBeijingAny(null)).toBe('—')
    expect(fmtBeijingAny('not a time')).toBe('not a time')
  })
})

describe('后台源文件不再用浏览器本地时区格式化时间', () => {
  const src = fileURLToPath(new URL('../../', import.meta.url))
  const walk = (d: string, out: string[] = []) => {
    for (const n of readdirSync(d)) {
      const p = join(d, n)
      if (statSync(p).isDirectory()) { if (n !== '__tests__') walk(p, out) } else if (/\.(vue|ts)$/.test(n)) out.push(p)
    }
    return out
  }
  const files = [
    ...walk(join(src, 'views/admin')),
    join(src, 'lib/cardpool.ts'), join(src, 'lib/payprobe.ts'),
  ]
  it('views/admin 与卡池、探测库没有 toLocaleString / getHours', () => {
    const bad = files.filter((f) => /toLocale(Date|Time)?String\(|\.getHours\(/.test(readFileSync(f, 'utf8')))
    expect(bad.map((f) => f.slice(src.length))).toEqual([])
  })
  it('用户端账单页仍按访客本地时区', () => {
    expect(readFileSync(join(src, 'lib/billingRecord.ts'), 'utf8')).toContain('toLocaleString(')
  })
})
