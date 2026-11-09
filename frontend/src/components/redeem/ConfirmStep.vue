<script setup lang="ts">
// 第 ③ 屏「核对账号」（规格 §6.4）：买家在这里决定要不要点「确认开通」付款。
// 四种状态（可以开通 / 不能用 / 要先恢复订阅 / 请自己确认）由页面算好用 confirmView 传进来，组件只负责显示和上报点击。
// 点数码在第 ② 屏勾过须知（termsAgreed）就不再显示须知，按已同意处理（规格 §6.4.1）。
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FlowError } from '../../composables/useRedeemFlow'
import { isCardSupply, isCreditCode, isOneMonthCode } from '../../lib/mwredeem'
import { planLabelT } from '../../lib/plan'
import { blockingKey, canConfirm, targetPlanOf, type AccountSummary, type ConfirmState } from '../../lib/redeemAccount'
import CreditTermsBox from './CreditTermsBox.vue'

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
  termsAgreed: boolean
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
// 换了账号、状态或码，之前的勾选作废，不能带到新账号上
watch(() => [props.account, props.confirmView, props.preview], () => { selfChecked.value = false; agreedTerms.value = false })

const isCredit = computed(() => isCreditCode(props.preview))
// 有效的同意：第 ② 屏勾过，或在本屏勾选
const termsOk = computed(() => props.termsAgreed || agreedTerms.value)
const planName = computed(() => planLabelT(targetPlanOf(props.preview), t))
const willOpen = computed(() =>
  isCardSupply(props.preview?.provider_kind) && isOneMonthCode(props.preview)
    ? t('redeemUi.confirm.withMonth', { plan: planName.value })
    : planName.value,
)
const currentText = computed(() => (props.account?.currentPlan ? planLabelT(props.account.currentPlan, t) : t('redeemUi.confirm.unknown')))
const showRenewalRow = computed(() => isOneMonthCode(props.preview))
// 条件行和勾选框按码的标志取：点数码或要求已订阅 → 已订阅；要求无订阅 → 无订阅；都没有 → 不显示条件行，勾选框用通用文字
const condKind = computed<'sub' | 'nosub' | 'none'>(() =>
  isCredit.value || props.preview?.requires_subscription ? 'sub' : props.preview?.requires_no_subscription ? 'nosub' : 'none')
const checkText = computed(() =>
  condKind.value === 'sub' ? t('redeemUi.confirm.checkSubscribed') : condKind.value === 'nosub' ? t('redeemUi.confirm.checkNoSubscription') : t('redeemUi.confirm.checkAccount'))
const busy = computed(() => props.submitState !== 'idle')

// 不能用的原因：有对照键用键；没有键用服务商给的原因；码也空是「当前套餐已满足」；有未知码但没原因用通用文案
const blockedText = computed(() => {
  const plan = planLabelT(props.account?.currentPlan ?? '', t)
  const key = blockingKey(props.blockingCode)
  if (key) return t(key, { plan, accountCurrency: props.extra?.accountCurrency ?? '', codeCurrency: props.extra?.codeCurrency ?? '' })
  if (props.blockingReason) return props.blockingReason
  if (props.blockingCode) return t('redeemUi.blocking.generic')
  return t('redeemUi.blocking.already_subscribed', { plan })
})

const submitEnabled = computed(() =>
  canConfirm({ confirmView: props.confirmView, submitState: props.submitState, selfChecked: selfChecked.value, isCredit: isCredit.value, agreedTerms: termsOk.value }),
)
const showForm = computed(() => props.confirmView === 'ok' || props.confirmView === 'self_check')

function submit() {
  if (!submitEnabled.value) return
  emit('confirm', { agreeCreditTerms: termsOk.value })
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
        <button v-if="canRecover" type="button" class="u-btn-primary step-btn" data-testid="confirm-recover" :disabled="busy" @click="emit('recover')">
          {{ t('redeemUi.confirm.recoverButton') }}
        </button>
        <button v-else type="button" class="u-btn-primary step-btn" data-testid="confirm-recheck" :disabled="busy" @click="emit('recheck')">
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
          <div v-if="!isCredit && showRenewalRow" class="step-row"><dt>{{ t('redeemUi.confirm.renewal') }}</dt><dd>{{ t('redeemUi.confirm.renewalWillOff') }}</dd></div>
          <div v-if="confirmView === 'self_check' && condKind !== 'none'" class="step-row">
            <dt>{{ t('redeemUi.confirm.condition') }}</dt>
            <dd>{{ condKind === 'sub' ? t('redeemUi.cond.subscription') : t('redeemUi.cond.noSubscription') }}</dd>
          </div>
        </dl>
        <p v-if="isCredit" class="step-points">{{ t('redeemUi.confirm.points', { n: preview?.credit_quantity ?? '' }) }}</p>

        <label v-if="confirmView === 'self_check'" class="step-check">
          <input v-model="selfChecked" type="checkbox" data-testid="confirm-self-check" :disabled="submitState !== 'idle'" />
          <span>{{ checkText }}</span>
        </label>

        <CreditTermsBox v-if="isCredit && !termsAgreed" v-model="agreedTerms" :disabled="submitState !== 'idle'" />
      </template>

      <p v-if="error?.message" class="u-error" role="alert" data-testid="confirm-error">{{ error.message }}</p>

      <template v-if="showForm">
        <p class="step-irreversible">{{ t('redeemUi.confirm.irreversible') }}</p>
        <button type="button" class="u-btn-primary step-btn" data-testid="confirm-submit" :disabled="!submitEnabled" @click="submit">
          <span v-if="submitState === 'submitting'" class="step-spin" aria-hidden="true" />
          {{ submitState === 'submitting' ? t('redeemUi.confirm.submitting') : t('redeemUi.confirm.submit', { plan: planName }) }}
        </button>
      </template>

      <button type="button" class="u-btn-secondary step-btn" data-testid="confirm-change" :disabled="busy" @click="emit('changeAccount')">
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
.step-row dt { color: var(--u-muted); flex: none; max-width: 45%; }
.step-row dd { margin: 0; color: var(--u-ink); font-weight: 600; text-align: right; overflow-wrap: anywhere; }
.step-points { margin: 0; font-size: 15px; font-weight: 600; color: var(--u-ink); }
.step-irreversible { margin: 0; font-size: 14px; color: var(--u-muted); }
.step-check { min-height: 48px; display: flex; gap: 10px; align-items: flex-start; font-size: 15px; color: var(--u-ink); }
.step-check input { margin-top: 4px; width: 18px; height: 18px; }
.step-btn { width: 100%; min-height: 48px; border-radius: 14px; gap: 10px; }
.step-spin {
  width: 18px; height: 18px; border-radius: 50%;
  border: 3px solid currentColor; border-right-color: transparent;
  animation: step-spin 0.8s linear infinite;
}
@keyframes step-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .step-spin { animation: none; } }
</style>
