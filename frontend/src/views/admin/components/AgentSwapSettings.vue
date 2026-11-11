<template>
  <section class="card" data-testid="agent-swap-settings">
    <h2 class="text-base font-semibold text-ink">代理换码设置</h2>
    <p class="text-sm text-muted mt-0.5 mb-3">
      代理凭密码进入隐藏页，将<strong>失败且未扣款</strong>的 CDK 换一张新码。
    </p>
    <el-form label-position="top" class="max-w-3xl" @submit.prevent>
      <el-form-item label="代理换码密码">
        <el-input
          v-model="password"
          type="password"
          show-password
          clearable
          :placeholder="pwHint"
          autocomplete="new-password"
        />
        <p class="w-full text-xs text-subtle mt-1">
          留空保存=不修改；
          <el-tag size="small" :type="configured ? 'success' : 'info'" effect="plain">{{ configured ? '已设置' : '未设置' }}</el-tag>
        </p>
        <p v-if="weak" class="w-full text-xs mt-1 text-err" data-testid="swap-password-weak">
          当前密码不足 12 位，仍可使用，但请尽快换成至少 12 位的新密码。
        </p>
      </el-form-item>
      <el-form-item label="每日换码上限（张）">
        <el-input-number v-model="swapLimit" :min="1" :max="100000" :step="1" :precision="0" data-field="agent_swap_daily_limit" />
        <p class="w-full text-xs text-subtle mt-1">
          同一个换码密码每个北京日最多换这么多张，必须大于 0。每换一张都会发出一张新码，调高前先想清楚。
        </p>
      </el-form-item>
      <el-form-item label="发给代理的换码链接（可复制）">
        <div class="flex flex-wrap items-center gap-2 w-full">
          <el-input :model-value="agentSwapUrl" readonly class="!flex-1 mono" />
          <el-button type="primary" @click="copyText(agentSwapUrl)">复制链接</el-button>
          <el-button @click="openSwap">打开</el-button>
        </div>
        <p class="w-full text-xs text-subtle mt-1">
          路径固定 <code class="mono">/partner/swap</code>（短链 <code class="mono">/a/swap</code>）。导航栏不展示，把完整链接 + 密码发给代理即可。
        </p>
      </el-form-item>
      <el-button type="primary" :loading="saving" @click="save">保存代理换码设置</el-button>
    </el-form>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { authFetch } from '../../../lib/api'
import { dialog } from '../../../lib/dialog'
import { copyToClipboard } from '../../../lib/clipboard'

const password = ref('')
const swapLimit = ref<number | null>(null)
let swapLimitLoaded: number | null = null
const configured = ref(false)
const weak = ref(false)
const loaded = ref(false)
const saving = ref(false)

const pwHint = computed(() => (configured.value ? '已设置（留空保存不修改）' : '设置代理换码密码（至少 12 位）'))
const agentSwapUrl = typeof window !== 'undefined' ? `${window.location.origin}/partner/swap` : '/partner/swap'

function apply(d: Record<string, any>) {
  configured.value = !!d.agent_swap_password_configured
  weak.value = !!d.agent_swap_password_weak
  const n = Number(d.agent_swap_daily_limit)
  swapLimit.value = n > 0 ? n : null
  swapLimitLoaded = swapLimit.value
}

async function load() {
  try {
    const r = await authFetch('/api/v1/admin/settings')
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      loaded.value = false
      dialog.toast(d.error || '读取代理换码设置失败，页面上显示的不是当前值，请刷新后再保存', 'err')
      return
    }
    loaded.value = true
    apply(d)
  } catch {
    loaded.value = false
    dialog.toast('网络错误，读取代理换码设置失败，页面上显示的不是当前值，请刷新后再保存', 'err')
  }
}

async function save() {
  saving.value = true
  try {
    const body: Record<string, string | number> = {}
    // 每日换码上限：改了才发，必须是大于 0 的整数；改动要二次确认（每换一张都花钱买新码）
    if (loaded.value && swapLimit.value !== swapLimitLoaded) {
      const n = swapLimit.value
      if (!n || !Number.isInteger(n) || n <= 0) {
        dialog.toast('每日换码上限必须是大于 0 的整数', 'warn')
        return
      }
      const ok = await dialog.confirm(`把代理每日换码上限从 ${swapLimitLoaded ?? '默认'} 张改为 ${n} 张？每换一张都会发出一张新码。`, { title: '确认修改换码上限', okText: '确认修改', danger: true })
      if (!ok) return
      body.agent_swap_daily_limit = n
    }
    const pw = password.value.trim()
    if (pw) body.agent_swap_password = pw
    if (!Object.keys(body).length) {
      dialog.toast('没有改动', 'info')
      return
    }
    const r = await authFetch('/api/v1/admin/settings', { method: 'PUT', body: JSON.stringify(body) })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '保存失败', 'err')
      return
    }
    apply(d)
    loaded.value = true
    password.value = ''
    dialog.toast('已保存', 'ok')
  } catch {
    dialog.toast('网络错误，不确定是否已保存，请刷新页面核对', 'err')
  } finally {
    saving.value = false
  }
}

function openSwap() {
  window.open(agentSwapUrl, '_blank')
}

async function copyText(t: string) {
  const ok = await copyToClipboard(t)
  dialog.toast(ok ? '已复制' : '复制失败，请在文本框中全选手动复制', ok ? 'ok' : 'err')
}

onMounted(load)
</script>
