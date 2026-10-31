<template>
  <div class="space-y-4">
    <div class="card space-y-3">
      <div>
        <h2 class="text-xl font-bold text-ink">MarsWallex 钱包</h2>
        <p class="text-sm text-muted mt-1">主钱包与子账户、充值地址、兑换、转账与流水</p>
      </div>

      <el-tabs v-model="tab" @tab-change="onTabChange">
        <!-- ============ 账户 ============ -->
        <el-tab-pane label="账户" name="accounts">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-button type="primary" @click="openCreate">新建子账户</el-button>
            <el-button :loading="accountsLoading" @click="loadAccounts(true)">刷新</el-button>
            <span v-if="pendingRefresh" class="text-sm text-warn">结果未知，10 秒后自动刷新确认</span>
          </div>
          <el-table :data="accounts" size="small" border empty-text="暂无账户" v-loading="accountsLoading" :row-class-name="accountRowClass">
            <el-table-column prop="account_name" label="账户名" min-width="140" show-overflow-tooltip />
            <el-table-column prop="account_no" label="账户号" min-width="170" show-overflow-tooltip />
            <el-table-column label="类型" width="90">
              <template #default="{ row }">{{ TYPE_LABEL[row.account_type] || row.account_type }}</template>
            </el-table-column>
            <el-table-column label="状态" width="110">
              <template #default="{ row }">
                <el-tag size="small" :type="acctTagType(row.status)">{{ STATUS_LABEL[row.status] || row.status }}</el-tag>
                <el-tag v-if="row.gone" size="small" type="info" class="ml-1">已消失</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="可用 / 冻结" min-width="200">
              <template #default="{ row }">
                <div v-for="b in row.balances || []" :key="b.currency" class="mono text-sm">
                  {{ b.currency }}：{{ b.available_balance }} / {{ b.freeze_balance }}
                </div>
                <span v-if="!(row.balances || []).length" class="text-muted">—</span>
              </template>
            </el-table-column>
            <el-table-column label="同步时间" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.synced_at) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="110" fixed="right">
              <template #default="{ row }">
                <template v-if="row.account_type === 'SUB_ACCOUNT' && !row.gone">
                  <el-button v-if="row.status === 'ACTIVE'" size="small" type="danger" plain @click="changeStatus(row, 'DISABLED')">停用</el-button>
                  <el-button v-else-if="row.status === 'DISABLED'" size="small" plain @click="changeStatus(row, 'ACTIVE')">启用</el-button>
                </template>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <!-- ============ 充值地址 ============ -->
        <el-tab-pane label="充值地址" name="addresses">
          <el-alert type="warning" :closable="false" show-icon class="mb-3" title="只能向对应的链转入对应的币种，转错链会丢失资金" />
          <div class="mb-3"><el-button :loading="addrLoading" @click="loadAddresses">刷新</el-button></div>
          <el-table :data="addresses" size="small" border empty-text="暂无充值地址" v-loading="addrLoading">
            <el-table-column prop="chain_type" label="链" width="90" />
            <el-table-column label="支持币种" min-width="160">
              <template #default="{ row }">{{ (row.currencies || []).join('、') }}</template>
            </el-table-column>
            <el-table-column label="地址" min-width="360">
              <template #default="{ row }"><span class="mono">{{ row.address }}</span></template>
            </el-table-column>
            <el-table-column label="操作" width="90">
              <template #default="{ row }">
                <el-button size="small" plain @click="copyAddr(row.address)">复制</el-button>
              </template>
            </el-table-column>
          </el-table>
        </el-tab-pane>

        <!-- ============ 兑换 ============ -->
        <el-tab-pane label="兑换" name="conversion">
          <el-form label-position="top" class="max-w-xl" @submit.prevent>
            <el-form-item label="卖出币种（主钱包余额币种）">
              <el-select v-model="conv.sell_currency" style="width: 100%">
                <el-option v-for="c in mainCurrencies" :key="c" :label="c" :value="c" />
              </el-select>
            </el-form-item>
            <el-form-item label="卖出金额">
              <el-input v-model="conv.sell_amount" placeholder="如 12.50" class="mono" />
            </el-form-item>
            <el-form-item label="买入币种">
              <el-input v-model="conv.buy_currency" placeholder="USD" class="mono" />
            </el-form-item>
            <el-button type="primary" :loading="convSubmitting" :disabled="convSubmitting" @click="submitConversion">提交兑换</el-button>
          </el-form>
          <h3 class="font-bold text-ink mt-6 mb-2">兑换单据</h3>
          <el-table :data="convOrders.items" size="small" border empty-text="暂无单据" v-loading="convOrders.loading">
            <el-table-column prop="out_order_no" label="单号" min-width="190" show-overflow-tooltip />
            <el-table-column label="卖出" min-width="120">
              <template #default="{ row }">{{ money(row.amount_cents) }} {{ row.currency }}</template>
            </el-table-column>
            <el-table-column label="买入" min-width="120">
              <template #default="{ row }">{{ detailOf(row).buy_amount || '—' }} {{ detailOf(row).buy_currency || row.buy_currency }}</template>
            </el-table-column>
            <el-table-column label="汇率" min-width="100">
              <template #default="{ row }">{{ detailOf(row).rate || '—' }}</template>
            </el-table-column>
            <el-table-column label="手续费" min-width="110">
              <template #default="{ row }">{{ detailOf(row).fee_amount || '—' }} {{ detailOf(row).fee_currency || '' }}</template>
            </el-table-column>
            <el-table-column label="状态" width="120">
              <template #default="{ row }"><el-tag size="small" :type="orderTagType(row.state)">{{ row.state }}</el-tag></template>
            </el-table-column>
            <el-table-column prop="last_error" label="错误" min-width="140" show-overflow-tooltip />
            <el-table-column label="创建" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.created_at) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="160" fixed="right">
              <template #default="{ row }">
                <template v-if="row.state === 'needs_review'">
                  <el-button size="small" plain @click="recheck(row, convOrders)">重查</el-button>
                  <el-button size="small" type="danger" plain @click="confirmFailed(row, convOrders)">确认失败</el-button>
                </template>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination class="mt-3" layout="total, prev, pager, next" :total="convOrders.total" :page-size="PAGE_SIZE"
            v-model:current-page="convOrders.page" @current-change="loadOrders(convOrders)" />
        </el-tab-pane>

        <!-- ============ 转账 ============ -->
        <el-tab-pane label="转账" name="transfer">
          <el-form label-position="top" class="max-w-xl" @submit.prevent>
            <el-form-item label="方向">
              <el-radio-group v-model="xfer.direction" @change="onDirectionChange">
                <el-radio-button value="to_sub">主钱包 → 子账户</el-radio-button>
                <el-radio-button value="to_main">子账户 → 主钱包</el-radio-button>
              </el-radio-group>
            </el-form-item>
            <el-form-item label="子账户（仅启用中）">
              <el-select v-model="xfer.sub_no" placeholder="选择子账户" style="width: 100%" @change="onSubChange">
                <el-option v-for="a in activeSubs" :key="a.account_no" :label="`${a.account_name}（${a.account_no}）`" :value="a.account_no" />
              </el-select>
            </el-form-item>
            <el-form-item label="币种（付款方余额币种）">
              <el-select v-model="xfer.currency" placeholder="选择币种" style="width: 100%">
                <el-option v-for="c in payerCurrencies" :key="c" :label="c" :value="c" />
              </el-select>
            </el-form-item>
            <el-form-item label="金额">
              <el-input v-model="xfer.amount" placeholder="如 1.00" class="mono" />
            </el-form-item>
            <el-form-item label="备注（最多 200 字）">
              <el-input v-model="xfer.remark" maxlength="200" show-word-limit />
            </el-form-item>
            <el-button type="primary" :loading="xferSubmitting" :disabled="xferSubmitting" @click="submitTransfer">提交转账</el-button>
          </el-form>
          <h3 class="font-bold text-ink mt-6 mb-2">转账单据</h3>
          <el-table :data="xferOrders.items" size="small" border empty-text="暂无单据" v-loading="xferOrders.loading">
            <el-table-column prop="out_order_no" label="单号" min-width="190" show-overflow-tooltip />
            <el-table-column prop="payer_account_no" label="付款账户" min-width="160" show-overflow-tooltip />
            <el-table-column prop="payee_account_no" label="收款账户" min-width="160" show-overflow-tooltip />
            <el-table-column label="金额" min-width="110">
              <template #default="{ row }">{{ money(row.amount_cents) }} {{ row.currency }}</template>
            </el-table-column>
            <el-table-column label="平台单号" min-width="150" show-overflow-tooltip>
              <template #default="{ row }">{{ detailOf(row).order_no || '—' }}</template>
            </el-table-column>
            <el-table-column label="状态" width="120">
              <template #default="{ row }"><el-tag size="small" :type="orderTagType(row.state)">{{ row.state }}</el-tag></template>
            </el-table-column>
            <el-table-column prop="last_error" label="错误" min-width="140" show-overflow-tooltip />
            <el-table-column prop="note" label="备注" min-width="120" show-overflow-tooltip />
            <el-table-column label="创建" min-width="150">
              <template #default="{ row }">{{ fmtTs(row.created_at) }}</template>
            </el-table-column>
            <el-table-column label="操作" width="160" fixed="right">
              <template #default="{ row }">
                <template v-if="row.state === 'needs_review'">
                  <el-button size="small" plain @click="recheck(row, xferOrders)">重查</el-button>
                  <el-button size="small" type="danger" plain @click="confirmFailed(row, xferOrders)">确认失败</el-button>
                </template>
              </template>
            </el-table-column>
          </el-table>
          <el-pagination class="mt-3" layout="total, prev, pager, next" :total="xferOrders.total" :page-size="PAGE_SIZE"
            v-model:current-page="xferOrders.page" @current-change="loadOrders(xferOrders)" />
        </el-tab-pane>

        <!-- ============ 流水 ============ -->
        <el-tab-pane label="流水" name="serials">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-select v-model="ser.account_no" placeholder="账户" style="width: 260px" @change="onSerAccountChange">
              <el-option v-for="a in ledgerAccounts" :key="a.account_no" :label="`${a.account_name}（${a.account_no}）`" :value="a.account_no" />
            </el-select>
            <el-select v-model="ser.currency" placeholder="币种" style="width: 120px">
              <el-option v-for="c in serCurrencies" :key="c" :label="c" :value="c" />
            </el-select>
            <input v-model="ser.start" type="date" class="date-input" :min="serStartMin" :max="ser.end" />
            <span class="text-muted">至</span>
            <input v-model="ser.end" type="date" class="date-input" :min="ser.start" :max="serEndMax" />
            <el-button type="primary" :loading="ser.loading" @click="querySerials(1)">查询</el-button>
            <span class="text-sm text-muted">最长 90 天</span>
          </div>
          <el-table :data="ser.items" size="small" border empty-text="暂无流水" v-loading="ser.loading">
            <el-table-column prop="serial_id" label="流水号" min-width="200" show-overflow-tooltip />
            <el-table-column label="收支方向" width="130">
              <template #default="{ row }">{{ FLAG_LABEL[row.flag_dc] || row.flag_dc }}</template>
            </el-table-column>
            <el-table-column label="交易类型" width="100">
              <template #default="{ row }">{{ TXN_LABEL[row.transaction_type] || row.transaction_type }}</template>
            </el-table-column>
            <el-table-column label="金额" min-width="130">
              <template #default="{ row }"><span class="mono">{{ row.amount }} {{ row.currency }}</span></template>
            </el-table-column>
            <el-table-column label="时间" min-width="160">
              <template #default="{ row }">{{ fmtTs(row.create_time) }}</template>
            </el-table-column>
          </el-table>
          <el-pagination class="mt-3" layout="total, prev, pager, next" :total="ser.total" :page-size="SER_PAGE_SIZE"
            v-model:current-page="ser.page" @current-change="querySerials" />
        </el-tab-pane>
      </el-tabs>
    </div>

    <el-dialog v-model="createOpen" title="新建子账户" width="420px" :close-on-click-modal="false">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="账户名（1–50 字，不含空白字符）">
          <el-input v-model="createName" maxlength="50" @keyup.enter="submitCreate" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="createOpen = false">取消</el-button>
        <el-button type="primary" :loading="createSubmitting" :disabled="createSubmitting" @click="submitCreate">创建</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { dialog } from '../../lib/dialog'
