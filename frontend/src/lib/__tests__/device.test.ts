import { describe, it, expect } from 'vitest'
import { detectDevice } from '../device'

describe('detectDevice', () => {
  it('iPhone Safari：ios', () => {
    expect(detectDevice('Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1', 5)).toBe('ios')
  })
  it('iPad 报 Macintosh 且有触点：ios', () => {
    expect(detectDevice('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15', 5)).toBe('ios')
  })
  it('Android Chrome：android', () => {
    expect(detectDevice('Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Mobile Safari/537.36', 5)).toBe('android')
  })
  it('Windows Chrome：desktop', () => {
    expect(detectDevice('Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.0.0 Safari/537.36', 0)).toBe('desktop')
  })
  it('macOS Safari 无触点：desktop', () => {
    expect(detectDevice('Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15', 0)).toBe('desktop')
  })
})
