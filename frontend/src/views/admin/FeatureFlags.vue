<template>
  <div class="space-y-4">
    <AdminPageHeader
      class="!mb-0"
      title="功能开关"
      description="还没准备好上线的功能先关着，用户和代理商都看不到。改开关立即生效，不用重启，会记进审计日志。"
    />

    <div class="card space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 class="font-semibold">代理商功能</h3>
          <p class="text-sm text-muted mt-1">当前：{{ statusText }}</p>
        </div>
        <el-switch
          :model-value="adminFeatures.agent"
          :disabled="!adminFeatures.loaded || busy"
          :loading="busy"
          active-text="打开"
          inactive-text="关闭"
          @change="onToggle"
        />
      </div>
      <div class="text-sm space-y-1">
        <p>这个开关管：</p>
        <ul class="list-disc pl-5 text-muted">
          <li>代理商后台（/agent）：关闭时登录页只显示「代理商功能暂未开放」；</li>
          <li>异次元对接（/shared/*）：关闭时代理商的发卡站取不到货；</li>
          <li>后台「代理商」菜单（代理商、批发商品与价格）。</li>
        </ul>
        <p class="text-subtle">不受影响：老的代理换码页（/partner/swap）、首页售后查询、兑换流程。关闭不会删除任何数据，重新打开即恢复。</p>
      </div>
      <p v-if="loadError" class="text-sm text-warn" role="alert">{{ loadError }}</p>
      <p v-if="saveError" class="text-sm text-err" role="alert">{{ saveError }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { dialog } from '../../lib/dialog'
import { adminFeatures, loadAdminFeatures, saveAgentFeature } from '../../lib/features'

const busy = ref(false)
const saveError = ref('')
const loadError = ref('')

const statusText = computed(() => {
  if (!adminFeatures.loaded) return '读取中…'
  return adminFeatures.agent ? '已打开' : '已关闭'
})

async function load() {
  loadError.value = ''
  await loadAdminFeatures()
  if (!adminFeatures.loaded) loadError.value = '读取开关失败，请刷新页面重试'
}

async function onToggle(v: string | number | boolean) {
  const on = v === true
  const msg = on
    ? '打开后，代理商可以登录后台、下单取码，异次元对接开始接单，后台显示「代理商」菜单。确定打开吗？'
    : '关闭后，代理商不能登录，异次元那边会取不到货（客户付了款也拿不到码），后台隐藏「代理商」菜单。数据都保留，重新打开即恢复。确定关闭吗？'
  if (!(await dialog.confirm(msg, { title: on ? '打开代理商功能' : '关闭代理商功能', okText: on ? '打开' : '关闭', danger: !on }))) return
  busy.value = true
  saveError.value = ''
  try {
    saveError.value = await saveAgentFeature(on)
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>
