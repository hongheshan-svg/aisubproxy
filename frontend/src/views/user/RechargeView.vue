<script setup lang="ts">
// 兑换页（规格 §6.2–§6.6、§7）：按 useRedeemFlow 的 screen 切换五屏；自动开通途中按 shownScreen 仍显示第 ② 屏。
// 流程判断都在 useRedeemFlow，页面上的分支在 lib/redeemPage（有单测），这里只接线。
//
// 资金安全：
// - 兑换请求只在两种情况下各发一次——第 ② 屏核对通过后自动发（flow.checkAndRedeem）、第 ③ 屏买家点「确认开通」（flow.redeem）；
//   页面从不自动重发；
// - 不确定是否已提交时只有「查一下结果」，页面上的换码入口一律锁住；
// - 第 ④⑤ 屏已经建单：「查一下结果」只调 refreshProgress / refreshLookup，不会回到「确认开通」（裁决 R30）；
// - 「取消原订阅」先弹确认框，买家确认后才调 recoverGrace()。
// 登录信息（第 ② 屏提交的内容）只放在本页一个普通变量里：不存储、不进网址、不写日志；
// 换账号、换码、进入开通后就清掉，刷新后自然没有。
// 第 ② 屏勾过的点数须知（termsAgreed）不是敏感数据，也只在内存里：换账号、换码时清掉，刷新后第 ③ 屏照旧显示须知。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import UserPageHeader from '../../components/UserPageHeader.vue'
import CodeEntry from '../../components/redeem/CodeEntry.vue'
import CodeStatusCard from '../../components/redeem/CodeStatusCard.vue'
import ConfirmStep from '../../components/redeem/ConfirmStep.vue'
import CredentialStep from '../../components/redeem/CredentialStep.vue'
import DeviceGuide from '../../components/redeem/DeviceGuide.vue'
import FaqList from '../../components/redeem/FaqList.vue'
import ProgressStep from '../../components/redeem/ProgressStep.vue'
import ProgressTicks from '../../components/redeem/ProgressTicks.vue'
import ResultStep from '../../components/redeem/ResultStep.vue'
import UserShell from '../../components/redeem/UserShell.vue'
import UserTwoColumn from '../../components/redeem/UserTwoColumn.vue'
import { useRedeemFlow } from '../../composables/useRedeemFlow'
import { detectDevice } from '../../lib/device'
import { dialog } from '../../lib/dialog'
import { isDesktop } from '../../lib/layoutMode'
import { isCardSupply, isCreditCode } from '../../lib/mwredeem'
import { planLabelT } from '../../lib/plan'
import { isChatgptCode, targetPlanOf } from '../../lib/redeemAccount'
import {
  canSwitchCode, checkActionFor, graceConfirmArgs, makeRecoverAction, makeStartOver, nextProgress, productOf, queryCode, recheckAction,
  showCheckButton, showGracePending, shownScreen, switchCodeEntry, ticksState, waitedLong,
} from '../../lib/redeemPage'
import { progressRows, type ProgressView } from '../../lib/redeemProgress'

type CredentialPayload = { credential: { mode: 'session'; session: string }; grokSession?: string; claudeOrgId?: string; agreeCreditTerms: boolean }

const { t } = useI18n({ useScope: 'global' })
const route = useRoute()
const router = useRouter()
const flow = useRedeemFlow()
// 直接用流程的 ref：传给 ConfirmStep 的 account、confirmView、preview 是稳定引用，提交失败回到空闲时不会清掉买家的勾选
const {
  screen, code, previewInfo, account, blockingCode, blockingReason, extra, result, lookup, error, busy, submitState,
  progressStartedAt, canRecover, confirmView, autoSubmitting,
} = flow

// 登录信息：普通变量，不做成响应式，也不交给模板
let credential: CredentialPayload | null = null
const codeInput = ref('')
// 右栏的设备说明用：和 CredentialStep 里一样在挂载后按浏览器判断
const device = ref<'ios' | 'android' | 'desktop'>('desktop')
const recoverTried = ref(false)
const termsAgreed = ref(false)
// 「已经等了 10 分钟」用：每 30 秒更新一次当前时间
const now = ref(Date.now())
let clock: ReturnType<typeof setInterval> | null = null

