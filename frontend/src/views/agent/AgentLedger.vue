<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">余额流水</h1>
    <div class="card p-4 flex flex-wrap items-end gap-3">
      <label class="block"><span class="block text-sm mb-1">开始日期</span><input v-model="from" type="date" class="input" /></label>
      <label class="block"><span class="block text-sm mb-1">结束日期</span><input v-model="to" type="date" class="input" /></label>
      <button class="btn-primary" type="button" @click="go(1)">查询</button>
      <a class="btn-ghost" :href="exportHref" download>导出 CSV</a>
    </div>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
      <div class="card p-3"><div class="text-sm opacity-70">期初余额</div><div class="font-semibold">{{ fmtUsd(summary.opening_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">收入</div><div class="font-semibold">{{ fmtUsd(summary.income_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">支出</div><div class="font-semibold">{{ fmtUsd(summary.expense_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">期末余额</div><div class="font-semibold">{{ fmtUsd(summary.closing_cents) }}</div></div>
    </div>
    <div class="card p-2 overflow-x-auto">
      <table class="w-full text-sm">
        <thead><tr class="text-left"><th class="p-2">时间</th><th class="p-2">类型</th><th class="p-2">金额</th><th class="p-2">变动后余额</th><th class="p-2">订单</th><th class="p-2">备注</th></tr></thead>
        <tbody>
          <tr v-for="l in items" :key="l.id" class="border-t">
            <td class="p-2 whitespace-nowrap">{{ fmtBeijing(l.created_at) }}</td>
            <td class="p-2">{{ LEDGER_KIND_LABELS[l.kind] || l.kind }}</td>
            <td class="p-2">{{ fmtUsd(l.amount_cents) }}</td><td class="p-2">{{ fmtUsd(l.balance_after_cents) }}</td>
            <td class="p-2"><router-link v-if="l.order_id" :to="`/agent/orders/${l.order_id}`" class="underline">#{{ l.order_id }}</router-link></td>
            <td class="p-2 break-all">{{ l.note }}</td>
          </tr>
          <tr v-if="!items.length"><td colspan="6" class="p-3 opacity-70">没有流水</td></tr>
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
import { computed, onMounted, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { beijingRange, defaultLedgerRange, ledgerQuery, LEDGER_KIND_LABELS } from '../../lib/agentViews'
import { fmtBeijing } from '../../lib/mwwebhook'

const PAGE = 50
const def = defaultLedgerRange(Math.floor(Date.now() / 1000))
const from = ref(def.from)
const to = ref(def.to)
const items = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const err = ref('')
const summary = ref({ opening_cents: 0, closing_cents: 0, income_cents: 0, expense_cents: 0 })

// 同源下载，cookie 自动带上；CSV 由后端生成
const exportHref = computed(() => {
  const r = beijingRange(from.value, to.value)
  return r ? `/api/v1/agent/ledger/export?${ledgerQuery(r.from, r.to)}` : '#'
})

async function go(p: number) {
  const r0 = beijingRange(from.value, to.value)
  if (!r0) { err.value = '日期范围不正确'; return }
  page.value = p
  try {
    const r = await agentApi(`/ledger?${ledgerQuery(r0.from, r0.to)}&page=${p}&page_size=${PAGE}`)
    if (r.ok) {
      items.value = r.data.items || []; total.value = r.data.total || 0
      if (r.data.summary) summary.value = r.data.summary
      err.value = ''
    } else err.value = r.data?.error === 'range_too_large' ? '日期范围不能超过 366 天' : agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' }
}
onMounted(() => go(1))
</script>
