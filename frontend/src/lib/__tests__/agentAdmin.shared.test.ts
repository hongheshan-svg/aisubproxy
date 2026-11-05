import { describe, expect, it } from 'vitest'
import { adminSharedView } from '../agentAdmin'

describe('adminSharedView', () => {
  it('已开通且白名单为空', () => {
    expect(adminSharedView({ status: 'active', ip_allowlist: [] })).toEqual({
      statusText: '已开通', canDisable: true, canEnable: false, allowlistText: '不限',
    })
  })
  it('已被管理员关闭，白名单用顿号连接', () => {
    const v = adminSharedView({ status: 'admin_disabled', ip_allowlist: ['1.2.3.4', '10.0.0.0/8'] })
    expect(v.canEnable).toBe(true)
    expect(v.canDisable).toBe(false)
    expect(v.allowlistText).toBe('1.2.3.4、10.0.0.0/8')
  })
  it('未知状态显示原始值', () => {
    const v = adminSharedView({ status: 'weird', ip_allowlist: null })
    expect(v.statusText).toBe('weird')
    expect(v.allowlistText).toBe('不限')
  })
})