const plan = computed(() => targetPlanOf(previewInfo.value))
// 按卡密恢复的进度没有预览，套餐名退回售后查询给的
const planText = computed(() => (plan.value ? planLabelT(plan.value, t) : lookup.value?.plan || ''))
const codeLabel = computed(() => (plan.value ? t('redeemUi.ticks.planCode', { plan: planText.value }) : null))
// 页面按这一屏显示：自动开通途中第 ③ 屏不闪出来
const shown = computed(() => shownScreen(screen.value, autoSubmitting.value))
const ticks = computed(() => ticksState(shown.value, result.value.status))
const product = computed(() => productOf(plan.value))
const kind = computed<'card_supply' | 'upstream'>(() => (isCardSupply(previewInfo.value?.provider_kind) ? 'card_supply' : 'upstream'))
const creditQuantity = computed(() => {
  const n = Number(previewInfo.value?.credit_quantity)
  return isCreditCode(previewInfo.value) && n > 0 ? n : undefined
})
const maskedEmail = computed(() => account.value?.maskedEmail || lookup.value?.account_email || '')
const waited = computed(() => waitedLong(progressStartedAt.value, now.value))
const switchEntry = computed(() => switchCodeEntry(shown.value, submitState.value))
const gracePending = computed(() => showGracePending({ tried: recoverTried.value, screen: shown.value, confirmView: confirmView.value, error: error.value }))
const checkButton = computed(() => showCheckButton({ screen: shown.value, error: error.value, status: result.value.status, hasLookup: !!lookup.value }))
const headerTitle = computed(() => (shown.value === 'code' ? t('redeemUi.home.title') : t('hero.redeemTitle')))

// 开通中的三行：每次把上一次的行传进去，认不出的状态保持当前行
const progress = ref<ProgressView>(progressRows('', ''))
watch(
  () => [screen.value, result.value.status, result.value.stage] as const,
  ([s, status, stage]) => {
    // 已经提交了：登录信息用不上，不再留在内存里
    if (s === 'progress' || s === 'result') credential = null
    progress.value = nextProgress(s, status, stage, progress.value.rows)
  },
  { immediate: true },
)

function clearLocal() {
  credential = null
  recoverTried.value = false
  termsAgreed.value = false
}

/** 换一张卡密：编排见 makeStartOver（不确定是否已提交时什么都不做）。 */
const startOver = makeStartOver({
  canSwitch: () => canSwitchCode(submitState.value),
  clearLocal,
  reset: () => flow.reset(),
  setInput: (c) => { codeInput.value = c },
  replaceQuery: (query) => { void router.replace({ query }) },
  preview: (c) => { void flow.preview(c) },
})

/** 「查一下结果」：按屏选方法，第 ④⑤ 屏不会用 checkResult（裁决 R30）。 */
const CHECKS = { checkResult: flow.checkResult, refreshProgress: flow.refreshProgress, refreshLookup: flow.refreshLookup }
function onCheck() {
  const action = checkActionFor(screen.value, submitState.value)
  if (action) void CHECKS[action]()
}

/** 第 ② 屏「核对账号并开通」：核对结论是「可以开通」就直接开通，其余停在第 ③ 屏（规格 §6.4.1）。 */
function onCredential(p: CredentialPayload) {
  credential = p
  recoverTried.value = false
  termsAgreed.value = p.agreeCreditTerms
  void flow.checkAndRedeem(p.credential, { agreeCreditTerms: p.agreeCreditTerms, grokSession: p.grokSession, claudeOrgId: p.claudeOrgId })
}

/** 第 ③ 屏「确认开通」：自动开通没走成的兜底（刷新恢复、恢复订阅后重新核对、请自己确认、限流后再点）。 */
function onConfirm(o: { agreeCreditTerms: boolean }) {
  void flow.redeem({ agreeCreditTerms: o.agreeCreditTerms, grokSession: credential?.grokSession, claudeOrgId: credential?.claudeOrgId })
}

/** 第 ③ 屏「换一个账号」、第 ⑤ 屏「换个账号再试」：回第 ② 屏，卡密保留，登录信息清掉。 */
function onChangeAccount() {
  clearLocal()
  flow.changeAccount()
}

const recover = makeRecoverAction(
  () => dialog.confirm(...graceConfirmArgs(t)),
  () => flow.recoverGrace(),
)
async function onRecover() {
  // 只有真的发了恢复请求才算点过，被守卫挡掉时不显示「还没确认取消」
  if (await recover()) recoverTried.value = true
}

/** 「重新核对」：内存里还有上次的登录信息就直接再核对，没有（例如刷新过）就回第 ② 屏重新粘贴（裁决 R19）。 */
function onRecheck() {
  recoverTried.value = false
  const held = credential
  if (recheckAction(held !== null) === 'preflight' && held) void flow.preflight(held.credential)
  else onChangeAccount()
}

