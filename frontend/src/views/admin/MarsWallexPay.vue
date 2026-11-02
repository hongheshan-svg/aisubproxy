<template>
  <div class="space-y-4">
    <AdminPageHeader title="自动付款" description="用 ChatGPT 会话和卡池里的卡完成套餐付款：发起付款、付款订单与付款配置" class="!mb-0" />
    <div class="card space-y-3">

      <el-tabs v-model="tab" @tab-change="onTabChange">
        <!-- ============ 测试兑换 ============ -->
        <el-tab-pane label="发起付款" name="test">
          <el-alert v-if="config?.lease_warning" type="warning" :closable="false" show-icon class="mb-3" :title="leaseWarningText" />
          <div class="space-y-3 max-w-3xl" v-loading="configLoading">
            <div>
              <div class="text-sm mb-1">ChatGPT 会话</div>
              <el-input
                v-model="session" type="textarea" :rows="5" autocomplete="off" spellcheck="false"
                placeholder="粘贴 ChatGPT 会话（JSON 文本）。提交成功后会立即清空，不会保存在浏览器里"
              />
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <el-select v-model="planKey" placeholder="套餐" style="width: 180px">
                <el-option v-for="p in config?.plans || []" :key="p.key" :label="p.label || p.key" :value="p.key" />
              </el-select>
              <el-select v-model="region" placeholder="地区" style="width: 140px">
                <el-option v-for="r in regionList" :key="r" :label="r" :value="r" />
              </el-select>
              <el-switch v-model="dryRun" active-text="试运行（不选卡、不付款）" inactive-text="关闭 = 真实付款" />
            </div>
            <div class="text-sm">
              <template v-if="regionConf && regionConf.cap_minor > 0">
                应付金额上限：<b>{{ formatMinor(regionConf.cap_minor, regionConf.currency) }}</b>；
                注资：<b>{{ usd(regionConf.fund_cents) }} 美元</b>
              </template>
              <span v-else class="text-muted">该套餐在此地区未配置金额</span>
            </div>
            <div v-if="submitBlockReason" class="text-sm text-warn">{{ submitBlockReason }}</div>
            <div>
              <el-button :type="dryRun ? 'primary' : 'danger'" :loading="submitting" :disabled="!!submitBlockReason || !session.trim()" @click="submit">
                {{ dryRun ? '试运行' : '真实付款' }}
              </el-button>
            </div>
          </div>
        </el-tab-pane>

        <!-- ============ 订单 ============ -->
        <el-tab-pane label="付款订单" name="orders">
          <div class="flex flex-wrap items-center gap-2 mb-3">
            <el-select v-model="stateFilter" clearable placeholder="全部状态" style="width: 160px" @change="onFilterChange">
              <el-option v-for="s in STATES" :key="s" :label="stateLabel(s)" :value="s" />
            </el-select>
            <el-button :loading="ordersLoading" @click="loadOrders">刷新</el-button>
          </div>
          <el-table :data="orders" size="small" border empty-text="暂无订单" v-loading="ordersLoading" @row-click="(row: any) => openDetail(row.order_no)" class="cursor-pointer">
            <el-table-column label="单号" min-width="190">
              <template #default="{ row }"><span class="mono">{{ row.order_no }}</span></template>
            </el-table-column>
            <el-table-column label="套餐 / 地区" min-width="120">
              <template #default="{ row }">{{ row.plan_key }} / {{ row.region }}</template>
            </el-table-column>
            <el-table-column label="状态" min-width="120">
              <template #default="{ row }">
                <el-tag size="small" :type="stateTagType(row.state)">{{ stateLabel(row.state) }}</el-tag>
                <el-tag v-if="row.dry_run" size="small" type="info" class="ml-1">试运行</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="金额" min-width="130">
              <template #default="{ row }">{{ row.amount_minor > 0 ? formatMinor(row.amount_minor, row.currency) : '—' }}</template>
            </el-table-column>
            <el-table-column label="卡尾号" width="80">
              <template #default="{ row }">{{ row.card_last4 || '—' }}</template>
            </el-table-column>
            <el-table-column label="账号" min-width="170" show-overflow-tooltip>
              <template #default="{ row }">{{ row.account_email || '—' }}</template>
            </el-table-column>
            <el-table-column label="创建时间" min-width="160">
              <template #default="{ row }">{{ fmtTs(row.created_at) }}</template>
            </el-table-column>
          </el-table>
          <div class="mt-3 flex justify-end">
            <el-pagination
              v-model:current-page="page" :page-size="pageSize" :total="total" layout="total, prev, pager, next" size="small"
              @current-change="loadOrders"
            />
          </div>
        </el-tab-pane>

        <!-- ============ 配置 ============ -->
        <el-tab-pane label="付款配置" name="config">
          <el-alert v-if="config?.lease_warning" type="warning" :closable="false" show-icon class="mb-3" :title="leaseWarningText" />
          <div class="space-y-4" v-loading="configLoading">
            <div v-for="p in plansForm" :key="p.key" class="space-y-2">
              <div class="font-semibold">
                {{ p.label || p.key }}
                <el-tag size="small" class="ml-2" :type="p.rows.some((r: any) => r.enabled) ? 'success' : 'info'">已启用 {{ p.rows.filter((r: any) => r.enabled).length }}/{{ p.rows.length }}</el-tag>
              </div>
              <el-table :data="p.rows" size="small" border>
                <el-table-column label="地区" width="70" prop="region" />
                <el-table-column label="启用" width="70">
                  <template #default="{ row }"><el-switch v-model="row.enabled" /></template>
                </el-table-column>
                <el-table-column label="币种" width="110">
                  <template #default="{ row }"><el-input v-model="row.currency" size="small" maxlength="3" autocomplete="off" /></template>
                </el-table-column>
                <el-table-column label="应付金额上限" min-width="170">
                  <template #default="{ row }">
                    <el-input-number v-model="row.cap" size="small" :min="0" :precision="currencyDigits(row.currency)" :controls="false" style="width: 130px" />
                  </template>
                </el-table-column>
                <el-table-column label="注资（美元）" min-width="150">
                  <template #default="{ row }">
                    <el-input-number v-model="row.fund" size="small" :min="0" :precision="2" :controls="false" style="width: 110px" />
                  </template>
                </el-table-column>
              </el-table>
            </div>
            <div class="flex flex-wrap items-center gap-3">
              <span class="text-sm">并发数</span>
              <el-input-number v-model="concurrency" :min="1" :max="maxConcurrency" size="small" />
              <span class="text-sm text-muted">最多 {{ maxConcurrency }}</span>
              <el-button type="primary" :loading="saving" @click="saveConfig">保存</el-button>
              <el-button v-if="formDirty" :loading="configLoading" @click="discardEdits">放弃修改，重新读取</el-button>
              <span class="text-sm text-muted">套餐表与并发数一并保存</span>
              <span v-if="formDirty" class="text-sm text-warn">有未保存的修改</span>
            </div>
          </div>

          <div class="mt-6 space-y-2">
            <div class="font-semibold">各地区代理</div>
            <p class="text-sm text-muted">代理地址只写不读：已配置只显示「已配置」，留空表示不修改。socks5 不支持账号密码，需要账号密码请用 http 代理。</p>
            <div v-if="proxiesFailed" class="text-sm text-warn">
              代理状态读取失败，下面的状态不可信。<el-button size="small" link type="primary" @click="loadProxies">重试</el-button>
            </div>
            <div v-for="r in regionList" :key="r" class="flex flex-wrap items-center gap-2">
              <span class="w-10 text-sm">{{ r }}</span>
              <el-tag v-if="proxiesFailed" size="small" type="warning">读取失败</el-tag>
              <el-tag v-else size="small" :type="proxies[r] ? 'success' : 'info'">{{ proxies[r] ? '已配置' : '未配置' }}</el-tag>
              <el-input
                v-model="proxyInputs[r]" type="password" autocomplete="new-password" style="width: 320px" size="small"
                :placeholder="proxies[r] ? '留空不修改，填写则覆盖' : '如 http://user:pass@host:port'"
              />
              <el-button v-if="proxies[r]" size="small" type="danger" plain :loading="proxySaving" @click="clearProxy(r)">清除</el-button>
            </div>
            <el-button type="primary" :loading="proxySaving" :disabled="!hasProxyInput" @click="saveProxies">保存代理</el-button>
          </div>

          <div class="mt-6 space-y-2">
            <div class="font-semibold">浏览器自检</div>
            <el-button :loading="checking" :disabled="checking" @click="runCheck">运行浏览器自检</el-button>
            <div v-if="checkResult" class="text-sm space-y-1">
              <template v-if="checkResult.ok">
                <div class="text-ok">自检通过</div>
                <div>版本：{{ checkResult.version }}；模式：{{ checkResult.mode }}；耗时：{{ checkResult.elapsed_ms }} 毫秒</div>
              </template>
              <template v-else>
                <div class="text-err">自检失败：{{ checkResult.error }}</div>
                <div v-if="checkResult.install_hint">
                  安装命令：<code class="mono select-all">{{ checkResult.install_hint }}</code>
                </div>
              </template>
            </div>
          </div>
        </el-tab-pane>
      </el-tabs>
    </div>

    <!-- ============ 订单详情 ============ -->
    <el-drawer v-model="detailOpen" :title="`订单 ${detailNo}`" size="min(760px, 100vw)" @closed="onDetailClosed">
      <div v-loading="detailLoading" class="space-y-4">
        <template v-if="detail">
          <el-descriptions :column="1" size="small" border label-width="84px">
            <el-descriptions-item label="状态">
              <el-tag size="small" :type="stateTagType(detail.order.state)">{{ stateLabel(detail.order.state) }}</el-tag>
              <el-tag v-if="detail.order.dry_run" size="small" type="info" class="ml-1">试运行</el-tag>
            </el-descriptions-item>
            <el-descriptions-item label="套餐 / 地区">{{ detail.order.plan_key }} / {{ detail.order.region }}</el-descriptions-item>
            <el-descriptions-item label="金额">
              {{ detail.order.amount_minor > 0 ? formatMinor(detail.order.amount_minor, detail.order.currency) : '—' }}
              （上限 {{ formatMinor(detail.order.cap_minor, detail.order.currency) }}，注资 {{ usd(detail.order.fund_cents) }} 美元）
            </el-descriptions-item>
            <el-descriptions-item label="账号">{{ detail.order.account_email || '—' }}</el-descriptions-item>
            <el-descriptions-item label="卡">尾号 {{ detail.order.card_last4 || '—' }}；注资单 {{ detail.order.fund_order_no || '—' }}</el-descriptions-item>
            <el-descriptions-item label="页面提示">{{ pageOutcomeLabel(detail.order.page_outcome) }}</el-descriptions-item>
            <el-descriptions-item label="提交时间">{{ fmtTs(detail.order.submitted_at) }}</el-descriptions-item>
            <el-descriptions-item label="创建 / 完成">{{ fmtTs(detail.order.created_at) }} / {{ fmtTs(detail.order.finished_at) }}</el-descriptions-item>
            <el-descriptions-item v-if="detail.order.failure || detail.order.error_message" label="失败原因">
              <div v-if="detail.order.failure">{{ failureText(detail.order.failure, detail.order.resolved_by) }}</div>
              <div v-if="detail.order.error_message">{{ detail.order.error_message }}</div>
              <div v-if="detail.order.error_code" class="text-xs text-muted">{{ detail.order.error_code }}</div>
            </el-descriptions-item>
            <el-descriptions-item v-if="detail.order.resolved_by" label="人工定论">
              {{ detail.order.resolved_by }}：{{ detail.order.resolve_note }}
            </el-descriptions-item>
          </el-descriptions>

          <div>
            <div class="font-semibold mb-2">时间线</div>
            <div v-if="!detail.events.length" class="text-sm text-muted">暂无记录</div>
            <div v-for="e in detail.events" :key="e.id" class="text-sm py-1 border-b border-gray-500/20">
              <span class="text-muted">{{ fmtTs(e.at) }}</span>
              <el-tag size="small" class="mx-1" :type="e.level === 'error' ? 'danger' : e.level === 'warn' ? 'warning' : 'info'">{{ stateLabel(e.state) }}</el-tag>
              <span>{{ e.message }}</span>
              <el-button v-if="e.screenshot" size="small" link type="primary" :loading="shotLoading === e.screenshot" @click="openShot(e.screenshot)">查看截图</el-button>
            </div>
          </div>

          <div>
            <div class="font-semibold mb-2">该卡的授权交易（提交前 5 分钟起）</div>
            <p class="text-xs text-muted mb-2">同一张卡以前的旧交易可能混入，请对照时间判断。</p>
            <el-alert v-if="detail.evidence_error" type="warning" :closable="false" show-icon title="授权交易暂时读不出" description="这不代表没有授权交易，请稍后刷新再看，不要据此判定未扣款。" />
            <el-table v-else :data="detail.evidence" size="small" border empty-text="没有授权交易">
              <el-table-column label="时间" min-width="150"><template #default="{ row }">{{ fmtTs(row.occurred_at) }}</template></el-table-column>
              <el-table-column prop="kind" label="类型" min-width="90" />
              <el-table-column prop="status" label="状态" min-width="90" />
              <el-table-column label="金额" min-width="110"><template #default="{ row }">{{ row.txn_amount }} {{ row.txn_currency }}</template></el-table-column>
              <el-table-column prop="merchant_name" label="商户" min-width="130" show-overflow-tooltip />
              <el-table-column prop="decline_reason" label="拒绝原因" min-width="120" show-overflow-tooltip />
            </el-table>
          </div>

          <div v-if="detail.order.state === 'needs_review'" class="space-y-2">
            <div class="font-semibold">人工定论</div>
            <el-input v-model="resolveNote" type="textarea" :rows="3" placeholder="备注（必填）：说明判定依据" />
            <div class="flex gap-2">
              <el-button type="success" :loading="resolving" @click="resolve('succeeded')">标记成功</el-button>
              <el-button type="danger" :loading="resolving" @click="resolve('failed')">标记失败</el-button>
            </div>
          </div>
        </template>
      </div>
    </el-drawer>

    <el-dialog v-model="shotOpen" title="截图" width="min(900px, 96vw)" append-to-body @closed="clearShot">
      <img v-if="shotUrl" :src="shotUrl" alt="付款过程截图" style="max-width: 100%" />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'
