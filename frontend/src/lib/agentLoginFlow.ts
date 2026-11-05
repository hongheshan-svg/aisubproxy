// 代理商登录步骤流转（纯函数）：输入当前步骤和后端响应，输出下一步、成功或错误。
import { agentErrorText } from './agentApi'

export type LoginStep = 'password' | 'change_password' | 'setup_totp' | 'totp'

export type LoginOutcome =
  | { kind: 'step'; step: LoginStep }
  | { kind: 'done' }
  | { kind: 'error'; text: string; restart: boolean }

const MID_STEPS: LoginStep[] = ['change_password', 'setup_totp', 'totp']

export function nextLoginStep(current: LoginStep, res: { ok: boolean; data: any }): LoginOutcome {
  if (!res.ok) {
    return { kind: 'error', text: agentErrorText(res.data), restart: res.data?.error === 'pre_expired' }
  }
  const next = res.data?.next
  // 只有最后两步（绑定确认、动态码）收到 next === 'done' 才算登录成功
  if (current === 'setup_totp' || current === 'totp') {
    if (next === 'done') return { kind: 'done' }
    return { kind: 'error', text: '登录流程异常，请重新登录', restart: true }
  }
  if (typeof next === 'string' && (MID_STEPS as string[]).includes(next)) return { kind: 'step', step: next as LoginStep }
  return { kind: 'error', text: '登录流程异常，请重新登录', restart: true }
}
