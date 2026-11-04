import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { agentGuardDecision } from '../lib/agentSession'

import { OPS_BASE, routes } from './routes'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
})

let setupChecked = false
let setupInstalled = true

async function ensureSetupStatus(): Promise<boolean> {
  if (setupChecked) return setupInstalled
  try {
    const r = await fetch('/api/v1/setup/status')
    const d = await r.json()
    setupInstalled = !!d.installed
  } catch {
    setupInstalled = true // 后端不可达时不阻断登录页
  }
  setupChecked = true
  return setupInstalled
}

// 本地登录态（localStorage 到期时间）只代表「曾经登录过」，服务端重启/换密钥后会话可能已失效。
// 每次页面加载只向服务端确认一次；明确 401/403 才判定失效，网络错误不踢人。
let sessionVerified = false
async function serverSessionAlive(): Promise<boolean> {
  if (sessionVerified) return true
  try {
    const r = await fetch('/api/v1/admin/settings', { credentials: 'include' })
    if (r.status === 401 || r.status === 403) return false
  } catch {
    /* 后端不可达：沿用本地登录态 */
  }
  sessionVerified = true
  return true
}

router.beforeEach(async (to) => {
  if (to.matched.some((r) => r.meta.requiresAgent)) {
    return (await agentGuardDecision(true, to.fullPath, () => fetch('/api/v1/agent/me', { credentials: 'include' }))) ?? undefined
  }
  if (to.path === '/agent/login') return
  const authStore = useAuthStore()
  const installed = await ensureSetupStatus()

  // 未安装：强制进安装向导（登录页除外可看，但推荐 setup）
  if (!installed && !to.meta.setupPage && to.path.startsWith(OPS_BASE) && to.path !== `${OPS_BASE}/login`) {
    return `${OPS_BASE}/setup`
  }
  if (installed && to.meta.setupPage) {
    return `${OPS_BASE}/login`
  }

  const toLogin = to.path === `${OPS_BASE}/login` || to.path === '/auth/login'
  if ((to.meta.requiresAdmin || toLogin) && authStore.isLoggedIn && !(await serverSessionAlive())) {
    authStore.logout()
    if (toLogin) return
    return { path: `${OPS_BASE}/login`, query: { redirect: to.fullPath } }
  }

  if (to.meta.requiresAdmin && !authStore.isLoggedIn) {
    return {
      path: `${OPS_BASE}/login`,
      query: { redirect: to.fullPath },
    }
  }

  if ((to.path === `${OPS_BASE}/login` || to.path === '/auth/login') && authStore.isLoggedIn) {
    return OPS_BASE
  }
})

export default router