import { errText, fmtTs } from '../../lib/cardpool'
import {
  STATES, currencyDigits, failureText, formatMinor, fromMinor, isTerminal, pageOutcomeLabel, payApi, stateLabel, toMinor, payBlockReason,
} from '../../lib/pay'

const REGIONS = ['PH', 'US', 'JP', 'CL', 'EG']

interface RegionConf { enabled: boolean; currency: string; cap_minor: number; fund_cents: number }
interface PlanConf {
  key: string; label: string; chatgpt_plan_name: string; expect_plan_type: string
  regions: Record<string, RegionConf>
}
interface PayConfig {
  plans: PlanConf[]; concurrency: number
  lease_minutes: number; max_pay_minutes: number; max_concurrency: number; lease_warning: boolean
}
interface FormRow { region: string; enabled: boolean; currency: string; cap: number | null; fund: number | null }
interface FormPlan { key: string; label: string; chatgpt_plan_name: string; expect_plan_type: string; rows: FormRow[] }

const tab = ref('test')
const regionList = REGIONS
const usd = (cents: number) => (cents / 100).toFixed(2)

// ---------- 配置 ----------
const config = ref<PayConfig | null>(null)
const configLoading = ref(false)
const plansForm = ref<FormPlan[]>([])
const concurrency = ref(1)
const saving = ref(false)

