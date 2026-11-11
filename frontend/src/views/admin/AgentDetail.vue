<template>
  <div class="space-y-4" data-page="agent-detail">
    <div class="flex flex-wrap items-center gap-3">
      <router-link to="/ops/agents" class="underline text-sm">← 代理商</router-link>
      <h1 class="text-xl font-semibold">{{ agent ? agent.username : '代理商详情' }}</h1>
      <el-tag v-if="agent" size="small" :type="agent.status === 'active' ? 'success' : 'info'">{{ agent.status === 'active' ? '启用' : '已停用' }}</el-tag>
    </div>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>

    <section v-if="agent" class="card p-4 space-y-3">
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4 text-sm">
        <div><div class="opacity-70">余额</div><div class="font-semibold text-lg">{{ fmtUsd(agent.balance_cents) }}</div></div>
        <div><div class="opacity-70">名称</div><div>{{ agent.display_name || '—' }}</div></div>
        <div><div class="opacity-70">价格等级</div><div>{{ agent.tier_name }}</div></div>
        <div><div class="opacity-70">码数</div><div>{{ agent.code_count }}</div></div>
        <div><div class="opacity-70">两步验证</div><div>{{ agent.totp_bound ? '已绑定' : '未绑定' }}</div></div>
        <div><div class="opacity-70">下次登录须改密码</div><div>{{ agent.must_change_password ? '是' : '否' }}</div></div>
        <div><div class="opacity-70">最近登录</div><div>{{ agent.last_login_at ? fmtBeijing(agent.last_login_at) : '从未' }}</div></div>
        <div><div class="opacity-70">创建时间</div><div>{{ fmtBeijing(agent.created_at) }}</div></div>
        <div class="sm:col-span-2 lg:col-span-4"><div class="opacity-70">备注</div><div class="break-all">{{ agent.note || '—' }}</div></div>
      </div>
      <div class="flex flex-wrap gap-2">
        <el-button type="primary" @click="openAdjust">调账</el-button>
        <el-button @click="openEdit">编辑资料</el-button>
        <el-button @click="resetPassword">重置密码</el-button>
        <el-button @click="resetTotp">重置两步验证</el-button>
        <el-button v-if="agent.status === 'active'" type="danger" plain :loading="busy" @click="toggle(false)">停用</el-button>
        <el-button v-else type="success" plain :loading="busy" @click="toggle(true)">启用</el-button>
      </div>
    </section>

    <section v-if="agent" class="card p-4 space-y-3">
      <div class="flex flex-wrap gap-2 text-sm">
        <button v-for="t in TABS" :key="t.key" type="button" :class="tab === t.key ? 'btn-primary' : 'btn-ghost'" @click="switchTab(t.key)">{{ t.label }}</button>
      </div>

      <!-- 流水 -->
      <div v-if="tab === 'ledger'" class="space-y-3">
        <div class="flex flex-wrap items-end gap-3">
          <label class="block"><span class="block text-sm mb-1">开始日期</span><input v-model="from" type="date" class="input" /></label>
          <label class="block"><span class="block text-sm mb-1">结束日期</span><input v-model="to" type="date" class="input" /></label>
          <button class="btn-primary" type="button" @click="searchLedger">查询</button>
          <button class="btn-ghost" type="button" :disabled="!snapRange" @click="exportLedger">导出 CSV（上次查询的范围）</button>
        </div>
        <p v-if="ledgerErr" class="text-sm text-red-500" role="alert">{{ ledgerErr }}</p>
        <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 text-sm">
          <div class="card p-3"><div class="opacity-70">期初</div><b>{{ fmtUsd(summary.opening_cents) }}</b></div>
          <div class="card p-3"><div class="opacity-70">收入</div><b>{{ fmtUsd(summary.income_cents) }}</b></div>
          <div class="card p-3"><div class="opacity-70">支出</div><b>{{ fmtUsd(summary.expense_cents) }}</b></div>
          <div class="card p-3"><div class="opacity-70">期末</div><b>{{ fmtUsd(summary.closing_cents) }}</b></div>
        </div>
        <el-table :data="ledger" size="small" stripe empty-text="没有流水">
          <el-table-column label="时间" width="160"><template #default="{ row }">{{ fmtBeijing(row.created_at) }}</template></el-table-column>
          <el-table-column label="类型" width="80"><template #default="{ row }">{{ LEDGER_KIND_LABELS[row.kind] || row.kind }}</template></el-table-column>
          <el-table-column label="金额" width="110"><template #default="{ row }">{{ fmtUsd(row.amount_cents) }}</template></el-table-column>
          <el-table-column label="变动后余额" width="120"><template #default="{ row }">{{ fmtUsd(row.balance_after_cents) }}</template></el-table-column>
          <el-table-column label="订单" width="80"><template #default="{ row }">{{ row.order_id ? '#' + row.order_id : '' }}</template></el-table-column>
          <el-table-column label="来源" width="80"><template #default="{ row }">{{ sourceLabel(row.source) }}</template></el-table-column>
          <el-table-column prop="operator" label="操作人" width="110" show-overflow-tooltip />
          <el-table-column prop="ext_ref" label="外部单号" min-width="110" show-overflow-tooltip />
          <el-table-column prop="note" label="备注" min-width="140" show-overflow-tooltip />
        </el-table>
        <div class="flex justify-end"><el-pagination v-model:current-page="ledgerPage" :page-size="PAGE" :total="ledgerTotal" layout="total, prev, pager, next" size="small" @current-change="loadLedger" /></div>
      </div>

      <!-- 订单 -->
      <div v-if="tab === 'orders'" class="space-y-3">
        <p v-if="ordersErr" class="text-sm text-red-500" role="alert">{{ ordersErr }}</p>
        <el-table :data="orders" size="small" stripe empty-text="没有订单">
          <el-table-column prop="id" label="订单" width="80" />
          <el-table-column label="时间" width="160"><template #default="{ row }">{{ fmtBeijing(row.created_at) }}</template></el-table-column>
          <el-table-column prop="product_name" label="商品" min-width="140" show-overflow-tooltip />
          <el-table-column prop="quantity" label="数量" width="70" />
          <el-table-column label="单价" width="100"><template #default="{ row }">{{ fmtUsd(row.unit_price_cents) }}</template></el-table-column>
          <el-table-column label="合计" width="110"><template #default="{ row }">{{ fmtUsd(row.total_cents) }}</template></el-table-column>
          <el-table-column label="来源" width="80"><template #default="{ row }">{{ sourceLabel(row.source) }}</template></el-table-column>
          <el-table-column prop="external_ref" label="对方订单号" min-width="130" show-overflow-tooltip />
          <el-table-column prop="note" label="备注" min-width="120" show-overflow-tooltip />
        </el-table>
        <div class="flex justify-end"><el-pagination v-model:current-page="ordersPage" :page-size="PAGE" :total="ordersTotal" layout="total, prev, pager, next" size="small" @current-change="loadOrders" /></div>
      </div>

      <!-- 覆盖价 -->
      <div v-if="tab === 'overrides'" class="space-y-3">
        <p class="text-sm opacity-70">给这个代理商单独定价，优先于所属等级的价格；留空或填 0 表示取消覆盖，回到等级价。</p>
        <p v-if="overridesErr" class="text-sm text-red-500" role="alert">{{ overridesErr }}</p>
        <el-table :data="overrides" size="small" stripe empty-text="没有商品">
          <el-table-column prop="name" label="商品" min-width="160" show-overflow-tooltip />
          <el-table-column prop="product_code" label="编码" min-width="140" class-name="mono" />
          <el-table-column label="等级价" width="110"><template #default="{ row }">{{ row.tier_price_cents ? fmtUsd(row.tier_price_cents) : '不卖' }}</template></el-table-column>
          <el-table-column label="覆盖价（美元）" width="170">
            <template #default="{ row }"><el-input v-model="overrideEdit[String(row.product_id)]" size="small" placeholder="不覆盖" /></template>
          </el-table-column>
        </el-table>
        <el-button type="primary" :loading="savingOverrides" @click="saveOverrides">保存覆盖价</el-button>
      </div>

      <!-- 异次元对接 -->
      <AgentSharedPanel v-if="tab === 'shared'" :agent-id="id" />

      <!-- 本站码 -->
      <div v-if="tab === 'codes'" class="space-y-3">
        <p v-if="codesErr" class="text-sm text-red-500" role="alert">{{ codesErr }}</p>
        <el-table :data="codes" size="small" stripe empty-text="没有码">
          <el-table-column prop="id" label="ID" width="70" />
          <el-table-column label="码前缀" min-width="130"><template #default="{ row }"><span class="mono">{{ row.code_prefix }}…</span></template></el-table-column>
          <el-table-column label="套餐 / 地区" min-width="130"><template #default="{ row }">{{ row.plan }} / {{ row.region }}</template></el-table-column>
          <el-table-column label="状态" min-width="150"><template #default="{ row }">{{ localCdkStatusText(row.status, row.status_reason) }}</template></el-table-column>
          <el-table-column prop="note" label="备注" min-width="120" show-overflow-tooltip />
          <el-table-column label="时间" width="160"><template #default="{ row }">{{ fmtCdkTime(row.created_at) }}</template></el-table-column>
        </el-table>
        <div class="flex justify-end"><el-pagination v-model:current-page="codesPage" :page-size="PAGE" :total="codesTotal" layout="total, prev, pager, next" size="small" @current-change="loadCodes" /></div>
      </div>
    </section>

    <!-- 编辑资料 -->
    <el-dialog v-model="editOpen" title="编辑资料" width="min(480px, 96vw)" append-to-body :close-on-click-modal="false">
      <div class="space-y-3">
        <label class="block text-sm"><span class="block mb-1">名称</span><el-input v-model="editForm.display_name" maxlength="100" /></label>
        <label class="block text-sm"><span class="block mb-1">价格等级</span>
          <el-select v-model="editForm.tier_id" class="w-full"><el-option v-for="t in tiers" :key="t.id" :label="t.name" :value="t.id" /></el-select>
        </label>
        <label class="block text-sm"><span class="block mb-1">备注</span><el-input v-model="editForm.note" maxlength="200" /></label>
        <p v-if="editErr" class="text-sm text-red-500" role="alert">{{ editErr }}</p>
      </div>
      <template #footer>
        <el-button @click="editOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="saveEdit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 调账 -->
    <el-dialog v-model="adjustOpen" :title="`调账：${agent?.username || ''}`" width="min(480px, 96vw)" append-to-body :show-close="!adjBusy" :close-on-click-modal="false" :close-on-press-escape="false" :before-close="beforeCloseAdjust">
      <div class="space-y-3" data-testid="adjust-dialog">
        <p class="text-sm">代理商 <b>{{ agent?.username }}</b> 当前余额 <b>{{ agent ? fmtUsd(agent.balance_cents) : '' }}</b>。正数充值，负数扣款，不能扣成负数；每笔调账都会记入流水和审计。</p>
        <label class="block text-sm"><span class="block mb-1">金额（美元，可带负号）</span><el-input v-model="adjAmount" :disabled="adjLocked || adjBusy || !!adjPending" placeholder="例如 100 或 -20.5" /></label>
        <label class="block text-sm"><span class="block mb-1">原因（必填）</span><el-input v-model="adjNote" :disabled="adjLocked || adjBusy || !!adjPending" maxlength="200" /></label>
        <div v-if="preview" class="text-sm rounded p-2" :class="preview.direction === 'credit' ? 'adj-credit' : 'adj-debit'" data-testid="adjust-preview">
          <div class="font-semibold">{{ preview.text }}</div>
          <div>{{ preview.afterText }}</div>
          <div v-if="preview.warning" class="font-semibold" role="alert">{{ preview.warning }}</div>
        </div>
        <p v-if="adjMsg" class="text-sm" :class="adjLocked ? 'text-amber-600' : 'text-red-500'" role="alert">{{ adjLocked ? `代理商 ${agent?.username}：` : '' }}{{ adjMsg }}</p>
      </div>
      <template #footer>
        <el-button v-if="adjLocked" type="danger" plain :disabled="adjBusy" @click="abandonAdjust">放弃这次调账</el-button>
        <el-button v-else :disabled="adjBusy" @click="closeAdjust">取消</el-button>
        <el-button type="primary" :loading="adjBusy" :disabled="adjBusy || !!adjPending" @click="submitAdjust">{{ adjLocked ? '原样重试' : '确认调账' }}</el-button>
      </template>
    </el-dialog>

    <AgentPasswordDialog :password="newPassword" :username="agent?.username" title="新密码" @close="newPassword = ''" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate, useRoute } from 'vue-router'
