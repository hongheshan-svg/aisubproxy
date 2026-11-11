<template>
  <SideNavShell
    :groups="groups"
    :is-active="isActive"
    :brand-name="brand.name || '代理商后台'"
    :logo-src="logoSrc"
    sub-label="代理商后台"
    nav-label="代理商导航"
    nav-id="agent-sidenav"
    pref-key="agent-sidenav"
    home-path="/agent"
  >
    <template #topbar>
      <span class="agent-topbar-title">{{ currentTitle }}</span>
    </template>

    <template #foot="{ rail }">
      <AgentSideMe v-if="me" :name="me.display_name || me.username || ''" :balance-cents="me.balance_cents || 0" :rail="rail" />
      <div class="sn-tools"><ThemeToggle /></div>
      <button type="button" class="sn-logout" title="退出登录" @click="logout">
        <el-icon><SwitchButton /></el-icon>
        <span v-if="!rail">退出登录</span>
      </button>
    </template>

    <router-view />
  </SideNavShell>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { agentApi, setAgentUnauthorizedHandler } from '../lib/agentApi'
import { resetAgentSession } from '../lib/agentSession'
import { orderRetry } from '../lib/agentViews'
import { siteBrand } from '../theme'
import ThemeToggle from '../components/ThemeToggle.vue'
import AgentSideMe from '../components/shell/AgentSideMe.vue'
import SideNavShell from '../components/shell/SideNavShell.vue'
import type { NavGroup } from '../lib/adminNav'

// 菜单：一个无名分组（不显示分组标题），顺序、路径、文字不变
const nav = [
  { path: '/agent', title: '概览', icon: 'Odometer' },
  { path: '/agent/order', title: '下单', icon: 'ShoppingCart' },
  { path: '/agent/codes', title: '我的码', icon: 'Tickets' },
  { path: '/agent/orders', title: '订单', icon: 'List' },
  { path: '/agent/ledger', title: '流水', icon: 'Wallet' },
  { path: '/agent/integration', title: '对接异次元', icon: 'Connection' },
  { path: '/agent/account', title: '账号', icon: 'User' },
]
const groups: NavGroup[] = [
  { name: '', items: nav.map((n) => ({ ...n, fullTitle: n.title, group: '', feature: '' })) },
]

const brand = siteBrand
const logoSrc = computed(() => {
  const v = brand.value.logoVersion
  return v ? `/api/v1/public/site/logo?v=${encodeURIComponent(v)}` : ''
})

const route = useRoute()
const router = useRouter()
const me = ref<{ username?: string; display_name?: string; balance_cents?: number } | null>(null)

function isActive(p: string) {
  return p === '/agent' ? route.path === p : route.path === p || route.path.startsWith(p + '/')
}
const currentTitle = computed(() => nav.find((n) => isActive(n.path))?.title || '')

function toLogin() {
  resetAgentSession()
  router.replace({ path: '/agent/login', query: { redirect: route.fullPath } })
}

async function loadMe() {
  try {
    const r = await agentApi('/me')
    if (r.ok) {
      me.value = r.data
      // 核对身份：恢复本人结果未知的订单（并拦截刷新、关标签页），别人的丢弃
      orderRetry.bind(String(r.data?.username || ''))
    }
  } catch {
    /* 网络异常时不显示余额，页面照常 */
  }
}

async function logout() {
  if (orderRetry.refresh() && !window.confirm('有一笔订单结果未知，退出后这台电脑上不能再原样重试。确定退出吗？')) return
  try {
    await agentApi('/auth/logout', 'POST')
  } catch {
    /* 退出失败也回登录页，cookie 过期后自然失效 */
  }
  // 退出登录清掉待重试订单（只有请求号和下单内容），换别的代理商登录也拿不到
  orderRetry.clear()
  resetAgentSession()
  router.replace('/agent/login')
}

// 代理商功能关闭时（后台「系统 → 功能开关」）回登录页，登录页只显示「暂未开放」
async function checkEnabled() {
  try {
    const r = await agentApi<{ enabled?: boolean }>('/status')
    if (r.ok && r.data?.enabled === false) {
      resetAgentSession()
      router.replace('/agent/login')
    }
  } catch {
    /* 读不到就照常显示，接口自己会拦 */
  }
}

onMounted(() => {
  setAgentUnauthorizedHandler(toLogin)
  checkEnabled()
  loadMe()
})
onBeforeUnmount(() => setAgentUnauthorizedHandler(null))
</script>

<style scoped>
/* 侧栏底部（插槽内容，样式在这里）：颜色全用侧栏变量，不用浅色皮肤的变量 */
.sn-tools { display: flex; gap: 6px; }
.sn-tools :deep(.btn-ghost) { color: var(--side-ink); background: transparent; border-color: transparent; box-shadow: none; }
.sn-tools :deep(.btn-ghost:hover) { color: var(--side-ink); background: color-mix(in srgb, var(--side-ink) 12%, transparent); border-color: transparent; }
.sn-tools :deep(.btn-ghost:focus-visible),
.sn-logout:focus-visible { outline: 2px solid var(--side-ink); outline-offset: -2px; }
.is-collapsed .sn-tools { justify-content: center; }
.sn-logout {
  width: 100%; display: flex; align-items: center; gap: 8px; font-size: 13px;
  padding: 6px 8px; border-radius: var(--radius-md);
  background: transparent; border: 1px solid transparent; color: var(--side-ink); cursor: pointer;
}
.is-collapsed .sn-logout { justify-content: center; padding: 6px 0; }
.sn-logout:hover { background: color-mix(in srgb, var(--side-ink) 12%, transparent); }
.agent-topbar-title { font-size: 14px; font-weight: 600; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