onMounted(() => {
  if (typeof navigator !== 'undefined') device.value = detectDevice(navigator.userAgent, navigator.maxTouchPoints)
  clock = setInterval(() => { now.value = Date.now() }, 30_000)
  const q = queryCode(route.query)
  // 带码进入：没有这张码的进度可恢复，就自动预览（规格 §6.2）
  if (!flow.restore(q) && q) {
    codeInput.value = q
    void flow.preview(q)
  }
})

onUnmounted(() => {
  if (clock) clearInterval(clock)
})
</script>

<template>
  <UserShell>
    <UserPageHeader :title="headerTitle" :subtitle="shown === 'code' ? t('redeemUi.home.subtitle') : ''" back />
    <main class="u-page u-page--split recharge">
      <ProgressTicks :code-label="codeLabel" :current="ticks.current" :done="ticks.done" />

      <UserTwoColumn>
        <template v-if="shown === 'code'">
          <CodeEntry v-model="codeInput" :busy="busy" :error="lookup ? '' : error?.message || ''" @submit="startOver(codeInput)" />
          <CodeStatusCard v-if="lookup" class="recharge-gap" :result="lookup" context="home" @redeem="startOver" @reset="startOver()" />
        </template>

        <CredentialStep
          v-else-if="shown === 'credential'"
          :preview="previewInfo"
          :busy="busy || autoSubmitting"
          :submitting="autoSubmitting && submitState === 'submitting'"
          :error="error"
          :guide-in-aside="isDesktop"
          @submit="onCredential"
        />

        <template v-else-if="shown === 'confirm'">
          <ConfirmStep
            :preview="previewInfo"
            :account="account"
            :blocking-code="blockingCode"
            :blocking-reason="blockingReason"
            :extra="extra"
            :submit-state="submitState"
            :error="error"
            :confirm-view="confirmView"
            :can-recover="canRecover"
            :terms-agreed="termsAgreed"
            @confirm="onConfirm"
            @change-account="onChangeAccount"
            @recover="onRecover"
            @recheck="onRecheck"
            @check-result="onCheck"
          />
          <p v-if="gracePending" class="u-warn recharge-gap" role="status" data-testid="grace-pending">{{ t('redeemUi.grace.pending') }}</p>
        </template>

        <ProgressStep v-else-if="shown === 'progress'" :plan-text="planText" :view="progress" :waited-long="waited" />

        <ResultStep
          v-else
          :code="code"
          :plan-text="planText"
          :status="result.status"
          :kind="kind"
          :product="product"
          :masked-email="maskedEmail"
          :lookup="lookup"
          :credit-quantity="creditQuantity"
          :attempts-left="result.attemptsLeft"
          @retry="onChangeAccount"
          @reset="startOver()"
          @use-code="startOver"
        />

        <template v-if="checkButton">
          <p v-if="error?.message" class="u-error recharge-gap" role="alert" data-testid="page-error">{{ error.message }}</p>
          <button type="button" class="u-btn-secondary recharge-btn" data-testid="page-check-result" :disabled="busy" @click="onCheck">
            {{ t('redeemUi.errors.checkResult') }}
          </button>
        </template>

        <button v-if="switchEntry === 'show'" type="button" class="u-link recharge-switch" data-testid="switch-code" @click="startOver()">
          {{ t('redeemUi.home.another') }}
        </button>
        <p v-else-if="switchEntry === 'locked'" class="recharge-hint" data-testid="switch-code-locked">{{ t('redeemUi.errors.switchCodeLocked') }}</p>

        <!-- 右栏只在电脑布局出现：粘贴登录信息这屏放设备说明，其余屏放常见问题 -->
        <template v-if="isDesktop" #aside>
          <DeviceGuide v-if="shown === 'credential' && isChatgptCode(previewInfo)" :initial="device" />
          <FaqList v-else />
        </template>
      </UserTwoColumn>
    </main>
  </UserShell>
</template>

<style scoped>
.recharge-gap { margin-top: 16px; }
.recharge-btn { width: 100%; margin-top: 12px; }
.recharge-switch {
  display: block; min-height: 48px; margin: 12px auto 0; padding: 0 12px;
  font: inherit; font-size: 15px; background: none; border: 0; border-radius: var(--u-radius);
}
.recharge-hint { margin: 16px 0 0; font-size: 14px; color: var(--u-muted); text-align: center; }
</style>
