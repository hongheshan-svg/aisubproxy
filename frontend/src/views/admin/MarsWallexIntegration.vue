<template>
  <div class="space-y-4">
    <div v-if="cfg && !cfg.master_key_ready" class="card warn-box">
      服务器未配置 APP_MASTER_KEY，无法保存密钥（API Token、私钥）；地址、Developer ID、平台公钥仍可保存
      <span v-if="cfg.master_key_error" class="text-xs">（{{ cfg.master_key_error }}）</span>
    </div>

    <!-- 1. 环境与地址 -->
    <div class="card space-y-4">
      <div>
        <h2 class="text-xl font-bold text-ink">MarsWallex 对接</h2>
        <p class="text-sm text-muted mt-1">环境与地址</p>
      </div>
      <div class="flex flex-wrap gap-2">
        <el-button
          v-for="p in cfg?.base_url_presets || []"
          :key="p.id"
          round
          :type="presetActive === p.id ? 'primary' : 'default'"
          @click="form.base_url = p.url"
        >
          {{ p.label }}
        </el-button>
      </div>
      <el-input v-model="form.base_url" size="large" class="mono max-w-xl" placeholder="Base URL" />
    </div>

    <!-- 2. 凭证 -->
    <div class="card space-y-4">
      <h3 class="text-lg font-bold text-ink">凭证</h3>
      <el-form label-position="top" class="max-w-xl" @submit.prevent>
        <el-form-item label="Developer ID">
          <el-input v-model="form.developer_id" size="large" class="mono" />
        </el-form-item>
        <el-form-item label="API Token">
          <el-input
            v-model="secrets.api_token"
            type="password"
            show-password
            size="large"
            autocomplete="new-password"
            :disabled="!masterKeyReady"
            :placeholder="secretPlaceholder(cfg?.api_token_set, '请输入')"
          />
        </el-form-item>
        <el-form-item label="私钥（PEM）">
          <el-input
            v-model="secrets.private_key"
            type="textarea"
            :rows="5"
            class="mono"
            autocomplete="off"
            :disabled="!masterKeyReady"
            :placeholder="secretPlaceholder(cfg?.private_key_set, '粘贴私钥 PEM')"
          />
          <p v-if="cfg?.private_key_error" class="field-error">{{ cfg.private_key_error }}</p>
        </el-form-item>
        <el-form-item label="平台公钥（PEM）">
          <el-input
            v-model="secrets.platform_public_key"
            type="textarea"
            :rows="5"
            class="mono"
            autocomplete="off"
            :placeholder="cfg?.platform_public_key_set ? '已配置，留空不修改' : '粘贴平台公钥 PEM'"
          />
        </el-form-item>
      </el-form>

      <div v-if="cfg" class="space-y-2">
        <div class="text-xs uppercase tracking-wide text-muted">我们的公钥（提供给 MarsWallex）</div>
        <pre class="pem mono">{{ cfg.public_key_pem || '（尚未生成）' }}</pre>
        <el-button v-if="cfg.public_key_pem" size="small" @click="copyText(cfg.public_key_pem)">复制</el-button>
        <div class="text-xs text-subtle">
          我们的公钥指纹：<code class="mono">{{ cfg.public_key_fingerprint || '—' }}</code>
        </div>
        <div class="text-xs text-subtle">
          平台公钥指纹：<code class="mono">{{ cfg.platform_public_key_fingerprint || '—' }}</code>
        </div>
      </div>

      <el-button
        type="primary"
        size="large"
        :loading="saving"
        :disabled="!cfg"
        @click="save"
      >
        保存
      </el-button>
    </div>

    <!-- 3. 测试连接 -->
    <div class="card space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h3 class="text-lg font-bold text-ink">测试连接</h3>
        <el-button type="success" plain :loading="testing" @click="runTest">测试连接</el-button>
      </div>
      <div v-if="result" class="space-y-2">
        <div class="flex flex-wrap items-center gap-2">
          <el-tag :type="outcomeType" effect="dark">{{ result.outcome }}</el-tag>
          <span v-if="result.http_status" class="text-sm text-muted">HTTP {{ result.http_status }}</span>
          <span v-if="result.code" class="text-sm text-muted mono">{{ result.code }}</span>
        </div>
        <p v-if="result.hint" class="text-sm" :class="result.outcome === 'success' ? 'text-ink' : 'text-warn'">
          {{ result.hint }}
        </p>
        <p v-if="result.message || result.reason" class="text-xs text-subtle">
          {{ result.message }} {{ result.reason }}
        </p>
        <el-table v-if="balanceRows.length" :data="balanceRows" size="small" border>
          <el-table-column prop="account_no" label="账户" min-width="140" />
          <el-table-column prop="account_name" label="名称" min-width="120" />
          <el-table-column prop="currency" label="币种" width="90" />
          <el-table-column prop="available_balance" label="可用" min-width="110" />
          <el-table-column prop="freeze_balance" label="冻结" min-width="110" />
        </el-table>
      </div>
    </div>

    <!-- 4. Webhook -->
    <div class="card space-y-3">
      <h3 class="text-lg font-bold text-ink">Webhook</h3>
      <div class="flex flex-wrap items-center gap-2">
        <el-input :model-value="cfg?.webhook_url || ''" readonly size="large" class="!flex-1 mono" />
        <el-button :disabled="!cfg?.webhook_url" @click="copyText(cfg?.webhook_url || '')">复制</el-button>
      </div>
      <p class="text-xs text-subtle">在 MarsWallex 开发者平台配置此地址</p>
      <div class="flex items-center justify-between">
        <div class="text-sm text-muted">最近事件</div>
        <el-button size="small" :loading="loadingEvents" @click="loadEvents">刷新</el-button>
      </div>
      <el-table :data="events" size="small" border empty-text="暂无事件">
        <el-table-column label="时间" min-width="160">
          <template #default="{ row }">{{ fmtTime(row.received_at) }}</template>
        </el-table-column>
        <el-table-column prop="topic" label="Topic" min-width="140" />
        <el-table-column prop="message_id" label="消息 ID" min-width="200" />
        <el-table-column label="处理状态" min-width="150">
          <template #default="{ row }">
            <el-tag v-if="row.processed_at" type="success" size="small">已处理</el-tag>
            <el-tag v-else-if="row.process_error" type="danger" size="small">失败（已重试 {{ row.process_attempts || 0 }} 次）</el-tag>
            <el-tag v-else type="info" size="small">待处理</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="备注 / 错误" min-width="220">
          <template #default="{ row }">
            <div v-if="row.process_error" class="text-xs text-warn">{{ row.process_error }}</div>
            <div v-if="row.process_note" class="text-xs text-subtle">{{ row.process_note }}</div>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="110">
          <template #default="{ row }">
            <el-button v-if="row.process_error || row.process_note" size="small" plain :loading="reprocessingId === row.id" @click="reprocess(row)">重新处理</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'

