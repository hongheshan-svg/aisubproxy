<template>
  <div class="pb-2 space-y-5">
    <AdminPageHeader class="!mb-0" title="总览" description="CDK 白标门户运营概况：卡台发码、公开兑换、Webhook、Session 账单查询。" />

    <ApiErrorNotice :message="statsError" />
    <div v-if="stats.counts_partial" class="alert alert-warning text-sm">
      CDK 数量较多，状态分布为扫描样本汇总；总数仍以卡台 total 为准。
    </div>

    <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <router-link v-for="card in statCards" :key="card.label" :to="card.to" class="stat-card">
        <div class="text-sm text-muted">{{ card.label }}</div>
        <div class="mt-2 text-3xl font-bold text-ink mono">{{ card.value }}</div>
        <div class="mt-1 text-sm text-subtle">{{ card.hint }}</div>
      </router-link>
    </div>

    <section class="space-y-2" aria-label="待办汇总">
      <div class="flex items-center justify-between">
        <h2 class="text-base font-semibold text-ink">待办汇总</h2>
        <el-button size="small" :loading="todosLoading" @click="loadTodos">刷新</el-button>
      </div>
      <div class="grid gap-4 sm:grid-cols-2 xl:grid-cols-5">
        <router-link v-for="t in todoCards" :key="t.key" :to="t.to" class="stat-card" :class="{ 'stat-attn': t.attn }" :data-todo="t.key">
          <div class="text-sm text-muted">{{ t.label }}</div>
          <div class="mt-2 text-2xl font-bold mono" :class="t.failed ? 'text-err' : 'text-ink'">{{ t.value }}</div>
          <div class="mt-1 text-xs text-subtle">{{ t.hint }}</div>
        </router-link>
      </div>
    </section>

    <div class="grid gap-4 lg:grid-cols-3">
      <section class="card space-y-3 lg:col-span-2">
        <h2 class="text-base font-semibold text-ink">快捷入口</h2>
        <div class="grid gap-3 sm:grid-cols-2">
          <div v-for="g in quickGroups" :key="g.name">
            <div class="text-xs text-muted mb-1.5">{{ g.name }}</div>
            <div class="flex flex-wrap gap-2">
              <router-link v-for="it in g.items" :key="it.to" :to="it.to" class="btn-secondary !py-1.5 !px-3 text-sm">{{ it.title }}</router-link>
            </div>
          </div>
        </div>
      </section>

      <section class="card space-y-2">
        <h2 class="text-base font-semibold text-ink">版本</h2>
        <div class="flex justify-between gap-2 text-sm">
          <span class="text-muted">当前</span>
          <span class="mono font-semibold text-ink">{{ versionInfo.current ? `v${versionInfo.current}` : '—' }}</span>
        </div>
        <div class="flex justify-between gap-2 text-sm">
          <span class="text-muted">GitHub 最新</span>
          <span class="mono font-semibold" :style="versionInfo.update_available ? 'color: var(--warn)' : ''">
            {{ versionInfo.latest ? `v${versionInfo.latest}` : '—' }}
          </span>
        </div>
        <router-link to="/ops/update" class="btn-secondary !py-1.5 !px-3 text-sm inline-block">
          {{ versionInfo.update_available ? '有新版本，去更新' : '版本更新' }}
        </router-link>
      </section>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import ApiErrorNotice from '../../components/ApiErrorNotice.vue'
import { authFetch } from '../../lib/api'
import { payApi } from '../../lib/pay'
import { poolApi } from '../../lib/cardpool'
import { breakerReason, type Breaker } from '../../lib/payui'
import {
  FAIL_TEXT, availableCards, mainUsdBalance, sumTodos, todoNeedsAttention, todoText,
  type PoolOverview, type TodoState,
} from '../../lib/adminui'

const router = useRouter()
const stats = ref<any>({})
const statsError = ref('')
const statsLoading = ref(false)
const versionInfo = ref<any>({})

function numOrDash(v: unknown) {
  if (v === null || v === undefined || v === '') return '—'
  const n = Number(v)
  return Number.isFinite(n) ? n : '—'
}

const statCards = computed(() => {
  const s = stats.value || {}
  const v = (x: unknown) => (statsLoading.value ? '…' : numOrDash(x))
  return [
    { label: 'CDK 总数', value: v(s.total_cdks ?? s.total_cdkeys), hint: '来自卡台', to: '/ops/cdkeys' },
    { label: '可用 CDK', value: v(s.unused_cdks ?? s.active_cdks ?? s.active_cdkeys), hint: '未使用', to: '/ops/cdkeys' },
    { label: '已兑换', value: v(s.consumed_cdks), hint: '已消耗', to: '/ops/cdkeys' },
    { label: '兑换订单', value: v(s.total_cdk_orders ?? s.total_tasks), hint: '卡台 CDK 订单', to: '/ops/orders' },
  ]
})

// ---------- 待办汇总：每一项单独查询，失败显示「获取失败」，绝不当成 0 ----------
const loadingT: TodoState<any> = { state: 'loading' }
const tReviewPay = ref<TodoState<number>>(loadingT)
const tReviewPool = ref<TodoState<number>>(loadingT)
const tBreaker = ref<TodoState<Breaker>>(loadingT)
const tWebhook = ref<TodoState<number>>(loadingT)
const tCards = ref<TodoState<number>>(loadingT)
const tBalance = ref<TodoState<string>>(loadingT)
const todosLoading = ref(false)
const failMsg = (r: { status: number; data: any }, what: string): TodoState<any> =>
  ({ state: 'fail', error: r.data?.error || `${what}（${r.status || '网络错误'}）` })

