import { beforeEach, describe, expect, it } from 'vitest'
import { isBusy, setBusy } from '../busyState'
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
