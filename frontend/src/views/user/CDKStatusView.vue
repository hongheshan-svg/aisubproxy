<template>
  <UserShell>
    <UserPageHeader :title="t('cdkLookup.title')" :subtitle="t('cdkLookup.subtitle')" :max-width="!isDesktop && mode === 'batch' && batchResults.length ? 960 : 0" back />
    <main class="u-page u-page--split lk" :class="{ 'lk-wide': mode === 'batch' && batchResults.length }">
      <UserTwoColumn>
        <div class="u-card lk-stack">
          <div class="lk-modes">
            <button type="button" class="u-btn-secondary lk-mode" :class="{ 'is-on': mode === 'single' }" :aria-pressed="mode === 'single'" @click="setMode('single')">
              {{ t('cdkLookup.modeSingle') }}
            </button>
            <button type="button" class="u-btn-secondary lk-mode" :class="{ 'is-on': mode === 'batch' }" :aria-pressed="mode === 'batch'" @click="setMode('batch')">
              {{ t('cdkLookup.modeBatch') }}
            </button>
          </div>

          <template v-if="mode === 'single'">
            <h2 class="lk-h">{{ t('cdkLookup.queryTitle') }}</h2>
            <p class="u-help lk-note">{{ t('cdkLookup.queryHint') }}</p>
            <label class="lk-label">
              {{ t('cdkLookup.cdkLabel') }}
              <input
                v-model="cdkCode"
                type="text"
                :placeholder="t('cdkLookup.cdkPlaceholder')"
                class="u-input u-mono"
                @keyup.enter="query"
              />
            </label>
            <button type="button" class="u-btn-primary lk-submit" :disabled="!cdkCode.trim() || querying" @click="query">
              {{ querying ? t('common.querying') : t('cdkLookup.queryBtn') }}
            </button>
          </template>

          <template v-else>
            <h2 class="lk-h">{{ t('cdkLookup.modeBatch') }}</h2>
            <p class="u-help lk-note">{{ t('cdkLookup.batchHint', { max: LOOKUP_BATCH_MAX }) }}</p>
            <label class="lk-label">
              {{ t('cdkLookup.batchLabel') }}
              <textarea
                v-model="batchText"
                rows="8"
                class="u-input u-mono lk-area"
                :placeholder="t('cdkLookup.batchPlaceholder')"
              />
            </label>
            <p class="u-help lk-note">
              {{ t('cdkLookup.batchRecognized', { n: parsedCodes.length }) }}
              <span v-if="parsedCodes.length > LOOKUP_BATCH_MAX">· {{ t('cdkLookup.batchTruncated', { max: LOOKUP_BATCH_MAX }) }}</span>
            </p>
            <button type="button" class="u-btn-primary lk-submit" :disabled="parsedCodes.length === 0 || querying" @click="queryBatch">
              {{ querying ? t('common.querying') : t('cdkLookup.batchQueryBtn') }}
            </button>
          </template>

          <p v-if="error" class="u-error" role="alert">{{ error }}</p>
        </div>

        <CodeStatusCard
          v-if="mode === 'single' && result"
          class="lk-gap"
          :result="result"
          context="home"
          @redeem="goRedeemCard"
          @reset="reset"
        />

        <template v-if="isDesktop" #aside><FaqList /></template>

        <!-- 批量结果的表格较宽：电脑布局放在两栏之后的整宽区 -->
        <template #wide>
        <section v-if="mode === 'batch' && batchResults.length" class="u-card lk-stack lk-gap">
          <div class="lk-head">
            <h2 class="lk-h">{{ t('cdkLookup.resultTitle') }}</h2>
            <div class="lk-tools">
              <button type="button" class="u-btn-secondary lk-small" @click="copyUsedEmails">{{ t('cdkLookup.copyEmails') }}</button>
              <button type="button" class="u-btn-secondary lk-small" @click="exportCsv">{{ t('cdkLookup.exportCsv') }}</button>
            </div>
          </div>
          <ul class="lk-sum">
            <li>{{ t('cdkLookup.summaryUsed', { n: batchSummary.used }) }}</li>
            <li>{{ t('cdkLookup.summaryFailed', { n: batchSummary.failed }) }}</li>
            <li>{{ t('cdkLookup.summaryUnused', { n: batchSummary.unused }) }}</li>
            <li>{{ t('cdkLookup.summaryProcessing', { n: batchSummary.processing }) }}</li>
            <li>{{ t('cdkLookup.summaryUnknown', { n: batchSummary.unknown }) }}</li>
            <li v-if="batchSummary.replaced">{{ t('cdkLookup.summaryReplaced', { n: batchSummary.replaced }) }}</li>
            <li v-if="batchSummary.unsupported">{{ t('cdkLookup.summaryUnsupported', { n: batchSummary.unsupported }) }}</li>
          </ul>
          <div class="lk-table-wrap">
            <table class="lk-table">
              <thead>
                <tr>
                  <th>#</th>
                  <th>{{ t('cdkLookup.cdkCode') }}</th>
                  <th>{{ t('cdkLookup.useStatus') }}</th>
                  <th>{{ t('cdkLookup.rechargeEmail') }}</th>
                  <th>{{ t('cdkLookup.plan') }}</th>
                  <th>{{ t('cdkLookup.usedAt') }}</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(row, i) in batchResults" :key="row.cdk_code + i">
                  <td>{{ i + 1 }}</td>
                  <td class="u-mono lk-break">{{ row.cdk_code }}</td>
                  <td class="lk-status" :class="`lk-status--${statusTone(row.status)}`">{{ statusText(row.status) }}</td>
                  <td class="u-mono lk-break">{{ row.account_email || t('cdkLookup.emailEmpty') }}</td>
                  <td>{{ row.plan || t('cdkLookup.emailEmpty') }}</td>
                  <td class="lk-nowrap">{{ fmtLookupTime(row.used_at) || row.used_at || t('cdkLookup.emailEmpty') }}</td>
                  <td class="lk-nowrap">
                    <button v-if="row.can_resubmit" type="button" class="u-btn-primary lk-small" @click="goRedeem(row.cdk_code)">{{ t('cdkLookup.resubmit') }}</button>
                    <button v-else-if="row.status === 'unused'" type="button" class="u-btn-secondary lk-small" @click="goRedeem(row.cdk_code)">{{ t('cdkLookup.goRedeem') }}</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
        </template>
      </UserTwoColumn>

      <UserFooter />
    </main>
  </UserShell>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import UserPageHeader from '../../components/UserPageHeader.vue'