import { copyToClipboard } from '../../lib/clipboard'
import {
  errText, FLAG_LABEL, fmtTs, hasEmoji, isUncertain, TXN_LABEL, kindLabel, money, parseAmountCents, poolApi,
} from '../../lib/cardpool'

interface Balance { currency: string; available_balance: string; freeze_balance: string }
interface Account {
  account_no: string; out_account_no: string; account_name: string
  account_type: 'MAIN' | 'SUB_ACCOUNT'; purpose: string; status: string
  balances: Balance[] | null; gone: boolean; synced_at: number; created_at: number
}
interface Order {
  out_order_no: string; kind: string; amount_cents: number; currency: string; buy_currency: string
  payer_account_no: string; payee_account_no: string; detail: string; state: string
  last_error: string; note: string; created_at: number; finished_at: number
}
interface OrderList { kind: string; items: Order[]; total: number; page: number; loading: boolean }

const PAGE_SIZE = 20
const SER_PAGE_SIZE = 20
const TYPE_LABEL: Record<string, string> = { MAIN: '主钱包', SUB_ACCOUNT: '子账户' }
const STATUS_LABEL: Record<string, string> = {
  ACTIVE: '启用', DISABLED: '停用', INITIALIZING: '初始化中', FAILED: '失败', REJECTED: '已拒绝',
}