const leaseWarningText = computed(() =>
  `卡池租约时长 ${config.value?.lease_minutes} 分钟，少于付款流程最长的 ${config.value?.max_pay_minutes} 分钟，卡可能在确认结束前被回收。请到卡池策略里调大。`)

const maxConcurrency = computed(() => config.value?.max_concurrency || 3)
let formSnapshot = '' // 上次同步到表单时的内容，用来判断有没有未保存的编辑
const formState = () => JSON.stringify([plansForm.value, concurrency.value])
const formDirty = computed(() => formSnapshot !== '' && formState() !== formSnapshot)

// syncForm=false 时只更新服务端数据 config，不动 plansForm 编辑缓冲。
function applyConfig(c: PayConfig, syncForm = true) {
  config.value = c
  // 默认选第一个能提交的套餐（Go 的套餐名待核实，选中它只会看到禁用的按钮）
  if (!planKey.value && c.plans.length) planKey.value = (c.plans.find((p) => p.chatgpt_plan_name) ?? c.plans[0]).key
  if (!region.value) region.value = REGIONS[0]
  if (!syncForm) return
  concurrency.value = c.concurrency
  plansForm.value = c.plans.map((p) => ({
    key: p.key, label: p.label, chatgpt_plan_name: p.chatgpt_plan_name, expect_plan_type: p.expect_plan_type,
    rows: REGIONS.map((r) => {
      const rc = p.regions?.[r] || { enabled: false, currency: '', cap_minor: 0, fund_cents: 0 }
      return { region: r, enabled: rc.enabled, currency: rc.currency, cap: fromMinor(rc.cap_minor, rc.currency), fund: rc.fund_cents / 100 }
    }),
  }))
  formSnapshot = formState()
}

