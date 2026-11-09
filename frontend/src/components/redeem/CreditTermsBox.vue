<script setup lang="ts">
// 点数兑换须知和「我已阅读并同意」勾选（规格 §6.3、§6.4）：第 ② 屏和第 ③ 屏共用。勾选只在内存里，不存浏览器。
import { useI18n } from 'vue-i18n'

defineProps<{ modelValue: boolean; disabled: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const { t } = useI18n({ useScope: 'global' })
</script>

<template>
  <div class="terms" data-testid="credit-terms">
    <p class="terms-title">{{ t('redeemUi.creditTerms.title') }}</p>
    <ul>
      <li>{{ t('redeemUi.creditTerms.item1') }}</li>
      <li>{{ t('redeemUi.creditTerms.item2') }}</li>
      <li>{{ t('redeemUi.creditTerms.item3') }}</li>
    </ul>
    <label class="terms-check">
      <input
        type="checkbox"
        data-testid="agree-terms"
        :checked="modelValue"
        :disabled="disabled"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
      />
      <span>{{ t('redeemUi.creditTerms.agree') }}</span>
    </label>
  </div>
</template>

<style scoped>
.terms { display: flex; flex-direction: column; gap: 4px; font-size: 14px; color: var(--u-ink); }
.terms-title { margin: 0; font-weight: 600; }
.terms ul { margin: 0; padding-left: 20px; color: var(--u-muted); }
.terms-check { min-height: 48px; display: flex; gap: 10px; align-items: flex-start; font-size: 15px; color: var(--u-ink); }
.terms-check input { margin-top: 4px; width: 18px; height: 18px; }
</style>
