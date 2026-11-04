<template>
  <div class="space-y-4" data-page="agent-list">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <h1 class="text-xl font-semibold">代理商</h1>
      <div class="flex gap-2">
        <el-button :loading="loading" @click="load">刷新</el-button>
        <el-button type="primary" @click="openCreate">新建代理商</el-button>
      </div>
    </div>

    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>

    <!-- 对账总览 -->
    <section class="grid gap-3 sm:grid-cols-3">
      <div class="card p-3"><div class="text-sm opacity-70">代理商余额合计</div><div class="font-semibold text-lg">{{ fmtUsd(summary.total_balance_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">今日下单</div><div class="font-semibold text-lg">{{ fmtUsd(summary.today_order_cents) }}</div></div>
      <div class="card p-3"><div class="text-sm opacity-70">本月下单</div><div class="font-semibold text-lg">{{ fmtUsd(summary.month_order_cents) }}</div></div>
    </section>

    <section v-if="summary.reconcile" class="card p-3 text-sm space-y-2" :class="summary.reconcile.ok ? '' : 'recon-bad'" data-testid="reconcile">
      <div v-if="summary.reconcile.ok">对账正常（{{ fmtBeijing(summary.reconcile.at) }}，北京时间）：每个代理商的余额都等于流水累计。</div>
      <template v-else>
        <div class="font-semibold" role="alert">对账不一致（{{ fmtBeijing(summary.reconcile.at) }}，北京时间）：下列代理商的余额和流水累计对不上，请先核对再调账。</div>
        <ul class="list-disc pl-5">
          <li v-for="m in summary.reconcile.mismatches" :key="m.agent_id">
            <router-link :to="`/ops/agents/${m.agent_id}`" class="underline">{{ nameOf(m.agent_id) }}</router-link>
            ：相差 {{ fmtUsd(m.diff_cents) }}
          </li>
        </ul>
      </template>
    </section>
    <section v-else-if="loaded" class="card p-3 text-sm opacity-70">还没有对账记录。</section>

    <el-table :data="items" size="small" stripe v-loading="loading" empty-text="还没有代理商">
      <el-table-column label="登录名" min-width="130">
        <template #default="{ row }"><router-link :to="`/ops/agents/${row.id}`" class="underline mono">{{ row.username }}</router-link></template>
      </el-table-column>
      <el-table-column prop="display_name" label="名称" min-width="120" show-overflow-tooltip />
      <el-table-column prop="tier_name" label="等级" width="110" />
      <el-table-column label="余额" width="120"><template #default="{ row }">{{ fmtUsd(row.balance_cents) }}</template></el-table-column>
      <el-table-column label="状态" width="90">
        <template #default="{ row }"><el-tag size="small" :type="row.status === 'active' ? 'success' : 'info'">{{ row.status === 'active' ? '启用' : '已停用' }}</el-tag></template>
      </el-table-column>
      <el-table-column prop="code_count" label="码数" width="80" />
      <el-table-column label="最近登录" min-width="150"><template #default="{ row }">{{ row.last_login_at ? fmtBeijing(row.last_login_at) : '从未' }}</template></el-table-column>
      <el-table-column label="操作" width="80"><template #default="{ row }"><router-link :to="`/ops/agents/${row.id}`" class="underline">详情</router-link></template></el-table-column>
    </el-table>

    <el-dialog v-model="createOpen" title="新建代理商" width="min(480px, 96vw)" append-to-body :close-on-click-modal="false">
      <div class="space-y-3">
        <label class="block text-sm"><span class="block mb-1">登录名（小写字母、数字等，大小写不敏感）</span><el-input v-model="form.username" maxlength="64" /></label>
        <label class="block text-sm"><span class="block mb-1">名称</span><el-input v-model="form.display_name" maxlength="100" /></label>
        <label class="block text-sm"><span class="block mb-1">价格等级</span>
          <el-select v-model="form.tier_id" class="w-full" placeholder="选择等级">
            <el-option v-for="t in tiers" :key="t.id" :label="t.name" :value="t.id" />
          </el-select>
        </label>
        <label class="block text-sm"><span class="block mb-1">备注（选填）</span><el-input v-model="form.note" maxlength="200" /></label>
        <p v-if="createErr" class="text-sm text-red-500" role="alert">{{ createErr }}</p>
        <p v-if="!tiers.length" class="text-sm opacity-70">还没有价格等级，请先到「批发商品与价格」建一个。</p>
      </div>
      <template #footer>
        <el-button @click="createOpen = false">取消</el-button>
        <el-button type="primary" :loading="creating" :disabled="creating || !form.username.trim() || !form.tier_id" @click="create">创建</el-button>
      </template>
    </el-dialog>

    <AgentPasswordDialog :password="newPassword" :username="newUsername" title="代理商已创建：初始密码" @close="clearPassword" />
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import AgentPasswordDialog from './components/AgentPasswordDialog.vue'
import { adminApi } from '../../lib/provider'
import { adminErrorText, USERNAME_TAKEN_HINT } from '../../lib/agentAdmin'
import { fmtUsd } from '../../lib/agentMoney'
import { fmtBeijing } from '../../lib/mwwebhook'
import { dialog } from '../../lib/dialog'

const items = ref<any[]>([])
const tiers = ref<any[]>([])
const summary = ref<any>({ total_balance_cents: 0, today_order_cents: 0, month_order_cents: 0, reconcile: null })
const loading = ref(false)
const loaded = ref(false)
const err = ref('')

async function load() {
  loading.value = true
  err.value = ''
  const [a, s, t] = await Promise.all([adminApi<any>('/agents'), adminApi<any>('/agent-summary'), adminApi<any>('/agent-tiers')])
  loading.value = false
  if (a.ok) items.value = a.data.items || []
  else err.value = '代理商列表读取失败：' + adminErrorText(a.data, a.status)
  if (s.ok) summary.value = { ...summary.value, ...s.data }
  else err.value = (err.value ? err.value + '；' : '') + '对账总览读取失败：' + adminErrorText(s.data, s.status)
  if (t.ok) tiers.value = t.data.items || []
  loaded.value = a.ok && s.ok
}

function nameOf(id: number): string {
  const a = items.value.find((x) => x.id === id)
  return a ? `${a.username}（${a.display_name || '无名称'}）` : `#${id}`
}

// ---------- 新建 ----------
const createOpen = ref(false)
const creating = ref(false)
const createErr = ref('')
let createUncertain = false // 上一次建号请求结果未知（网络异常或 5xx）
const form = reactive({ username: '', display_name: '', tier_id: 0 as number, note: '' })
const newPassword = ref('')
const newUsername = ref('')

function openCreate() {
  Object.assign(form, { username: '', display_name: '', tier_id: tiers.value[0]?.id || 0, note: '' })
  createErr.value = ''
  createUncertain = false
  createOpen.value = true
}

async function create() {
  if (creating.value) return
  creating.value = true
  createErr.value = ''
  const r = await adminApi<any>('/agents', 'POST', {
    username: form.username.trim(), display_name: form.display_name.trim(), tier_id: form.tier_id, note: form.note.trim(),
  })
  creating.value = false
  if (!r.ok) {
    let msg = adminErrorText(r.data, r.status)
    if (r.status === 0 || r.status >= 500) createUncertain = true
    else if (r.data?.error === 'username_taken' && createUncertain) msg += '。' + USERNAME_TAKEN_HINT
    createErr.value = msg
    return
  }
  createUncertain = false
  createOpen.value = false
  newUsername.value = String(r.data.agent?.username || '')
  newPassword.value = String(r.data.password || '')
  dialog.toast('代理商已创建')
  load()
}

function clearPassword() { newPassword.value = ''; newUsername.value = '' }

onMounted(load)
</script>

<style scoped>
.recon-bad { border-color: #dc2626; background: rgba(220, 38, 38, 0.08); color: #dc2626; }
</style>