import AgentPasswordDialog from './components/AgentPasswordDialog.vue'
import AgentSharedPanel from './components/AgentSharedPanel.vue'
import { sourceLabel } from '../../lib/agentShared'
import { adminApi, centsToUsd, localCdkStatusText } from '../../lib/provider'
import { authFetch } from '../../lib/api'
import {
  ADJUST_LEAVE_TEXT, ADJUST_LOCKED_ELSEWHERE, adjustAbandon, adjustBegin, adjustKeeperFor, adjustLeaveWarning, adjustOpenState, adjustPendingFor, adjustPreview,
  adjustRecheck, adjustReopenBlock, adjustRun, adminErrorText, overrideChanges, type PendingAdjust,
} from '../../lib/agentAdmin'
import { IdempotencyLockedError } from '../../lib/idempotency'
import { fmtUsd, parseSignedUsd } from '../../lib/agentMoney'
import { beijingRange, defaultLedgerRange, ledgerQuery, LEDGER_KIND_LABELS } from '../../lib/agentViews'
import { fmtBeijing } from '../../lib/mwwebhook'
import { dialog } from '../../lib/dialog'

const PAGE = 20

function downloadBlob(name: string, blob: Blob) {
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
const TABS = [
  { key: 'ledger', label: '余额流水' }, { key: 'orders', label: '订单' },
  { key: 'overrides', label: '覆盖价' }, { key: 'shared', label: '异次元对接' }, { key: 'codes', label: '本站码' },
]
const route = useRoute()
const id = computed(() => Number(route.params.id))
const agent = ref<any>(null)
const tiers = ref<any[]>([])
const err = ref('')
const busy = ref(false)
const tab = ref('ledger')

function fmtCdkTime(v: unknown) {
  return typeof v === 'number' ? fmtBeijing(v) : v ? String(v) : '—'
}

// 路由参数换了（组件复用）时，上一个代理商在途的请求回来不能写到新页面；同一列表连续翻页只认最新一次
let agentSeq = 0
async function loadAgent() {
  const my = ++agentSeq
  const cur = id.value
  const r = await adminApi<any>(`/agents/${cur}`)
  if (my !== agentSeq || cur !== id.value) return // 已换到别的代理商，或已有更新的请求
  if (!r.ok) { err.value = '读取失败：' + adminErrorText(r.data, r.status); return }
  err.value = ''
  agent.value = r.data.agent
}

// ---------- 流水 ----------
const def = defaultLedgerRange(Math.floor(Date.now() / 1000))
const from = ref(def.from)
const to = ref(def.to)
const snap = ref({ from: def.from, to: def.to })
const snapRange = computed(() => beijingRange(snap.value.from, snap.value.to))
const ledger = ref<any[]>([])
const ledgerTotal = ref(0)
const ledgerPage = ref(1)
const ledgerErr = ref('')
const summary = ref({ opening_cents: 0, closing_cents: 0, income_cents: 0, expense_cents: 0 })

function searchLedger() {
  if (!beijingRange(from.value, to.value)) { ledgerErr.value = '日期范围不正确'; return }
  snap.value = { from: from.value, to: to.value }
  ledgerPage.value = 1
  loadLedger()
}
let ledgerSeq = 0
async function loadLedger() {
  const rg = snapRange.value
  if (!rg) { ledgerErr.value = '日期范围不正确'; return }
  const my = ++ledgerSeq
  const cur = id.value
  const r = await adminApi<any>(`/agents/${cur}/ledger?${ledgerQuery(rg.from, rg.to)}&page=${ledgerPage.value}&page_size=${PAGE}`)
  if (my !== ledgerSeq || cur !== id.value) return // 已换到别的代理商，或已有更新的请求
  if (!r.ok) { ledgerErr.value = adminErrorText(r.data, r.status); return }
  ledgerErr.value = ''
  ledger.value = r.data.items || []
  ledgerTotal.value = r.data.total || 0
  if (r.data.summary) summary.value = r.data.summary
}
async function exportLedger() {
  const rg = snapRange.value
  if (!rg) return
  try {
    const res = await authFetch(`/api/v1/admin/agents/${id.value}/ledger/export?${ledgerQuery(rg.from, rg.to)}`)
    if (!res.ok) {
      const d = await res.json().catch(() => ({}))
      dialog.toast(adminErrorText(d, res.status), 'err')
      return
    }
    // 直接用字节建文件，保留后端写的 BOM
    downloadBlob(`agent-${id.value}-ledger.csv`, new Blob([await res.arrayBuffer()], { type: 'text/csv;charset=utf-8' }))
  } catch { dialog.toast('网络异常，请稍后再试', 'err') }
}

// ---------- 订单 ----------
const orders = ref<any[]>([])
const ordersTotal = ref(0)
const ordersPage = ref(1)
const ordersErr = ref('')
let ordersSeq = 0
async function loadOrders() {
  const my = ++ordersSeq
  const cur = id.value
  const r = await adminApi<any>(`/agents/${cur}/orders?page=${ordersPage.value}&page_size=${PAGE}`)
  if (my !== ordersSeq || cur !== id.value) return // 已换到别的代理商，或已有更新的请求
  if (!r.ok) { ordersErr.value = adminErrorText(r.data, r.status); return }
  ordersErr.value = ''
  orders.value = r.data.items || []
  ordersTotal.value = r.data.total || 0
}

// ---------- 覆盖价 ----------
const overrides = ref<any[]>([])
const overrideEdit = reactive<Record<string, string>>({})
const overridesErr = ref('')
const savingOverrides = ref(false)
function fillOverrides(items: any[]) {
  overrides.value = items
  for (const k of Object.keys(overrideEdit)) delete overrideEdit[k]
  for (const o of items) overrideEdit[String(o.product_id)] = o.override_price_cents ? centsToUsd(o.override_price_cents) : ''
}
let overridesSeq = 0
async function loadOverrides() {
  const my = ++overridesSeq
  const cur = id.value
  const r = await adminApi<any>(`/agents/${cur}/overrides`)
  if (my !== overridesSeq || cur !== id.value) return // 已换到别的代理商，或已有更新的请求
  if (!r.ok) { overridesErr.value = adminErrorText(r.data, r.status); return }
  overridesErr.value = ''
  fillOverrides(r.data.items || [])
}
async function saveOverrides() {
  const orig = overrides.value.map((o) => ({ product_id: o.product_id, price_cents: o.override_price_cents || 0 }))
  const { cells, errors } = overrideChanges(orig, { ...overrideEdit })
  if (errors.length) { overridesErr.value = errors.join('；'); return }
  if (!cells.length) { dialog.toast('没有改动', 'info'); return }
  savingOverrides.value = true
  const r = await adminApi<any>(`/agents/${id.value}/overrides`, 'PUT', { cells })
  savingOverrides.value = false
  if (!r.ok) { overridesErr.value = adminErrorText(r.data, r.status); return }
  overridesErr.value = ''
  fillOverrides(r.data.items || [])
  dialog.toast('覆盖价已保存')
}

// ---------- 本站码 ----------
const codes = ref<any[]>([])
const codesTotal = ref(0)
const codesPage = ref(1)
const codesErr = ref('')
let codesSeq = 0
async function loadCodes() {
  const my = ++codesSeq
  const cur = id.value
  const r = await adminApi<any>(`/cdks?agent_id=${cur}&page=${codesPage.value}&page_size=${PAGE}`)
  if (my !== codesSeq || cur !== id.value) return // 已换到别的代理商，或已有更新的请求
  if (!r.ok) { codesErr.value = adminErrorText(r.data, r.status); return }
  codesErr.value = ''
  codes.value = r.data.list || []
  codesTotal.value = r.data.total || 0
}

const loaders: Record<string, () => Promise<void>> = { ledger: loadLedger, orders: loadOrders, overrides: loadOverrides, codes: loadCodes }
const overridesDirty = () => overrideChanges(
  overrides.value.map((o) => ({ product_id: o.product_id, price_cents: o.override_price_cents || 0 })), { ...overrideEdit },
).cells.length > 0
async function switchTab(key: string) {
  if (key === tab.value) return
  if (tab.value === 'overrides' && key !== 'overrides' && overridesDirty()) {
    const ok = await dialog.confirm('覆盖价有没保存的修改，切换页签会丢掉这些修改。确认切换？', { title: '有未保存的修改', okText: '丢掉并切换', danger: true })
    if (!ok) return
  }
  tab.value = key
  loaders[key]?.()
}

// ---------- 编辑、启停、重置 ----------
const editOpen = ref(false)
const saving = ref(false)
const editErr = ref('')
const editForm = reactive({ display_name: '', tier_id: 0, note: '' })
function openEdit() {
  Object.assign(editForm, { display_name: agent.value.display_name, tier_id: agent.value.tier_id, note: agent.value.note })
  editErr.value = ''
  editOpen.value = true
}
async function saveEdit() {
  saving.value = true
  const r = await adminApi<any>(`/agents/${id.value}`, 'PUT', { display_name: editForm.display_name.trim(), tier_id: editForm.tier_id, note: editForm.note.trim() })
  saving.value = false
  if (!r.ok) { editErr.value = adminErrorText(r.data, r.status); return }
  agent.value = r.data.agent
  editOpen.value = false
  dialog.toast('已保存')
}
async function toggle(enable: boolean) {
  const ok = await dialog.confirm(enable ? '启用这个代理商？' : '停用后这个代理商的登录会话立即失效，不能再下单。确认停用？', { title: enable ? '启用' : '停用', okText: enable ? '启用' : '停用', danger: !enable })
  if (!ok) return
  busy.value = true
  const r = await adminApi<any>(`/agents/${id.value}/${enable ? 'enable' : 'disable'}`, 'POST')
  busy.value = false
  if (!r.ok) { dialog.toast(adminErrorText(r.data, r.status), 'err'); return }
  agent.value = r.data.agent
  dialog.toast(enable ? '已启用' : '已停用')
}
const newPassword = ref('')
async function resetPassword() {
  const ok = await dialog.confirm('重置后旧密码立即失效，代理商下次登录必须用新密码并改密码。确认重置？', { title: '重置密码', okText: '重置', danger: true })
  if (!ok) return
  const r = await adminApi<any>(`/agents/${id.value}/reset-password`, 'POST')
  if (!r.ok) { dialog.toast(adminErrorText(r.data, r.status), 'err'); return }
  newPassword.value = String(r.data.password || '')
  loadAgent()
}
async function resetTotp() {
  const ok = await dialog.confirm('重置后代理商下次登录需要重新扫码绑定两步验证。确认重置？', { title: '重置两步验证', okText: '重置', danger: true })
  if (!ok) return
  const r = await adminApi<any>(`/agents/${id.value}/reset-totp`, 'POST')
  if (!r.ok) { dialog.toast(adminErrorText(r.data, r.status), 'err'); return }
  dialog.toast('已重置')
  loadAgent()
}

// ---------- 调账 ----------
// 未决调账（结果未知）和幂等键按代理商 ID 落 localStorage（lib/agentAdmin 的 adjustRetry）：
// 关对话框、切页面、刷新、关标签页重开、新标签页都不丢；只存幂等键、金额和原因
const adjustOpen = ref(false)
const adjAmount = ref('')
const adjNote = ref('')
const adjBusy = ref(false)
const adjLocked = ref(false) // 结果未知：锁定金额和原因，只能原样重试或放弃
const adjMsg = ref('')
const adjPending = ref('') // 非空：同一代理商有调账请求在途

const preview = computed(() => {
  const p = parseSignedUsd(adjAmount.value)
  if (!p.ok || !agent.value) return null
  return adjustPreview(agent.value.username, agent.value.balance_cents, p.cents, adjLocked.value)
})

function openAdjust() {
  const st = adjustOpenState(adjustPendingFor(id.value)) // 重读存储：刷新前、别的标签页留下的未决也算
  adjAmount.value = st.amountText
  adjNote.value = st.note
  adjLocked.value = st.locked
  adjMsg.value = st.locked ? '上一次调账结果未知，请原样重试，或放弃后先到「余额流水」确认' : ''
  adjPending.value = adjustReopenBlock(id.value) // 有请求在途：只显示提示，不许提交
  if (adjPending.value) { adjLocked.value = false; adjMsg.value = adjPending.value }
  else if (!st.locked) adjustKeeperFor(id.value).done() // 没有未决、也没有在途才换新键
  adjustOpen.value = true
}
function closeAdjust() { if (!adjBusy.value) adjustOpen.value = false }
async function abandonAdjust() {
  if (adjBusy.value) return
  const ok = await dialog.confirm(`这次对 ${agent.value?.username} 的调账可能已经入账。放弃后不会再重试，请先到「余额流水」确认有没有这笔记录，再决定是否重新调账。确认放弃？`, { title: '放弃这次调账', okText: '放弃', danger: true })
  if (!ok) return
  adjustAbandon(id.value)
  adjLocked.value = false
  adjMsg.value = ''
  adjustOpen.value = false
  loadAgent()
  loadLedger()
}
function beforeCloseAdjust(done: () => void) {
  if (adjBusy.value) return
  if (adjLocked.value) { dialog.toast('结果未知，请先原样重试或放弃这次调账', 'warn'); return }
  done()
}
async function submitAdjust() {
  if (adjBusy.value || !agent.value || adjPending.value) return
  const p = parseSignedUsd(adjAmount.value)
  if (!p.ok) { adjMsg.value = p.error; return }
  const note = adjNote.value.trim()
  if (!note) { adjMsg.value = '请填写原因'; return }
  const pv = adjustPreview(agent.value.username, agent.value.balance_cents, p.cents, adjLocked.value)
  if (pv.willBeNegative && !adjLocked.value) { adjMsg.value = pv.warning; return }
  const ok = await dialog.confirm(`${pv.text}\n${pv.afterText}\n原因：${note}\n确认调账？`, { title: `确认调账：${agent.value.username}`, okText: pv.direction === 'credit' ? '确认充值' : '确认扣款', danger: pv.direction === 'debit' })
  if (!ok) return
  const aid = id.value
  const payload: PendingAdjust = { agent_id: aid, amount_cents: p.cents, note }
  // 发出前重读存储（多个标签页共用）：别处锁定了一笔就改显示那笔；这里锁定的那笔别处已处理完就不许再发（再发会换新键重复入账）
  const rc = adjustRecheck(aid, payload, adjLocked.value)
  if (rc.kind === 'locked') { showLockedAdjust(rc.pending, ADJUST_LOCKED_ELSEWHERE); return }
  if (rc.kind === 'settled') {
    adjLocked.value = false
    adjMsg.value = '这笔调账已在别的页面处理完（成功或已放弃），请先到「余额流水」核对，再决定是否重新调账'
    loadAgent(); loadLedger()
    return
  }
  if (!adjustBegin(aid)) { adjPending.value = adjustReopenBlock(aid); adjMsg.value = adjPending.value; return }
  adjBusy.value = true
  adjMsg.value = ''
  try {
    // 发出前落盘、返回后只结算一次（lib/agentAdmin 的 run）；下面更新界面出异常也不会再记一条未决、换键（Task 11）
    await adjustRun(aid, payload, (key) => adminApi<any>(`/agents/${aid}/adjust`, 'POST', { amount_cents: p.cents, note }, { 'Idempotency-Key': key }), (o) => {
      if (aid !== id.value) return // 请求期间已切到别的代理商：不再动当前页面
      if (o.kind === 'unknown') { adjLocked.value = true; adjMsg.value = o.message; return }
      adjLocked.value = false
      if (o.kind === 'failed') { adjMsg.value = o.message; return }
      dialog.toast(o.message, o.kind === 'replayed' ? 'info' : 'ok', o.kind === 'replayed' ? 6000 : 2800)
      adjustOpen.value = false
      loadAgent()
      if (tab.value === 'ledger') loadLedger()
    })
  } catch (e) {
    // 只接住「发出前被别的标签页抢先锁定」：请求没发、不记未决；别的异常照旧往外抛
    if (!(e instanceof IdempotencyLockedError)) throw e
    if (aid !== id.value) return
    const cur = adjustPendingFor(aid)
    if (cur) showLockedAdjust(cur, ADJUST_LOCKED_ELSEWHERE)
    else adjMsg.value = '这个代理商的调账刚被别的页面锁定又处理完了，请先到「余额流水」核对，再决定是否重新调账'
  } finally {
    adjBusy.value = false
  }
}
function showLockedAdjust(pending: PendingAdjust, msg: string) {
  const st = adjustOpenState(pending)
  adjAmount.value = st.amountText
  adjNote.value = st.note
  adjLocked.value = true
  adjMsg.value = msg
}
// 打开详情页（或换到另一个代理商）时：有结果未知的调账就直接打开锁定的对话框，只许原样重试或放弃
function resumeAdjust() {
  if (agent.value && !adjustOpen.value && adjustPendingFor(id.value)) openAdjust()
}

// 有未决调账时离开（或换到另一个代理商）先提示
async function confirmLeave(): Promise<boolean> {
  if (!adjustLeaveWarning(id.value)) return true
  return dialog.confirm(ADJUST_LEAVE_TEXT, { title: '调账结果未知', okText: '仍然离开', danger: true })
}
onBeforeRouteLeave(async () => (await confirmLeave()) || false)
onBeforeRouteUpdate(async () => (await confirmLeave()) || false)

function resetForId() {
  agent.value = null
  err.value = ''
  adjustOpen.value = false
  adjAmount.value = ''
  adjNote.value = ''
  adjLocked.value = false
  adjMsg.value = ''
  adjPending.value = ''
  for (const k of Object.keys(overrideEdit)) delete overrideEdit[k]
  overrides.value = []
  ledger.value = []; orders.value = []; codes.value = []
  ledgerPage.value = ordersPage.value = codesPage.value = 1
  tab.value = 'ledger'
  loadAgent().then(resumeAdjust)
  loadLedger()
}
watch(id, (n, o) => { if (n && n !== o) resetForId() })

onMounted(async () => {
  loadAgent().then(resumeAdjust)
  loadLedger()
  const t = await adminApi<any>('/agent-tiers')
  if (t.ok) tiers.value = t.data.items || []
})
</script>

<style scoped>
.adj-credit { background: rgba(22, 163, 74, 0.1); color: #15803d; }
.adj-debit { background: rgba(220, 38, 38, 0.1); color: #b91c1c; }
</style>
