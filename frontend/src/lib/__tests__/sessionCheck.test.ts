import { describe, it, expect } from 'vitest'
import { checkPastedSession, maskEmail } from '../sessionCheck'

const now = Date.parse('2026-11-09T00:00:00Z')
const good = { user: { email: 'bun@example.com' }, sessionToken: 'fake-session-token', expires: '2026-12-01T00:00:00.000Z' }
const check = (raw: string) => checkPastedSession(raw, { now })

describe('checkPastedSession', () => {
  it('空和纯空白：empty', () => {
    expect(check('')).toEqual({ ok: false, problem: 'empty' })
    expect(check('  \n\t ')).toEqual({ ok: false, problem: 'empty' })
  })
  it('eyJ 开头的三段式：access_token', () => {
    expect(check('eyJfake.eyJpayload.sig-fake_1')).toEqual({ ok: false, problem: 'access_token' })
    expect(check('  eyJfake.eyJpayload.sig\n')).toEqual({ ok: false, problem: 'access_token' })
  })
  it('不是 JSON：not_json', () => {
    expect(check('hello world')).toEqual({ ok: false, problem: 'not_json' })
    expect(check('{"user": ')).toEqual({ ok: false, problem: 'not_json' })
    expect(check('[1,2]')).toEqual({ ok: false, problem: 'not_json' })
    expect(check('eyJonlytwo.parts')).toEqual({ ok: false, problem: 'not_json' })
  })
  it('{} 或没有字段：not_logged_in', () => {
    expect(check('{}')).toEqual({ ok: false, problem: 'not_logged_in' })
    expect(check(' {  } ')).toEqual({ ok: false, problem: 'not_logged_in' })
  })
  it('有字段但没有令牌：no_token', () => {
    expect(check(JSON.stringify({ user: { email: 'bun@example.com' } }))).toEqual({ ok: false, problem: 'no_token' })
    expect(check(JSON.stringify({ ...good, sessionToken: '  ' }))).toEqual({ ok: false, problem: 'no_token' })
    expect(check(JSON.stringify({ ...good, sessionToken: 123 }))).toEqual({ ok: false, problem: 'no_token' })
  })
  it('只有 accessToken 没有 sessionToken：no_token', () => {
    expect(check(JSON.stringify({ user: { email: 'bun@example.com' }, accessToken: 'eyJa.b.c' }))).toEqual({ ok: false, problem: 'no_token' })
  })
  it('session_token 也认', () => {
    const r = check(JSON.stringify({ user: { email: 'bun@example.com' }, session_token: 'fake' }))
    expect(r.ok).toBe(true)
  })
  it('有令牌没有邮箱：no_email', () => {
    expect(check(JSON.stringify({ sessionToken: 'fake' }))).toEqual({ ok: false, problem: 'no_email' })
    expect(check(JSON.stringify({ sessionToken: 'fake', user: null }))).toEqual({ ok: false, problem: 'no_email' })
    expect(check(JSON.stringify({ sessionToken: 'fake', user: { email: '  ' } }))).toEqual({ ok: false, problem: 'no_email' })
  })
  it('expires 早于 now：expired', () => {
    expect(check(JSON.stringify({ ...good, expires: '2026-11-08T23:59:59Z' }))).toEqual({ ok: false, problem: 'expired' })
  })
  it('expires 解析不了不算过期', () => {
    expect(check(JSON.stringify({ ...good, expires: '不是日期' })).ok).toBe(true)
  })
  it('没有 expires 通过；expires 晚于 now 通过', () => {
    const { expires: _e, ...noExp } = good
    expect(check(JSON.stringify(noExp)).ok).toBe(true)
    expect(check(JSON.stringify(good)).ok).toBe(true)
  })
  it('通过：value 是去空白原文，带脱敏邮箱', () => {
    const raw = JSON.stringify(good)
    expect(check(`  ${raw}\n`)).toEqual({ ok: true, value: raw, maskedEmail: 'bu***@example.com' })
  })
  it('首尾带换行空格、美化过的多行 JSON 也通过', () => {
    const pretty = JSON.stringify(good, null, 2)
    const r = check(`\n  ${pretty}  \n\n`)
    expect(r).toEqual({ ok: true, value: pretty, maskedEmail: 'bu***@example.com' })
  })
})

describe('maskEmail', () => {
  it('本地部分不超过 2 个字符只留 1 个，否则留 2 个', () => {
    expect(maskEmail('a@x.com')).toBe('a***@x.com')
    expect(maskEmail('ab@x.com')).toBe('a***@x.com')
    expect(maskEmail('alice@gmail.com')).toBe('al***@gmail.com')
  })
  it('按字符截，中文不乱码', () => {
    expect(maskEmail('张三丰@x.com')).toBe('张三***@x.com')
  })
})
