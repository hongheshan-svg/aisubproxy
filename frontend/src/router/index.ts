import { createRouter, createWebHistory, RouteRecordRaw } from 'vue-router'
import { useAuthStore } from '../stores/auth'

// 管理端入口故意不用 /admin，降低扫路径风险。API 仍为 /api/v1/admin/*（服务端鉴权）。
const OPS_BASE = '/ops'

const routes: RouteRecordRaw[] = [
  {
    path: '/',
    name: 'Home',
    component: () => import('../views/HomeView.vue'),
  },
  // 旧登录路径 → 隐蔽入口
  { path: '/auth/login', redirect: `${OPS_BASE}/login` },
  { path: '/admin', redirect: OPS_BASE },
  { path: '/admin/:pathMatch(.*)*', redirect: (to) => `${OPS_BASE}/${to.params.pathMatch || ''}` },
  {
    path: `${OPS_BASE}/login`,
    name: 'Login',
    component: () => import('../views/auth/LoginView.vue'),
  },
  {
    path: `${OPS_BASE}/setup`,
    name: 'Setup',
    component: () => import('../views/admin/SetupView.vue'),
    meta: { setupPage: true },
  },
  {
    path: '/auth/register',
    name: 'Register',
    component: () => import('../views/auth/RegisterView.vue'),
  },
  {
    path: '/billing',
    name: 'BillingCheck',
    component: () => import('../views/user/BillingCheckView.vue'),
  },
  {
    path: '/recharge',
    name: 'Recharge',
    component: () => import('../views/user/RechargeView.vue'),
  },
  {
    path: '/batch',
    name: 'BatchRedeem',
    component: () => import('../views/user/BatchRedeemView.vue'),
  },
  {
    path: '/history',
    name: 'CDKStatusLookup',
    component: () => import('../views/user/CDKStatusView.vue'),
  },
  {
    path: '/lookup',
    redirect: '/history',
  },
  // 代理隐藏换码页（不进导航；管理员设密码后使用）
  {
    path: '/partner/swap',
    name: 'AgentCDKSwap',
    component: () => import('../views/user/AgentSwapView.vue'),
  },
  {
    path: '/a/swap',
    redirect: '/partner/swap',
  },
  {
    path: OPS_BASE,
    component: () => import('../layouts/AdminLayout.vue'),
    meta: { requiresAuth: true, requiresAdmin: true },
    children: [
      { path: '', name: 'AdminDashboard', component: () => import('../views/admin/AdminDashboard.vue'), meta: { title: '总览', group: '', icon: 'Odometer' } },
      // 通用能力
      { path: 'cdkeys', name: 'CDKeyManagement', component: () => import('../views/admin/CDKeyManagement.vue'), meta: { title: 'CDK 卡密', group: '卡密与付款', icon: 'Tickets' } },
      { path: 'marswallex-pay', name: 'MarsWallexPay', component: () => import('../views/admin/MarsWallexPay.vue'), meta: { title: '自动付款', group: '卡密与付款', icon: 'Money' } },
      { path: 'providers', name: 'CardProviders', component: () => import('../views/admin/CardProviders.vue'), meta: { title: '卡服务商', group: '卡密与付款', icon: 'Coin' } },
      // 各服务商专有：菜单里用短名，fullTitle 带服务商名
      { path: 'integration', name: 'CardIntegration', component: () => import('../views/admin/CardIntegration.vue'), meta: { title: '接入配置', fullTitle: 'ZovoCard 接入配置', group: 'ZovoCard', icon: 'Connection' } },
      { path: 'card-selection', name: 'CardSelectionConfig', component: () => import('../views/admin/CardSelectionConfig.vue'), meta: { title: '选卡规则', fullTitle: 'ZovoCard 选卡规则', group: 'ZovoCard', icon: 'Operation' } },
      { path: 'orders', name: 'OrderReconcile', component: () => import('../views/admin/OrderReconcile.vue'), meta: { title: '兑换订单', fullTitle: 'ZovoCard 兑换订单', group: 'ZovoCard', icon: 'Document' } },
      { path: 'webhooks', name: 'Webhooks', component: () => import('../views/admin/WebhookEvents.vue'), meta: { title: 'Webhook', fullTitle: 'ZovoCard Webhook', group: 'ZovoCard', icon: 'Bell' } },
      { path: 'marswallex', name: 'MarsWallexIntegration', component: () => import('../views/admin/MarsWallexIntegration.vue'), meta: { title: '接入配置', fullTitle: 'MarsWallex 接入配置', group: 'MarsWallex', icon: 'Platform' } },
      { path: 'marswallex-wallet', name: 'MarsWallexWallet', component: () => import('../views/admin/MarsWallexWallet.vue'), meta: { title: '钱包', fullTitle: 'MarsWallex 钱包', group: 'MarsWallex', icon: 'Wallet' } },
      { path: 'cardpool', name: 'CardPool', component: () => import('../views/admin/CardPool.vue'), meta: { title: '卡池', fullTitle: 'MarsWallex 卡池', group: 'MarsWallex', icon: 'CreditCard' } },
      { path: 'marswallex-selection', name: 'MarsWallexSelection', component: () => import('../views/admin/MarsWallexSelection.vue'), meta: { title: '选卡规则', fullTitle: 'MarsWallex 选卡规则', group: 'MarsWallex', icon: 'Operation' } },
      // 系统
      { path: 'appearance', name: 'SiteAppearance', component: () => import('../views/admin/SiteAppearance.vue'), meta: { title: '外观与品牌', group: '系统', icon: 'Brush' } },
      { path: 'telegram', name: 'TelegramAlerts', component: () => import('../views/admin/TelegramAlerts.vue'), meta: { title: 'Telegram 告警', group: '系统', icon: 'ChatDotRound' } },
      { path: 'audit', name: 'AuditLogs', component: () => import('../views/admin/AuditLogs.vue'), meta: { title: '审计日志', group: '系统', icon: 'List' } },
      { path: 'update', name: 'SystemUpdate', component: () => import('../views/admin/SystemUpdate.vue'), meta: { title: '版本更新', group: '系统', icon: 'Refresh' } },
      // 旧路径兼容
      { path: 'accounts', redirect: OPS_BASE },
      { path: 'proxies', redirect: OPS_BASE },
      { path: 'recharge-requests', redirect: OPS_BASE },
      { path: 'billing', redirect: `${OPS_BASE}/orders` },
      { path: 'cards', redirect: `${OPS_BASE}/integration` },
      { path: 'statistics', redirect: OPS_BASE },
      { path: 'checkout', redirect: OPS_BASE },
      { path: 'reconcile', redirect: `${OPS_BASE}/orders` },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('../views/NotFoundView.vue'),
  },
]

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

const isLoginPath = (path: string) => path === `${OPS_BASE}/login` || path === '/auth/login'

router.beforeEach(async (to) => {
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

export default router
