<script setup lang="ts">
// 第 ② 屏「粘贴登录信息」（规格 §6.3）。按码种换内容：ChatGPT 码给设备教程和粘贴框，Claude、Grok、X 沿用各自的校验。
// 登录信息是敏感数据：只在本组件内存里，不写日志、不存浏览器、不进网址。
// 点「核对账号并开通」后先核对，结论是「可以开通」就直接开通（规格 §6.3「下一步」、§6.4.1），所以点数码的须知在这里就要勾。
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import type { FlowError } from '../../composables/useRedeemFlow'
import { claudeCredential, claudeOrgCheck, normalizeOrgId } from '../../lib/claude'
import { detectDevice } from '../../lib/device'
import { grokCredential } from '../../lib/grok'
import { isCreditCode } from '../../lib/mwredeem'
import { credentialKindOf } from '../../lib/redeemAccount'
import { checkPastedSession } from '../../lib/sessionCheck'
import { xPremiumCredential } from '../../lib/x-premium'
import CreditTermsBox from './CreditTermsBox.vue'
import DeviceGuide from './DeviceGuide.vue'
import SessionPasteBox from './SessionPasteBox.vue'

// busy：核对或自动开通途中；submitting：自动开通已发出兑换请求
// eslint-disable-next-line @typescript-eslint/no-explicit-any
const props = withDefaults(
  // guideInAside：电脑布局下设备说明放到页面右栏，本组件不再画
  defineProps<{ preview: any; busy: boolean; submitting: boolean; error: FlowError | null; guideInAside?: boolean }>(),
  { guideInAside: false },
)
const emit = defineEmits<{
  submit: [payload: { credential: { mode: 'session'; session: string }; grokSession?: string; claudeOrgId?: string; agreeCreditTerms: boolean }]
}>()
const { t } = useI18n({ useScope: 'global' })

const kind = computed(() => credentialKindOf(props.preview))

// 没有 navigator（渲染测试、服务端）时默认电脑；挂载后再按设备改
const device = ref<'ios' | 'android' | 'desktop'>('desktop')
onMounted(() => {
  if (typeof navigator !== 'undefined') device.value = detectDevice(navigator.userAgent, navigator.maxTouchPoints)
})

const raw = ref('')
const orgFirst = ref('')
const orgSecond = ref('')
const freeConfirmed = ref(false)
const agreedTerms = ref(false)
const isCredit = computed(() => isCreditCode(props.preview))
const localError = ref('')
// 改了输入就清掉上一次的本地提示
watch([raw, orgFirst, orgSecond, freeConfirmed], () => { localError.value = '' })

const hasInput = computed(() => kind.value === 'claude' ? !!orgFirst.value.trim() : !!raw.value.trim())
// ChatGPT 码：本地检查通过才可点。这里每次现算、不用 SessionPasteBox 的 check 事件：登录信息有过期时间，
// 提交时必须按当下时间重查，存下的旧结果可能已过期；check 事件留给只想监听结果的使用方。
// 其余码有内容就可点，点下去再按各自的规则提示
const ready = computed(() => kind.value === 'chatgpt' ? checkPastedSession(raw.value, { now: Date.now() }).ok : hasInput.value)
const canSubmit = computed(() => !props.busy && !props.submitting && ready.value && (!isCredit.value || agreedTerms.value))
const shownError = computed(() => localError.value || props.error?.message || '')

function submit() {
  if (!canSubmit.value) return
  localError.value = ''
  if (kind.value === 'claude') {
    const bad = claudeOrgCheck(orgFirst.value, orgSecond.value, freeConfirmed.value)
    if (bad) { localError.value = t('claude.err.' + bad); return }
    emit('submit', { credential: claudeCredential(orgFirst.value), claudeOrgId: normalizeOrgId(orgFirst.value), agreeCreditTerms: agreedTerms.value })
  } else if (kind.value === 'grok') {
    const session = grokCredential(raw.value)
    if (!session) { localError.value = t('grok.invalid'); return }
    emit('submit', { credential: { mode: 'session', session }, grokSession: session, agreeCreditTerms: agreedTerms.value })
  } else if (kind.value === 'x') {
    const session = xPremiumCredential(raw.value)
    if (!session) { localError.value = t('xPremium.invalid'); return }
    emit('submit', { credential: { mode: 'session', session }, agreeCreditTerms: agreedTerms.value })
  } else {
    const r = checkPastedSession(raw.value, { now: Date.now() })
    // 粘贴之后、点击之前登录信息可能刚好过期：不能静默返回，要和 SessionPasteBox 一样给出原因
    if (!r.ok) { localError.value = t(`redeemUi.paste.${r.problem}`); return }
    emit('submit', { credential: { mode: 'session', session: r.value }, agreeCreditTerms: agreedTerms.value })
  }
}
</script>

