import { START_LOCATION, createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { agentGuardDecision } from '../lib/agentSession'

import { setSkinFontsAllowed } from '../theme'
import { hashScroll, prefersReducedMotion } from './scroll'
import { OPS_BASE, isAdminPath, routes, surfaceOf } from './routes'
import { hasNewVersion, shouldFullReload } from '../lib/newVersionWatch'
import { isChunkLoadError, recoverFromChunkError } from '../lib/chunkReload'
import { assignPage, reloadPage } from '../lib/pageReload'
import { dialog } from '../lib/dialog'
import { i18n } from '../i18n'

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
  scrollBehavior: (to) => hashScroll(to, prefersReducedMotion()),
})

let setupChecked = false
let setupInstalled = true

async function ensureSetupStatus(): Promise<boolean> {
  if (setupChecked) return setupInstalled
  try {
    const r = await fetch('/api/v1/setup/status')
    if (!r.ok) throw new Error('status')
    const d = await r.json()
    setupInstalled = !!d.installed
  } catch {
    // 后端不可达或回了错误：这次放行，不记结果，下次跳转再问（不把管理员误送去安装向导）
    return true
  }
  setupChecked = true
  return setupInstalled
}

const isLoginPath = (path: string) => path === `${OPS_BASE}/login`

// 正在进行的导航目标：vite:preloadError 事件里拿不到目标页，靠它刷新到用户想去的页面
let pendingHref: string | null = null

// 必须最先注册（在鉴权守卫之前）：已检测到新版本时，换页就整页跳过去，拿新入口和新分包。
// 组件的 onBeforeRouteLeave（批量兑换进行中、未保存提醒等）先于全局守卫执行，被它拦下的导航到不了这里，不会被刷新。
router.beforeEach((to, from) => {
  pendingHref = router.resolve(to).href
  if (shouldFullReload({ hasNew: hasNewVersion.value, initial: from === START_LOCATION, toPath: to.path, fromPath: from.path })) {
    assignPage(pendingHref)
    return false
  }
})

router.beforeEach(async (to) => {
  if (to.matched.some((r) => r.meta.requiresAgent)) {
    return (await agentGuardDecision(true, to.fullPath, () => fetch('/api/v1/agent/me', { credentials: 'include' }))) ?? undefined
  }
  if (to.path === '/agent/login') return
  // 安装状态只有后台页面用得上；用户端页面不等这次请求
  if (!isAdminPath(to.path)) return
  const authStore = useAuthStore()
  const installed = await ensureSetupStatus()

  // 未安装：强制进安装向导（登录页除外可看，但推荐 setup）
  if (!installed && !to.meta.setupPage && to.path.startsWith(OPS_BASE) && to.path !== `${OPS_BASE}/login`) {
    return `${OPS_BASE}/setup`
  }
  if (installed && to.meta.setupPage) {
    return `${OPS_BASE}/login`
  }

  const toLogin = isLoginPath(to.path)
  // 登录态以服务端为准：本次页面加载第一次进后台或登录页时问一次 /auth/admin/me。
  // 本地没有记录但 cookie 还有效（刷新、清过存储）照样算已登录；服务端说失效就清掉本地记录；
  // 服务端暂时不可达时沿用本地记录，不踢人。登录、退出之后不再问。
  if (to.meta.requiresAdmin || toLogin) {
    await authStore.ensureSession()
  }

  if (to.meta.requiresAdmin && !authStore.isLoggedIn) {
    return {
      path: `${OPS_BASE}/login`,
      query: { redirect: to.fullPath },
    }
  }

  if (toLogin && authStore.isLoggedIn) {
    return OPS_BASE
  }
})

// Google 字体只在后台页面加载，用户端不拉
router.afterEach((to) => {
  pendingHref = null
  setSkinFontsAllowed(isAdminPath(to.path))
  document.documentElement.setAttribute('data-surface', surfaceOf(to.path))
})

// 兜底：取不到旧分包（升级后文件已换掉）时整页刷新一次；10 秒内刷过就只提示
function recoverFromChunk(href: string | null) {
  recoverFromChunkError({
    now: () => Date.now(),
    storage: () => window.sessionStorage,
    reload: () => (href ? assignPage(href) : reloadPage()),
    toast: () => dialog.toast(i18n.global.t('chunkError.failed'), 'warn', 6000),
  })
}

router.onError((err, to) => {
  if (isChunkLoadError(err)) recoverFromChunk(router.resolve(to).href)
})

if (typeof window !== 'undefined') {
  // Vite 的动态导入预加载失败（含样式文件）会先派发这个事件；不拦截的话错误会继续抛出
  window.addEventListener('vite:preloadError', (e) => {
    e.preventDefault()
    recoverFromChunk(pendingHref)
  })
}

export default router
