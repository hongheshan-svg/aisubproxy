import { describe, expect, it } from 'vitest'
import { applySettings, buildSavePayload, checkLogoFile, LOGO_EMPTY, LOGO_MAX_BYTES } from '../siteAppearance'

describe('checkLogoFile 文件检查', () => {
  it('PNG、JPEG、WebP 且不超过 256 KB 通过', () => {
    for (const type of ['image/png', 'image/jpeg', 'image/webp']) {
      expect(checkLogoFile({ size: 1000, type })).toBe('')
    }
    expect(checkLogoFile({ size: LOGO_MAX_BYTES, type: 'image/png' })).toBe('')
  })
  it('超过 256 KB 给出提示', () => {
    expect(checkLogoFile({ size: LOGO_MAX_BYTES + 1, type: 'image/png' })).toBe('Logo 不能超过 256 KB，请换一张小一点的图片')
  })
  it('SVG 等类型不收', () => {
    expect(checkLogoFile({ size: 10, type: 'image/svg+xml' })).toContain('PNG')
  })
  it('空文件单独提示', () => {
    expect(checkLogoFile({ size: 0, type: 'image/png' })).toBe('这个文件是空的，请换一张图片')
    expect(LOGO_EMPTY).toBe('这个文件是空的，请换一张图片')
  })
})

describe('applySettings 设置转表单状态', () => {
  const cur = { brand_name: 'A', brand_sub: 'B', logoVersion: 'old' }
  it('缺 theme_mode 时不改主题', () => {
    expect(applySettings({}, cur).theme).toBeUndefined()
    expect(applySettings({ theme_mode: 'dark' }, cur).theme).toBe('dark')
  })
  it('服务端返回 primary_color 时，表单状态里没有这个键', () => {
    const r = applySettings({ primary_color: '#1E4FA8' }, cur)
    expect(Object.keys(r)).not.toContain('primary_color')
    expect(Object.keys(r)).not.toContain('serverPrimary')
  })
  it('logo_version 为空和不为空', () => {
    expect(applySettings({ logo_version: '' }, cur).logoVersion).toBe('')
    expect(applySettings({}, cur).logoVersion).toBe('')
    expect(applySettings({ logo_version: 'abc' }, cur).logoVersion).toBe('abc')
  })
  it('站名、副标题、皮肤', () => {
    const r = applySettings({ brand_name: 'X', skin: 'ink' }, cur)
    expect(r.brand_name).toBe('X')
    expect(r.brand_sub).toBe('B')
    expect(r.skin).toBe('ink')
    expect(applySettings({}, cur).skin).toBeUndefined()
  })
})

describe('buildSavePayload 保存内容', () => {
  it('键正好是 brand_name、brand_sub、skin、theme_mode', () => {
    const p = buildSavePayload({ brand_name: 'A', brand_sub: 'B', logoVersion: 'x' }, 'ink' as any, 'dark')
    expect(Object.keys(p).sort()).toEqual(['brand_name', 'brand_sub', 'skin', 'theme_mode'])
    expect(p).toEqual({ brand_name: 'A', brand_sub: 'B', skin: 'ink', theme_mode: 'dark' })
  })
})
