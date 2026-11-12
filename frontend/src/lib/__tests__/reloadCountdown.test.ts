import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createReloadCountdown } from '../reloadCountdown'

beforeEach(() => { vi.useFakeTimers() })
afterEach(() => { vi.useRealTimers() })

function make() {
  const ticks: number[] = []
  const done = vi.fn()
  const c = createReloadCountdown({ seconds: 3, onTick: (n) => ticks.push(n), onDone: done })
  return { c, ticks, done }
}

describe('升级后的刷新倒计时', () => {
  it('3 秒后调用刷新，每秒更新剩余秒数', async () => {
    const { c, ticks, done } = make()
    c.start()
    expect(ticks).toEqual([3])
    await vi.advanceTimersByTimeAsync(2999)
    expect(done).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1)
    expect(ticks).toEqual([3, 2, 1, 0])
    expect(done).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(5000)
    expect(done).toHaveBeenCalledTimes(1)
  })
  it('倒数中取消（离开页面或点了立即刷新）：不再刷新', async () => {
    const { c, done } = make()
    c.start()
    await vi.advanceTimersByTimeAsync(1500)
    c.cancel()
    await vi.advanceTimersByTimeAsync(5000)
    expect(done).not.toHaveBeenCalled()
  })
})

// 版本更新页的接线：.vue 在本机以 SSR 模式编译、没有 DOM，按源码断言（与同目录其他用例一致）
describe('SystemUpdate 接线', () => {
  const src = readFileSync(fileURLToPath(new URL('../../views/admin/SystemUpdate.vue', import.meta.url)), 'utf8')
  const recovered = src.slice(src.indexOf('onRecovered:'), src.indexOf('onTimeout:'))
  it('只有版本确实变了才开始倒计时', () => {
    expect(recovered).toContain("const changed = !!after && after !== before")
    expect(recovered).toMatch(/if \(changed && !unmounted\) startReloadCountdown\(after\)/)
  })
  it('文案、立即刷新按钮、到点刷新、离开页面取消', () => {
    expect(src).toContain('已更新到 v${version}，${left} 秒后自动刷新页面')
    expect(src).toContain('@click="reloadNow"')
    expect(src).toContain('seconds: RELOAD_COUNTDOWN_S')
    expect(src).toContain('onDone: reloadNow')
    expect(src).toMatch(/onBeforeUnmount\(\(\) => \{[^}]*countdown\?\.cancel\(\)/)
  })
})
