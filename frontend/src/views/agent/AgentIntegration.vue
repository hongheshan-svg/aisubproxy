<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">对接异次元</h1>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
    <p v-if="msg" class="text-sm" role="status">{{ msg }}</p>

    <div v-if="info" class="card p-4 space-y-3 text-sm">
      <div>状态：<span class="font-semibold">{{ SHARED_STATUS_TEXT[info.status as SharedStatus] || info.status }}</span></div>
      <template v-if="info.status === 'inactive'">
        <p>把本站的商品接到你的异次元（acg-faka）店铺，客户在异次元下单，本站自动扣你的余额并出码。条件：acg-faka 版本不低于 {{ info.min_acg_version || '3.6.1' }}，本站使用 HTTPS。</p>
      </template>
      <p v-if="info.status === 'admin_disabled'" class="text-amber-600">管理员已关闭你的对接，生成和重置密钥暂不可用。</p>
      <template v-if="info.status === 'active'">
        <div class="flex flex-wrap items-center gap-2"><span class="opacity-70">上游地址</span><code class="break-all">{{ origin }}</code><button type="button" class="btn-ghost" @click="copy(origin)">复制</button></div>
        <div class="flex flex-wrap items-center gap-2"><span class="opacity-70">商户 ID</span><code class="break-all">{{ info.app_id }}</code><button type="button" class="btn-ghost" @click="copy(info.app_id)">复制</button></div>
        <div><span class="opacity-70">密钥生成时间</span>　{{ info.key_created_at ? fmtBeijing(info.key_created_at) : '无' }}</div>
        <div><span class="opacity-70">最后使用时间</span>　{{ info.last_used_at ? fmtBeijing(info.last_used_at) : '从未使用' }}</div>
        <p v-if="info.key_invalid" class="text-red-500" role="alert">{{ KEY_INVALID_TEXT }}</p>
      </template>
      <div class="flex flex-wrap gap-2">
        <button v-if="info.status !== 'active'" type="button" class="btn-primary" :disabled="info.status === 'admin_disabled'" @click="ask('create')">生成商户密钥</button>
        <template v-else>
          <button type="button" class="btn-primary" @click="ask('reset')">重置密钥</button>
          <button type="button" class="btn-ghost" @click="ask('disable')">停用对接</button>
        </template>
      </div>
      <p v-if="info.status === 'active'" class="text-xs opacity-70">{{ RESET_WARNING }}</p>
    </div>

    <div v-if="keyInfo.app_key" class="card p-4 space-y-2 text-sm border border-red-300" role="dialog" aria-label="商户密钥">
      <p class="text-red-500 font-semibold">{{ KEY_ONCE_WARNING }}</p>
      <p class="text-xs opacity-70">{{ LOCK_HINT }}</p>
      <div class="flex flex-wrap items-center gap-2"><span class="opacity-70">上游地址</span><code class="break-all">{{ origin }}</code><button type="button" class="btn-ghost" @click="copy(origin)">复制</button></div>
      <div class="flex flex-wrap items-center gap-2"><span class="opacity-70">商户 ID</span><code class="break-all">{{ keyInfo.app_id }}</code><button type="button" class="btn-ghost" @click="copy(keyInfo.app_id)">复制</button></div>
      <div class="flex flex-wrap items-center gap-2"><span class="opacity-70">商户密钥</span><code class="break-all">{{ keyInfo.app_key }}</code><button type="button" class="btn-ghost" @click="copy(keyInfo.app_key)">复制</button></div>
      <button type="button" class="btn-primary" @click="clearKey">我已保存，关闭</button>
    </div>

    <div v-if="info && info.status === 'active'" class="card p-4 space-y-2 text-sm">
      <h2 class="font-semibold">IP 白名单</h2>
      <p class="opacity-70">最多 5 项，支持单个地址或网段（如 10.0.0.0/8），用逗号、空格或换行分隔；留空表示不限制。</p>
      <textarea v-model="allowText" class="input w-full font-mono text-xs" rows="3" aria-label="IP 白名单"></textarea>
      <button type="button" class="btn-ghost" @click="ask('allowlist')">保存白名单</button>
    </div>

    <form v-if="info" class="card p-4 space-y-2 text-sm" @submit.prevent="saveLow">
      <h2 class="font-semibold">余额提醒线</h2>
      <p class="opacity-70">余额低于这个金额时，总览页会提醒你。填 0 或留空表示关闭，最高 10000 美元。</p>
      <div class="flex flex-wrap items-center gap-2">
        <span>$</span><input v-model.trim="lowText" class="input" inputmode="decimal" aria-label="余额提醒线（美元）" />
        <button type="submit" class="btn-ghost" :disabled="lowBusy">{{ lowBusy ? '保存中…' : '保存' }}</button>
      </div>
      <p v-if="lowErr" class="text-red-500" role="alert">{{ lowErr }}</p>
    </form>

    <div class="card p-4 text-sm">
      <h2 class="font-semibold mb-2">接入步骤</h2>
      <ol class="list-decimal pl-5 space-y-1">
        <li v-for="(s, i) in ACG_SETUP_STEPS" :key="i" :class="{ 'text-red-500': i === 1 }">{{ s }}</li>
      </ol>
    </div>

    <div class="card p-2 overflow-x-auto">
      <h2 class="font-semibold p-2">最近失败记录</h2>
      <table class="w-full text-sm">
        <thead><tr class="text-left"><th class="p-2">时间</th><th class="p-2">对方订单号</th><th class="p-2">商品</th><th class="p-2">数量</th><th class="p-2">原因</th><th class="p-2">次数</th><th class="p-2">状态</th></tr></thead>
        <tbody>
          <tr v-for="f in failures" :key="f.id" class="border-t">
            <td class="p-2 whitespace-nowrap">{{ fmtBeijing(f.last_at) }}</td>
            <td class="p-2 break-all">{{ f.request_no }}</td>
            <td class="p-2">{{ f.product_code }}</td>
            <td class="p-2">{{ f.quantity }}</td>
            <td class="p-2">{{ FAILURE_REASON_TEXT[f.reason] || f.reason }}</td>
            <td class="p-2">{{ f.attempts }}</td>
            <td class="p-2">{{ f.resolved_order_id ? '已解决' : '未解决' }}</td>
          </tr>
          <tr v-if="!failures.length"><td colspan="7" class="p-3 opacity-70">暂无失败记录</td></tr>
        </tbody>
      </table>
    </div>
    <div class="flex items-center gap-3 text-sm">
      <button class="btn-ghost" :disabled="page <= 1" @click="loadFailures(page - 1)">上一页</button>
      <span>第 {{ page }} 页 / 共 {{ total }} 条</span>
      <button class="btn-ghost" :disabled="page * PAGE >= total" @click="loadFailures(page + 1)">下一页</button>
    </div>

    <div v-if="action" class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" role="dialog" aria-modal="true" :aria-label="ACTION_TITLE[action]">
      <form class="card p-4 space-y-3 w-full max-w-sm" @submit.prevent="confirm">
        <h2 class="font-semibold">{{ ACTION_TITLE[action] }}</h2>
        <p v-if="action === 'reset'" class="text-sm text-red-500">{{ RESET_WARNING }}</p>
        <p v-if="action === 'reset'" class="text-xs opacity-70">{{ LOCK_HINT }}</p>
        <label class="block"><span class="block text-sm mb-1">动态码</span><input v-model.trim="code" class="input w-full" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required /></label>
        <p v-if="dlgErr" class="text-sm text-red-500" role="alert">{{ dlgErr }}</p>
        <div class="flex gap-2">
          <button class="btn-primary" type="submit" :disabled="busy || code.length !== 6">{{ busy ? '提交中…' : '确定' }}</button>
          <button class="btn-ghost" type="button" :disabled="busy" @click="closeDialog">取消</button>
        </div>
      </form>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { copyToClipboard } from '../../lib/clipboard'
