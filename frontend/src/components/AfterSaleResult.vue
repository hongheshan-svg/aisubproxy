<template>
  <div class="space-y-4" data-testid="aftersale-result">
    <div class="rounded-xl bg-soft p-5 space-y-3">
      <div class="flex justify-between items-center gap-3 text-sm">
        <span class="text-muted shrink-0">{{ t('cdkLookup.cdkCode') }}</span>
        <span class="font-mono text-ink text-right break-all">{{ result.cdk_code }}</span>
      </div>
      <p class="font-semibold" :style="{ color: toneColor }" data-testid="aftersale-title">{{ t(view.titleKey) }}</p>
      <p v-if="view.guideKey || view.note" class="text-sm text-ink leading-relaxed" data-testid="aftersale-guide">
        <span v-if="view.guideKey">{{ t(view.guideKey) }}</span>
        <span v-if="view.note" class="block mt-1 break-words" data-testid="aftersale-note">{{ view.note }}</span>
      </p>
      <div v-for="f in view.fields" :key="f.labelKey" class="flex justify-between items-center gap-3 text-sm">
        <span class="text-muted shrink-0">{{ t(f.labelKey) }}</span>
        <span class="text-ink text-right break-all" :class="{ 'font-mono': f.labelKey === 'aftersale.field.email' }">{{ f.value }}</span>
      </div>
      <p v-if="view.renewalKey" class="text-sm text-muted">{{ t(view.renewalKey) }}</p>
      <div v-if="view.replacement" class="space-y-1 text-sm">
        <div class="flex justify-between items-center gap-3">
          <span class="text-muted shrink-0">{{ t('aftersale.newCode') }}</span>
          <span class="font-mono text-ink text-right break-all" data-testid="replacement-code">{{ view.replacement.code }}</span>
        </div>
        <p class="text-muted">{{ t(view.replacement.statusKey) }}</p>
      </div>
    </div>

    <div v-if="view.actions.length" class="flex flex-col sm:flex-row gap-3">
      <button v-if="view.actions.includes('redeem')" type="button" class="btn-primary flex-1" @click="goRedeem(result.cdk_code)">{{ t('cdkLookup.goRedeem') }}</button>
      <button v-if="view.actions.includes('resubmit')" type="button" class="btn-primary flex-1" @click="goRedeem(result.cdk_code)">{{ t('cdkLookup.resubmit') }}</button>
      <button v-if="view.replacement && view.actions.includes('copy_replacement')" type="button" class="btn-secondary flex-1" data-testid="copy-replacement" @click="copyNew(view.replacement.code)">{{ t('aftersale.copyNew') }}</button>
      <button v-if="view.replacement && view.actions.includes('redeem_replacement')" type="button" class="btn-primary flex-1" data-testid="redeem-replacement" @click="goRedeem(view.replacement.code)">{{ t('aftersale.redeemNew') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { afterSaleView, type LookupResult } from '../lib/aftersale'
import { copyToClipboard } from '../lib/clipboard'
import { toast } from '../lib/ui'

const props = defineProps<{ result: LookupResult }>()
const { t } = useI18n({ useScope: 'global' })
const router = useRouter()

const view = computed(() => afterSaleView(props.result))
const toneColor = computed(() => ({
  ok: 'var(--good, #16a34a)',
  info: 'var(--ink)',
  warn: 'var(--warn, #d97706)',
  err: 'var(--err, #dc2626)',
}[view.value.tone]))

function goRedeem(code: string) {
  router.push(`/recharge?code=${encodeURIComponent(code)}`)
}

async function copyNew(code: string) {
  const ok = await copyToClipboard(code)
  toast(ok ? t('cdkLookup.copied') : t('cdkLookup.copyFail'), ok ? 'ok' : 'err')
}
</script>