async function loadTodos() {
  todosLoading.value = true
  ;[tReviewPay, tReviewPool, tBreaker, tWebhook, tCards, tBalance].forEach((t) => { t.value = loadingT })
  const [orders, ov, br] = await Promise.all([
    payApi<{ total: number }>('/orders?state=needs_review&page=1&page_size=1'),
    poolApi<PoolOverview>('/overview'),
    payApi<Breaker>('/breaker'),
  ])
  tReviewPay.value = orders.ok && Number.isFinite(orders.data?.total) ? { state: 'ok', value: orders.data.total } : failMsg(orders, '付款单')
  if (ov.ok) {
    tReviewPool.value = { state: 'ok', value: ov.data.needs_review ?? 0 }
    tWebhook.value = { state: 'ok', value: ov.data.failed_events ?? 0 }
    tCards.value = { state: 'ok', value: availableCards(ov.data) }
    const bal = mainUsdBalance(ov.data)
    tBalance.value = bal === null ? { state: 'fail', error: ov.data.balance_error || '没有美元余额项' } : { state: 'ok', value: bal }
  } else {
    const f = failMsg(ov, '卡池概览')
    tReviewPool.value = f; tWebhook.value = f; tCards.value = f; tBalance.value = f
  }
  tBreaker.value = br.ok ? { state: 'ok', value: br.data } : failMsg(br, '付款熔断')
  todosLoading.value = false
}

const todoCards = computed(() => {
  const review = sumTodos([tReviewPay.value, tReviewPool.value])
  const failedParts = [tReviewPay.value, tReviewPool.value].some((t) => t.state === 'fail')
  const reviewHint = failedParts
    ? `${tReviewPay.value.state === 'fail' ? '付款单' : ''}${tReviewPay.value.state === 'fail' && tReviewPool.value.state === 'fail' ? '、' : ''}${tReviewPool.value.state === 'fail' ? '卡池单据' : ''}查询失败`
    : `付款单 ${todoText(tReviewPay.value)} · 卡池单据 ${todoText(tReviewPool.value)}`
  const b = tBreaker.value
  const brValue = b.state === 'loading' ? '…' : b.state === 'fail' ? FAIL_TEXT : b.value.reason === 'check_failed' ? FAIL_TEXT : b.value.paused ? '已暂停' : '运行中'
  const brHint = b.state === 'ok' ? (b.value.paused ? breakerReason(b.value) || '去恢复' : '没有暂停') : b.state === 'fail' ? b.error : ''
  return [
    { key: 'review', label: '待人工单据', value: todoText(review), hint: reviewHint, to: '/ops/marswallex-pay?tab=orders', failed: review.state === 'fail', attn: todoNeedsAttention(review) },
    { key: 'breaker', label: '付款是否暂停', value: brValue, hint: brHint, to: '/ops/marswallex-pay', failed: brValue === FAIL_TEXT, attn: brValue === '已暂停' || brValue === FAIL_TEXT },
    { key: 'webhook', label: '失败 Webhook', value: todoText(tWebhook.value), hint: tWebhook.value.state === 'fail' ? tWebhook.value.error : 'MarsWallex 事件处理失败', to: '/ops/marswallex', failed: tWebhook.value.state === 'fail', attn: todoNeedsAttention(tWebhook.value) },
    { key: 'cards', label: '可用卡数', value: todoText(tCards.value), hint: tCards.value.state === 'fail' ? tCards.value.error : '卡池里可租用的卡', to: '/ops/cardpool', failed: tCards.value.state === 'fail', attn: tCards.value.state === 'fail' || (tCards.value.state === 'ok' && tCards.value.value === 0) },
    { key: 'balance', label: '主钱包余额', value: todoText(tBalance.value, (v) => `${v} USD`), hint: tBalance.value.state === 'fail' ? tBalance.value.error : '可用美元', to: '/ops/marswallex-wallet', failed: tBalance.value.state === 'fail', attn: tBalance.value.state === 'fail' },
  ]
})

// 快捷入口跟侧栏同源：读路由 meta，不再单独维护一份
const quickGroups = computed(() => {
  const root = router.options.routes.find((r) => r.path === '/ops')
  const gs: { name: string; items: { to: string; title: string }[] }[] = []
  for (const c of root?.children || []) {
    const m = (c.meta || {}) as Record<string, string>
    if (!m.title || !m.group) continue
    let g = gs.find((x) => x.name === m.group)
    if (!g) gs.push((g = { name: m.group, items: [] }))
    g.items.push({ to: `/ops/${c.path}`, title: m.title })
  }
  return gs
})

onMounted(async () => {
  loadTodos()
  statsLoading.value = true
  statsError.value = ''
  try {
    const r = await authFetch('/api/v1/stats/system')
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      statsError.value = d.error || '加载统计失败'
    } else {
      stats.value = d
      if (d.error) {
        statsError.value = d.configured === false ? 'card_api_key not configured' : String(d.error)
      }
    }
  } catch (e: any) {
    statsError.value = e?.message || '网络错误'
  } finally {
    statsLoading.value = false
  }
  try {
    const r = await authFetch('/api/v1/admin/system/version')
    if (r.ok) versionInfo.value = await r.json().catch(() => ({}))
  } catch { /* 版本信息拿不到不影响总览 */ }
})
</script>

<style scoped>
.stat-card {
  display: block; text-decoration: none;
  padding: 20px; border-radius: var(--radius-md);
  background: var(--surface); border: 1px solid var(--brd);
  transition: border-color .15s, transform .15s;
}
.stat-card.stat-attn { border-color: var(--warn, #d97706); }
.text-err { color: #dc2626; }
.stat-card:hover { border-color: var(--primary); transform: translateY(-1px); }
</style>