interface Preset { id: string; label: string; url: string }
interface Config {
  base_url: string
  base_url_presets: Preset[]
  developer_id: string
  api_token_set: boolean
  private_key_set: boolean
  public_key_pem: string
  public_key_fingerprint: string
  platform_public_key_set: boolean
  platform_public_key_fingerprint: string
  master_key_ready: boolean
  master_key_error: string
  private_key_error?: string
  webhook_url: string
}
interface Balance { currency: string; available_balance: string; freeze_balance: string }
interface Account { account_no: string; account_name: string; balances?: Balance[] }
interface TestResult {
  outcome: string
  reason?: string
  http_status?: number
  code?: string
  message?: string
  hint?: string
  accounts?: Account[]
}
interface WebhookEvent {
  id: number | string
  message_id: string
  topic: string
  received_at: string
  processed_at: string | null
  process_error?: string
  process_attempts?: number
  process_note?: string
}

const cfg = ref<Config | null>(null)
const form = reactive({ base_url: '', developer_id: '' })
const secrets = reactive({ api_token: '', private_key: '', platform_public_key: '' })
const saving = ref(false)
const testing = ref(false)
const result = ref<TestResult | null>(null)
const events = ref<WebhookEvent[]>([])
const loadingEvents = ref(false)
const reprocessingId = ref<number | string | null>(null)

// 主密钥未就绪时只禁用、不提交密钥字段（API Token、私钥）；其余配置照常保存。
const masterKeyReady = computed(() => !!cfg.value?.master_key_ready)

function secretPlaceholder(isSet: boolean | undefined, empty: string) {
  if (!masterKeyReady.value) return '服务器未配置 APP_MASTER_KEY，暂不能录入'
  return isSet ? '已配置，留空不修改' : empty
}

const presetActive = computed(() => {
  const presets = cfg.value?.base_url_presets || []
  const cur = form.base_url.trim().replace(/\/+$/, '')
  const hit = presets.find((p) => p.url.replace(/\/+$/, '') === cur)
  return hit ? hit.id : ''
})

