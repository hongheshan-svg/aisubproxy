// 代理商路由守卫的判断逻辑（纯函数，便于测试）。不读写管理员的 authStore。

let verified = false

/** 退出登录后调用，让下一次进入代理商页面重新确认会话。 */
export function resetAgentSession() {
  verified = false
}

export type AgentGuardRedirect = { path: string; query: { redirect: string } }

/**
 * 进入 requiresAgent 页面时，每次页面加载只请求一次 /me：401 返回登录页跳转，其余（含网络错误）放行。
 * 返回 null 表示放行。
 */
export async function agentGuardDecision(
  requiresAgent: boolean,
  fullPath: string,
  fetchMe: () => Promise<{ status: number }>,
): Promise<AgentGuardRedirect | null> {
  if (!requiresAgent) return null
  if (verified) return null
  try {
    const r = await fetchMe()
    if (r.status === 401) return { path: '/agent/login', query: { redirect: fullPath } }
  } catch {
    /* 后端不可达：放行，页面自己的请求会再报错 */
  }
  verified = true
  return null
}