import {
  ACG_SETUP_STEPS, FAILURE_REASON_TEXT, KEY_INVALID_TEXT, KEY_ONCE_WARNING, LOCK_HINT, RESET_WARNING, SHARED_STATUS_TEXT,
  parseAllowlistInput, parseLowBalanceInput, type SharedStatus,
} from '../../lib/agentShared'
import { fmtUsd } from '../../lib/agentMoney'
import { fmtBeijing } from '../../lib/mwwebhook'

type Action = 'create' | 'reset' | 'disable' | 'allowlist'
const ACTION_TITLE: Record<Action, string> = { create: '生成商户密钥', reset: '重置密钥', disable: '停用对接', allowlist: '保存 IP 白名单' }

const PAGE = 20
const origin = typeof location !== 'undefined' ? location.origin : ''
const info = ref<any>(null)
const err = ref('')
const msg = ref('')
// 商户密钥只放在这个 ref 里：不写存储、不进 URL、不打日志；关掉提示区或离开页面就清空
const keyInfo = ref({ app_id: '', app_key: '' })
const allowText = ref('')
const lowText = ref('')
const lowErr = ref('')
const lowBusy = ref(false)
const failures = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const action = ref<Action | null>(null)
const code = ref('')
const dlgErr = ref('')
const busy = ref(false)