async function loadConfig({ syncForm = true } = {}): Promise<boolean> {
  configLoading.value = true
  const r = await payApi<PayConfig>('/config')
  configLoading.value = false
  if (!r.ok) { dialog.toast(errText(r, '读取配置失败'), 'err'); return false }
  applyConfig(r.data, syncForm)
  return true
}

function discardEdits() { loadConfig() }

async function saveConfig() {
  const plans = plansForm.value.map((p) => ({
    key: p.key, label: p.label, chatgpt_plan_name: p.chatgpt_plan_name, expect_plan_type: p.expect_plan_type,
    regions: Object.fromEntries(p.rows.map((row) => {
      const cur = row.currency.trim().toUpperCase()
      return [row.region, {
        enabled: row.enabled, currency: cur,
        cap_minor: toMinor(row.cap || 0, cur), fund_cents: Math.round((row.fund || 0) * 100),
      }]
    })),
  }))
  saving.value = true
  const r = await payApi<PayConfig>('/config', 'PUT', { plans, concurrency: concurrency.value })
  saving.value = false
  if (!r.ok) { dialog.toast(errText(r, '保存失败'), 'err'); return }
  applyConfig(r.data)
  dialog.toast('配置已保存')
}

// ---------- 代理（只写） ----------
const proxies = ref<Record<string, boolean>>({})
const proxiesFailed = ref(false)
const proxyInputs = reactive<Record<string, string>>({})
const proxySaving = ref(false)
const hasProxyInput = computed(() => REGIONS.some((r) => (proxyInputs[r] || '').trim() !== ''))

