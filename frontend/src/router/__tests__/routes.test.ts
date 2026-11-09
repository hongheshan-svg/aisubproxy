import { describe, expect, it } from 'vitest'
import { isAdminPath } from '../routes'

describe('isAdminPath', () => {
  it('后台路径', () => {
    expect(isAdminPath('/ops')).toBe(true)
    expect(isAdminPath('/ops/cdkeys')).toBe(true)
  })
  it('非后台路径', () => {
    for (const p of ['/', '/recharge', '/opsx']) expect(isAdminPath(p)).toBe(false)
  })
})