const tab = ref('accounts')
let unmounted = false

function acctTagType(s: string) {
  switch (s) {
    case 'ACTIVE': return 'success'
    case 'FAILED': case 'REJECTED': return 'danger'
    case 'INITIALIZING': return 'warning'
    default: return 'info'
  }
}
function orderTagType(s: string) {
  switch (s) {
    case 'success': return 'success'
    case 'failed': case 'not_sent': return 'info'
    case 'needs_review': return 'danger'
    case 'unknown': return 'warning'
    default: return ''
  }
}
function detailOf(o: Order): Record<string, string> {
  if (!o.detail) return {}
  try {
    const v = JSON.parse(o.detail)
    return v && typeof v === 'object' ? v : {}
  } catch {
    return {}
  }
}

// ---------- 账户 ----------
const accounts = ref<Account[]>([])
const accountsLoading = ref(false)
const pendingRefresh = ref(false)
let refreshTimer: ReturnType<typeof setTimeout> | null = null

function accountRowClass({ row }: { row: Account }) {
  return row.gone ? 'row-gone' : ''
}

async function loadAccounts(refresh = false) {
  accountsLoading.value = true
  try {
    const r = await poolApi<{ items: Account[] }>(`/wallet/accounts${refresh ? '?refresh=1' : ''}`)
    if (unmounted) return
    if (!r.ok) return dialog.toast(errText(r, '加载账户失败'), 'err')
    accounts.value = r.data.items || []
  } finally {
    accountsLoading.value = false
  }
}

