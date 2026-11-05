<template>
  <div class="space-y-3" data-testid="agent-shared-panel">
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <div v-if="data" class="space-y-3">
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 text-sm">
        <div><div class="opacity-70">状态</div><div class="font-semibold">{{ view.statusText }}</div></div>
        <div><div class="opacity-70">商户 ID</div><div class="mono break-all">{{ data.app_id || '—' }}</div></div>
        <div><div class="opacity-70">最后使用时间</div><div>{{ data.last_used_at ? fmtBeijing(data.last_used_at) : '—' }}</div></div>
        <div><div class="opacity-70">IP 白名单</div><div class="break-all">{{ view.allowlistText }}</div></div>
        <div><div class="opacity-70">余额提醒线</div><div>{{ data.low_balance_cents ? fmtUsd(data.low_balance_cents) : '未设置' }}</div></div>
      </div>
      <div class="flex flex-wrap gap-2">
        <el-button type="danger" plain :disabled="!view.canDisable || busy" :loading="busy" @click="change(false)">关闭对接</el-button>
        <el-button type="success" plain :disabled="!view.canEnable || busy" :loading="busy" @click="change(true)">重新允许</el-button>
      </div>
      <div class="text-sm font-semibold">最近取码失败（最多 50 条）</div>
      <el-table :data="data.failures || []" size="small" stripe empty-text="没有失败记录">
        <el-table-column label="时间" width="160"><template #default="{ row }">{{ row.last_at ? fmtBeijing(row.last_at) : '—' }}</template></el-table-column>
        <el-table-column prop="request_no" label="对方订单号" min-width="150" show-overflow-tooltip />
        <el-table-column prop="product_code" label="商品" min-width="130" show-overflow-tooltip />
        <el-table-column prop="quantity" label="数量" width="70" />
        <el-table-column label="原因" min-width="110"><template #default="{ row }">{{ FAILURE_REASON_TEXT[row.reason] || row.reason }}</template></el-table-column>
        <el-table-column prop="attempts" label="次数" width="70" />
        <el-table-column label="状态" width="90"><template #default="{ row }">{{ row.resolved_order_id ? '已解决' : '未解决' }}</template></el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { adminApi } from '../../../lib/provider'
import { adminErrorText, adminSharedView } from '../../../lib/agentAdmin'
import { FAILURE_REASON_TEXT } from '../../../lib/agentShared'
import { fmtUsd } from '../../../lib/agentMoney'
import { fmtBeijing } from '../../../lib/mwwebhook'
import { dialog } from '../../../lib/dialog'

const props = defineProps<{ agentId: number }>()
const data = ref<any>(null)
const err = ref('')
const busy = ref(false)
const view = computed(() => adminSharedView(data.value))

async function load() {
  const aid = props.agentId
  const r = await adminApi<any>(`/agents/${aid}/shared`)
  if (aid !== props.agentId) return
  if (!r.ok) { err.value = '读取失败：' + adminErrorText(r.data, r.status); return }
  err.value = ''
  data.value = r.data
}

async function change(enable: boolean) {
  if (busy.value) return
  const ok = await dialog.confirm(
    enable ? '重新允许后这个代理商原来的商户密钥继续可用，确定重新允许？' : '关闭后这个代理商的异次元站会立即取不到货，确定关闭？',
    { title: enable ? '重新允许对接' : '关闭对接', okText: enable ? '重新允许' : '关闭', danger: !enable },
  )
  if (!ok) return
  busy.value = true
  const aid = props.agentId
  const r = await adminApi<any>(`/agents/${aid}/shared/${enable ? 'enable' : 'disable'}`, 'POST')
  busy.value = false
  if (!r.ok) { dialog.toast(adminErrorText(r.data, r.status), 'err'); return }
  dialog.toast(enable ? '已重新允许' : '已关闭对接')
  if (aid === props.agentId) await load()
}

watch(() => props.agentId, () => { data.value = null; err.value = ''; load() })
onMounted(load)
</script>
