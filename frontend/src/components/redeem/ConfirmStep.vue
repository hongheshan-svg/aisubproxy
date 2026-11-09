<script setup lang="ts">
// 第 ③ 屏「核对账号」（规格 §6.4）：买家在这里决定要不要点「确认开通」付款。
// 四种状态（可以开通 / 不能用 / 要先恢复订阅 / 请自己确认）由页面算好用 confirmView 传进来，组件只负责显示和上报点击。
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FlowError } from '../../composables/useRedeemFlow'
import { isCardSupply, isCreditCode, isOneMonthCode } from '../../lib/mwredeem'
import { planLabel } from '../../lib/plan'
import { blockingKey, canConfirm, targetPlanOf, type AccountSummary, type ConfirmState } from '../../lib/redeemAccount'

const props = defineProps<{
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  preview: any
  account: AccountSummary | null
  blockingCode: string
  blockingReason: string
  extra: { accountCurrency?: string; codeCurrency?: string }
  submitState: 'idle' | 'submitting' | 'unknown'
  error: FlowError | null
  confirmView: ConfirmState
  canRecover: boolean
}>()
const emit = defineEmits<{
  confirm: [opts: { agreeCreditTerms: boolean }]
  changeAccount: []
  recover: []
  recheck: []
  checkResult: []
}>()
const { t } = useI18n({ useScope: 'global' })

const selfChecked = ref(false)
const agreedTerms = ref(false)

const isCredit = computed(() => isCreditCode(props.preview))
const planName = computed(() => planLabel(targetPlanOf(props.preview)))
const willOpen = computed(() =>
  isCardSupply(props.preview?.provider_kind) && isOneMonthCode(props.preview)
    ? t('redeemUi.confirm.withMonth', { plan: planName.value })
    : planName.value,
)
const currentText = computed(() => (props.account?.currentPlan ? planLabel(props.account.currentPlan) : t('redeemUi.confirm.unknown')))
const showRenewalRow = computed(() => isOneMonthCode(props.preview))

// 不能用的原因：有对照键用键；没有键用服务商给的原因；两者都空是「当前套餐已满足」
const blockedText = computed(() => {
  const plan = planLabel(props.account?.currentPlan ?? '')
  const key = blockingKey(props.blockingCode)
  if (key) return t(key, { plan, accountCurrency: props.extra?.accountCurrency ?? '', codeCurrency: props.extra?.codeCurrency ?? '' })
  if (props.blockingReason) return props.blockingReason
  return t('redeemUi.blocking.already_subscribed', { plan })
})

const submitEnabled = computed(() =>
  canConfirm({ confirmView: props.confirmView, submitState: props.submitState, selfChecked: selfChecked.value, isCredit: isCredit.value, agreedTerms: agreedTerms.value }),
)
const showForm = computed(() => props.confirmView === 'ok' || props.confirmView === 'self_check')

function submit() {
  if (!submitEnabled.value) return
  emit('confirm', { agreeCreditTerms: agreedTerms.value })
}
</script>

