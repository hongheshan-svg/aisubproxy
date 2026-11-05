<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">概览</h1>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <div v-if="failBanner" class="p-3 rounded text-sm bg-red-100 text-red-800" role="alert">{{ failBanner }}　<router-link to="/agent/integration" class="underline">查看</router-link></div>
    <div v-if="lowBanner" class="p-3 rounded text-sm bg-yellow-100 text-yellow-800" role="status">{{ lowBanner }}</div>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div class="card p-4"><div class="text-sm opacity-70">余额</div><div class="text-2xl font-semibold">{{ fmtUsd(data?.balance_cents || 0) }}</div></div>
      <div class="card p-4"><div class="text-sm opacity-70">本月下单金额</div><div class="text-2xl font-semibold">{{ fmtUsd(data?.month_order_cents || 0) }}</div></div>
    </div>
    <div class="card p-4">
      <div class="text-sm opacity-70 mb-2">码状态</div>
      <div class="flex flex-wrap gap-x-6 gap-y-1">
        <span v-for="(label, k) in CODE_STATUS_LABELS" :key="k">{{ label }}：{{ data?.status_counts?.[k] ?? 0 }}</span>
      </div>
    </div>
    <div class="card p-4">
      <div class="flex items-center justify-between mb-2"><h2 class="font-semibold">最近订单</h2><router-link to="/agent/orders" class="text-sm underline">全部订单</router-link></div>
      <p v-if="!data?.recent_orders?.length" class="text-sm opacity-70">暂无订单</p>
      <ul class="divide-y">
        <li v-for="o in data?.recent_orders || []" :key="o.id" class="py-2 flex flex-wrap gap-x-4 gap-y-1 text-sm">
          <router-link :to="`/agent/orders/${o.id}`" class="underline">#{{ o.id }}</router-link>
          <span>{{ o.product_name }} × {{ o.quantity }}</span><span>{{ fmtUsd(o.total_cents) }}</span><span class="opacity-70">{{ fmtBeijing(o.created_at) }}</span>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { failureBanner, lowBalanceBanner } from '../../lib/agentShared'
import { CODE_STATUS_LABELS } from '../../lib/agentViews'
import { fmtBeijing } from '../../lib/mwwebhook'

const data = ref<any>(null)
const err = ref('')
const failBanner = computed(() => failureBanner(data.value?.shared_failures_24h))
const lowBanner = computed(() => lowBalanceBanner(data.value?.low_balance))
onMounted(async () => {
  try {
    const r = await agentApi('/overview')
    if (r.ok) data.value = r.data
    else err.value = agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' }
})
</script>
