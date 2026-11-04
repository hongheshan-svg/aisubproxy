import type { RouteRecordRaw } from 'vue-router'

// 管理端入口故意不用 /admin，降低扫路径风险。API 仍为 /api/v1/admin/*（服务端鉴权）。
export const OPS_BASE = '/ops'

export const routes: RouteRecordRaw[] = [
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
    path: '/dashboard',
    name: 'Dashboard',
    component: () => import('../views/user/DashboardView.vue'),
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
      // 代理商（管理员侧）：详情页不设 title，不进侧栏
      { path: 'agents', name: 'AgentList', component: () => import('../views/admin/AgentList.vue'), meta: { title: '代理商', group: '代理商', icon: 'User' } },
      { path: 'agents/:id', name: 'AgentDetail', component: () => import('../views/admin/AgentDetail.vue') },
      { path: 'agent-pricing', name: 'AgentPricing', component: () => import('../views/admin/AgentPricing.vue'), meta: { title: '批发商品与价格', group: '代理商', icon: 'PriceTag' } },
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
  // 代理商后台：自己的登录页和布局，不进管理员侧栏（子路由不设 meta.title）
  {
    path: '/agent/login',
    name: 'AgentLogin',
    component: () => import('../views/agent/AgentLogin.vue'),
  },
  {
    path: '/agent',
    component: () => import('../layouts/AgentLayout.vue'),
    meta: { requiresAgent: true },
    children: [
      { path: '', name: 'AgentHome', component: () => import('../views/agent/AgentOverview.vue') },
      { path: 'order', name: 'AgentOrder', component: () => import('../views/agent/AgentOrder.vue') },
      { path: 'codes', name: 'AgentCodes', component: () => import('../views/agent/AgentCodes.vue') },
      { path: 'orders', name: 'AgentOrders', component: () => import('../views/agent/AgentOrders.vue') },
      { path: 'orders/:id', name: 'AgentOrderDetail', component: () => import('../views/agent/AgentOrderDetail.vue') },
      { path: 'ledger', name: 'AgentLedger', component: () => import('../views/agent/AgentLedger.vue') },
      { path: 'account', name: 'AgentAccount', component: () => import('../views/agent/AgentAccount.vue') },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'NotFound',
    component: () => import('../views/NotFoundView.vue'),
  },
]