async function loadProxies() {
  const r = await payApi<Record<string, boolean>>('/proxies')
  proxiesFailed.value = !r.ok
  if (r.ok) proxies.value = r.data
  else dialog.toast(errText(r, '读取代理状态失败'), 'err')
}

async function putProxies(body: Record<string, string>): Promise<boolean> {
  proxySaving.value = true
  const r = await payApi<Record<string, boolean>>('/proxies', 'PUT', body)
  proxySaving.value = false
  if (!r.ok) { dialog.toast(errText(r, '保存代理失败'), 'err'); return false }
  proxies.value = r.data
  proxiesFailed.value = false // 响应里就是最新状态
  return true
}

async function saveProxies() {
  const body: Record<string, string> = {}
  for (const r of REGIONS) { const v = (proxyInputs[r] || '').trim(); if (v) body[r] = v }
  if (!Object.keys(body).length) return
  if (await putProxies(body)) {
    for (const r of REGIONS) proxyInputs[r] = ''
    dialog.toast('代理已保存')
  }
}

async function clearProxy(r: string) {
  if (!(await dialog.confirm(`确认清除 ${r} 地区的代理？`, { title: '清除代理', okText: '清除', danger: true }))) return
  if (await putProxies({ [r]: '-' })) { proxyInputs[r] = ''; dialog.toast('已清除') }
}

// ---------- 浏览器自检 ----------
interface CheckResult { ok: boolean; version: string; mode: string; elapsed_ms: number; error: string; install_hint: string }
const checking = ref(false)
const checkResult = ref<CheckResult | null>(null)