/** 创建 / 启停结果未知：提示并在 10 秒后强制同步一次确认。 */
function scheduleConfirmRefresh() {
  pendingRefresh.value = true
  dialog.toast('结果未知，10 秒后自动刷新确认', 'warn', 6000)
  if (refreshTimer) clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    refreshTimer = null
    pendingRefresh.value = false
    if (!unmounted) loadAccounts(true)
  }, 10000)
}

const createOpen = ref(false)
const createName = ref('')
const createSubmitting = ref(false)

function openCreate() {
  createName.value = ''
  createOpen.value = true
}
async function submitCreate() {
  if (createSubmitting.value) return
  const name = createName.value
  if (!name || name.length > 50 || /\s/.test(name)) {
    return dialog.toast('账户名需 1–50 字且不含空白字符', 'warn')
  }
  createSubmitting.value = true
  try {
    const r = await poolApi('/wallet/accounts', 'POST', { account_name: name })
    if (isUncertain(r)) {
      createOpen.value = false
      return scheduleConfirmRefresh()
    }
    if (!r.ok) return dialog.toast(errText(r, '创建失败'), 'err')
    createOpen.value = false
    dialog.toast('子账户已创建', 'ok')
    loadAccounts()
  } finally {
    createSubmitting.value = false
  }
}

