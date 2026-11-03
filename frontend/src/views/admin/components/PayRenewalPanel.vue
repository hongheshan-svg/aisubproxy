<template>
  <div v-if="applies" class="space-y-2" data-testid="renewal-panel">
    <div class="font-semibold">关闭续费</div>
    <div class="text-sm flex flex-wrap items-center gap-2">
      <el-tag size="small" :type="renewalTagType(order.renewal_state)" data-testid="renewal-state">{{ renewalLabel(order.renewal_state) }}</el-tag>
      <span class="text-muted">已尝试 {{ order.renewal_attempts || 0 }} 次；最近核对 {{ fmtTs(order.renewal_checked_at) }}</span>
      <span v-if="order.renewal_state === 'pending' && order.renewal_next_at" class="text-muted">；下次自动尝试 {{ fmtTs(order.renewal_next_at) }}</span>
    </div>
    <p class="text-xs text-muted">
      本站开通的套餐只开一个月：开通成功后系统用这张单保存的用户会话关闭自动续费，并核实不再续费。会话最多保留 1 小时，清空后系统就没法再关。
    </p>
    <div v-if="needsUser" class="alert alert-warning text-sm" role="alert" data-testid="renewal-manual">
      续费未关闭。{{ RENEWAL_MANUAL_HINT }}。
    </div>
    <div class="flex flex-wrap items-center gap-2">
      <el-button size="small" :disabled="!!block" :loading="retrying" data-testid="renewal-retry" @click="retry">重试关闭续费</el-button>
      <span v-if="block" class="text-xs text-muted">{{ block }}</span>
    </div>
  </div>
</template>

<script setup lang="ts">
// 订单详情里的「关闭续费」：显示进度，会话还在且没关掉时可以手动重试（不付款、不动卡池）。
import { computed, ref } from 'vue'
import { dialog } from '../../../lib/dialog'
import { errText, fmtTs } from '../../../lib/cardpool'
import { payApi } from '../../../lib/pay'
import {
  RENEWAL_MANUAL_HINT, renewalLabel, renewalNeedsUser, renewalRetryBlock, renewalTagType, wantsRenewalCancel,
} from '../../../lib/payui'

const props = defineProps<{ order: any }>()
const emit = defineEmits<{ updated: [order: any] }>()

// 成功的真实订阅开通单，或者已经有续费进度的单（例如人工定论成功后排上的）才显示
const applies = computed(() => wantsRenewalCancel(props.order) || !!props.order?.renewal_state)
const needsUser = computed(() => renewalNeedsUser(props.order))
const block = computed(() => renewalRetryBlock(props.order))
const retrying = ref(false)

async function retry() {
  if (retrying.value || block.value) return
  const no = String(props.order.order_no)
  const ok = await dialog.confirm(
    `用这张单保存的用户会话再关闭一次续费并核实。\n不会付款，也不动卡池。\n单号：${no}`,
    { title: '重试关闭续费', okText: '重试' },
  )
  if (!ok) return
  retrying.value = true
  const r = await payApi<{ order: any }>('/orders/' + encodeURIComponent(no) + '/renewal/retry', 'POST')
  retrying.value = false
  if (!r.ok) {
    dialog.toast(errText(r, '重试关闭续费失败'), 'err')
    emit('updated', null) // 状态可能已经变了，让详情重新读一次
    return
  }
  const st = r.data?.order?.renewal_state
  if (st === 'cancelled') dialog.toast('续费已关闭并核实')
  else dialog.toast(`续费仍未关闭（${renewalLabel(st || '')}），详情见时间线`, 'warn')
  emit('updated', r.data?.order || null)
}
</script>
