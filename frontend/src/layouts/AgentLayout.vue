<template>
  <div class="min-h-screen agent-shell">
    <header class="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3 border-b">
      <router-link to="/agent" class="font-semibold">代理商后台</router-link>
      <nav class="flex flex-wrap gap-4 text-sm" aria-label="代理商导航">
        <router-link v-for="n in nav" :key="n.to" :to="n.to" :class="{ 'font-semibold underline': isActive(n) }">{{ n.label }}</router-link>
      </nav>
      <div class="ml-auto flex items-center gap-3 text-sm">
        <span v-if="me">{{ me.display_name || me.username }} · 余额 {{ fmtUsd(me.balance_cents || 0) }}</span>
        <button type="button" class="btn-ghost" @click="logout">退出登录</button>
      </div>
    </header>
    <main class="p-4 max-w-6xl mx-auto">
      <router-view />
    </main>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { agentApi, setAgentUnauthorizedHandler } from '../lib/agentApi'
import { fmtUsd } from '../lib/agentMoney'
import { resetAgentSession } from '../lib/agentSession'
import { orderRetry } from '../lib/agentViews'

type Nav = { to: string; label: string; exact?: boolean }
const nav: Nav[] = [
  { to: '/agent', label: '概览', exact: true },
  { to: '/agent/order', label: '下单' },
  { to: '/agent/codes', label: '我的码' },
  { to: '/agent/orders', label: '订单' },
  { to: '/agent/ledger', label: '流水' },
  { to: '/agent/integration', label: '对接异次元' },
  { to: '/agent/account', label: '账号' },
]

const route = useRoute()
const router = useRouter()
const me = ref<{ username?: string; display_name?: string; balance_cents?: number } | null>(null)

function isActive(n: Nav) {
  return n.exact ? route.path === n.to : route.path === n.to || route.path.startsWith(n.to + '/')
}

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

onMounted(() => {
  setAgentUnauthorizedHandler(toLogin)
  loadMe()
})
onBeforeUnmount(() => setAgentUnauthorizedHandler(null))
</script>
