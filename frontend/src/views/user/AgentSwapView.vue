<template>
  <UserShell>
    <UserPageHeader :title="t('agentSwap.title')" :subtitle="t('agentSwap.subtitle')" back />
    <main class="u-page swap">
      <div class="u-card swap-stack">
        <label class="swap-label">
          {{ t('agentSwap.passwordLabel') }}
          <span class="swap-pw">
            <input
              v-model="password"
              :type="showPw ? 'text' : 'password'"
              class="u-input"
              :placeholder="t('agentSwap.passwordPlaceholder')"
              autocomplete="off"
            />
            <button type="button" class="u-link swap-pw-toggle" :aria-pressed="showPw" @click="showPw = !showPw">
              {{ showPw ? t('redeemUi.hidePassword') : t('redeemUi.showPassword') }}
            </button>
          </span>
        </label>
        <label class="swap-label">
          {{ t('agentSwap.codeLabel') }}
          <textarea
            v-model="code"
            rows="3"
            class="u-input u-mono swap-area"
            :placeholder="t('agentSwap.codePlaceholder')"
          />
        </label>
        <button
          type="button"
          class="u-btn-primary swap-submit"
          :disabled="busy || locked || !password.trim() || !code.trim()"
          @click="doExchange"
        >
          {{ busy ? t('agentSwap.busy') : locked ? t('agentSwap.lockedButton') : t('agentSwap.submit') }}
        </button>
        <p v-if="error" class="u-error" role="alert">{{ error }}</p>
        <p v-if="locked" class="u-warn swap-strong" role="alert">
          {{ t('agentSwap.lockedNotice') }}
        </p>
      </div>

      <section v-if="result" class="u-card swap-stack swap-gap">
        <p class="u-help swap-ok">{{ result.message ? t('agentSwap.resultMessage') : t('agentSwap.resultFallback') }}</p>
        <div>
          <div class="u-help swap-cap">{{ t('agentSwap.newCodeCaption') }}</div>
          <div class="u-mono swap-code">{{ result.new_code }}</div>
        </div>
        <div class="swap-actions">
          <button type="button" class="u-btn-primary" @click="copyNew">{{ t('agentSwap.copy') }}</button>
          <button type="button" class="u-btn-secondary" @click="reset">{{ t('agentSwap.again') }}</button>
        </div>
        <div class="u-help swap-meta">
          <div>{{ t('agentSwap.metaPlan', { v: result.plan || '—' }) }}</div>
          <div>{{ t('agentSwap.metaOldStatus', { v: result.order_status || '—' }) }}</div>
          <div>{{ t('agentSwap.metaOldPrefix', { v: result.old_code_prefix || '—' }) }}</div>
        </div>
      </section>

      <UserFooter />
    </main>
  </UserShell>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { dialog } from '../../lib/dialog'
import { copyToClipboard } from '../../lib/clipboard'
import { swapFailureT } from '../../lib/apiError'
import UserPageHeader from '../../components/UserPageHeader.vue'
import UserShell from '../../components/redeem/UserShell.vue'
import UserFooter from '../../components/redeem/UserFooter.vue'

const { t, te } = useI18n({ useScope: 'global' })
const password = ref('')
const showPw = ref(false)
const code = ref('')
const busy = ref(false)
const error = ref('')
const result = ref<any>(null)
// 结果不明时锁住提交：刷新页面前不允许再点，避免重复发码
const locked = ref(false)

async function doExchange() {
  if (locked.value || busy.value) return
  error.value = ''
  result.value = null
  busy.value = true
  try {
    const r = await fetch('/api/v1/public/cdk/exchange', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        password: password.value.trim(),
        code: code.value.trim(),
      }),
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      const f = swapFailureT(r.status, d, t, te)
      error.value = f.message
      if (f.locked) locked.value = true
      return
    }
    result.value = d
    dialog.toast(t('agentSwap.toastIssued'), 'ok')
  } catch {
    // 请求发出后连接中断：服务端可能已经发了码
    const f = swapFailureT(0, {}, t, te)
    error.value = f.message
    locked.value = true
  } finally {
    busy.value = false
  }
}

async function copyNew() {
  const c = String(result.value?.new_code || '')
  if (!c) return
  const ok = await copyToClipboard(c)
  dialog.toast(ok ? t('agentSwap.toastCopied') : t('agentSwap.toastCopyFailed'), ok ? 'ok' : 'err')
}

function reset() {
  if (locked.value) return
  code.value = ''
  result.value = null
  error.value = ''
}
</script>

<style scoped>
.swap-pw { display: flex; align-items: center; gap: 10px; }
.swap-pw .u-input { flex: 1; min-width: 0; }
.swap-pw-toggle { flex: none; min-height: 48px; padding: 0 4px; font: inherit; font-size: 15px; background: none; border: 0; }
.swap-stack { display: flex; flex-direction: column; gap: 14px; }
.swap-gap { margin-top: 16px; }
.swap-label { display: flex; flex-direction: column; gap: 6px; font-size: 15px; font-weight: 600; }
.swap-area { min-height: 96px; font-size: 14px; resize: vertical; }
.swap-submit { width: 100%; }
.swap-strong { margin: 0; font-weight: 600; }
.swap-ok { margin: 0; color: var(--u-ink); font-size: 15px; font-weight: 600; }
.swap-cap { margin: 0 0 6px; }
.swap-code { word-break: break-all; font-size: 15px; padding: 12px 14px; background: var(--u-tint); border: 1px solid var(--u-line); border-radius: var(--u-radius); }
.swap-actions { display: flex; flex-wrap: wrap; gap: 10px; }
.swap-meta { margin: 0; display: flex; flex-direction: column; gap: 2px; }
</style>
