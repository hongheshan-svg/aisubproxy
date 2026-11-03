<template>
  <article class="card space-y-3" :class="{ 'lg:col-span-2': hasCombos }" :data-provider="info.name">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex flex-wrap items-center gap-2 min-w-0">
        <h2 class="text-base font-semibold text-ink">{{ info.display_name || info.name }}</h2>
        <el-tag size="small" type="info" effect="plain" data-testid="kind-tag">{{ info.kind_text || info.kind }}</el-tag>
        <span v-if="info.code_prefix" class="text-xs text-muted mono">码前缀 {{ info.code_prefix }}</span>
      </div>
      <el-tag v-if="readiness" :type="readiness.ok ? 'success' : 'danger'" size="small" data-testid="ready-tag">
        {{ readiness.ok ? '已就绪' : '未就绪' }}
      </el-tag>
      <el-tag v-else type="info" size="small">{{ loading ? '读取中' : '读取失败' }}</el-tag>
    </div>

    <template v-if="readiness">
      <div v-if="firstPaymentMissing" class="alert alert-warning text-sm first-pay" role="alert" data-testid="first-pay">
        <b>还差一笔系统自动确认成功的真实付款。</b>
        试运行单和人工定论的单都不算。在跑通这一笔之前，不能把 {{ info.display_name || info.name }} 的码卖给用户。
        <router-link :to="{ path: '/ops/marswallex-pay', query: { provider: info.name } }" class="underline" data-testid="first-pay-link">去自动付款做真实付款</router-link>
      </div>

      <div v-for="w in readiness.warnings || []" :key="w" class="alert alert-warning text-sm" data-testid="provider-warning">{{ w }}</div>

      <ul class="space-y-1.5 text-sm">
        <li v-for="c in readiness.checks || []" :key="c.key" class="flex gap-2 items-start">
          <span :class="c.ok ? 'text-ok' : 'text-err'" class="shrink-0 w-4" aria-hidden="true">{{ c.ok ? '✓' : '✗' }}</span>
          <span class="min-w-0">
            <b>{{ checkTitle(c.key, c.title) }}</b>
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
        <li v-if="!readiness.checks?.length && readiness.problems?.length" class="text-err">
          {{ readiness.problems.join('；') }}
        </li>
      </ul>

      <div v-if="readiness.extras?.length" class="text-sm text-muted" data-testid="extras">
        <span v-for="(x, i) in readiness.extras" :key="x.label">
          <template v-if="i > 0"> · </template>{{ x.label }} {{ x.value }}
        </span>
      </div>

      <div v-if="hasCombos" class="space-y-2" data-testid="combos">
        <h3 class="text-sm font-semibold text-ink">可发的套餐与地区</h3>
        <p class="text-xs text-muted">只列已启用的组合。注资金额是每张码兑换时给卡充的美元，金额上限是应付金额的上限。</p>
        <el-table :data="readiness.combos || []" size="small" border empty-text="没有已启用的套餐与地区">
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
      </div>
    </template>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatMinor } from '../../../lib/pay'
import {
  centsToUsd, checkTitle, CHECK_HINTS, needsFirstRealPayment, planNote,
  type ProviderInfo, type ProviderReadiness,
} from '../../../lib/provider'

const props = defineProps<{ info: ProviderInfo; readiness?: ProviderReadiness | null; loading?: boolean }>()

// 首笔真实付款这一条是检查项数据（key=first_real_payment），谁有谁显示，不按服务商名判断
const firstPaymentMissing = computed(() => needsFirstRealPayment(props.readiness))
const hasCombos = computed(() => Array.isArray(props.readiness?.combos))
</script>

<style scoped>
.text-ok { color: #16a34a; }
.text-err { color: #dc2626; }
.first-pay { border-width: 2px; }
</style>
