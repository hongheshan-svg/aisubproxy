<template>
  <section class="card animate-slideInUp" data-testid="aftersale-lookup" aria-labelledby="aftersale-title">
    <div class="text-center">
      <h2 id="aftersale-title" class="text-2xl font-bold text-ink">{{ t('aftersale.title') }}</h2>
      <p class="mt-2 text-sm text-muted">{{ t('aftersale.sub') }}</p>
    </div>
    <div class="mx-auto mt-6 max-w-xl space-y-4">
      <div class="flex flex-col sm:flex-row gap-3">
        <input
          v-model="code"
          type="text"
          :placeholder="t('cdkLookup.cdkPlaceholder')"
          class="input mono flex-1"
          @keyup.enter="query"
        />
        <button type="button" class="btn-primary disabled:opacity-50 disabled:cursor-not-allowed" :disabled="!code.trim() || querying" @click="query">
          <span v-if="!querying">{{ t('cdkLookup.queryBtn') }}</span>
          <span v-else class="flex items-center justify-center gap-2"><span class="spinner"></span>{{ t('common.querying') }}</span>
        </button>
      </div>
      <p v-if="error" class="text-sm" style="color: var(--err)" data-testid="aftersale-error">{{ error }}</p>
      <AfterSaleResult v-if="result" :result="result" />
      <p class="text-center text-sm">
        <router-link to="/history" class="app-link" data-testid="aftersale-batch-link">{{ t('aftersale.batchLink') }} →</router-link>
      </p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AfterSaleResult from './AfterSaleResult.vue'
import type { LookupResult } from '../lib/aftersale'
import { lookupAftersale } from '../lib/aftersaleClient'

const { t } = useI18n({ useScope: 'global' })
const code = ref('')
const querying = ref(false)
const error = ref('')
const result = ref<LookupResult | null>(null)

async function query() {
  if (querying.value || !code.value.trim()) return
  querying.value = true
  error.value = ''
  result.value = null
  const r = await lookupAftersale(code.value, { notFound: t('cdkLookup.errNotFound'), network: t('cdkLookup.errNetwork') })
  result.value = r.result ?? null
  error.value = r.error
  querying.value = false
}
</script>
