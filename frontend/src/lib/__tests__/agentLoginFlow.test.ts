import { describe, expect, it } from 'vitest'
import { nextLoginStep } from '../agentLoginFlow'

const ok = (next?: unknown) => ({ ok: true, data: next === undefined ? {} : { next } })

describe('nextLoginStep', () => {
  it('首次登录完整流转：password → change_password → setup_totp → done', () => {
    expect(nextLoginStep('password', ok('change_password'))).toEqual({ kind: 'step', step: 'change_password' })
    expect(nextLoginStep('change_password', ok('setup_totp'))).toEqual({ kind: 'step', step: 'setup_totp' })
    expect(nextLoginStep('setup_totp', ok('done'))).toEqual({ kind: 'done' })
  })
  it('日常登录：password → totp → done', () => {
    expect(nextLoginStep('password', ok('totp'))).toEqual({ kind: 'step', step: 'totp' })
    expect(nextLoginStep('totp', ok('done'))).toEqual({ kind: 'done' })
  })
  it('缺少 next、空串、未知值都是流程异常，最后两步更不能当成功', () => {
    for (const step of ['password', 'change_password', 'setup_totp', 'totp'] as const) {
      for (const n of [undefined, '', 'bogus', null]) {
        const r = nextLoginStep(step, n === undefined ? ok() : ok(n))
        expect(r.kind, `${step}/${String(n)}`).toBe('error')
      }
    }
  })
  it('前面的步骤返回 done 不算成功', () => {
    expect(nextLoginStep('password', ok('done')).kind).toBe('error')
    expect(nextLoginStep('change_password', ok('done')).kind).toBe('error')
  })
  it('pre_expired 回到密码步骤', () => {
    expect(nextLoginStep('totp', { ok: false, data: { error: 'pre_expired' } })).toEqual({ kind: 'error', text: '登录已过期，请重新输入密码', restart: true })
  })
  it('其他失败留在当前步骤并给中文提示', () => {
    expect(nextLoginStep('totp', { ok: false, data: { error: 'totp_invalid' } })).toEqual({ kind: 'error', text: '动态码错误', restart: false })
  })
})
