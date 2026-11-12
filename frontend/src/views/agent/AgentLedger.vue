<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">余额流水</h1>
    <div class="card p-4 flex flex-wrap items-end gap-3">
      <label class="block"><span class="block text-sm mb-1">开始日期</span><input v-model="from" type="date" class="input" /></label>
      <label class="block"><span class="block text-sm mb-1">结束日期</span><input v-model="to" type="date" class="input" /></label>
      <button class="btn-primary" type="button" @click="search">查询</button>
      <button class="btn-ghost" type="button" :disabled="!exportPath || !inputValid" @click="exportCsv">导出 CSV</button>
      <span class="text-xs opacity-70">导出的是上次查询的日期范围</span>
    </div>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <div class="grid grid-cols-2 lg:grid-cols-4 gap-3">
      <div class="card p-3"><div class="text-sm opacity-70">期初余额</div><div class="font-semibold">{{ fmtUsd(summary.opening_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">收入</div><div class="font-semibold">{{ fmtUsd(summary.income_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">支出</div><div class="font-semibold">{{ fmtUsd(summary.expense_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">期末余额</div><div class="font-semibold">{{ fmtUsd(summary.closing_cents) }}</div></div>
    </div>
    <!-- 手机和平板（< 1024px）：一条流水一张卡片。表格在窄屏上会把备注挤成一字一行，外部单号还会把来源列撑宽 -->
    <ul class="card p-2 text-sm divide-y lg:hidden" data-agent-list>
      <li v-for="l in items" :key="l.id" class="px-2 py-3 space-y-1">
        <div class="flex items-baseline justify-between gap-3">
          <span class="font-medium">{{ LEDGER_KIND_LABELS[l.kind] || l.kind }}</span>
          <span class="font-semibold whitespace-nowrap">{{ fmtUsd(l.amount_cents) }}</span>
        </div>
        <div class="flex items-baseline justify-between gap-3 text-xs opacity-70">
          <span class="whitespace-nowrap">{{ fmtBeijing(l.created_at) }}</span>
          <span class="whitespace-nowrap">变动后余额 {{ fmtUsd(l.balance_after_cents) }}</span>
        </div>
        <div v-if="l.order_id || sourceLabel(l.source) || l.external_ref" class="flex flex-wrap gap-x-4 text-xs opacity-70">
          <span v-if="l.order_id">订单 <router-link :to="`/agent/orders/${l.order_id}`" class="underline">#{{ l.order_id }}</router-link></span>
          <span v-if="sourceLabel(l.source) || l.external_ref" class="min-w-0 [overflow-wrap:anywhere]">来源 {{ sourceLabel(l.source) }} {{ l.external_ref }}</span>
        </div>
        <p v-if="l.note" class="text-xs [overflow-wrap:anywhere]">{{ l.note }}</p>
      </li>
      <li v-if="!items.length" class="p-3 opacity-70">没有流水</li>
    </ul>
    <!-- 电脑（>= 1024px）：表格；来源和备注列至少 10rem，外部单号和长备注只在本列折行 -->
    <div class="card p-2 overflow-x-auto hidden lg:block">
      <table class="w-full text-sm">
        <thead><tr class="text-left"><th class="p-2">时间</th><th class="p-2">类型</th><th class="p-2">金额</th><th class="p-2">变动后余额</th><th class="p-2">订单</th><th class="p-2">来源</th><th class="p-2">备注</th></tr></thead>
        <tbody>
          <tr v-for="l in items" :key="l.id" class="border-t">
            <td class="p-2 whitespace-nowrap">{{ fmtBeijing(l.created_at) }}</td>
            <td class="p-2 whitespace-nowrap">{{ LEDGER_KIND_LABELS[l.kind] || l.kind }}</td>
            <td class="p-2 whitespace-nowrap">{{ fmtUsd(l.amount_cents) }}</td><td class="p-2 whitespace-nowrap">{{ fmtUsd(l.balance_after_cents) }}</td>
            <td class="p-2"><router-link v-if="l.order_id" :to="`/agent/orders/${l.order_id}`" class="underline">#{{ l.order_id }}</router-link></td>
            <td class="p-2 min-w-[10rem]"><span class="whitespace-nowrap">{{ sourceLabel(l.source) }}</span><span v-if="l.external_ref" class="opacity-70 [overflow-wrap:anywhere]"> {{ l.external_ref }}</span></td>
            <td class="p-2 min-w-[10rem] [overflow-wrap:anywhere]">{{ l.note }}</td>
          </tr>
          <tr v-if="!items.length"><td colspan="7" class="p-3 opacity-70">没有流水</td></tr>
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
import { agentApi, agentDownload, agentErrorText } from '../../lib/agentApi'
import { downloadBlob } from '../../lib/agentExport'
import { fmtUsd } from '../../lib/agentMoney'
import { beijingRange, defaultLedgerRange, ledgerExportPath, ledgerQuery, LEDGER_KIND_LABELS } from '../../lib/agentViews'
import { sourceLabel } from '../../lib/agentShared'
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

// 导出和翻页都用上次查询时的日期快照；输入框里的日期不合法时禁用导出
// 导出用 fetch 下载（带 cookie）；会话失效走统一的登录跳转，不再裸打开 401 的 JSON
const snap = ref({ from: def.from, to: def.to })
const inputValid = computed(() => !!beijingRange(from.value, to.value))
const exportPath = computed(() => ledgerExportPath(snap.value.from, snap.value.to))

async function exportCsv() {
  if (!exportPath.value) return
  try {
    const r = await agentDownload(exportPath.value)
    if (!r.ok || !r.blob) {
      err.value = agentErrorText(r.data, r.status)
      return
    }
    err.value = ''
    // 直接用字节建文件，保留后端写的 BOM
    downloadBlob(r.filename || 'agent-ledger.csv', new Blob([await r.blob.arrayBuffer()], { type: 'text/csv;charset=utf-8' }))
  } catch { err.value = '网络异常，请稍后再试' }
}

function search() {
  if (!beijingRange(from.value, to.value)) { err.value = '日期范围不正确'; return }
  snap.value = { from: from.value, to: to.value }
  go(1)
}

async function go(p: number) {
  const r0 = beijingRange(snap.value.from, snap.value.to)
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
