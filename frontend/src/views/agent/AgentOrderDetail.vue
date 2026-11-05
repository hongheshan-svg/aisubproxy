<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">订单详情</h1>
    <router-link to="/agent/orders" class="text-sm underline">返回订单列表</router-link>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <dl v-if="order" class="card p-4 grid grid-cols-1 sm:grid-cols-2 gap-x-6 gap-y-2 text-sm">
      <div><dt class="opacity-70">订单号</dt><dd>#{{ order.id }}</dd></div>
      <div><dt class="opacity-70">时间</dt><dd>{{ fmtBeijing(order.created_at) }}</dd></div>
      <div><dt class="opacity-70">商品</dt><dd>{{ order.product_name }}（{{ order.product_code }}）</dd></div>
      <div><dt class="opacity-70">套餐 / 地区</dt><dd>{{ order.plan }} / {{ order.region }}</dd></div>
      <div><dt class="opacity-70">数量 × 单价</dt><dd>{{ order.quantity }} × {{ fmtUsd(order.unit_price_cents) }}</dd></div>
      <div><dt class="opacity-70">总价</dt><dd>{{ fmtUsd(order.total_cents) }}</dd></div>
      <div><dt class="opacity-70">来源</dt><dd>{{ sourceLabel(order.source) || '未知' }}</dd></div>
      <div v-if="order.external_ref"><dt class="opacity-70">对方订单号</dt><dd class="break-all">{{ order.external_ref }}</dd></div>
      <div class="sm:col-span-2"><dt class="opacity-70">备注</dt><dd class="break-all">{{ order.note || '无' }}</dd></div>
    </dl>
    <div v-if="order" class="card p-4 space-y-3">
      <button type="button" class="btn-primary" :disabled="busy" @click="reexport">{{ busy ? '导出中…' : '重新导出这笔订单的码' }}</button>
      <template v-if="codes">
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn-ghost" @click="copyAll">复制全部</button>
          <button type="button" class="btn-ghost" @click="downloadTxt">下载 .txt</button>
          <button type="button" class="btn-ghost" @click="downloadCsv">下载 .csv</button>
        </div>
        <p v-if="txt.skipped" class="text-sm text-amber-600">有 {{ txt.skipped }} 张无法取回，请联系管理员</p>
        <textarea class="input w-full font-mono text-xs" rows="8" readonly :value="txt.text"></textarea>
        <AgentOrderCodes :codes="codes" />
      </template>
      <p v-if="msg" class="text-sm">{{ msg }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { codesCsv, codesTxt, downloadText, type ExportedCode } from '../../lib/agentExport'
import { copyToClipboard } from '../../lib/clipboard'
import AgentOrderCodes from '../../components/AgentOrderCodes.vue'
import { sourceLabel } from '../../lib/agentShared'
import { fmtBeijing } from '../../lib/mwwebhook'

const route = useRoute()
const id = String(route.params.id || '')
const order = ref<any>(null)
const codes = ref<ExportedCode[] | null>(null)
const err = ref('')
const msg = ref('')
const busy = ref(false)
const txt = computed(() => codesTxt(codes.value || []))

onMounted(async () => {
  try {
    const r = await agentApi(`/orders/${encodeURIComponent(id)}`)
    if (r.ok) order.value = r.data.order
    else err.value = r.status === 404 ? '订单不存在' : agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' }
})

async function reexport() {
  busy.value = true; msg.value = ''
  try {
    const r = await agentApi(`/orders/${encodeURIComponent(id)}/export`, 'POST')
    if (r.ok) codes.value = r.data.codes || []
    else msg.value = agentErrorText(r.data, r.status)
  } catch { msg.value = '网络异常，请稍后再试' } finally { busy.value = false }
}
async function copyAll() { msg.value = (await copyToClipboard(txt.value.text)) ? '已复制' : '复制失败，请手动选择文本' }
function downloadCsv() {
  const o = order.value || {}
  const rows = (codes.value || []).map((c) => ({ ...c, product_code: o.product_code, plan: o.plan, region: o.region, created_at: o.created_at, order_id: o.id, note: o.note, source: o.source, external_ref: o.external_ref }))
  downloadText(`order-${id}.csv`, codesCsv(rows), 'text/csv')
}
function downloadTxt() { downloadText(`order-${id}.txt`, txt.value.text, 'text/plain') }
</script>