async function changeStatus(row: Account, target: 'ACTIVE' | 'DISABLED') {
  const verb = target === 'ACTIVE' ? '启用' : '停用'
  const tip = target === 'DISABLED' ? '\n停用会冻结该账户下发的卡；卡池的卡都从主钱包开，不受影响。' : ''
  const reason = await dialog.prompt(
    `${verb}子账户 ${row.account_name}（${row.account_no}）？${tip}\n请填写变更原因（2–100 字，不含 emoji）：`,
    { title: `${verb}子账户`, placeholder: '变更原因', okText: verb },
  )
  if (reason === null || reason === undefined) return
  const text = String(reason).trim()
  if (text.length < 2 || [...text].length > 100) return dialog.toast('原因需 2–100 字', 'warn')
  if (hasEmoji(text)) return dialog.toast('原因不能包含 emoji', 'warn')
  const r = await poolApi(`/wallet/accounts/${encodeURIComponent(row.account_no)}/status`, 'POST', { status: target, remark: text })
  if (isUncertain(r)) return scheduleConfirmRefresh()
  if (!r.ok) return dialog.toast(errText(r, `${verb}失败`), 'err')
  dialog.toast(`已${verb}`, 'ok')
  loadAccounts()
}

// ---------- 充值地址 ----------
interface Addr { currencies: string[]; address: string; chain_type: string }
const addresses = ref<Addr[]>([])
const addrLoading = ref(false)
async function loadAddresses() {
  addrLoading.value = true
  try {
    const r = await poolApi<{ items: Addr[] }>('/wallet/receiving-addresses')
    if (unmounted) return
    if (!r.ok) return dialog.toast(errText(r, '加载充值地址失败'), 'err')
    addresses.value = r.data.items || []
  } finally {
    addrLoading.value = false
  }
}
async function copyAddr(a: string) {
  const ok = await copyToClipboard(a)
  dialog.toast(ok ? '已复制' : '复制失败，请手动选择', ok ? 'ok' : 'warn')
}

// ---------- 单据（兑换 / 转账共用）----------
const convOrders = reactive<OrderList>({ kind: 'conversion', items: [], total: 0, page: 1, loading: false })
const xferOrders = reactive<OrderList>({ kind: 'wallet_transfer', items: [], total: 0, page: 1, loading: false })

async function loadOrders(list: OrderList) {
  list.loading = true
  try {
    const r = await poolApi<{ items: Order[]; total: number }>(`/orders?kind=${list.kind}&page=${list.page}&page_size=${PAGE_SIZE}`)
    if (unmounted) return
    if (!r.ok) return dialog.toast(errText(r, '加载单据失败'), 'err')
    list.items = r.data.items || []
    list.total = r.data.total || 0
  } finally {
    list.loading = false
  }
}
async function recheck(row: Order, list: OrderList) {
  const r = await poolApi(`/orders/${encodeURIComponent(row.out_order_no)}/recheck`, 'POST')
  if (!r.ok) return dialog.toast(errText(r, '重查失败'), 'err')
  dialog.toast(`重查完成：${r.data?.state || ''}`, 'info')
  loadOrders(list)
}
async function confirmFailed(row: Order, list: OrderList) {
  const note = await dialog.prompt(
    `确认单据 ${row.out_order_no}（${kindLabel(row.kind)} ${money(row.amount_cents)} ${row.currency}）已失败？\n请先在平台核实确未执行。请填写备注（必填）：`,
    { title: '确认失败', placeholder: '备注', okText: '确认失败' },
  )
  if (note === null || note === undefined) return
  const text = String(note).trim()
  if (!text) return dialog.toast('请填写备注', 'warn')
  const r = await poolApi(`/orders/${encodeURIComponent(row.out_order_no)}/confirm-failed`, 'POST', { note: text })
  if (!r.ok) return dialog.toast(errText(r, '操作失败'), 'err')
  dialog.toast('已确认失败', 'ok')
  loadOrders(list)
}

