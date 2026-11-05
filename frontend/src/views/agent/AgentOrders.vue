<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">订单</h1>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <div class="card p-2 overflow-x-auto">
      <table class="w-full text-sm">
        <thead><tr class="text-left"><th class="p-2">订单号</th><th class="p-2">商品</th><th class="p-2">数量</th><th class="p-2">总价</th><th class="p-2">备注</th><th class="p-2">时间</th></tr></thead>
        <tbody>
          <tr v-for="o in items" :key="o.id" class="border-t">
            <td class="p-2"><router-link :to="`/agent/orders/${o.id}`" class="underline">#{{ o.id }}</router-link></td>
            <td class="p-2">{{ o.product_name }}</td><td class="p-2">{{ o.quantity }}</td><td class="p-2">{{ fmtUsd(o.total_cents) }}</td>
            <td class="p-2 break-all">{{ o.note }}</td><td class="p-2 whitespace-nowrap">{{ fmtBeijing(o.created_at) }}</td>
          </tr>
          <tr v-if="!items.length"><td colspan="6" class="p-3 opacity-70">暂无订单</td></tr>
        </tbody>
      </table>
    </div>
    <div class="flex items-center gap-3 text-sm">
      <button class="btn-ghost" :disabled="page <= 1" @click="go(page - 1)">上一页</button>
      <span>第 {{ page }} 页 / 共 {{ total }} 条</span>
      <button class="btn-ghost" :disabled="page * PAGE >= total" @click="go(page + 1)">下一页</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { fmtBeijing } from '../../lib/mwwebhook'

const PAGE = 50
const items = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const err = ref('')
async function go(p: number) {
  page.value = p
  try {
    const r = await agentApi(`/orders?page=${p}&page_size=${PAGE}`)
    if (r.ok) { items.value = r.data.items || []; total.value = r.data.total || 0; err.value = '' }
    else err.value = agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' }
}
onMounted(() => go(1))
</script>