async function runCheck() {
  if (checking.value) return
  checking.value = true
  checkResult.value = null
  const r = await payApi<CheckResult>('/browser-check', 'POST')
  checking.value = false
  if (r.status === 409) { dialog.toast(errText(r, '已有检查在进行'), 'warn'); return }
  if (r.ok) { checkResult.value = r.data; return }
  checkResult.value = { ok: false, version: '', mode: '', elapsed_ms: 0, error: errText(r, '自检请求失败'), install_hint: '' }
}

// ---------- 测试兑换 ----------
const session = ref('')
const planKey = ref('')
const region = ref('')
const dryRun = ref(true)
const submitting = ref(false)

const planConf = computed(() => config.value?.plans.find((p) => p.key === planKey.value))
const regionConf = computed(() => planConf.value?.regions?.[region.value])

// 试运行只要求有 ChatGPT 套餐名；真实付款还要求地区启用且金额已配。
const submitBlockReason = computed(() => {
  if (!config.value) return '配置未加载'
  return payBlockReason(planConf.value, region.value, dryRun.value)
})

async function submit() {
  if (submitting.value || submitBlockReason.value) return
  submitting.value = true // 覆盖确认框期间，防止重复点击
  try {
    // 快照：确认框显示和请求体都只用这些值，期间改了表单也不影响
    const dry = dryRun.value
    const plan = planKey.value
    const reg = region.value
    const sess = session.value.trim()
    if (!dry) {
      // 动钱前以最新配置为准；不覆盖配置页未保存的编辑
      if (!(await loadConfig({ syncForm: false }))) return
      // 按快照判断，不读当前表单：等待期间改了下拉或开关也不影响这次提交
      const pc = config.value?.plans.find((p) => p.key === plan)
      const blocked = payBlockReason(pc, reg, false)
      if (blocked) { dialog.toast(blocked, 'warn'); return }
      const rc = pc?.regions?.[reg]
      if (!rc) return
      const ok = await dialog.confirm(
        `套餐：${pc?.label || plan}\n地区：${reg}\n卡：付款时从卡池选卡（提交时尚未确定卡尾号）\n` +
          `将取一张卡注资 ${usd(rc.fund_cents)} 美元并真实付款，应付金额上限 ${formatMinor(rc.cap_minor, rc.currency)}。\n确认真实付款？`,
        { title: '确认付款', okText: '确认付款', danger: true },
      )
      if (!ok) return
    }
    const r = await payApi<{ order_no: string }>('/orders', 'POST', {
      plan_key: plan, region: reg, session: sess, dry_run: dry,
    })
    if (!r.ok) { dialog.toast(errText(r, '提交失败'), 'err'); return } // 失败保留会话，方便改完再试
    session.value = ''
    dialog.toast(dry ? '试运行已受理' : '付款已受理')
    tab.value = 'orders'
    page.value = 1
    await loadOrders()
    openDetail(r.data.order_no)
  } finally {
    submitting.value = false
  }
}

