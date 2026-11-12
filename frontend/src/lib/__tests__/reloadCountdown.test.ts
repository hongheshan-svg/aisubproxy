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
  it('先不带 refresh 查版本，没变再带 refresh；只有版本变了才开始倒计时', () => {
    expect(recovered.indexOf('loadVersion(false)')).toBeGreaterThan(-1)
    expect(recovered.indexOf('loadVersion(false)')).toBeLessThan(recovered.indexOf('loadVersion(true)'))
    expect(recovered).toContain('const changed = !!after && after !== before')
    expect(recovered).toMatch(/if \(changed && !unmounted\) startReloadCountdown\(after\)/)
  })
  it('文案、立即刷新按钮、到点刷新、离开页面取消并置回 null', () => {
    expect(src).toContain('已更新到 v${version}，${left} 秒后自动刷新页面')
    expect(src).toContain('@click="reloadNow"')
    expect(src).toContain('onDone: reloadNow')
    expect(src).toMatch(/function cancelCountdown\(\) \{\s*countdown\?\.cancel\(\)\s*reloadCountdown\.value = null/)
    expect(src).toMatch(/onBeforeUnmount\(\(\) => \{[^}]*cancelCountdown\(\)/)
  })
  it('倒计时期间禁用一键更新，并登记忙碌（不弹提示条）', () => {
    expect(src).toContain('if (reloadCountdown.value !== null) return false')
    expect(src).toContain("useBusy('system-update', () => updating.value || reloadCountdown.value !== null)")
  })
})
