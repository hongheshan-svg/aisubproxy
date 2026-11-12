import { beforeEach, describe, expect, it } from 'vitest'
import { effectScope, nextTick, ref } from 'vue'
import { isBusy, setBusy, useBusy } from '../busyState'
import { hasNewVersion, showNewVersionBanner } from '../newVersionWatch'

beforeEach(() => {
  setBusy('a', false)
  setBusy('b', false)
  hasNewVersion.value = false
})

describe('busyState', () => {
  it('设置、清除；多个 key 都清掉才算不忙', () => {
    expect(isBusy.value).toBe(false)
    setBusy('a', true)
    setBusy('b', true)
    setBusy('a', true)
    expect(isBusy.value).toBe(true)
    setBusy('a', false)
    expect(isBusy.value).toBe(true)
    setBusy('b', false)
    expect(isBusy.value).toBe(false)
  })
  it('清除不存在的 key 无副作用', () => {
    setBusy('zzz', false)
    expect(isBusy.value).toBe(false)
  })
})

describe('useBusy', () => {
  it('跟随条件登记和清除；作用域销毁（组件卸载）时清掉自己的 key', async () => {
    const on = ref(true)
    const scope = effectScope()
    scope.run(() => useBusy('a', () => on.value))
    expect(isBusy.value).toBe(true)
    on.value = false
    await nextTick()
    expect(isBusy.value).toBe(false)
    on.value = true
    await nextTick()
    expect(isBusy.value).toBe(true)
    scope.stop()
    expect(isBusy.value).toBe(false)
    // 销毁后条件再变也不会重新登记
    on.value = false
    on.value = true
    await nextTick()
    expect(isBusy.value).toBe(false)
  })
  it('卸载一个实例不影响别的 key', () => {
    const s1 = effectScope()
    const s2 = effectScope()
    s1.run(() => useBusy('a', () => true))
    s2.run(() => useBusy('b', () => true))
    s1.stop()
    expect(isBusy.value).toBe(true)
    s2.stop()
    expect(isBusy.value).toBe(false)
  })
})

describe('提示条显示条件', () => {
  it('有新版本且不忙才显示；忙碌结束后出现', () => {
    expect(showNewVersionBanner.value).toBe(false)
    hasNewVersion.value = true
    expect(showNewVersionBanner.value).toBe(true)
    setBusy('a', true)
    expect(showNewVersionBanner.value).toBe(false)
    setBusy('a', false)
    expect(showNewVersionBanner.value).toBe(true)
  })
  it('没有新版本时忙碌也不显示', () => {
    setBusy('a', true)
    expect(showNewVersionBanner.value).toBe(false)
  })
})
