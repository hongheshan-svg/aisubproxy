import { describe, expect, it } from 'vitest'
import { checkLogoFile, checkPrimary, LOGO_MAX_BYTES, PRESET_COLORS } from '../siteAppearance'

describe('checkPrimary 是否可保存', () => {
  it('深青可保存，并给出两位小数的对比度', () => {
    const r = checkPrimary('#0F6B5F')
    expect(r.canSave).toBe(true)
    expect(r.ratio!).toBeGreaterThanOrEqual(4.5)
    expect(r.ratioText).toMatch(/^白字对比度 \d+\.\d{2}:1$/)
    expect(r.message).toBe('')
  })
  it('6 个预设色都可保存', () => {
    expect(PRESET_COLORS).toHaveLength(6)
    for (const p of PRESET_COLORS) expect(checkPrimary(p.hex).canSave).toBe(true)
  })
  it('太浅的颜色不可保存，提示逐字照抄', () => {
    const r = checkPrimary('#7FD4C4')
    expect(r.canSave).toBe(false)
    expect(r.message).toBe('这个颜色太浅，按钮上的白字看不清，请选深一点的颜色')
  })
  it('格式不对不可保存，提示格式要求', () => {
    for (const v of ['', '#FFF', '0F6B5F', '#GGGGGG', '#0F6B5F0']) {
      const r = checkPrimary(v)
      expect(r.canSave).toBe(false)
      expect(r.message).toContain('#RRGGBB')
    }
  })
})

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
  it('空文件不收', () => {
    expect(checkLogoFile({ size: 0, type: 'image/png' })).toContain('PNG')
  })
})
