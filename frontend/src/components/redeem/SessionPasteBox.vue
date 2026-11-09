<script setup lang="ts">
// 粘贴登录信息的输入框：输入或粘贴后立刻在本地检查，结果写在框下（规格 §6.3）。内容不写日志、不存浏览器、不进网址。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { checkPastedSession, type SessionCheck } from '../../lib/sessionCheck'

const props = defineProps<{ modelValue: string }>()
const emit = defineEmits<{ 'update:modelValue': [value: string]; check: [result: SessionCheck] }>()
const { t } = useI18n({ useScope: 'global' })

const result = computed<SessionCheck>(() => checkPastedSession(props.modelValue, { now: Date.now() }))

function onInput(e: Event) {
  const v = (e.target as HTMLTextAreaElement).value
  emit('update:modelValue', v)
  emit('check', checkPastedSession(v, { now: Date.now() }))
}
</script>

<template>
  <div class="paste">
    <label class="paste-label" for="session-paste">{{ t('redeemUi.paste.label') }}</label>
    <textarea
      id="session-paste"
      :value="modelValue"
      data-testid="session-input"
      class="u-input u-mono paste-box"
      rows="5"
      autocomplete="off"
      autocapitalize="off"
      spellcheck="false"
      :placeholder="t('redeemUi.paste.placeholder')"
      :aria-invalid="!result.ok && result.problem !== 'empty' ? 'true' : undefined"
      @input="onInput"
    />
    <p v-if="result.ok" class="paste-ok" data-testid="paste-ok" role="status">
      {{ result.maskedEmail ? t('redeemUi.paste.ok', { email: result.maskedEmail }) : t('redeemUi.paste.okNoEmail') }}
    </p>
    <p v-else-if="result.problem !== 'empty'" class="u-error" role="alert" data-testid="paste-problem">
      {{ t(`redeemUi.paste.${result.problem}`) }}
    </p>
  </div>
</template>

<style scoped>
.paste { display: flex; flex-direction: column; gap: 8px; }
.paste-label { font-size: 15px; font-weight: 600; color: var(--u-ink); }
.paste-box { width: 100%; min-height: 120px; resize: vertical; font-size: 14px; word-break: break-all; }
.paste-ok { margin: 0; font-size: 15px; color: var(--u-primary); }
</style>
