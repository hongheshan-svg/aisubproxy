import { describe, expect, it } from 'vitest'
import zh from '../../i18n/locales/zh'
import en from '../../i18n/locales/en'
import { GUIDE_CODES } from '../aftersale'

function flat(o: unknown, prefix = ''): string[] {
  if (o && typeof o === 'object') {
    return Object.entries(o as Record<string, unknown>).flatMap(([k, v]) => flat(v, prefix ? `${prefix}.${k}` : k))
  }
  return [prefix]
}

describe('aftersale 语言包', () => {
  const zk = flat((zh as any).aftersale).sort()
  const ek = flat((en as any).aftersale).sort()

  it('中英文键一致且非空', () => {
    expect(zk.length).toBeGreaterThan(0)
    expect(zk).toEqual(ek)
  })

  it('包含 11 个 guide 编号', () => {
    for (const g of GUIDE_CODES) expect(zk).toContain(`guide.${g}`)
  })

  it('所有文案是非空字符串，且不含 vue-i18n 特殊字符', () => {
    const walk = (o: any): string[] => Object.values(o).flatMap((v) => (typeof v === 'string' ? [v] : walk(v)))
    for (const t of [...walk((zh as any).aftersale), ...walk((en as any).aftersale)]) {
      expect(t.trim()).not.toBe('')
      expect(t).not.toMatch(/[@|{}]/)
    }
  })
})
