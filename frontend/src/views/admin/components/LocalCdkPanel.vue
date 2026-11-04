<template>
  <div class="space-y-4" data-panel="local-cdk">
    <!-- ============ 发码 ============ -->
    <section class="card space-y-3">
      <h2 class="text-base font-semibold text-ink">发 {{ name }} 码</h2>
      <p class="text-sm text-muted">
        {{ name }} 码由本站生成，兑换时用这家的虚拟卡注资付款。完整码只在发码成功后显示这一次，本站只存哈希，关闭后无法再次查看。
      </p>

      <ApiErrorNotice :message="readinessError" />
      <el-button v-if="readinessError" size="small" :loading="loadingReadiness" @click="loadReadiness">重试</el-button>

      <div v-if="mw && !mw.ok" class="alert alert-error text-sm" role="alert" data-testid="not-ready">
        <b>{{ name }} 未就绪，暂时不能发码：</b>
        <ul class="list-disc pl-5">
          <li v-for="p in mw.problems" :key="p">{{ p }}</li>
        </ul>
        <router-link v-if="firstPayMissing" :to="{ path: '/ops/marswallex-pay', query: { provider } }" class="underline" data-testid="first-pay-link">
          需要一笔系统自动确认成功的真实付款，去自动付款做
        </router-link>
        <router-link to="/ops/providers" class="underline ml-3">查看就绪详情</router-link>
      </div>

      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <label class="text-sm space-y-1 block">
          <span class="text-muted">套餐 / 地区（只列已启用的组合）</span>
          <el-select v-model="comboKey" class="w-full" placeholder="选择套餐和地区" :disabled="issuing" data-field="combo">
            <el-option v-for="c in combos" :key="comboId(c)" :value="comboId(c)" :disabled="!c.ok"
              :label="`${c.label || c.plan} · ${c.region}${c.ok ? '' : '（不可发）'}`" />
          </el-select>
        </label>
        <label class="text-sm space-y-1 block">
          <span class="text-muted">数量（1-{{ ISSUE_MAX }}）</span>
          <el-input-number v-model="count" :min="1" :max="ISSUE_MAX" :disabled="issuing" class="!w-full" />
        </label>
        <label class="text-sm space-y-1 block">
          <span class="text-muted">备注（选填，最多 200 字）</span>
          <el-input v-model="note" maxlength="200" :disabled="issuing" />
        </label>
      </div>

      <div v-if="combo" class="rounded-lg p-3 text-sm space-y-1 bg-soft" data-testid="combo-info">
        <div>
          每张码兑换时给卡注资 <b>{{ centsToUsd(combo.fund_cents) }} 美元</b>，应付金额上限 <b>{{ formatMinor(combo.cap_minor, combo.currency) }}</b>；
          当前可用卡 {{ combo.cards }} 张
        </div>
        <div v-if="noteOf(combo)" class="font-semibold" style="color: var(--warn)">{{ noteOf(combo) }}</div>
        <div v-if="!combo.ok" class="text-err">不可发：{{ (combo.problems || []).join('；') }}</div>
      </div>

      <label class="flex items-center gap-2 text-sm">
        <el-checkbox v-model="funding" :disabled="issuing">确认承担兑换资金（每张码兑换时由卡池付款）</el-checkbox>
      </label>

      <div class="flex flex-wrap items-center gap-3">
        <el-button type="primary" :loading="issuing" :disabled="!!blockReason" @click="issue()">
          {{ issuing ? '发码中…' : `发 ${count} 张 ${name} 码` }}
        </el-button>
        <span v-if="blockReason" class="text-sm text-muted" data-testid="block-reason">{{ blockReason }}</span>
      </div>

      <div v-if="failure && failure.kind !== 'already_issued'" class="alert alert-error text-sm" role="alert">
        {{ failure.message }}
        <ul v-if="failure.kind === 'not_ready'" class="list-disc pl-5"><li v-for="p in failure.problems" :key="p">{{ p }}</li></ul>
      </div>

      <div v-if="failure && failure.kind === 'already_issued'" class="alert alert-warning space-y-2" role="alert" data-testid="already-issued">
        <div class="font-semibold">这批码已经发过，但无法再显示</div>
        <p class="text-sm">
          同一次发码（同一个幂等键）已经成功，完整码只在第一次响应里返回过一次，本站只存哈希，拿不回来。
          <template v-if="failure.firstId">这批码的 ID 是 {{ failure.firstId }}–{{ failure.lastId }}（共 {{ failure.count }} 张）。</template>
          如果第一次的响应你没有保存，请禁用这批后重发。
        </p>
        <el-button type="danger" size="small" :loading="reissuing" @click="disableAndReissue">禁用这批后重发</el-button>
      </div>
    </section>

    <!-- ============ 列表 ============ -->
    <section class="card space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="text-base font-semibold text-ink">{{ name }} 码列表</h2>
          <p class="text-xs text-muted mt-0.5">共 {{ total }} 条 · 状态由码和付款单派生，不会和单据对不上</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <el-select v-model="fStatus" clearable placeholder="状态" class="!w-[130px]" @change="reload">
            <el-option v-for="(label, k) in STATUS_OPTIONS" :key="k" :label="label" :value="k" />
          </el-select>
          <el-input v-model="fPlan" clearable placeholder="套餐，如 plus" class="!w-[140px]" @keyup.enter="reload" @clear="reload" />
          <el-select v-model="fAgent" clearable placeholder="代理商" class="!w-[150px]" data-field="agent" @change="reload">
            <el-option label="后台发的码" value="0" />
            <el-option v-for="a in agentOptions" :key="a.id" :label="a.username" :value="String(a.id)" />
          </el-select>
          <el-button :loading="loading" @click="reload">刷新</el-button>
        </div>
      </div>

      <div v-if="listError" class="alert alert-error text-sm" role="alert">
        码列表读取失败，下面的表格不可信：{{ listError }}
        <el-button size="small" link type="primary" @click="load">重试</el-button>
      </div>

      <el-table :data="rows" size="small" stripe v-loading="loading" :empty-text="listError ? '读取失败' : '暂无本地码'">
        <el-table-column prop="id" label="ID" width="64" />
        <el-table-column label="服务商" width="100"><template #default="{ row }">{{ providerLabel(row.provider) }}</template></el-table-column>
        <el-table-column label="码前缀" min-width="130"><template #default="{ row }"><span class="mono">{{ row.code_prefix }}…</span></template></el-table-column>
        <el-table-column label="套餐 / 地区" min-width="130">
          <template #default="{ row }">
            {{ row.plan }} / {{ row.region }}
            <el-tag v-if="row.plan === 'pro_20x'" size="small" type="warning" class="ml-1">{{ NO_SUB_NOTE }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" min-width="170">
          <template #default="{ row }">
            <el-tag size="small" :type="localCdkTagType(row.status, row.status_reason)">{{ localCdkStatusText(row.status, row.status_reason) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="剩余次数" width="90">
          <template #default="{ row }">{{ row.attempts_left }}/3</template>
        </el-table-column>
        <el-table-column label="付款单" min-width="190">
          <template #default="{ row }">
            <template v-if="row.order_no">
              <router-link :to="`/ops/marswallex-pay?tab=orders`" class="mono underline">{{ row.order_no }}</router-link>
              <div class="text-xs text-muted">
                {{ row.order_state ? stateLabel(row.order_state) : '' }}
                <span v-if="row.order_error_code">· {{ payErrorText(row.order_error_code) || row.order_error_code }}</span>
              </div>
            </template>
            <span v-else class="text-muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="注资" width="90"><template #default="{ row }">{{ row.fund_cents ? centsToUsd(row.fund_cents) + ' 美元' : '—' }}</template></el-table-column>
        <el-table-column label="代理商" width="110">
          <template #default="{ row }">
            <router-link v-if="row.agent_id" :to="`/ops/agents/${row.agent_id}`" class="underline">{{ row.agent_username || '#' + row.agent_id }}</router-link>
            <span v-else class="text-muted">—</span>
          </template>
        </el-table-column>
        <el-table-column prop="note" label="备注" min-width="120" show-overflow-tooltip />
        <el-table-column label="时间" min-width="150"><template #default="{ row }">{{ fmtTime(row.created_at) }}</template></el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button v-if="canDisableLocal(row.status)" size="small" type="danger" plain :loading="busyId === row.id" @click="disableOne(row)">禁用</el-button>
            <el-button v-else-if="canEnableLocal(row.status, row.status_reason)" size="small" type="success" plain :loading="busyId === row.id" @click="enableOne(row)">启用</el-button>
          </template>
        </el-table-column>
      </el-table>
      <div class="flex justify-end">
        <el-pagination v-model:current-page="page" :page-size="PAGE_SIZE" :total="total" layout="total, prev, pager, next" size="small" @current-change="load" />
      </div>
    </section>

    <!-- ============ 发码结果：完整码只显示一次 ============ -->
    <el-dialog
      v-model="resultOpen" title="发码成功：请现在保存完整码" width="min(640px, 96vw)" append-to-body
      :close-on-click-modal="false" :close-on-press-escape="false" :before-close="beforeCloseResult" @closed="afterClosed"
    >
      <div class="space-y-3" data-testid="result-dialog">
        <div class="alert alert-warning text-sm font-semibold" role="alert">
          完整码只显示这一次，关闭后无法再次查看。本站只存哈希，不存完整码，也不写入浏览器存储。
        </div>
        <div class="text-sm text-muted">
          {{ resultMeta }} · 共 {{ resultCodes.length }} 张
          <span v-if="resultNote"> · {{ resultNote }}</span>
        </div>
        <textarea class="input mono text-sm w-full" style="min-height: 160px" readonly :value="resultCodes.join('\n')" @focus="($event.target as HTMLTextAreaElement).select()" />
        <div class="flex flex-wrap gap-2">
          <el-button type="primary" @click="copyResult">复制全部</el-button>
          <el-button @click="exportResult">导出 .txt</el-button>
          <span v-if="saved" class="text-sm text-ok self-center">已复制或导出</span>
        </div>
      </div>
      <template #footer>
        <el-button @click="requestClose">我已保存，关闭</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, onServerPrefetch, ref, watch } from 'vue'
import ApiErrorNotice from '../../../components/ApiErrorNotice.vue'
import { dialog } from '../../../lib/dialog'
import { copyToClipboard } from '../../../lib/clipboard'
import { errText } from '../../../lib/cardpool'
import { formatMinor, stateLabel } from '../../../lib/pay'
import { payErrorText } from '../../../lib/payui'
import { IdempotencyKeeper } from '../../../lib/idempotency'
import {
  adminApi, canDisableLocal, canEnableLocal, centsToUsd, classifyIssueFailure, localCdkStatusText, localCdkTagType,
  needsFirstRealPayment, NO_SUB_NOTE, planNote, providerLabel,
  type Combo, type IssueFailure, type LocalCdkRow, type ProviderReadiness,
} from '../../../lib/provider'

const ISSUE_MAX = 200
const PAGE_SIZE = 20
const STATUS_OPTIONS: Record<string, string> = { unused: '未使用', reserved: '处理中', consumed: '已兑换', failed: '已失败', disabled: '已禁用' }

const props = withDefaults(defineProps<{ provider: string; product?: string }>(), { product: 'gpt' })
const name = computed(() => providerLabel(props.provider))

// ---------- 就绪与组合 ----------
const mw = ref<ProviderReadiness | null>(null)
const combos = ref<Combo[]>([])
const readinessError = ref('')
const loadingReadiness = ref(false)
const firstPayMissing = computed(() => needsFirstRealPayment(mw.value))

const comboId = (c: Combo) => `${c.plan}|${c.region}`
const noteOf = (c: Combo) => planNote(c.plan, c.plan_note)

async function loadReadiness() {
  loadingReadiness.value = true
  readinessError.value = ''
  const r = await adminApi<any>('/providers/readiness')
  loadingReadiness.value = false
  if (!r.ok) {
    mw.value = null
    combos.value = []
    readinessError.value = errText(r, `读取 ${name.value} 就绪状态失败`)
    return
  }
  mw.value = r.data.providers?.[props.provider] || null
  combos.value = mw.value?.combos || []
  if (!combos.value.some((c) => comboId(c) === comboKey.value)) {
    comboKey.value = comboId(combos.value.find((c) => c.ok) || combos.value[0] || ({ plan: '', region: '' } as Combo))
    if (comboKey.value === '|') comboKey.value = ''
  }
}

// ---------- 发码 ----------
const comboKey = ref('')
const count = ref(1)
const note = ref('')
const funding = ref(false)
const issuing = ref(false)
const reissuing = ref(false)
const failure = ref<IssueFailure | null>(null)
const keeper = new IdempotencyKeeper()

const combo = computed(() => combos.value.find((c) => comboId(c) === comboKey.value) || null)

const blockReason = computed(() => {
  if (loadingReadiness.value) return '正在读取就绪状态…'
  if (readinessError.value) return '就绪状态读取失败，不能发码'
  if (!mw.value) return '就绪状态未知'
  if (!mw.value.ok) return `${name.value} 未就绪：${mw.value.problems?.[0] || '见上方原因'}`
  if (!combos.value.length) return '没有已启用的套餐和地区，请先到付款配置里启用'
  if (!combo.value) return '请选择套餐和地区'
  if (!combo.value.ok) return `该组合不可发：${(combo.value.problems || []).join('；')}`
  if (!(count.value >= 1 && count.value <= ISSUE_MAX)) return `数量须在 1-${ISSUE_MAX}`
  if (!funding.value) return '请先勾选「确认承担兑换资金」'
  return ''
})

// 选了别的组合、数量、备注后，旧的失败提示不再适用
watch([comboKey, count, note], () => { if (failure.value?.kind !== 'already_issued') failure.value = null })

const resultOpen = ref(false)
const resultCodes = ref<string[]>([])
const resultMeta = ref('')
const resultNote = ref('')
const saved = ref(false)

function payloadOf() {
  const c = combo.value!
  return {
    provider: props.provider, product: props.product, plan: c.plan, region: c.region, count: count.value,
    funding_confirmed: true, note: note.value.trim(),
  }
}

async function issue(skipConfirm = false): Promise<boolean> {
  if (issuing.value || blockReason.value || !combo.value) return false
  const c = combo.value
  const n = count.value
  if (!skipConfirm) {
    const ok = await dialog.confirm(
      `服务商：${name.value}\n套餐 / 地区：${c.label || c.plan} / ${c.region}\n数量：${n} 张\n` +
        `每张码兑换时注资 ${centsToUsd(c.fund_cents)} 美元，应付上限 ${formatMinor(c.cap_minor, c.currency)}；${n} 张最多共注资 ${centsToUsd(c.fund_cents * n)} 美元。\n` +
        `发码本身不扣款，用户兑换时才从卡池付款。${noteOf(c) ? '\n注意：' + noteOf(c) : ''}\n确认发码？`,
      { title: `确认发 ${name.value} 码`, okText: `发 ${n} 张`, danger: true },
    )
    if (!ok) return false
  }
  issuing.value = true
  failure.value = null
  try {
    const body = payloadOf()
    // 点击时按内容生成一次键；失败重试内容不变就沿用同一个，不会多发
    const r = await adminApi<any>('/cdks/issue', 'POST', body, { 'Idempotency-Key': keeper.keyFor(body) })
    if (!r.ok) {
      const f = classifyIssueFailure(r.status, r.data)
      if (f.kind === 'already_issued') keeper.done() // 否则再点一直 409；重发要用新键
      failure.value = f
      return false
    }
    const issued: any[] = Array.isArray(r.data.issued) ? r.data.issued : []
    const codes = issued.map((i) => String(i.full_code || i.code || '')).filter(Boolean)
    if (!codes.length) {
      // 响应成功但没有码：不能当成功清键，沿用原键重试会拿到 409，由 already_issued 路径处理
      failure.value = { kind: 'uncertain', message: '服务端返回成功但没有完整码。请到下面的列表按时间找到这批码，禁用后重发。' }
      return false
    }
    keeper.done()
    resultCodes.value = codes
    resultMeta.value = `${c.label || c.plan} / ${c.region}`
    resultNote.value = body.note
    saved.value = false
    resultOpen.value = true
    page.value = 1
    load()
    return true
  } finally {
    issuing.value = false
  }
}

// 禁用这批（first_id..last_id）后用新键重发
async function disableAndReissue() {
  const f = failure.value
  if (!f || f.kind !== 'already_issued' || reissuing.value || !f.firstId || !f.lastId) {
    dialog.toast('缺少这批码的 ID 范围，请到列表里手动禁用后重发', 'warn')
    return
  }
  const first = f.firstId
  const last = f.lastId
  const n = last - first + 1
  const ok = await dialog.confirm(
    `将禁用 ID ${first}–${last}（共 ${n} 张）这批码，它们的完整码你没有保存，用户也还没拿到。\n禁用成功后，用新的幂等键按当前表单重发 ${count.value} 张。\n已被兑换的码不会被禁用，遇到会停下。\n确认？`,
    { title: '禁用这批后重发', okText: '禁用并重发', danger: true },
  )
  if (!ok) return
  reissuing.value = true
  try {
    for (let id = first; id <= last; id++) {
      const r = await adminApi(`/cdks/${id}/disable`, 'POST')
      if (!r.ok) {
        dialog.toast(`禁用 #${id} 失败，已停止，没有重发：${errText(r, '未知错误')}`, 'err', 8000)
        load()
        return
      }
    }
    dialog.toast(`已禁用 ${n} 张`)
    failure.value = null
    keeper.done()
    await issue(true)
  } finally {
    reissuing.value = false
  }
}

async function copyResult() {
  if (await copyToClipboard(resultCodes.value.join('\n'))) { saved.value = true; dialog.toast('已复制') }
  else dialog.toast('复制失败，请手动选中文本框复制，或点导出', 'err')
}
function exportResult() {
  const blob = new Blob([resultCodes.value.join('\n') + '\n'], { type: 'text/plain;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = `${props.provider}-cdk-${new Date().toISOString().slice(0, 19).replace(/[:T]/g, '')}.txt`
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
  saved.value = true
}
async function requestClose() {
  if (!saved.value) {
    const ok = await dialog.confirm('你还没有复制或导出这批码。关闭后无法再次查看，只能禁用后重发。确定关闭？', { title: '确认关闭', okText: '仍然关闭', danger: true })
    if (!ok) return
  }
  resultOpen.value = false
}
function beforeCloseResult(done: () => void) { requestClose().then(() => { if (!resultOpen.value) done() }) }
function afterClosed() { resultCodes.value = []; saved.value = false }
onBeforeUnmount(() => { resultCodes.value = [] })

// ---------- 列表 ----------
const rows = ref<LocalCdkRow[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const listError = ref('')
const fStatus = ref('')
const fPlan = ref('')
const fAgent = ref('')
const agentOptions = ref<{ id: number; username: string }[]>([])
const busyId = ref(0)
let seq = 0

function fmtTime(v: unknown) {
  if (!v) return '—'
  const d = typeof v === 'number' ? new Date(v * 1000) : new Date(String(v))
  return Number.isNaN(d.getTime()) ? String(v) : d.toLocaleString()
}

async function load() {
  const my = ++seq
  loading.value = true
  listError.value = ''
  const q = new URLSearchParams({ page: String(page.value), page_size: String(PAGE_SIZE), provider: props.provider })
  if (fStatus.value) q.set('status', fStatus.value)
  if (fPlan.value.trim()) q.set('plan', fPlan.value.trim())
  if (fAgent.value !== '' && fAgent.value != null) q.set('agent_id', fAgent.value)
  const r = await adminApi<any>('/cdks?' + q.toString())
  if (my !== seq) return
  loading.value = false
  if (!r.ok) { listError.value = errText(r, '读取失败'); return } // 失败时保留旧数据不清空，但明确标红
  rows.value = r.data.list || []
  total.value = r.data.total || 0
}
function reload() { page.value = 1; load() }

async function disableOne(row: LocalCdkRow) {
  const ok = await dialog.confirm(
    `禁用本地码 #${row.id}（${row.code_prefix}…，${row.plan} / ${row.region}）？\n禁用后用户无法再兑换这张码。已经在途的付款单拦不住，只保证它结束后不能重兑。`,
    { title: '禁用', okText: '禁用', danger: true },
  )
  if (!ok) return
  busyId.value = row.id
  const r = await adminApi(`/cdks/${row.id}/disable`, 'POST')
  busyId.value = 0
  if (!r.ok) dialog.toast(errText(r, '禁用失败'), r.status === 409 ? 'warn' : 'err')
  else dialog.toast(r.data.changed ? '已禁用' : '已经是禁用状态')
  load()
}
async function enableOne(row: LocalCdkRow) {
  const ok = await dialog.confirm(`启用本地码 #${row.id}（${row.code_prefix}…，${row.plan} / ${row.region}）？启用后用户可以再次兑换，兑换会动用卡池资金。`, { title: '启用', okText: '启用' })
  if (!ok) return
  busyId.value = row.id
  const r = await adminApi(`/cdks/${row.id}/enable`, 'POST')
  busyId.value = 0
  if (!r.ok) dialog.toast(errText(r, '启用失败'), r.status === 409 ? 'warn' : 'err')
  else dialog.toast(r.data.changed ? '已启用' : '本来就是可用状态')
  load()
}

// 代理商下拉：读取失败不影响码列表，只是少了筛选项
async function loadAgentOptions() {
  const r = await adminApi<any>('/agents')
  if (r.ok) agentOptions.value = (r.data.items || []).map((a: any) => ({ id: a.id, username: a.username }))
}
onMounted(() => { loadReadiness(); load(); loadAgentOptions() })
// 仅服务端渲染（单元测试）会触发：让测试里渲染出的面板带上就绪数据。浏览器里不会执行。
onServerPrefetch(loadReadiness)
</script>

<style scoped>
.text-err { color: #dc2626; }
.text-ok { color: #16a34a; }
</style>
