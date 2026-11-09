<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { afterSaleView, type LookupResult } from '../../lib/aftersale'
import { copyToClipboard } from '../../lib/clipboard'

// 首页和兑换页的结果屏共用：判断全部来自 afterSaleView；context 为 result 时只换失败那两类的标题（规格 §6.6）
const props = defineProps<{ result: LookupResult; context: 'home' | 'result'; attemptsLeft?: number }>()
const emit = defineEmits<{ redeem: [code: string]; reset: [] }>()
const { t } = useI18n({ useScope: 'global' })

const view = computed(() => afterSaleView(props.result))
const isRetry = computed(() => view.value.titleKey === 'aftersale.state.failed_restored')
const isManual = computed(() => view.value.titleKey === 'aftersale.state.failed_manual')
const inResult = computed(() => props.context === 'result')

const title = computed(() => {
  if (inResult.value && isRetry.value) return t('redeemUi.result.retryTitle')
  if (inResult.value && isManual.value) return t('redeemUi.result.manualTitle')
  return t(view.value.titleKey)
})
const showAttemptsNote = computed(() => inResult.value && isRetry.value && props.attemptsLeft === 1)
const showManualNote = computed(() => inResult.value && isManual.value)

const toneClass = computed(() => `card-tone--${view.value.tone}`)

// 记下刚复制的是哪一个按钮，「复制新码」和「复制卡密」各显示各的「已复制」
const copiedKey = ref<'new' | 'code' | ''>('')
async function copy(kind: 'new' | 'code', text: string) {
  copiedKey.value = (await copyToClipboard(text)) ? kind : ''
}
</script>

<template>
  <section class="u-card status-card" data-testid="code-status-card" :aria-label="title">
    <p class="status-code u-mono" data-testid="status-code">{{ result.cdk_code }}</p>
    <h2 class="status-title" :class="toneClass" data-testid="status-title">{{ title }}</h2>

    <p v-if="showManualNote" class="status-text" data-testid="status-manual-note">{{ t('redeemUi.result.manualNote') }}</p>
    <p v-if="showAttemptsNote" class="u-warn" data-testid="status-attempts">{{ t('redeemUi.result.attemptsLeftOne') }}</p>

    <p v-if="view.guideKey || view.note" class="status-text" data-testid="status-guide">
      <span v-if="view.guideKey">{{ t(view.guideKey) }}</span>
      <span v-if="view.note" class="status-note" data-testid="status-note">{{ view.note }}</span>
    </p>

    <dl v-if="view.fields.length" class="status-fields">
      <div v-for="f in view.fields" :key="f.labelKey" class="status-field">
        <dt>{{ t(f.labelKey) }}</dt>
        <dd :class="{ 'u-mono': f.labelKey === 'aftersale.field.email' }">{{ f.value }}</dd>
      </div>
    </dl>
    <p v-if="view.renewalKey" class="status-hint">{{ t(view.renewalKey) }}</p>

    <div v-if="view.replacement" class="status-replacement" data-testid="status-replacement">
      <div class="status-field">
        <span class="status-label">{{ t('aftersale.newCode') }}</span>
        <span class="u-mono" data-testid="replacement-code">{{ view.replacement.code }}</span>
      </div>
      <p class="status-hint">{{ t(view.replacement.statusKey) }}</p>
    </div>

    <div class="status-actions">
      <button v-if="view.actions.includes('redeem')" type="button" class="u-btn-primary" @click="emit('redeem', result.cdk_code)">{{ t('cdkLookup.goRedeem') }}</button>
      <button v-if="view.actions.includes('resubmit')" type="button" class="u-btn-primary" data-testid="status-retry" @click="emit('redeem', result.cdk_code)">{{ t('redeemUi.result.retryButton') }}</button>
      <button v-if="view.replacement && view.actions.includes('redeem_replacement')" type="button" class="u-btn-primary" data-testid="redeem-replacement" @click="emit('redeem', view.replacement.code)">{{ t('redeemUi.result.useNewCode') }}</button>
      <button v-if="view.replacement && view.actions.includes('copy_replacement')" type="button" class="u-btn-secondary" data-testid="copy-replacement" @click="copy('new', view.replacement.code)">{{ copiedKey === 'new' ? t('redeemUi.result.copied') : t('aftersale.copyNew') }}</button>
      <button v-if="showManualNote" type="button" class="u-btn-secondary" data-testid="copy-code" @click="copy('code', result.cdk_code)">{{ copiedKey === 'code' ? t('redeemUi.result.copied') : t('redeemUi.result.copyCode') }}</button>
      <button type="button" class="u-btn-secondary" data-testid="status-reset" @click="emit('reset')">{{ t('redeemUi.home.another') }}</button>
    </div>
  </section>
</template>

<style scoped>
.status-card { display: flex; flex-direction: column; gap: 12px; }
.status-code { margin: 0; font-size: 14px; color: var(--u-muted); overflow-wrap: anywhere; }
.status-title { margin: 0; font-size: 20px; line-height: 1.4; font-weight: 700; }
.card-tone--ok { color: var(--u-primary); }
.card-tone--warn { color: var(--u-warn-ink); }
.card-tone--err { color: var(--u-error-ink); }
.status-text { margin: 0; font-size: 16px; }
.status-note { display: block; margin-top: 4px; font-size: 14px; color: var(--u-muted); overflow-wrap: anywhere; }
.status-hint { margin: 0; font-size: 14px; color: var(--u-muted); }
.status-fields { margin: 0; display: flex; flex-direction: column; gap: 6px; }
.status-field { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 4px 12px; font-size: 15px; }
.status-field dt, .status-label { color: var(--u-muted); }
.status-replacement { display: flex; flex-direction: column; gap: 4px; }
.status-field dd { margin: 0; overflow-wrap: anywhere; }
.status-actions { display: flex; flex-direction: column; gap: 10px; margin-top: 4px; }
.status-actions button { width: 100%; }
</style>
