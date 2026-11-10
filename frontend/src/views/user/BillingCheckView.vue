<template>
  <UserShell>
    <UserPageHeader :title="t('redeemTabs.billing')" :subtitle="t('hero.billingSub')" back />
    <main class="u-page bill">
      <div class="u-card bill-stack">
        <div class="bill-modes">
          <button type="button" class="u-btn-secondary bill-mode" :class="{ 'is-on': mode === 'cdk' }" :aria-pressed="mode === 'cdk'" @click="mode = 'cdk'">{{ t('billingCheck.modeCdk') }}</button>
          <button type="button" class="u-btn-secondary bill-mode" :class="{ 'is-on': mode === 'session' }" :aria-pressed="mode === 'session'" @click="mode = 'session'">{{ t('billingCheck.modeLogin') }}</button>
        </div>

        <template v-if="mode === 'cdk'">
          <p class="u-help bill-note">
            {{ t('billingCheck.cdkHint') }}
          </p>
          <label class="bill-label">
            {{ t('billingCheck.cdkLabel') }}
            <input
              v-model="cdkCode"
              class="u-input u-mono"
              placeholder="SXC-XXXX-XXXX-XXXX-XXXX"
              @keyup.enter="query"
            />
          </label>
        </template>

        <template v-else>
          <p class="u-help bill-note">
            <SessionHelp />
            <span class="bill-block">{{ t('sessionHelp.tokenRule') }}</span>
          </p>
          <label class="bill-label">
            {{ t('billingCheck.loginLabel') }}
            <textarea
              v-model="tokenInput"
              class="u-input u-mono bill-area"
              :placeholder="t('billingCheck.loginPlaceholder')"
            />
          </label>
        </template>

        <p v-if="error" class="u-error" role="alert">{{ error }}</p>
        <button class="u-btn-primary bill-submit" :disabled="loading || !canSubmit" @click="query">
          {{ loading ? t('billingCheck.loading') : t('billingCheck.submit') }}
        </button>
      </div>

      <section v-if="record" class="u-card bill-stack bill-gap">
        <div class="bill-row-head">
          <h2 class="bill-h">{{ t('billingCheck.recordTitle') }}</h2>
          <span class="u-help bill-src">{{ t('billingCheck.sourceCdk') }}</span>
        </div>
        <p v-if="billingNote" class="u-help bill-note">{{ billingNote }}</p>
        <dl class="bill-list">
          <div v-for="row in billingRecordRows(record, t, locale)" :key="row.key" class="bill-item"><dt>{{ row.label }}</dt><dd :class="{ 'u-mono': row.key !== 'plan' }">{{ row.value }}</dd></div>
        </dl>
      </section>

      <section v-if="summary" class="u-card bill-stack bill-gap">
        <div class="bill-row-head">
          <h2 class="bill-h">{{ t('billingCheck.summaryTitle') }}</h2>
          <span v-if="authSource" class="u-help bill-src">{{ authSource === 'cdk' ? t('billingCheck.sourceCdk') : t('billingCheck.sourceLogin') }}</span>
        </div>
        <dl class="bill-list">
          <div class="bill-item"><dt>{{ t('billingCheck.plan') }}</dt><dd>{{ planLabel }}</dd></div>
          <div class="bill-item"><dt>{{ t('billingCheck.activeSub') }}</dt><dd>{{ summary.has_active_subscription ? t('billingCheck.yes') : t('billingCheck.no') }}</dd></div>
          <div class="bill-item"><dt>{{ t('billingCheck.autoRenew') }}</dt><dd>{{ summary.will_renew == null ? '—' : (summary.will_renew ? t('billingCheck.renewOn') : t('billingCheck.renewOff')) }}</dd></div>
          <div class="bill-item"><dt>{{ t('billingCheck.billing') }}</dt><dd class="u-mono">{{ summary.billing_period || '—' }}</dd></div>
          <div class="bill-item"><dt>{{ t('billingCheck.expires') }}</dt><dd class="u-mono">{{ summary.active_until || summary.expires_at || summary.renews_at || '—' }}</dd></div>
        </dl>
      </section>

      <section v-if="invoices" class="u-card bill-stack bill-gap">
        <h2 class="bill-h">{{ t('billingCheck.invoicesTitle', { n: invoices.length }) }}</h2>
        <p v-if="!invoices.length" class="u-help bill-note">{{ t('billingCheck.noInvoices') }}</p>
        <dl v-else class="bill-list">
          <div v-for="row in invoiceRows(invoices, t, locale)" :key="row.key" class="bill-item"><dt class="u-mono">{{ row.date }}</dt><dd>{{ row.status }}</dd></div>
        </dl>
      </section>

      <UserFooter />
    </main>
  </UserShell>
