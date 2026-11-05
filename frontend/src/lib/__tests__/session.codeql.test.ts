// 安装令牌不落浏览器存储（codeql #3）：安装页只留内存；旧版本留下的 setup_token 在页面加载时清掉。
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

import { LEGACY_SETUP_KEYS, clearSetupLeftovers } from '../session'

describe('安装令牌不在 sessionStorage 里留存（codeql #3）', () => {
  it('旧版本残留的 setup_token 会被清掉，其他键保留', () => {
    expect(LEGACY_SETUP_KEYS).toContain('setup_token')
    const data: Record<string, string> = { setup_token: 'tok-123', other: 'keep' }
    clearSetupLeftovers({ removeItem: (k: string) => { delete data[k] } })
    expect(data).toEqual({ other: 'keep' })
  })

  it('安装页源码不再读写 setup_token', () => {
    const src = readFileSync(new URL('../../views/admin/SetupView.vue', import.meta.url), 'utf8')
    expect(src).not.toMatch(/(setItem|getItem)\(\s*['"]setup_token['"]/)
    expect(src).not.toMatch(/sessionStorage\.removeItem\(\s*['"]setup_token['"]/)
  })
})