/** 提交资金单后的统一结果处理；返回是否已成功受理。 */
function afterSubmit(r: { ok: boolean; status: number; data: any }, list: OrderList, what: string): boolean {
  if (isUncertain(r)) {
    dialog.toast(r.status === 0 ? '网络异常，结果未知，请先查看下方单据，切勿重复提交' : `${what}结果未知，已记录单据，请在下方列表查看并重查，切勿重复提交`, 'warn', 8000)
    loadOrders(list)
    return false
  }
  if (!r.ok) {
    dialog.toast(errText(r, `${what}失败`), 'err')
    return false
  }
  dialog.toast(`${what}已提交：${r.data?.state || ''}`, 'ok')
  loadOrders(list)
  return true
}

// ---------- 兑换 ----------
const mainAccount = computed(() => accounts.value.find((a) => a.account_type === 'MAIN' && !a.gone))
const mainCurrencies = computed(() => {
  const cs = (mainAccount.value?.balances || []).map((b) => b.currency)
  return cs.includes('USDT') ? cs : ['USDT', ...cs]
})
const conv = reactive({ sell_currency: 'USDT', sell_amount: '', buy_currency: 'USD' })
const convSubmitting = ref(false)

async function submitConversion() {
  if (convSubmitting.value) return
  const cents = parseAmountCents(conv.sell_amount)
  if (cents === null) return dialog.toast('金额格式不正确（最多 2 位小数，须大于 0）', 'warn')
  const buy = conv.buy_currency.trim().toUpperCase()
  if (!conv.sell_currency || !buy) return dialog.toast('请填写币种', 'warn')
  if (buy === conv.sell_currency) return dialog.toast('买入与卖出币种不能相同', 'warn')
  const amount = money(cents)
  const ok = await dialog.confirm(
    `卖出币种：${conv.sell_currency}\n卖出金额：${amount}\n买入币种：${buy}\n汇率与手续费以平台成交为准。确认提交兑换？`,
    { title: '确认兑换', okText: '确认兑换' },
  )
  if (!ok) return
  convSubmitting.value = true
  try {
    const r = await poolApi('/wallet/conversions', 'POST', { sell_currency: conv.sell_currency, sell_amount: amount, buy_currency: buy })
    if (afterSubmit(r, convOrders, '兑换')) {
      conv.sell_amount = ''
      loadAccounts()
    }
  } finally {
    convSubmitting.value = false
  }
}

// ---------- 转账 ----------
const xfer = reactive({ direction: 'to_sub', sub_no: '', currency: '', amount: '', remark: '' })
const xferSubmitting = ref(false)
const activeSubs = computed(() => accounts.value.filter((a) => a.account_type === 'SUB_ACCOUNT' && a.status === 'ACTIVE' && !a.gone))
const payerAccount = computed(() =>
  xfer.direction === 'to_sub' ? mainAccount.value : accounts.value.find((a) => a.account_no === xfer.sub_no))
const payerCurrencies = computed(() => (payerAccount.value?.balances || []).map((b) => b.currency))

function onDirectionChange() { xfer.currency = '' }
function onSubChange() { xfer.currency = '' }

async function submitTransfer() {
  if (xferSubmitting.value) return
  const main = mainAccount.value
  const sub = activeSubs.value.find((a) => a.account_no === xfer.sub_no)
  if (!main) return dialog.toast('未找到主钱包，请先刷新账户', 'warn')
  if (!sub) return dialog.toast('请选择启用中的子账户', 'warn')
  if (!xfer.currency) return dialog.toast('请选择币种', 'warn')
  const cents = parseAmountCents(xfer.amount)
  if (cents === null) return dialog.toast('金额格式不正确（最多 2 位小数，须大于 0）', 'warn')
  const remark = xfer.remark.trim()
  if ([...remark].length > 200) return dialog.toast('备注最多 200 字', 'warn')
  const payer = xfer.direction === 'to_sub' ? main : sub
  const payee = xfer.direction === 'to_sub' ? sub : main
  const amount = money(cents)
  const ok = await dialog.confirm(
    `付款账户：${payer.account_name}（${payer.account_no}）\n收款账户：${payee.account_name}（${payee.account_no}）\n币种：${xfer.currency}\n金额：${amount}\n备注：${remark || '无'}\n确认提交转账？`,
    { title: '确认转账', okText: '确认转账' },
  )
  if (!ok) return
  xferSubmitting.value = true
  try {
    const r = await poolApi('/wallet/transfers', 'POST', {
      payer_account_no: payer.account_no, payee_account_no: payee.account_no,
      currency: xfer.currency, amount, remark,
    })
    if (afterSubmit(r, xferOrders, '转账')) {
      xfer.amount = ''
      loadAccounts()
    }
  } finally {
    xferSubmitting.value = false
  }
}

