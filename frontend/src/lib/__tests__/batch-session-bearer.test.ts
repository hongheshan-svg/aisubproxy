import { describe, expect, it } from 'vitest'
import { extractCdkSession } from '../batch-session'

// F1-18：带 Bearer 前缀的纯 Access Token（三段 JWT）不能当 CDK 凭证
describe('extractCdkSession：纯 Access Token', () => {
  const jwt = 'eyJ' + 'a'.repeat(40) + '.' + 'b'.repeat(20) + '.' + 'c'.repeat(20)
  // 注意：不带前缀的三段串字符全在 [A-Za-z0-9._-] 内，会先被 X 的裸 auth_token 规则认走（既有行为），这里不断言它
  it('带 Bearer 前缀的三段 JWT 也返回空串', () => {
    expect(extractCdkSession('Bearer ' + jwt)).toBe('')
    expect(extractCdkSession('bearer ' + jwt)).toBe('')
  })
  it('五段 JWE 仍原样返回', () => {
    const jwe = 'eyJ' + ['a', 'b', 'c', 'd', 'e'].map((x) => x.repeat(12)).join('.')
    expect(extractCdkSession(jwe)).toBe(jwe)
  })
})
