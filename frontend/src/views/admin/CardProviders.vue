<template>
  <div class="space-y-4">
    <AdminPageHeader title="卡服务商" description="选择新发的 CDK 由哪家卡服务商处理，并查看两家是否就绪。" class="!mb-0" />

    <div class="alert alert-info text-sm">
      「卡台选卡规则」管的是 ZovoCard 内部用哪个卡头，和这里的「默认发码服务商」不是一回事。
      <router-link to="/ops/card-selection" class="underline">去看卡台选卡规则</router-link>
    </div>

    <ApiErrorNotice :message="loadError" />
    <el-button v-if="loadError" size="small" :loading="loading" @click="load">重试</el-button>

    <section class="card space-y-3">
      <h2 class="text-base font-semibold text-ink">默认发码服务商</h2>
      <p class="text-sm text-muted">后台发码时默认选这一家。切换只影响之后新发的码，已发的码、在途单都不变。</p>
      <el-radio-group :model-value="defaultProvider" :disabled="switching || loading || !!loadError" @change="onSwitch">
        <el-radio-button v-for="p in options" :key="p" :value="p">{{ providerLabel(p) }}</el-radio-button>
      </el-radio-group>
      <span v-if="loading" class="text-sm text-muted ml-2">读取中…</span>
      <div v-if="switchWarn.length" class="alert alert-warning text-sm">
        已保存，但 {{ providerLabel(defaultProvider) }} 目前未就绪，发码会被拒绝：
        <ul class="list-disc pl-5"><li v-for="p in switchWarn" :key="p">{{ p }}</li></ul>
      </div>
    </section>

    <section class="grid gap-4 lg:grid-cols-2">
      <article v-for="id in options" :key="id" class="card space-y-3" :data-provider="id">
        <div class="flex items-center justify-between gap-2">
          <h2 class="text-base font-semibold text-ink">{{ providerLabel(id) }}</h2>
          <el-tag v-if="rd" :type="rd.providers[id]?.ok ? 'success' : 'danger'" size="small">
            {{ rd.providers[id]?.ok ? '已就绪' : '未就绪' }}
          </el-tag>
          <el-tag v-else type="info" size="small">{{ loading ? '读取中' : '读取失败' }}</el-tag>
        </div>

        <template v-if="rd && rd.providers[id]">
          <div v-if="id === 'marswallex' && firstPaymentMissing" class="alert alert-warning text-sm first-pay" role="alert">
            <b>还差一笔系统自动确认成功的真实付款。</b>
            试运行单和人工定论的单都不算。在跑通这一笔之前，不能把 MarsWallex 码卖给用户。
            <router-link to="/ops/marswallex-pay" class="underline">去自动付款做真实付款</router-link>
          </div>

          <ul class="space-y-1.5 text-sm">
            <li v-for="c in rd.providers[id].checks" :key="c.key" class="flex gap-2 items-start">
              <span :class="c.ok ? 'text-ok' : 'text-err'" class="shrink-0 w-4" aria-hidden="true">{{ c.ok ? '✓' : '✗' }}</span>
              <span class="min-w-0">
                <b>{{ checkTitle(c.key) }}</b>
                <span class="text-muted"> — {{ c.detail }}</span>
                <details v-if="c.raw" class="text-xs text-muted mt-0.5">
                  <summary class="cursor-pointer">详情</summary>
                  <code class="break-all">{{ c.raw }}</code>
                </details>
                <router-link v-if="!c.ok && CHECK_HINTS[c.key]?.link" :to="CHECK_HINTS[c.key].link!.to" class="ml-2 underline">
                  {{ CHECK_HINTS[c.key].link!.label }}
                </router-link>
              </span>
            </li>
            <li v-if="!rd.providers[id].checks?.length && rd.providers[id].problems?.length" class="text-err">
              {{ rd.providers[id].problems.join('；') }}
            </li>
          </ul>

          <div v-if="id === 'zovocard' && rd.zovocard" class="text-sm text-muted">
            卡台余额 {{ rd.zovocard.balance }} · 可用 {{ rd.zovocard.spendable_balance }}
          </div>
        </template>
      </article>
    </section>

    <section class="card space-y-3">
      <h2 class="text-base font-semibold text-ink">MarsWallex 可发的套餐与地区</h2>
      <p class="text-sm text-muted">只列已在「付款配置」里启用的组合。注资金额是每张码兑换时给卡充的美元，金额上限是应付金额的上限。</p>
      <el-table :data="rd?.combos || []" size="small" border :empty-text="rd ? '没有已启用的套餐与地区' : '读取失败或读取中'">
        <el-table-column label="套餐" min-width="150">
          <template #default="{ row }">
            {{ row.label || row.plan }}
            <el-tag v-if="planNote(row.plan, row.plan_note)" size="small" type="warning" class="ml-1">{{ planNote(row.plan, row.plan_note) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="region" label="地区" width="80" />
        <el-table-column label="注资" width="110"><template #default="{ row }">{{ centsToUsd(row.fund_cents) }} 美元</template></el-table-column>
        <el-table-column label="金额上限" min-width="130"><template #default="{ row }">{{ formatMinor(row.cap_minor, row.currency) }}</template></el-table-column>
        <el-table-column label="可用卡" width="80" prop="cards" />
        <el-table-column label="状态" min-width="200">
          <template #default="{ row }">
            <el-tag :type="row.ok ? 'success' : 'danger'" size="small">{{ row.ok ? '可发' : '不可发' }}</el-tag>
            <span v-if="!row.ok" class="text-xs text-muted ml-1">{{ (row.problems || []).join('；') }}</span>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <section class="card space-y-3">
      <h2 class="text-base font-semibold text-ink">能力矩阵</h2>
      <p class="text-sm text-muted">哪家服务商支持哪个产品和档位。不支持的组合发码会被直接拒绝，不会自动改用另一家。</p>
      <el-table :data="rd?.capabilities || []" size="small" border :empty-text="rd ? '没有数据' : '读取失败或读取中'">
        <el-table-column prop="product" label="产品" width="110" />
        <el-table-column label="档位" min-width="160">
          <template #default="{ row }">{{ row.plan }}<span v-if="row.comment" class="text-xs text-muted ml-1">{{ row.comment }}</span></template>
        </el-table-column>
        <el-table-column v-for="id in options" :key="id" :label="providerLabel(id)" min-width="150">
          <template #default="{ row }">
            <span :class="row.support?.[id] ? 'text-ok' : 'text-muted'">{{ row.support?.[id] ? '支持' : '不支持' }}</span>
            <el-tag v-if="row.support?.[id] && row.notes?.[id]" size="small" type="warning" class="ml-1">{{ row.notes[id] }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import ApiErrorNotice from '../../components/ApiErrorNotice.vue'
import { dialog } from '../../lib/dialog'
import { errText } from '../../lib/cardpool'
import { formatMinor } from '../../lib/pay'
import {
  adminApi, centsToUsd, checkTitle, CHECK_HINTS, needsFirstRealPayment, planNote, providerLabel,
  type Combo, type ProviderReadiness,
} from '../../lib/provider'

interface Readiness {
  default: string
  providers: Record<string, ProviderReadiness>
  combos: Combo[]
  zovocard?: { balance: number; spendable_balance: number }
  capabilities: { product: string; plan: string; support: Record<string, boolean>; notes?: Record<string, string>; comment?: string }[]
}

const loading = ref(false)
const loadError = ref('')
const switching = ref(false)
const defaultProvider = ref('')
const options = ref<string[]>(['zovocard', 'marswallex'])
const rd = ref<Readiness | null>(null)
const switchWarn = ref<string[]>([])

const firstPaymentMissing = computed(() => needsFirstRealPayment(rd.value?.providers?.marswallex))

async function load() {
  loading.value = true
  loadError.value = ''
  const [d, r] = await Promise.all([adminApi('/providers/default'), adminApi<Readiness>('/providers/readiness')])
  loading.value = false
  if (!d.ok) loadError.value = errText(d, '读取默认服务商失败')
  else {
    defaultProvider.value = d.data.provider
    if (Array.isArray(d.data.options) && d.data.options.length) options.value = d.data.options
  }
  if (!r.ok) {
    rd.value = null
    loadError.value = loadError.value || errText(r, '读取就绪状态失败')
  } else {
    rd.value = { ...r.data, combos: r.data.combos || [], capabilities: r.data.capabilities || [] }
  }
}

async function onSwitch(next: string | number | boolean | undefined) {
  const to = String(next)
  const from = defaultProvider.value
  if (!to || to === from || switching.value) return
  const target = rd.value?.providers?.[to]
  let msg = `把默认发码服务商从 ${providerLabel(from)} 切换为 ${providerLabel(to)}。\n\n只影响之后新发的码；已发的码、在途的付款单和已占用的卡都不变。`
  if (target && !target.ok) {
    msg += `\n\n注意：${providerLabel(to)} 目前未就绪，保存后发码会被拒绝：\n- ${(target.problems || []).join('\n- ')}`
  }
  if (!(await dialog.confirm(msg, { title: '切换默认发码服务商', okText: `切换为 ${providerLabel(to)}`, danger: !!target && !target.ok }))) {
    return
  }
  switching.value = true
  const r = await adminApi('/providers/default', 'PUT', { provider: to })
  switching.value = false
  if (!r.ok) { dialog.toast(errText(r, '切换失败'), 'err'); return }
  defaultProvider.value = r.data.provider
  switchWarn.value = r.data.readiness && !r.data.readiness.ok ? (r.data.readiness.problems || []) : []
  dialog.toast(`默认发码服务商已改为 ${providerLabel(r.data.provider)}`)
  load()
}

onMounted(load)
</script>

<style scoped>
.text-ok { color: #16a34a; }
.text-err { color: #dc2626; }
.first-pay { border-width: 2px; }
</style>