// ---------- 流水 ----------
const DAY_MS = 86400000
const MAX_DAYS = 90
function ymd(d: Date) {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}
function parseYmd(s: string) {
  const [y, m, d] = s.split('-').map(Number)
  return new Date(y, m - 1, d)
}
function addDays(s: string, n: number) {
  const d = parseYmd(s)
  d.setDate(d.getDate() + n)
  return ymd(d)
}
const today = ymd(new Date())
const ser = reactive({
  account_no: '', currency: '', start: addDays(today, -6), end: today,
  items: [] as any[], total: 0, page: 1, loading: false,
})
const ledgerAccounts = computed(() => accounts.value.filter((a) => !a.gone))
const serCurrencies = computed(() =>
  (accounts.value.find((a) => a.account_no === ser.account_no)?.balances || []).map((b) => b.currency))
// 起止最多相隔 90 天（含首尾）：起点不早于终点前 89 天，终点不晚于起点后 89 天且不晚于今天
const serStartMin = computed(() => addDays(ser.end || today, -(MAX_DAYS - 1)))
const serEndMax = computed(() => {
  const cap = ser.start ? addDays(ser.start, MAX_DAYS - 1) : today
  return cap < today ? cap : today
})

function onSerAccountChange() { ser.currency = '' }

async function querySerials(page = 1) {
  if (!ser.account_no) return dialog.toast('请选择账户', 'warn')
  if (!ser.currency) return dialog.toast('请选择币种', 'warn')
  if (!ser.start || !ser.end || ser.start > ser.end) return dialog.toast('请选择有效的时间段', 'warn')
  const spanDays = Math.round((parseYmd(ser.end).getTime() - parseYmd(ser.start).getTime()) / DAY_MS) + 1
  if (spanDays > MAX_DAYS) return dialog.toast('时间段不能超过 90 天', 'warn')
  const startSec = Math.floor(parseYmd(ser.start).getTime() / 1000)
  const endSec = Math.min(Math.floor(parseYmd(addDays(ser.end, 1)).getTime() / 1000) - 1, Math.floor(Date.now() / 1000))
  ser.page = page
  ser.loading = true
  try {
    const q = new URLSearchParams({
      account_no: ser.account_no, currency: ser.currency,
      start_time: String(startSec), end_time: String(endSec),
      page: String(page), page_size: String(SER_PAGE_SIZE),
    })
    const r = await poolApi<{ items: any[]; total: number }>(`/wallet/serials?${q.toString()}`)
    if (unmounted) return
    if (!r.ok) return dialog.toast(errText(r, '查询流水失败'), 'err')
    ser.items = r.data.items || []
    ser.total = r.data.total || 0
  } finally {
    ser.loading = false
  }
}

// ---------- 生命周期 ----------
function onTabChange(name: string | number) {
  if (name === 'addresses') loadAddresses()
  else if (name === 'conversion') loadOrders(convOrders)
  else if (name === 'transfer') loadOrders(xferOrders)
}

onMounted(async () => {
  await loadAccounts(true)
  const main = mainAccount.value
  if (main) ser.account_no = main.account_no
})
onBeforeUnmount(() => {
  unmounted = true
  if (refreshTimer) clearTimeout(refreshTimer)
})
</script>

<style scoped>
.text-warn { color: #d97706; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.date-input {
  height: 32px;
  padding: 0 8px;
  border-radius: 4px;
  border: 1px solid rgba(127, 127, 127, 0.35);
  background: transparent;
  color: inherit;
}
:deep(.row-gone) { opacity: 0.5; }
</style>