</template>

<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import UserPageHeader from '../../components/UserPageHeader.vue'
import UserShell from '../../components/redeem/UserShell.vue'
import UserFooter from '../../components/redeem/UserFooter.vue'
import SessionHelp from '../../components/SessionHelp.vue'
import { pickBillingNote } from '../../lib/billingNote'
import { billingRecordRows, invoiceRows, pickBillingResult, type BillingRecord } from '../../lib/billingRecord'
import { apiErrorText } from '../../lib/apiError'
const { t, te, locale } = useI18n({ useScope: 'global' })


const route = useRoute()
const mode = ref<'cdk' | 'session'>('cdk')
const cdkCode = ref('')
const billingNote = ref('')
const tokenInput = ref('')
const loading = ref(false)
const error = ref('')
const summary = ref<any>(null)
const record = ref<BillingRecord | null>(null)
const invoices = ref<any[] | null>(null)
const authSource = ref('')

const canSubmit = computed(() =>
  mode.value === 'cdk' ? !!cdkCode.value.trim() : !!tokenInput.value.trim(),
)

const planLabel = computed(() => {
  const s = summary.value || {}
  const raw = (s.plan_type || s.subscription_plan || '').toString()
  if (!raw || raw === 'free') return t('billingCheck.planFreeUnknown')
  return raw.replace('chatgpt', 'ChatGPT ').replace(/_/g, ' ')
})

onMounted(() => {
  const qcdk = String(route.query.cdk || '').trim()
  if (qcdk) {
    cdkCode.value = qcdk
    mode.value = 'cdk'
    query()
  }
})

async function query() {
  error.value = ''
  summary.value = null
  record.value = null
  invoices.value = null
  authSource.value = ''
  billingNote.value = ''
  loading.value = true
  try {
    const body =
      mode.value === 'cdk'
        ? { cdk_code: cdkCode.value.trim() }
        : { token_input: tokenInput.value }
    const r = await fetch('/api/v1/public/billing/check', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      error.value = apiErrorText(t, te, 'billingCheck.errors', d, 'billingCheck.queryFailed')
      return
    }
    authSource.value = d.auth_source || ''
    // 凭卡密查询：后端给本站开通记录（record），不是 ChatGPT 的账单；登录信息查询给订阅摘要和账单
    const shown = pickBillingResult(d)
    record.value = shown.record
    summary.value = shown.summary
    invoices.value = shown.invoices
    // 后端的说明文字只有一种（本站记录不是实时账单），按当前语言显示对应文案；有说明才显示
    billingNote.value = shown.record && pickBillingNote(d) ? t('billingCheck.cdkNote') : ''
  } catch {
    error.value = t('billingCheck.networkError')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.bill-stack { display: flex; flex-direction: column; gap: 14px; }
.bill-gap { margin-top: 16px; }
.bill-modes { display: flex; gap: 8px; }
.bill-mode { flex: 1; font-size: 16px; padding: 8px 12px; }
.bill-mode.is-on { background: var(--u-tint); }
.bill-note { margin: 0; }
.bill-block { display: block; margin-top: 4px; }
.bill-label { display: flex; flex-direction: column; gap: 6px; font-size: 15px; font-weight: 600; }
.bill-area { min-height: 144px; font-size: 14px; resize: vertical; }
.bill-submit { width: 100%; }
.bill-row-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.bill-h { margin: 0; font-size: 18px; font-weight: 700; }
.bill-src { margin: 0; }
.bill-list { margin: 0; display: flex; flex-direction: column; }
.bill-item { display: flex; justify-content: space-between; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--u-line); font-size: 15px; }
.bill-item dt { color: var(--u-muted); }
.bill-item dd { margin: 0; font-weight: 600; text-align: right; word-break: break-all; }
</style>
