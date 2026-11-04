<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">我的码</h1>
    <form class="card p-4 grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-5 gap-3" @submit.prevent="search">
      <label class="block"><span class="block text-sm mb-1">商品</span>
        <select v-model="f.product" class="input w-full"><option value="">全部</option><option v-for="p in products" :key="p.product_code" :value="p.product_code">{{ p.name }}</option></select></label>
      <label class="block"><span class="block text-sm mb-1">状态</span>
        <select v-model="f.status" class="input w-full"><option value="">全部</option><option v-for="(l, k) in CODE_STATUS_LABELS" :key="k" :value="k">{{ l }}</option></select></label>
      <label class="block"><span class="block text-sm mb-1">订单号</span><input v-model.trim="f.orderId" class="input w-full" inputmode="numeric" /></label>
      <label class="block"><span class="block text-sm mb-1">开始日期</span><input v-model="f.from" type="date" class="input w-full" /></label>
      <label class="block"><span class="block text-sm mb-1">结束日期</span><input v-model="f.to" type="date" class="input w-full" /></label>
      <div class="sm:col-span-2 lg:col-span-5 flex flex-wrap gap-2 items-center">
        <button class="btn-primary" type="submit">查询</button>
        <select v-model="fmt" class="input"><option value="txt">.txt</option><option value="csv">.csv</option></select>
        <button type="button" class="btn-ghost" :disabled="busy || !selected.size" @click="doExport({ ids: [...selected] })">导出勾选（{{ selected.size }}）</button>
        <button type="button" class="btn-ghost" :disabled="busy || !total" @click="exportFiltered">按当前筛选导出全部（{{ total }}）</button>
      </div>
    </form>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <p v-if="msg" class="text-sm text-amber-600" role="status">{{ msg }}</p>
    <div class="card p-2 overflow-x-auto">
      <table class="w-full text-sm">
        <thead><tr class="text-left"><th class="p-2"><input type="checkbox" :checked="allSelected" aria-label="全选本页" @change="toggleAll" /></th><th class="p-2">码</th><th class="p-2">商品</th><th class="p-2">状态</th><th class="p-2">订单</th><th class="p-2">邮箱</th><th class="p-2">购买时间</th></tr></thead>
        <tbody>
          <tr v-for="c in items" :key="c.id" class="border-t">
            <td class="p-2"><input type="checkbox" :checked="selected.has(c.id)" :aria-label="`选择 ${c.code_prefix}`" @change="toggle(c.id)" /></td>
            <td class="p-2 font-mono">{{ c.code_prefix }}…</td>
            <td class="p-2">{{ c.product_code }}</td>
            <td class="p-2">{{ CODE_STATUS_LABELS[c.status] || c.status }}</td>
            <td class="p-2"><router-link v-if="c.order_id" :to="`/agent/orders/${c.order_id}`" class="underline">#{{ c.order_id }}</router-link></td>
            <td class="p-2 break-all">{{ c.email || '' }}</td>
            <td class="p-2 whitespace-nowrap">{{ fmtBeijing(c.created_at) }}</td>
          </tr>
          <tr v-if="!items.length"><td colspan="7" class="p-3 opacity-70">没有码</td></tr>
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
import { computed, onMounted, reactive, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { codesCsv, codesTxt, downloadText } from '../../lib/agentExport'
import { beijingRange, CODE_STATUS_LABELS, codesQuery, EXPORT_MAX, exportBody, type CodeFilter } from '../../lib/agentViews'
import { fmtBeijing } from '../../lib/mwwebhook'

const PAGE = 50
const f = reactive<CodeFilter>({ product: '', status: '', orderId: '', from: '', to: '' })
const products = ref<any[]>([])
const items = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const err = ref('')
const msg = ref('')
const busy = ref(false)
const fmt = ref<'txt' | 'csv'>('txt')
const selected = ref(new Set<number>())
const allSelected = computed(() => items.value.length > 0 && items.value.every((c) => selected.value.has(c.id)))

function toggle(id: number) {
  const s = new Set(selected.value)
  s.has(id) ? s.delete(id) : s.add(id)
  selected.value = s
}
function toggleAll() {
  const s = new Set(selected.value)
  if (allSelected.value) items.value.forEach((c) => s.delete(c.id))
  else items.value.forEach((c) => s.add(c.id))
  selected.value = s
}

function dateOk(): boolean {
  if ((f.from || f.to) && !(f.from && f.to && beijingRange(f.from, f.to))) { err.value = '请同时填写正确的开始和结束日期'; return false }
  return true
}
function search() { err.value = ''; if (dateOk()) go(1) }

async function go(p: number) {
  page.value = p
  try {
    const r = await agentApi('/codes' + codesQuery(f, p, PAGE))
    if (r.ok) { items.value = r.data.items || []; total.value = r.data.total || 0; err.value = '' }
    else err.value = agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' }
}

function exportFiltered() {
  err.value = ''
  if (!dateOk()) return
  doExport({ filter: { ...f } })
}

async function doExport(src: { ids: number[] } | { filter: CodeFilter }) {
  err.value = ''; msg.value = ''
  const n = 'ids' in src ? src.ids.length : total.value
  if (n > EXPORT_MAX) { err.value = `一次最多导出 ${EXPORT_MAX} 张，请缩小范围后分批导出`; return }
  busy.value = true
  try {
    const r = await agentApi('/codes/export', 'POST', exportBody(src))
    if (!r.ok) { err.value = r.data?.error === 'export_too_large' ? `一次最多导出 ${EXPORT_MAX} 张，请缩小范围后分批导出` : agentErrorText(r.data, r.status); return }
    const codes = r.data.codes || []
    const stamp = new Date().toISOString().slice(0, 10)
    if (fmt.value === 'txt') {
      const t = codesTxt(codes)
      downloadText(`codes-${stamp}.txt`, t.text, 'text/plain')
      if (t.skipped) msg.value = `有 ${t.skipped} 张无法取回，请联系管理员`
    } else {
      downloadText(`codes-${stamp}.csv`, codesCsv(codes), 'text/csv')
    }
  } catch { err.value = '网络异常，请稍后再试' } finally { busy.value = false }
}

onMounted(async () => {
  go(1)
  try { const r = await agentApi('/products'); if (r.ok) products.value = r.data.items || [] } catch { /* 商品下拉拿不到不影响查询 */ }
})
</script>