<template>
  <div class="u-card step">
    <!-- 不确定是否已提交：不给开通按钮，也不给「换一个账号」，免得重复付款（规格 §8） -->
    <template v-if="submitState === 'unknown'">
      <p class="u-warn" role="alert" data-testid="confirm-unknown">{{ t('redeemUi.errors.submitUnknown') }}</p>
      <button type="button" class="u-btn-primary step-btn" data-testid="confirm-check-result" @click="emit('checkResult')">
        {{ t('redeemUi.errors.checkResult') }}
      </button>
    </template>

    <template v-else>
      <h2 class="step-title" data-testid="confirm-title">
        {{ confirmView === 'ok' ? t('redeemUi.confirm.okTitle')
          : confirmView === 'blocked' ? t('redeemUi.confirm.blockedTitle')
          : confirmView === 'recover' ? t('redeemUi.confirm.recoverTitle')
          : t('redeemUi.confirm.verifyTitle') }}
      </h2>

      <template v-if="confirmView === 'blocked'">
        <p class="u-warn" data-testid="confirm-blocked">{{ blockedText }}</p>
      </template>

      <template v-else-if="confirmView === 'recover'">
        <p class="u-warn" data-testid="confirm-recover-note">{{ t('redeemUi.confirm.recoverHint') }}</p>
        <button v-if="canRecover" type="button" class="u-btn-primary step-btn" data-testid="confirm-recover" @click="emit('recover')">
          {{ t('redeemUi.confirm.recoverButton') }}
        </button>
        <button v-else type="button" class="u-btn-primary step-btn" data-testid="confirm-recheck" @click="emit('recheck')">
          {{ t('redeemUi.confirm.recheckButton') }}
        </button>
      </template>

      <template v-else>
        <dl class="step-rows">
          <div class="step-row"><dt>{{ t('redeemUi.confirm.account') }}</dt><dd>{{ account?.maskedEmail || t('redeemUi.confirm.unknown') }}</dd></div>
          <template v-if="confirmView === 'ok'">
            <div class="step-row"><dt>{{ t('redeemUi.confirm.current') }}</dt><dd>{{ currentText }}</dd></div>
          </template>
          <div class="step-row"><dt>{{ t('redeemUi.confirm.willOpen') }}</dt><dd>{{ willOpen }}</dd></div>
          <div v-if="isCredit" class="step-row"><dt>{{ t('redeemUi.confirm.points', { n: preview?.credit_quantity ?? '' }) }}</dt></div>
          <div v-else-if="showRenewalRow" class="step-row"><dt>{{ t('redeemUi.confirm.renewal') }}</dt><dd>{{ t('redeemUi.confirm.renewalWillOff') }}</dd></div>
          <div v-if="confirmView === 'self_check'" class="step-row">
            <dt>{{ t('redeemUi.confirm.condition') }}</dt>
            <dd>{{ preview?.requires_subscription ? t('redeemUi.cond.subscription') : t('redeemUi.cond.noSubscription') }}</dd>
          </div>
        </dl>

        <label v-if="confirmView === 'self_check'" class="step-check">
          <input v-model="selfChecked" type="checkbox" data-testid="confirm-self-check" :disabled="submitState !== 'idle'" />
          <span>{{ isCredit ? t('redeemUi.confirm.checkSubscribed') : t('redeemUi.confirm.checkNoSubscription') }}</span>
        </label>

        <div v-if="isCredit" class="step-terms" data-testid="credit-terms">
          <p class="step-terms-title">{{ t('redeemUi.creditTerms.title') }}</p>
          <ul>
            <li>{{ t('redeemUi.creditTerms.item1') }}</li>
            <li>{{ t('redeemUi.creditTerms.item2') }}</li>
            <li>{{ t('redeemUi.creditTerms.item3') }}</li>
          </ul>
          <label class="step-check">
            <input v-model="agreedTerms" type="checkbox" data-testid="confirm-agree-terms" :disabled="submitState !== 'idle'" />
            <span>{{ t('redeemUi.creditTerms.agree') }}</span>
          </label>
        </div>
      </template>

      <p v-if="error?.message" class="u-error" role="alert" data-testid="confirm-error">{{ error.message }}</p>

      <template v-if="showForm">
        <p class="step-irreversible">{{ t('redeemUi.confirm.irreversible') }}</p>
        <button type="button" class="u-btn-primary step-btn" data-testid="confirm-submit" :disabled="!submitEnabled" @click="submit">
          <span v-if="submitState === 'submitting'" class="step-spin" aria-hidden="true" />
          {{ submitState === 'submitting' ? t('redeemUi.confirm.submitting') : t('redeemUi.confirm.submit', { plan: planName }) }}
        </button>
      </template>

      <button type="button" class="u-btn-secondary step-btn" data-testid="confirm-change" :disabled="submitState === 'submitting'" @click="emit('changeAccount')">
        {{ t('redeemUi.confirm.switchAccount') }}
      </button>
    </template>
  </div>
</template>

<style scoped>
.step { display: flex; flex-direction: column; gap: 16px; }
.step-title { margin: 0; font-size: 20px; font-weight: 700; color: var(--u-ink); }
.step-rows { margin: 0; display: flex; flex-direction: column; gap: 10px; }
.step-row { display: flex; justify-content: space-between; gap: 12px; font-size: 15px; }
.step-row dt { color: var(--u-muted); }
.step-row dd { margin: 0; color: var(--u-ink); font-weight: 600; text-align: right; overflow-wrap: anywhere; }
.step-irreversible { margin: 0; font-size: 14px; color: var(--u-muted); }
.step-check { min-height: 48px; display: flex; gap: 10px; align-items: flex-start; font-size: 15px; color: var(--u-ink); }
.step-check input { margin-top: 4px; width: 18px; height: 18px; }
.step-terms { display: flex; flex-direction: column; gap: 4px; font-size: 14px; color: var(--u-ink); }
.step-terms-title { margin: 0; font-weight: 600; }
.step-terms ul { margin: 0; padding-left: 20px; color: var(--u-muted); }
.step-btn { width: 100%; min-height: 48px; border-radius: 14px; gap: 10px; }
.step-spin {
  width: 18px; height: 18px; border-radius: 50%;
  border: 3px solid currentColor; border-right-color: transparent;
  animation: step-spin 0.8s linear infinite;
}
@keyframes step-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .step-spin { animation: none; } }
</style>
