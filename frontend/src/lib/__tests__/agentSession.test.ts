import { beforeEach, describe, expect, it, vi } from 'vitest'
import { agentGuardDecision, resetAgentSession } from '../agentSession'

const okMe = () => vi.fn(async () => ({ status: 200 }))

describe('agentGuardDecision', () => {
  beforeEach(() => resetAgentSession())

  it('非代理商页面不请求 /me', async () => {
    const f = okMe()
    expect(await agentGuardDecision(false, '/ops', f)).toBeNull()
    expect(f).not.toHaveBeenCalled()
  })
  it('进入代理商页面只请求一次 /me', async () => {
    const f = okMe()
    expect(await agentGuardDecision(true, '/agent', f)).toBeNull()
    expect(await agentGuardDecision(true, '/agent/codes', f)).toBeNull()
    expect(f).toHaveBeenCalledTimes(1)
  })
  it('401 跳登录页并带原路径，且不缓存', async () => {
    const f = vi.fn(async () => ({ status: 401 }))
    expect(await agentGuardDecision(true, '/agent/codes?x=1', f)).toEqual({ path: '/agent/login', query: { redirect: '/agent/codes?x=1' } })
    await agentGuardDecision(true, '/agent', f)
    expect(f).toHaveBeenCalledTimes(2)
  })
  it('网络错误放行', async () => {
    const f = vi.fn(async () => { throw new Error('net') })
    expect(await agentGuardDecision(true, '/agent', f)).toBeNull()
  })
  it('重置后重新验证', async () => {
    const f = okMe()
    await agentGuardDecision(true, '/agent', f)
    resetAgentSession()
    await agentGuardDecision(true, '/agent', f)
    expect(f).toHaveBeenCalledTimes(2)
  })
})
