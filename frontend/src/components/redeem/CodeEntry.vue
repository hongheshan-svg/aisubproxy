<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ modelValue: string; busy: boolean; error: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string]; submit: [] }>()
const { t } = useI18n({ useScope: 'global' })
</script>

<template>
  <form class="code-entry" novalidate @submit.prevent="emit('submit')">
    <input
      :value="modelValue"
      data-testid="code-input"
      type="text"
      class="u-input u-mono"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      :placeholder="t('redeemUi.home.inputPlaceholder')"
      :aria-label="t('redeemUi.home.inputPlaceholder')"
      :aria-invalid="error ? 'true' : undefined"
      :readonly="busy"
      @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
    />
    <p v-if="error" class="u-error" role="alert" data-testid="code-error">{{ error }}</p>
    <button type="submit" class="u-btn-primary" data-testid="code-submit" :disabled="busy || !modelValue.trim()">
      <span v-if="busy" class="code-spin" aria-hidden="true" />
      {{ t('redeemUi.home.continue') }}
    </button>
  </form>
</template>

<style scoped>
.code-entry { display: flex; flex-direction: column; gap: 12px; }
.code-entry .u-btn-primary { width: 100%; gap: 10px; }
.code-spin {
  width: 18px; height: 18px; border-radius: 50%;
  border: 3px solid currentColor; border-right-color: transparent;
  animation: code-spin 0.8s linear infinite;
}
@keyframes code-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .code-spin { animation: none; } }
</style>