function clearKey() { keyInfo.value = { app_id: '', app_key: '' } }

async function copy(text: string) {
  msg.value = (await copyToClipboard(text)) ? '已复制' : '复制失败，请手动选择文本'
}

async function load() {
  try {
    const r = await agentApi('/shared')
    if (!r.ok) { err.value = agentErrorText(r.data, r.status); return }
    err.value = ''
    info.value = r.data
    allowText.value = (r.data.ip_allowlist || []).join('\n')
    lowText.value = r.data.low_balance_cents > 0 ? (r.data.low_balance_cents / 100).toFixed(2) : ''
  } catch { err.value = '网络异常，请稍后再试' }
}

async function loadFailures(p: number) {
  try {
    const r = await agentApi(`/shared/failures?page=${p}&page_size=${PAGE}`)
    if (r.ok) { failures.value = r.data.items || []; total.value = r.data.total || 0; page.value = p }
    else err.value = agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' }
}

function ask(a: Action) {
  dlgErr.value = ''; msg.value = ''; code.value = ''
  if (a === 'allowlist') {
    const p = parseAllowlistInput(allowText.value)
    if (p.error) { err.value = p.error; return }
    err.value = ''
  }
  action.value = a
}
function closeDialog() { action.value = null; code.value = ''; dlgErr.value = '' }

async function confirm() {
  const a = action.value
  if (!a || busy.value) return
  busy.value = true; dlgErr.value = ''
  const c = code.value
  code.value = ''
  try {
    let r
    if (a === 'create' || a === 'reset') r = await agentApi('/shared/key', 'POST', { code: c })
    else if (a === 'disable') r = await agentApi('/shared/disable', 'POST', { code: c })
    else r = await agentApi('/shared/allowlist', 'PUT', { code: c, ips: parseAllowlistInput(allowText.value).ips })
    if (!r.ok) { dlgErr.value = agentErrorText(r.data, r.status); return }
    closeDialog()
    if (a === 'create' || a === 'reset') {
      keyInfo.value = { app_id: String(r.data?.app_id || ''), app_key: String(r.data?.app_key || '') }
      msg.value = ''
    } else {
      clearKey()
      msg.value = a === 'disable' ? '已停用对接' : '白名单已保存'
    }
    await load()
  } catch { dlgErr.value = '网络异常，请稍后再试' } finally { busy.value = false }
}

async function saveLow() {
  lowErr.value = ''; msg.value = ''
  const r0 = parseLowBalanceInput(lowText.value)
  if (!r0.ok) { lowErr.value = r0.error; return }
  lowBusy.value = true
  try {
    const r = await agentApi('/shared/low-balance', 'PUT', { low_balance_cents: r0.cents })
    if (r.ok) msg.value = r0.cents ? `提醒线已设为 ${fmtUsd(r0.cents)}` : '已关闭余额提醒'
    else lowErr.value = agentErrorText(r.data, r.status)
  } catch { lowErr.value = '网络异常，请稍后再试' } finally { lowBusy.value = false }
}

onMounted(() => { load(); loadFailures(1) })
onBeforeUnmount(() => { clearKey(); code.value = '' })
</script>