const outcomeType = computed(() => {
  switch (result.value?.outcome) {
    case 'success': return 'success'
    case 'rejected': return 'danger'
    default: return 'warning'
  }
})

const balanceRows = computed(() => {
  const rows: { account_no: string; account_name: string; currency: string; available_balance: string; freeze_balance: string }[] = []
  for (const a of result.value?.accounts || []) {
    const bs = a.balances || []
    if (!bs.length) {
      rows.push({ account_no: a.account_no, account_name: a.account_name, currency: '—', available_balance: '—', freeze_balance: '—' })
    }
    for (const b of bs) {
      rows.push({
        account_no: a.account_no,
        account_name: a.account_name,
        currency: b.currency,
        available_balance: b.available_balance,
        freeze_balance: b.freeze_balance,
      })
    }
  }
  return rows
})

function fmtTime(s: string) {
  const d = new Date(s)
  return isNaN(d.getTime()) ? s : d.toLocaleString()
}

async function copyText(t: string) {
  try {
    await navigator.clipboard.writeText(t)
    dialog.toast('已复制', 'ok')
  } catch {
    dialog.toast('复制失败', 'err')
  }
}

function applyConfig(d: Config) {
  cfg.value = d
  // base_url 为空表示使用生产预设
  const prod = d.base_url_presets?.find((p) => p.id === 'prod')
  form.base_url = d.base_url || prod?.url || ''
  form.developer_id = d.developer_id || ''
}

async function loadConfig() {
  const r = await authFetch('/api/v1/admin/marswallex/config')
  const d = await r.json().catch(() => ({}))
  if (!r.ok) {
    dialog.toast(d.error || '加载配置失败', 'err')
    return
  }
  applyConfig(d)
}

async function save() {
  saving.value = true
  try {
    const body: Record<string, string> = {
      base_url: form.base_url.trim(),
      developer_id: form.developer_id.trim(),
    }
    if (masterKeyReady.value) {
      if (secrets.api_token.trim()) body.api_token = secrets.api_token.trim()
      if (secrets.private_key.trim()) body.private_key = secrets.private_key.trim()
    }
    if (secrets.platform_public_key.trim()) body.platform_public_key = secrets.platform_public_key.trim()
    const r = await authFetch('/api/v1/admin/marswallex/config', { method: 'PUT', body: JSON.stringify(body) })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '保存失败', 'err')
      return
    }
    secrets.api_token = ''
    secrets.private_key = ''
    secrets.platform_public_key = ''
    // 配置已变，旧的测试结果不再代表当前配置。
    result.value = null
    dialog.toast('已保存', 'ok')
    await loadConfig()
  } finally {
    saving.value = false
  }
}

async function runTest() {
  testing.value = true
  try {
    const r = await authFetch('/api/v1/admin/marswallex/test', { method: 'POST' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '测试失败', 'err')
      return
    }
    result.value = d
  } finally {
    testing.value = false
  }
}

async function loadEvents() {
  loadingEvents.value = true
  try {
    const r = await authFetch('/api/v1/admin/marswallex/webhook-events?limit=50')
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '加载事件失败', 'err')
      return
    }
    events.value = d.events || []
  } finally {
    loadingEvents.value = false
  }
}

async function reprocess(row: WebhookEvent) {
  reprocessingId.value = row.id
  try {
    const r = await authFetch(`/api/v1/admin/marswallex/webhook-events/${encodeURIComponent(String(row.id))}/reprocess`, { method: 'POST' })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      dialog.toast(d.error || '重新处理失败', 'err')
      return
    }
    dialog.toast('已提交重新处理', 'ok')
    await loadEvents()
  } finally {
    reprocessingId.value = null
  }
}

onMounted(() => {
  loadConfig()
  loadEvents()
})
</script>

<style scoped>
.warn-box {
  color: #b91c1c;
  background: #fef2f2;
  border: 1px solid #fecaca;
  font-weight: 600;
}
.pem {
  white-space: pre-wrap;
  word-break: break-all;
  font-size: 12px;
  padding: 8px 10px;
  border-radius: 6px;
  background: rgba(127, 127, 127, 0.1);
  max-height: 200px;
  overflow: auto;
}
.text-warn {
  color: #d97706;
}
.field-error {
  margin-top: 4px;
  font-size: 12px;
  line-height: 1.4;
  color: #dc2626;
}
</style>
