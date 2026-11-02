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
.stat-card:hover { border-color: var(--primary); transform: translateY(-1px); }
</style>