import UserShell from '../../components/redeem/UserShell.vue'
import UserFooter from '../../components/redeem/UserFooter.vue'
import FaqList from '../../components/redeem/FaqList.vue'
import UserTwoColumn from '../../components/redeem/UserTwoColumn.vue'
import { isDesktop } from '../../lib/layoutMode'
import CodeStatusCard from '../../components/redeem/CodeStatusCard.vue'
import { parseCdks } from '../../lib/batch-session'
import { copyToClipboard } from '../../lib/clipboard'
import { dialog } from '../../lib/dialog'
import { batchSummary as summarize, csvRow, fmtLookupTime, type LookupResult } from '../../lib/aftersale'

const { t } = useI18n({ useScope: 'global' })
const router = useRouter()

const LOOKUP_BATCH_MAX = 100

const mode = ref<'single' | 'batch'>('single')
const cdkCode = ref('')
const batchText = ref('')
const querying = ref(false)
const error = ref('')
const result = ref<LookupResult | null>(null)
const batchResults = ref<LookupResult[]>([])

const parsedCodes = computed(() => parseCdks(batchText.value))

const batchSummary = computed(() => summarize(batchResults.value))

function statusText(status: string) {
  const key = `cdkLookup.status.${status}`
  const label = t(key)
  return label === key ? status : label
}

function statusTone(status: string) {
  if (status === 'used') return 'ok'
  if (status === 'failed') return 'err'
  if (status === 'processing') return 'primary'
  if (status === 'disabled' || status === 'expired' || status === 'unknown') return 'warn'
  return 'ink'
}

function goRedeem(code: string) {
  router.push({ path: '/recharge', query: { cdk: code } })
}

// 单张查询结果卡上的「去兑换」：带着码回到兑换页
function goRedeemCard(code: string) {
  router.push(`/recharge?code=${encodeURIComponent(code)}`)
}

async function query() {
  const code = cdkCode.value.trim()
  if (!code) return
  querying.value = true
  error.value = ''
  result.value = null
  try {
    const r = await fetch(`/api/v1/lookup/cdk?code=${encodeURIComponent(code)}`)
    const data = await r.json().catch(() => ({}))
    if (!r.ok) {
      error.value = data.error || data.message || t('cdkLookup.errNotFound')
      return
    }
    result.value = data as LookupResult
  } catch {
    error.value = t('cdkLookup.errNetwork')
  } finally {
    querying.value = false
  }
}

