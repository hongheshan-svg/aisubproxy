<script setup lang="ts">
// 第 ⑤ 屏「结果」（规格 §6.6）。成功：大勾、账号、续费或点数、打开对应产品；
// 其余情况用和首页同一个结果卡（CodeStatusCard，判断全在 afterSaleView），不另写一套。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { LookupResult } from '../../lib/aftersale'
import { codeAction } from '../../lib/redeemPage'
import CodeStatusCard from './CodeStatusCard.vue'

const props = defineProps<{
  /** 当前卡密（页面传 flow.code），用来区分结果卡上的按钮是同一张码还是换来的新码 */
  code: string
  planText: string
  status: string
  /** 供卡型（本站付款）还是上游发码型。保留给以后的服务商，现在不按它分支 */
  kind: 'card_supply' | 'upstream'
  product: string
  maskedEmail: string
  lookup: LookupResult | null
  creditQuantity?: number
  attemptsLeft?: number
}>()
const emit = defineEmits<{ retry: []; reset: []; useCode: [code: string] }>()
const { t } = useI18n({ useScope: 'global' })

const TARGETS: Record<string, { href: string; key: string; app: string }> = {
  chatgpt: { href: 'https://chatgpt.com', key: 'redeemUi.result.openChatgpt', app: 'ChatGPT' },
  claude: { href: 'https://claude.ai', key: 'redeemUi.result.openClaude', app: 'Claude' },
  grok: { href: 'https://grok.com', key: 'redeemUi.result.openGrok', app: 'Grok' },
  x: { href: 'https://x.com', key: 'redeemUi.result.openX', app: 'X' },
}

const success = computed(() => props.status === 'completed')
const target = computed(() => TARGETS[props.product] ?? TARGETS.chatgpt)
const isCredit = computed(() => props.creditQuantity !== undefined || props.lookup?.plan_kind === 'credit')
// 订阅码才列自动续费，取售后查询的 renewal；查不到就不显示这一行
const renewalText = computed(() => {
  if (isCredit.value) return ''
  const r = props.lookup?.renewal
  return r === 'off' ? t('redeemUi.result.renewalOff') : r === 'closing' ? t('redeemUi.result.renewalClosing') : ''
})
// 结果卡上带码的按钮：同一张码是「换个账号再试」，换来的新码交给页面重新开始（裁决 R28）
function onRedeem(code: string) {
  if (codeAction(code, props.code) === 'retry') emit('retry')
  else emit('useCode', code)
}
</script>

<template>
  <section v-if="success" class="u-card res" data-testid="result-success">
    <span class="res-mark" aria-hidden="true">
      <svg class="res-check" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12" /></svg>
    </span>
    <h2 class="res-title" data-testid="result-title">{{ t('redeemUi.result.successTitle', { plan: planText }).trim() }}</h2>
    <dl v-if="maskedEmail || renewalText" class="res-rows">
      <div v-if="maskedEmail" class="res-row"><dt>{{ t('redeemUi.result.account') }}</dt><dd class="u-mono">{{ maskedEmail }}</dd></div>
      <div v-if="renewalText" class="res-row" data-testid="result-renewal"><dt>{{ t('redeemUi.result.renewal') }}</dt><dd>{{ renewalText }}</dd></div>
    </dl>
    <p v-if="creditQuantity !== undefined" class="res-points" data-testid="result-points">{{ t('redeemUi.result.points', { n: creditQuantity }) }}</p>
    <p class="res-note">{{ t('redeemUi.result.refreshNote', { app: target.app }) }}</p>
    <a class="u-btn-primary res-btn" data-testid="result-open" :href="target.href" target="_blank" rel="noopener noreferrer">{{ t(target.key) }}</a>
    <button type="button" class="u-btn-secondary res-btn" data-testid="result-reset" @click="emit('reset')">{{ t('redeemUi.home.another') }}</button>
  </section>

  <CodeStatusCard v-else-if="lookup" :result="lookup" context="result" :attempts-left="attemptsLeft" @redeem="onRedeem" @reset="emit('reset')" />

  <!-- 没开通成功、售后结果也没取到：页面在下面给「查一下结果」 -->
  <section v-else class="u-card res" data-testid="result-unknown">
    <h2 class="res-title res-title--plain">{{ t('aftersale.state.unknown') }}</h2>
    <button type="button" class="u-btn-secondary res-btn" data-testid="result-reset" @click="emit('reset')">{{ t('redeemUi.home.another') }}</button>
  </section>
</template>

<style scoped>
.res { display: flex; flex-direction: column; gap: 14px; }
.res-mark {
  display: inline-flex; align-items: center; justify-content: center; align-self: flex-start;
  width: 56px; height: 56px; border-radius: 50%; background: var(--u-primary); color: var(--u-on-primary);
}
.res-check { width: 30px; height: 30px; stroke-dasharray: 24; stroke-dashoffset: 0; animation: res-draw 0.4s ease-out; }
@keyframes res-draw { from { stroke-dashoffset: 24; } to { stroke-dashoffset: 0; } }
@media (prefers-reduced-motion: reduce) { .res-check { animation: none; } }
.res-title { margin: 0; font-size: 22px; line-height: 1.4; font-weight: 700; color: var(--u-ink); overflow-wrap: anywhere; }
.res-title--plain { font-size: 20px; }
.res-rows { margin: 0; display: flex; flex-direction: column; gap: 10px; }
.res-row { display: flex; justify-content: space-between; gap: 12px; font-size: 15px; }
.res-row dt { color: var(--u-muted); }
.res-row dd { margin: 0; color: var(--u-ink); font-weight: 600; text-align: right; overflow-wrap: anywhere; }
.res-points { margin: 0; font-size: 15px; font-weight: 600; color: var(--u-ink); }
.res-note { margin: 0; font-size: 15px; color: var(--u-muted); }
.res-btn { width: 100%; text-decoration: none; }
</style>
