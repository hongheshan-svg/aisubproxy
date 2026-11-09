<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

const props = defineProps<{ codeLabel: string | null; current: 1 | 2 | 3; done: boolean }>()
const { t } = useI18n({ useScope: 'global' })

// 第一格认出码之前叫「核对卡密」；第三格完成后改叫「已开通」
const ticks = computed(() => {
  const labels = [
    props.codeLabel || t('redeemUi.ticks.checkCode'),
    t('redeemUi.ticks.confirmAccount'),
    props.done ? t('redeemUi.ticks.activated') : t('redeemUi.ticks.activate'),
  ]
  return labels.map((label, i) => {
    const n = i + 1
    const checked = props.done || n < props.current
    return { n, label, checked, current: !checked && n === props.current }
  })
})
</script>

<template>
  <ol class="ticks" data-testid="progress-ticks">
    <li
      v-for="tk in ticks"
      :key="tk.n"
      class="tick"
      :class="{ 'tick--done': tk.checked, 'tick--now': tk.current }"
      :aria-current="tk.current ? 'step' : undefined"
    >
      <span class="tick-dot" aria-hidden="true">
        <svg v-if="tk.checked" class="tick-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>
        <template v-else>{{ tk.n }}</template>
      </span>
      <span class="tick-label">{{ tk.label }}</span>
    </li>
  </ol>
</template>

<style scoped>
.ticks { display: flex; gap: 8px; list-style: none; margin: 0 0 20px; padding: 0; }
.tick { flex: 1; min-width: 0; display: flex; flex-direction: column; align-items: flex-start; gap: 6px; font-size: 14px; color: var(--u-muted); }
.tick-dot {
  display: inline-flex; align-items: center; justify-content: center;
  width: 28px; height: 28px; border-radius: 50%; font-size: 14px; font-weight: 700;
  border: 3px solid var(--u-line); background: var(--u-surface); color: var(--u-muted);
}
.tick--now { color: var(--u-ink); font-weight: 600; }
.tick--now .tick-dot { border-color: var(--u-primary); color: var(--u-primary); }
.tick--done .tick-dot { background: var(--u-primary); border-color: var(--u-primary); color: var(--u-on-primary); }
.tick--done { color: var(--u-ink); }
.tick-check { width: 14px; height: 14px; stroke-dasharray: 24; stroke-dashoffset: 0; animation: tick-draw 0.3s ease-out; }
@keyframes tick-draw { from { stroke-dashoffset: 24; } to { stroke-dashoffset: 0; } }
@media (prefers-reduced-motion: reduce) { .tick-check { animation: none; } }
.tick-label { overflow-wrap: anywhere; }
</style>