async function queryBatch() {
  const codes = parsedCodes.value.slice(0, LOOKUP_BATCH_MAX)
  if (!codes.length) {
    error.value = t('cdkLookup.batchEmpty')
    return
  }
  querying.value = true
  error.value = ''
  batchResults.value = []
  try {
    const r = await fetch('/api/v1/lookup/cdk/batch', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ codes }),
    })
    const data = await r.json().catch(() => ({}))
    if (!r.ok) {
      error.value = data.error || data.message || t('cdkLookup.errNetwork')
      return
    }
    batchResults.value = Array.isArray(data.results) ? data.results : []
  } catch {
    error.value = t('cdkLookup.errNetwork')
  } finally {
    querying.value = false
  }
}

async function copyUsedEmails() {
  const emails = Array.from(
    new Set(
      batchResults.value
        .map((row) => String(row.account_email || '').trim())
        .filter(Boolean),
    ),
  )
  if (!emails.length) {
    dialog.toast(t('cdkLookup.emailEmpty'), 'warn')
    return
  }
  const ok = await copyToClipboard(emails.join('\n'))
  dialog.toast(ok ? t('cdkLookup.copied') : t('cdkLookup.copyFail'), ok ? 'ok' : 'err')
}

function exportCsv() {
  const header = ['cdk_code', 'status', 'can_resubmit', 'account_email', 'plan', 'used_at', 'message']
  const lines = [header.join(',')]
  for (const row of batchResults.value) lines.push(csvRow(row))
  const blob = new Blob([`\uFEFF${lines.join('\n')}`], { type: 'text/csv;charset=utf-8' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = `cdk-lookup-${new Date().toISOString().slice(0, 10)}.csv`
  a.click()
  URL.revokeObjectURL(a.href)
}

function setMode(next: 'single' | 'batch') {
  mode.value = next
  error.value = ''
}

function reset() {
  result.value = null
  error.value = ''
  cdkCode.value = ''
}
</script>

<style scoped>
/* 960px 限宽只在窄屏；电脑布局交给 .u-page--split 的 1120px */
@media (max-width: 1023px) {
  .lk-wide { max-width: 960px; }
}
.lk-stack { display: flex; flex-direction: column; gap: 14px; }
.lk-gap { margin-top: 16px; }
.lk-modes { display: flex; gap: 8px; }
.lk-mode { flex: 1; font-size: 16px; padding: 8px 12px; }
.lk-mode.is-on { background: var(--u-tint); }
.lk-h { margin: 0; font-size: 18px; font-weight: 700; }
.lk-note { margin: 0; }
.lk-label { display: flex; flex-direction: column; gap: 6px; font-size: 15px; font-weight: 600; }
.lk-area { min-height: 160px; font-size: 14px; resize: vertical; }
.lk-submit { width: 100%; }
.lk-head { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; }
.lk-tools { display: flex; flex-wrap: wrap; gap: 8px; }
.lk-small { min-height: 48px; padding: 6px 14px; font-size: 15px; }
.lk-sum { list-style: none; margin: 0; padding: 0; display: flex; flex-wrap: wrap; gap: 8px; font-size: 14px; }
.lk-sum li { background: var(--u-tint); border-radius: var(--u-radius); padding: 8px 12px; }
.lk-table-wrap { overflow-x: auto; border: 1px solid var(--u-line); border-radius: var(--u-radius-card); }
.lk-table { width: 100%; min-width: 720px; border-collapse: collapse; font-size: 14px; }
.lk-table th { text-align: left; padding: 10px 12px; color: var(--u-muted); font-weight: 600; background: var(--u-tint); }
.lk-table td { padding: 10px 12px; border-top: 1px solid var(--u-line); vertical-align: middle; }
.lk-break { white-space: nowrap; }
.lk-nowrap { white-space: nowrap; }
.lk-status { font-weight: 600; white-space: nowrap; }
.lk-status--ok { color: var(--u-primary); }
.lk-status--err { color: var(--u-error-ink); }
.lk-status--warn { color: var(--u-warn-ink); }
.lk-status--primary { color: var(--u-primary); }
</style>
