<script setup lang="ts">
// 第 ④ 屏「开通中」（规格 §6.5）：三行进度，做完打勾、当前行转圈、没到的空心圆；下面一张白卡说可以关掉页面。
// 行的状态由页面用 progressRows 算好传进来，组件只负责显示。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { ProgressView } from '../../lib/redeemProgress'

const props = defineProps<{ planText: string; view: ProgressView; waitedLong: boolean }>()
const { t } = useI18n({ useScope: 'global' })

const title = computed(() => t('redeemUi.progress.title', { plan: props.planText }).trim())
const LABEL_KEYS = ['redeemUi.progress.rowCheck', 'redeemUi.progress.rowPay', 'redeemUi.progress.rowActivate']
const rows = computed(() => props.view.rows.map((state, i) => ({ key: LABEL_KEYS[i], label: t(LABEL_KEYS[i]), state })))
</script>

<template>
  <div class="prog">
    <h2 class="prog-title" data-testid="progress-title">{{ title }}</h2>
    <ol class="prog-rows" :aria-label="title" aria-live="polite">
      <li
        v-for="row in rows"
        :key="row.key"
        class="prog-row"
        :class="`prog-row--${row.state}`"
        :aria-current="row.state === 'current' ? 'step' : undefined"
      >
        <span class="prog-icon" aria-hidden="true">
          <svg v-if="row.state === 'done'" data-testid="prog-check" class="prog-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>
          <span v-else-if="row.state === 'current'" data-testid="prog-spin" class="prog-spin" />
          <span v-else data-testid="prog-hollow" class="prog-hollow" />
        </span>
        <span class="prog-label">{{ row.label }}</span>
      </li>
    </ol>
    <section class="u-card prog-note" data-testid="progress-note">
      <p>{{ t('redeemUi.progress.canClose') }}</p>
      <p v-if="waitedLong" data-testid="progress-slow">{{ t('redeemUi.progress.slow') }}</p>
    </section>
  </div>
</template>

<style scoped>
.prog { display: flex; flex-direction: column; gap: 16px; }
.prog-title { margin: 0; font-size: 20px; line-height: 1.4; font-weight: 700; color: var(--u-ink); overflow-wrap: anywhere; }
.prog-rows { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 14px; }
.prog-row { display: flex; align-items: center; gap: 12px; min-height: 32px; font-size: 17px; color: var(--u-muted); }
.prog-row--done, .prog-row--current { color: var(--u-ink); }
.prog-row--current { font-weight: 600; }
.prog-icon { flex-shrink: 0; display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; }
.prog-check {
  box-sizing: border-box; width: 28px; height: 28px; padding: 6px; border-radius: 50%;
  background: var(--u-primary); color: var(--u-on-primary);
  stroke-dasharray: 24; stroke-dashoffset: 0; animation: prog-draw 0.3s ease-out;
}
.prog-spin {
  box-sizing: border-box; width: 24px; height: 24px; border-radius: 50%;
  border: 3px solid var(--u-line); border-top-color: var(--u-primary);
  animation: prog-spin 0.8s linear infinite;
}
.prog-hollow { box-sizing: border-box; width: 24px; height: 24px; border-radius: 50%; border: 3px solid var(--u-line); }
@keyframes prog-spin { to { transform: rotate(360deg); } }
@keyframes prog-draw { from { stroke-dashoffset: 24; } to { stroke-dashoffset: 0; } }
@media (prefers-reduced-motion: reduce) { .prog-check, .prog-spin { animation: none; } }
.prog-note { display: flex; flex-direction: column; gap: 8px; font-size: 15px; color: var(--u-ink); }
.prog-note p { margin: 0; }
</style>
