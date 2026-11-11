import { describe, expect, it } from 'vitest'
import { brandColorVars, contrastRatio, darkPrimaryFrom, isUsablePrimary, sideColorsFrom, tintFrom } from '../brandColor'

describe('brandColor', () => {
  it('对比度', () => { expect(contrastRatio('#FFFFFF', '#0F6B5F')).toBeCloseTo(6.39, 1); expect(contrastRatio('#000000', '#FFFFFF')).toBeCloseTo(21, 0) })
  it('预设都可用，浅色不可用', () => {
    for (const c of ['#0F6B5F', '#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F']) expect(isUsablePrimary(c)).toBe(true)
    for (const c of ['#7FD4C4', '0F6B5F', '#0F6B5', 'red']) expect(isUsablePrimary(c)).toBe(false)
  })
  it('暗色主色', () => {
    expect(darkPrimaryFrom('#0f6b5f')).toBe('#3FB8A5')
    for (const c of ['#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F', '#000000', '#0000FF'])
    {
      expect(contrastRatio(darkPrimaryFrom(c), '#0C1A18')).toBeGreaterThanOrEqual(4.5) // 极深、无饱和度也要能结束
      expect(contrastRatio(darkPrimaryFrom(c), '#132522')).toBeGreaterThanOrEqual(4.5) // 卡片底色上也要达标
    }
  })
  it('浅色调', () => { expect(tintFrom('#0F6B5F')).toBe('#E2EDEC') })
  it('变量兜底', () => { expect(brandColorVars('#7FD4C4')['--brand-primary']).toBe('#0F6B5F'); expect(brandColorVars(undefined)['--brand-primary-dark']).toBe('#3FB8A5'); expect(brandColorVars(undefined)['--brand-tint-dark']).toBe('#1C423C') })
  it('侧栏颜色：深青固定值', () => {
    expect(sideColorsFrom('#0F6B5F')).toEqual({ bg: '#0A473F', ink: '#C9D7D5', muted: '#9DB5B2' })
  })
  it('侧栏颜色：各种主色都达标', () => {
    for (const p of ['#0F6B5F', '#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F', '#767676', '#05080A']) {
      const s = sideColorsFrom(p)
      expect(contrastRatio('#FFFFFF', s.bg)).toBeGreaterThanOrEqual(10)
      expect(contrastRatio(s.ink, s.bg)).toBeGreaterThanOrEqual(4.5)
      expect(contrastRatio(s.muted, s.bg)).toBeGreaterThanOrEqual(4.5)
    }
  })
  it('侧栏颜色：rose、violet 暗色主色的菜单字和分组标题也达标', () => {
    for (const p of ['#fb7185', '#a78bfa']) {
      const s = sideColorsFrom(p)
      expect(contrastRatio(s.ink, s.bg), `${p} ink`).toBeGreaterThanOrEqual(4.5)
      expect(contrastRatio(s.muted, s.bg), `${p} muted`).toBeGreaterThanOrEqual(4.5)
    }
  })
})