// ---------- 订单 ----------
const orders = ref<any[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const stateFilter = ref('')
const ordersLoading = ref(false)

function stateTagType(s: string): 'success' | 'danger' | 'warning' | 'info' | 'primary' {
  if (s === 'succeeded') return 'success'
  if (s === 'failed') return 'danger'
  if (s === 'needs_review') return 'warning'
  if (s === 'previewed') return 'info'
  return 'primary'
}

let ordersSeq = 0
async function loadOrders() {
  const my = ++ordersSeq
  ordersLoading.value = true
  const q = new URLSearchParams({ page: String(page.value), page_size: String(pageSize) })
  if (stateFilter.value) q.set('state', stateFilter.value)
  const r = await payApi<{ orders: any[]; total: number }>('/orders?' + q.toString())
  if (my !== ordersSeq) return // 已有更新的请求，丢弃旧响应
  ordersLoading.value = false
  if (!r.ok) { dialog.toast(errText(r, '读取订单失败'), 'err'); return }
  orders.value = r.data.orders || []
  total.value = r.data.total || 0
}

function onFilterChange() { page.value = 1; loadOrders() }

function onTabChange(name: string | number) {
  if (name === 'orders') loadOrders()
  if (name === 'config') { loadConfig({ syncForm: !formDirty.value }); loadProxies() }
}

// ---------- 详情、轮询、截图 ----------
interface Detail { order: any; events: any[]; evidence: any[]; evidence_error?: string }
const detailOpen = ref(false)
const detailNo = ref('')
const detail = ref<Detail | null>(null)
const detailLoading = ref(false)
const resolveNote = ref('')
const resolving = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null
let seq = 0 // 切换单据后丢弃旧请求的返回

function stopPoll() { if (pollTimer) { clearTimeout(pollTimer); pollTimer = null } }

// 只在抽屉打开且单据非终态时轮询；上一次返回后才排下一次，所以不会重叠。
function schedulePoll() {
  stopPoll()
  if (!detailOpen.value || !detail.value || isTerminal(detail.value.order.state)) return
  pollTimer = setTimeout(async () => {
    await loadDetail(detailNo.value, true)
    schedulePoll()
  }, 5000)
}

async function loadDetail(no: string, silent = false) {
  const my = ++seq
  if (!silent) detailLoading.value = true
  const r = await payApi<Detail>('/orders/' + encodeURIComponent(no))
  if (my !== seq) return
  detailLoading.value = false
  if (!r.ok) {
    if (!silent) dialog.toast(errText(r, '读取订单详情失败'), 'err')
    return
  }
  detail.value = { ...r.data, events: r.data.events || [], evidence: r.data.evidence || [] }
}

async function openDetail(no: string) {
  stopPoll()
  clearShot()
  detail.value = null
  resolveNote.value = ''
  detailNo.value = no
  detailOpen.value = true
  await loadDetail(no)
  if (detailNo.value === no) schedulePoll()
}

function onDetailClosed() {
  stopPoll()
  seq++
  clearShot()
  detail.value = null
  detailNo.value = ''
}

const shotOpen = ref(false)
const shotUrl = ref('')
const shotLoading = ref('')

function clearShot() {
  shotOpen.value = false
  if (shotUrl.value) { URL.revokeObjectURL(shotUrl.value); shotUrl.value = '' }
}

async function openShot(name: string) {
  const no = detailNo.value
  shotLoading.value = name
  try {
    const r = await authFetch(`/api/v1/admin/pay/orders/${encodeURIComponent(no)}/shots/${encodeURIComponent(name)}`)
    if (!r.ok) { dialog.toast('截图读取失败', 'err'); return }
    const blob = await r.blob()
    if (no !== detailNo.value) return // 已切换单据
    if (shotUrl.value) URL.revokeObjectURL(shotUrl.value)
    shotUrl.value = URL.createObjectURL(blob)
    shotOpen.value = true
  } catch {
    dialog.toast('截图读取失败', 'err')
  } finally {
    shotLoading.value = ''
  }
}

async function resolve(result: 'succeeded' | 'failed') {
  const note = resolveNote.value.trim()
  if (!note) { dialog.toast('请先填写备注', 'warn'); return }
  const label = result === 'succeeded' ? '成功' : '失败'
  const ok = await dialog.confirm(`确认把本单标记为${label}？只改本单结果，不会触发任何付款。`, { title: '人工定论', okText: `标记${label}`, danger: result === 'failed' })
  if (!ok) return
  const no = detailNo.value
  resolving.value = true
  const r = await payApi('/orders/' + encodeURIComponent(no) + '/resolve', 'POST', { result, note })
  resolving.value = false
  if (r.status === 409) {
    dialog.toast(errText(r, '单据状态已变化'), 'warn')
  } else if (!r.ok) {
    dialog.toast(errText(r, '提交失败'), 'err')
    return
  } else {
    dialog.toast('已标记')
  }
  // POST 期间可能已关闭抽屉或切到别的单据：只有仍是这张单才刷新详情、清备注，免得内容与标题对不上
  if (detailOpen.value && detailNo.value === no) {
    if (r.ok) resolveNote.value = ''
    await loadDetail(no, true)
  }
  loadOrders()
}

onMounted(loadConfig)
onUnmounted(() => {
  stopPoll()
  seq++
  if (shotUrl.value) URL.revokeObjectURL(shotUrl.value)
  session.value = ''
})
</script>

<style scoped>
.text-warn { color: #d97706; }
.text-ok { color: #16a34a; }
.text-err { color: #dc2626; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
</style>
