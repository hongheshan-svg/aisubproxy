import { describe, expect, it } from 'vitest'
import { contrastRatio, isUsablePrimary, sideColorsFrom } from '../brandColor'

describe('brandColor', () => {
  it('对比度', () => { expect(contrastRatio('#FFFFFF', '#0F6B5F')).toBeCloseTo(6.39, 1); expect(contrastRatio('#000000', '#FFFFFF')).toBeCloseTo(21, 0) })
  it('预设都可用，浅色不可用', () => {
    for (const c of ['#0F6B5F', '#1E4FA8', '#5B3FA0', '#A3283B', '#2F6B2F', '#36454F']) expect(isUsablePrimary(c)).toBe(true)
    for (const c of ['#7FD4C4', '0F6B5F', '#0F6B5', 'red']) expect(isUsablePrimary(c)).toBe(false)
  })
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
