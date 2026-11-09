<template>
  <UserShell>
    <UserPageHeader :title="t('redeemTabs.billing')" :subtitle="t('hero.billingSub')" back />
    <main class="u-page bill">
      <div class="u-card bill-stack">
        <div class="bill-modes">
          <button type="button" class="u-btn-secondary bill-mode" :class="{ 'is-on': mode === 'cdk' }" :aria-pressed="mode === 'cdk'" @click="mode = 'cdk'">卡密查询</button>
          <button type="button" class="u-btn-secondary bill-mode" :class="{ 'is-on': mode === 'session' }" :aria-pressed="mode === 'session'" @click="mode = 'session'">Session 查询</button>
        </div>

        <template v-if="mode === 'cdk'">
          <p class="u-help bill-note">
            输入兑换用的 CDK。系统会使用兑换时保存的登录信息去拉取账单（无需再粘贴）。
            若从未用 Session 完成过兑换，请改用「Session 查询」。
          </p>
          <label class="bill-label">
            CDK 卡密
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
            完整 Session 内容
            <textarea
              v-model="tokenInput"
              class="u-input u-mono bill-area"
              placeholder="粘贴完整的 Session 内容"
            />
          </label>
        </template>

        <p v-if="error" class="u-error" role="alert">{{ error }}</p>
        <button class="u-btn-primary bill-submit" :disabled="loading || !canSubmit" @click="query">
          {{ loading ? '查询中…' : '查询订阅与账单' }}
        </button>
      </div>

      <section v-if="summary" class="u-card bill-stack bill-gap">
        <div class="bill-row-head">
          <h2 class="bill-h">订阅摘要</h2>
          <span v-if="authSource" class="u-help bill-src">{{ authSource === 'cdk' ? '来自卡密绑定' : '来自 Session' }}</span>
        </div>
        <dl class="bill-list">
          <div class="bill-item"><dt>方案</dt><dd>{{ planLabel }}</dd></div>
          <div class="bill-item"><dt>有效订阅</dt><dd>{{ summary.has_active_subscription ? '是' : '否' }}</dd></div>
          <div class="bill-item"><dt>自动续费</dt><dd>{{ summary.will_renew == null ? '—' : (summary.will_renew ? '开启' : '已关闭') }}</dd></div>
          <div class="bill-item"><dt>计费</dt><dd class="u-mono">{{ summary.billing_currency || '—' }} {{ summary.billing_period || '' }}</dd></div>
          <div class="bill-item"><dt>到期 / 续费</dt><dd class="u-mono">{{ summary.active_until || summary.expires_at || summary.renews_at || '—' }}</dd></div>
        </dl>
      </section>

      <section v-if="invoices" class="u-card bill-stack bill-gap">
        <h2 class="bill-h">付款账单（{{ invoices.length }}）</h2>
        <p v-if="billingNote" class="u-help bill-note">{{ billingNote }}</p>
        <p v-if="!invoices.length" class="u-help bill-note">无付款账单记录。</p>
        <div v-for="inv in invoices" :key="inv.id || inv.number" class="bill-inv">
          <div class="bill-inv-head">
            <b class="u-mono">{{ formatAmount(inv.total, inv.currency) }}</b>
            <span class="u-help bill-note">{{ inv.paid ? '已付' : (inv.status || '未付') }} · {{ formatTs(inv.created) }}</span>
          </div>
          <div v-if="inv.description" class="u-help bill-note">{{ inv.description }}</div>
          <div class="bill-links">
            <a v-if="inv.hosted_invoice_url" class="u-link" :href="inv.hosted_invoice_url" target="_blank" rel="noopener">账单链接</a>
            <a v-if="inv.invoice_pdf" class="u-link" :href="inv.invoice_pdf" target="_blank" rel="noopener">PDF</a>
          </div>
        </div>
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
const { t } = useI18n({ useScope: 'global' })


const route = useRoute()
const mode = ref<'cdk' | 'session'>('cdk')
const cdkCode = ref('')
const billingNote = ref('')
const tokenInput = ref('')
const loading = ref(false)
const error = ref('')
const summary = ref<any>(null)
const invoices = ref<any[] | null>(null)
const authSource = ref('')

const canSubmit = computed(() =>
  mode.value === 'cdk' ? !!cdkCode.value.trim() : !!tokenInput.value.trim(),
)

const planLabel = computed(() => {
  const s = summary.value || {}
  const raw = (s.plan_type || s.subscription_plan || '').toString()
  if (!raw || raw === 'free') return '免费 / 未知'
  return raw.replace('chatgpt', 'ChatGPT ').replace(/_/g, ' ')
})

function formatAmount(total: any, currency: any) {
  const n = Number(total)
  if (!Number.isFinite(n)) return '—'
  const cur = (currency || 'usd').toString().toUpperCase()
  return `${(n / 100).toFixed(2)} ${cur}`
}
function formatTs(v: any) {
  const n = Number(v)
  if (!Number.isFinite(n) || n <= 0) return '—'
  try {
    return new Date(n * 1000).toLocaleString()
  } catch {
    return String(v)
  }
}

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
      error.value = d.error || '查询失败'
      return
    }
    summary.value = d.summary || {}
    invoices.value = d.invoices || []
    authSource.value = d.auth_source || ''
    billingNote.value = pickBillingNote(d)
  } catch (e: any) {
    error.value = e?.message || '网络错误'
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
.bill-inv { background: var(--u-tint); border-radius: 14px; padding: 12px 14px; display: flex; flex-direction: column; gap: 6px; font-size: 15px; }
.bill-inv-head { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 8px; }
.bill-links { display: flex; gap: 16px; }
</style>
