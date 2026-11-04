import { describe, expect, it } from 'vitest'
import { isLocalSessionAuthFailure } from '../session'

describe('isLocalSessionAuthFailure 错误码优先（auth N-3）', () => {
  it('卡台和网络探测接口收到本站鉴权错误码也算会话失效', () => {
    expect(isLocalSessionAuthFailure('/api/v1/admin/cardplatform/ping', 401, { error_code: 'unauthorized' })).toBe(true)
    expect(isLocalSessionAuthFailure('/api/v1/admin/network/egress', 403, { error_code: 'csrf_failed' })).toBe(true)
    expect(isLocalSessionAuthFailure('/api/v1/admin/cardplatform/balance', 403, { error_code: 'admin_required' })).toBe(true)
  })
  it('没有错误码的旧响应，卡台和网络接口的 401/403 仍不算（可能是卡台上游的）', () => {
    expect(isLocalSessionAuthFailure('/api/v1/admin/cardplatform/ping', 401, { error: 'invalid or expired token' })).toBe(false)
    expect(isLocalSessionAuthFailure('/api/v1/admin/network/egress', 401, {})).toBe(false)
  })
  it('卡台上游 401 映射成的业务错误码不算', () => {
    expect(isLocalSessionAuthFailure('/api/v1/admin/cardplatform/ping', 401, { error_code: 'upstream_error' })).toBe(false)
  })
  it('普通接口行为不变', () => {
    expect(isLocalSessionAuthFailure('/api/v1/admin/settings', 401, { error_code: 'unauthorized' })).toBe(true)
    expect(isLocalSessionAuthFailure('/api/v1/admin/settings', 401, { error: 'invalid or expired token' })).toBe(true)
    expect(isLocalSessionAuthFailure('/api/v1/admin/settings', 403, { error: '旧密码错误' })).toBe(false)
    expect(isLocalSessionAuthFailure('/api/v1/admin/settings', 500, { error_code: 'unauthorized' })).toBe(false)
  })
})