<template>
  <form class="u-card step" novalidate @submit.prevent="submit">
    <p v-if="preview?.requires_no_subscription" class="u-warn" data-testid="cond-warn">{{ t('redeemUi.cond.noSubscription') }}</p>
    <p v-else-if="preview?.requires_subscription" class="u-warn" data-testid="cond-warn">{{ t('redeemUi.cond.subscription') }}</p>

    <template v-if="kind === 'chatgpt'">
      <DeviceGuide v-if="!guideInAside" :initial="device" />
      <SessionPasteBox v-model="raw" />
    </template>

    <template v-else-if="kind === 'claude'">
      <p class="step-hint">{{ t('claude.findOrg') }}</p>
      <label class="step-label" for="claude-org">{{ t('claude.orgLabel') }}</label>
      <input id="claude-org" v-model="orgFirst" data-testid="claude-org" class="u-input u-mono" autocomplete="off" spellcheck="false" :placeholder="t('claude.orgPlaceholder')" />
      <label class="step-label" for="claude-org-confirm">{{ t('claude.orgConfirmLabel') }}</label>
      <input id="claude-org-confirm" v-model="orgSecond" data-testid="claude-org-confirm" class="u-input u-mono" autocomplete="off" spellcheck="false" :placeholder="t('claude.orgPlaceholder')" />
      <label class="step-check">
        <input v-model="freeConfirmed" type="checkbox" data-testid="claude-free" />
        <span>{{ t('claude.freeConfirm') }}</span>
      </label>
    </template>

    <template v-else>
      <label class="step-label" for="cred-raw">{{ kind === 'grok' ? t('grok.credential') : t('xPremium.credential') }}</label>
      <p class="step-hint">{{ kind === 'grok' ? t('grok.hint') : t('xPremium.hint') }}</p>
      <input id="cred-raw" v-model="raw" data-testid="cred-input" class="u-input u-mono" autocomplete="off" autocapitalize="off" spellcheck="false" :placeholder="kind === 'grok' ? t('grok.placeholder') : t('xPremium.placeholder')" />
    </template>

    <CreditTermsBox v-if="isCredit" v-model="agreedTerms" :disabled="busy || submitting" />

    <p v-if="shownError" class="u-error" role="alert" data-testid="credential-error">{{ shownError }}</p>

    <p class="step-irreversible">{{ t('redeemUi.next.irreversible') }}</p>
    <button type="submit" class="u-btn-primary step-next" data-testid="credential-next" :disabled="!canSubmit">
      <span v-if="busy || submitting" class="step-spin" aria-hidden="true" />
      {{ submitting ? t('redeemUi.next.submitting') : busy ? t('redeemUi.next.checking') : t('redeemUi.next.button') }}
    </button>
  </form>
</template>

<style scoped>
.step { display: flex; flex-direction: column; gap: 16px; }
.step-label { font-size: 15px; font-weight: 600; color: var(--u-ink); }
.step-hint { margin: 0; font-size: 14px; color: var(--u-muted); }
.step-irreversible { margin: 0; font-size: 14px; color: var(--u-muted); }
.step-check { min-height: 48px; display: flex; gap: 10px; align-items: flex-start; font-size: 15px; color: var(--u-ink); }
.step-check input { margin-top: 4px; width: 18px; height: 18px; }
.step-next { width: 100%; gap: 10px; }
/* 组织 ID 的格式提示很长，375 宽下用小一号字才显示得全 */
input[data-testid^='claude-org']::placeholder { font-size: 13px; }
.step-spin {
  width: 18px; height: 18px; border-radius: 50%;
  border: 3px solid currentColor; border-right-color: transparent;
  animation: step-spin 0.8s linear infinite;
}
@keyframes step-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .step-spin { animation: none; } }
</style>
