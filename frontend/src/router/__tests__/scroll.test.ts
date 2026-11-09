import { describe, expect, it } from 'vitest'
import { hashScroll } from '../scroll'

describe('hashScroll', () => {
  it('有 hash：平时平滑滚动', () => {
    expect(hashScroll({ hash: '#help' }, false)).toEqual({ el: '#help', behavior: 'smooth' })
  })
  it('有 hash：设了减少动态效果时直接跳', () => {
    expect(hashScroll({ hash: '#help' }, true)).toEqual({ el: '#help', behavior: 'auto' })
  })
  it('没有 hash：不处理，滚动行为保持不变', () => {
    expect(hashScroll({ hash: '' }, false)).toBe(false)
  })
})
