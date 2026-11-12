<template>
  <UserShell>
    <UserPageHeader :title="t('batch.title')" :subtitle="t('batch.subtitle')" :max-width="isDesktop ? 0 : 768" back />
    <main class="u-page u-page--split batch-page">
      <UserTwoColumn>
        <p class="hint-line text-sm text-muted mb-3">{{ t('xPremium.batchHint') }}</p>
        <p class="hint-line text-sm text-muted mb-3">{{ t('grok.batchHint') }}</p>
        <p class="hint-line text-sm text-muted mb-3">{{ t('claude.batchHint') }}</p>

        <div class="u-card space-y-4">
          <div class="flex items-start gap-3 pb-3 border-b bd">
            <div
              class="w-10 h-10 rounded-xl flex items-center justify-center shrink-0 text-white text-[10px] font-bold leading-tight text-center"
              style="background: var(--primary)"
            >
              批量<br />兑换
            </div>
            <div>
              <h2 class="text-base font-semibold text-ink">{{ t('batch.panelTitle') }}</h2>
              <p class="text-xs text-muted mt-0.5 leading-relaxed">{{ t('batch.panelHint') }}</p>
            </div>
          </div>

          <!-- input phase -->
          <template v-if="phase === 'input'">
            <div>
              <label class="block text-sm font-medium text-ink mb-1.5">
                {{ t('batch.cdkListLabel', { max: BATCH_MAX_KEYS }) }}
              </label>
              <textarea
                v-model="cdkText"
                rows="6"
                class="u-input u-mono text-sm min-h-[120px]"
                :placeholder="t('batch.cdkPlaceholder')"
              />
              <p class="text-xs text-muted mt-1">
                {{ t('batch.cdkRecognized', { n: parsedCdks.length }) }}
                <span v-if="parsedCdks.length > BATCH_MAX_KEYS" class="text-warn">
                  · {{ t('batch.cdkOverLimit', { max: BATCH_MAX_KEYS }) }}
                </span>
              </p>
            </div>

            <div>
              <label class="block text-sm font-medium text-ink mb-1.5">{{ t('batch.credMode') }}</label>
              <div class="flex gap-2">
                <button
                  type="button"
                  class="u-btn-secondary !py-1.5 flex-1"
                  :class="{ 'ring-2': credMode === 'session' }"
                  @click="credMode = 'session'"
                >
                  {{ t('batch.modeSession') }}
                </button>
                <button
                  type="button"
                  class="u-btn-secondary !py-1.5 flex-1"
                  :class="{ 'ring-2': credMode === 'mailbox' }"
                  @click="credMode = 'mailbox'"
                >
                  {{ t('batch.modeMailbox') }}
                </button>
              </div>
            </div>

            <ExcelImportBlock
              v-if="credMode === 'session'"
              :session-pool="sessionPool"
              :import-msg="importMsg"
              :importing="importing"
              @pick="onPickFile"
              @clear="clearSessionPool"
            />

            <div v-else class="space-y-1.5">
              <label class="block text-sm font-medium text-ink">{{ t('batch.mailboxLabel') }}</label>
              <textarea
                v-model="mailboxText"
                rows="6"
                class="u-input u-mono text-sm min-h-[120px]"
                :placeholder="t('batch.mailboxPlaceholder')"
              />
              <p class="text-xs text-muted">
                {{ t('batch.mailboxRecognized', { n: parsedMailboxes.length }) }}
              </p>
              <input
                ref="mailboxFileRef"
                type="file"
                accept=".xlsx,.xls,.csv,.txt"
                class="hidden"
                @change="onPickMailboxFile"
              />
              <button
                type="button"
                class="w-full min-h-[48px] py-2.5 rounded-xl border border-dashed text-sm font-medium disabled:opacity-40"
                style="border-color: color-mix(in srgb, var(--primary) 45%, var(--brd)); color: var(--primary)"
                :disabled="importing"
                @click="mailboxFileRef?.click()"
              >
                {{ importing ? t('batch.importing') : t('batch.mailboxImportBtn') }}
              </button>
              <p v-if="importMsg" class="text-xs text-muted leading-relaxed">{{ importMsg }}</p>
              <p class="text-[11px] text-subtle">{{ t('batch.mailboxHint') }}</p>
            </div>

            <button
              type="button"
              class="u-btn-primary w-full"
              :disabled="verifying || parsedCdks.length === 0"
              @click="handleVerifyBatch"
            >
              {{ verifying ? t('batch.verifying') : t('batch.verifyStart') }}
            </button>
            <p v-if="sessionError" class="text-xs text-err">{{ sessionError }}</p>
          </template>

          <!-- run phase -->
          <template v-else>
            <div class="grid gap-2 text-center" :class="stats.unknown > 0 ? 'grid-cols-5' : 'grid-cols-4'">
              <div class="rounded-lg bg-soft py-2">
                <div class="text-lg font-bold tabular-nums text-ink">{{ stats.total }}</div>
                <div class="text-[10px] text-muted">{{ t('batch.statTotal') }}</div>
              </div>
              <div class="rounded-lg py-2" style="background: color-mix(in srgb, var(--warn, #d97706) 12%, transparent)">
                <div class="text-lg font-bold tabular-nums text-warn">{{ stats.wait }}</div>
                <div class="text-[10px] text-muted">{{ t('batch.statWait') }}</div>
              </div>
              <div class="rounded-lg py-2" style="background: color-mix(in srgb, var(--primary) 12%, transparent)">
                <div class="text-lg font-bold tabular-nums" style="color: var(--primary)">{{ stats.run }}</div>
                <div class="text-[10px] text-muted">{{ t('batch.statRun') }}</div>
              </div>
              <div class="rounded-lg py-2" style="background: color-mix(in srgb, var(--good, #16a34a) 12%, transparent)">
                <div class="text-lg font-bold tabular-nums text-good">
                  {{ stats.ok }}<span class="text-muted text-sm font-normal">/{{ stats.fail }}</span>
                </div>
                <div class="text-[10px] text-muted">{{ t('batch.statOkFail') }}</div>
              </div>
              <!-- 到时限还没查到结果的单独计：不是失败，不能重提，要买家去卡密查询页核对（F2-03 裁定 1） -->
              <div v-if="stats.unknown > 0" class="rounded-lg py-2" style="background: color-mix(in srgb, var(--warn, #d97706) 12%, transparent)">
                <div class="text-lg font-bold tabular-nums text-warn">{{ stats.unknown }}</div>
                <div class="text-[10px] text-muted">{{ t('batch.statUnknown') }}</div>
              </div>
            </div>

            <div v-if="!verifying && pendingSessionItems.length > 0 && credMode === 'session'" class="rounded-xl border bd p-3">
              <ExcelImportBlock
                :session-pool="sessionPool"
                :import-msg="importMsg"
                :importing="importing"
                @pick="onPickFile"
                @clear="clearSessionPool"
              />
            </div>
            <div v-else-if="!verifying && pendingSessionItems.length > 0 && credMode === 'mailbox'" class="rounded-xl border bd p-3 space-y-2">
              <label class="block text-sm font-medium text-ink">{{ t('batch.mailboxLabel') }}</label>
              <textarea
                v-model="mailboxText"
                rows="4"
                class="u-input u-mono text-sm"
                :placeholder="t('batch.mailboxPlaceholder')"
              />
              <p class="text-xs text-muted">{{ t('batch.mailboxRecognized', { n: parsedMailboxes.length }) }}</p>
            </div>

            <p v-if="pollNotice" class="text-xs text-err" role="alert">{{ pollNotice }}</p>

            <div v-if="verifying" class="text-sm text-muted text-center py-4">
              {{ t('batch.verifyingKeys') }}
            </div>

            <div
              v-else-if="currentItem"
              class="rounded-xl border p-4 space-y-3"
              style="border-color: color-mix(in srgb, var(--primary) 35%, var(--brd)); background: color-mix(in srgb, var(--primary) 6%, transparent)"
            >
              <div class="flex items-center justify-between gap-2 flex-wrap">
                <div class="text-sm font-medium text-ink">
                  {{ t('batch.currentOrdinal', { n: currentOrdinal, total: totalVerified }) }}
                  <span class="text-xs font-normal text-muted ml-1">
                    · {{ t('batch.remaining', { n: pendingSessionItems.length }) }}
                  </span>
                </div>
                <div class="text-xs font-mono" style="color: var(--primary)">
                  {{ currentItem.cardKey }}
                  <span v-if="currentItem.planLabel" class="ml-1.5 text-muted">({{ currentItem.planLabel }})</span>
                </div>
              </div>
              <template v-if="credMode === 'session'">
                <p class="text-xs text-muted leading-relaxed"><SessionHelp /></p>
                <p
                  v-if="currentExcelIdx >= 0 && sessionPool[currentExcelIdx]?.email"
                  class="text-xs text-muted"
                >
                  {{ t('batch.willUseAccount') }}
                  <span class="font-mono ml-1 text-ink">{{ maskEmail(sessionPool[currentExcelIdx].email) }}</span>
                </p>
                <textarea
                  ref="sessionBoxRef"
                  v-model="sessionInput"
                  rows="5"
                  class="u-input u-mono text-xs min-h-[100px]"
                  :placeholder="t('batch.sessionPlaceholder')"
                  :disabled="submitting || autoSubmitting"
                  @input="sessionError = ''"
                />
              </template>
              <template v-else>
                <p
                  v-if="currentExcelIdx >= 0 && mailboxPool[currentExcelIdx]?.email"
                  class="text-xs text-muted"
                >
                  {{ t('batch.willUseAccount') }}
                  <span class="font-mono ml-1 text-ink">{{ maskEmail(mailboxPool[currentExcelIdx].email) }}</span>
                </p>
                <textarea
                  ref="sessionBoxRef"
                  v-model="sessionInput"
                  rows="3"
                  class="u-input u-mono text-xs min-h-[72px]"
                  :placeholder="t('batch.mailboxLinePlaceholder')"
                  :disabled="submitting || autoSubmitting"
                  @input="sessionError = ''"
                />
              </template>
              <p v-if="sessionError" class="text-xs text-err">{{ sessionError }}</p>
              <div class="flex flex-col gap-2">
                <button
                  type="button"
                  class="u-btn-primary w-full"
                  :disabled="!sessionInput.trim() || submitting || autoSubmitting"
                  @click="handleSubmitSession"
                >
                  {{ submitting ? t('batch.submitting') : t('batch.submitNext') }}
                </button>
                <button
                  v-if="credMode === 'session' ? sessionPool.length > 0 : mailboxPool.length > 0"
                  type="button"
                  class="u-btn-primary w-full"
                  :disabled="submitting || autoSubmitting"
                  @click="handleAutoSubmitAll"
                >
                  {{
                    autoSubmitting
                      ? t('batch.autoSubmitting')
                      : t('batch.autoSubmit', {
                          n: credMode === 'session' ? sessionPool.length : mailboxPool.length,
                        })
                  }}
                </button>
              </div>
              <p class="text-[11px] text-muted text-center">
                {{ credMode === 'session' ? t('batch.pairHint') : t('batch.mailboxPairHint') }}
              </p>
            </div>

            <div
              v-else-if="allSessionsCollected"
              class="rounded-xl border p-4 text-center space-y-2"
              style="border-color: color-mix(in srgb, var(--good, #16a34a) 35%, var(--brd)); background: color-mix(in srgb, var(--good, #16a34a) 8%, transparent)"
            >
              <p class="text-sm font-medium text-good">
                {{ t('batch.allSubmitted', { n: sessionsDone }) }}
              </p>
              <p class="text-xs text-muted">{{ t('batch.watchProgress') }}</p>
              <button
                v-if="canExport"
                type="button"
                class="text-xs px-3 py-1.5 rounded-lg text-white"
                style="background: var(--good, #16a34a)"
                @click="exportBatchSuccess"
              >
                {{ exportLabel }}
              </button>
            </div>

            <div v-else class="text-sm text-muted text-center py-3">
              {{ t('batch.noValidKeys') }}
            </div>

            <div class="rounded-xl border bd overflow-hidden">
              <div class="px-3 py-2 bg-soft text-xs font-medium text-muted">{{ t('batch.liveProgress') }}</div>
              <ul class="divide-y max-h-80 overflow-y-auto" style="border-color: var(--brd)">
                <li
                  v-for="(it, idx) in items"
                  :key="it.id"
                  class="px-3 py-2.5 text-xs space-y-1"
                  style="border-color: var(--brd)"
                >
                  <div class="flex items-center gap-2 flex-wrap">
                    <span class="text-subtle tabular-nums w-5">{{ idx + 1 }}</span>
                    <span class="font-mono text-ink break-all">{{ it.cardKey }}</span>
                    <span v-if="it.planLabel" class="text-[10px] text-muted">{{ it.planLabel }}</span>
                    <span class="ml-auto px-1.5 py-0.5 rounded font-medium status-pill" :data-st="it.status">
                      {{ statusLabel(it.status) }}
                    </span>
                  </div>
                  <div
                    v-if="it.progressMsg || it.verifyMsg || it.email || it.cardLastFour || it.redemptionToken"
                    class="pl-7 text-[11px] text-muted space-y-0.5"
                  >
                    <div v-if="it.email">{{ t('batch.account') }}：{{ maskEmail(it.email) }}</div>
                    <div v-if="it.cardLastFour">{{ t('batch.card') }}：•••• {{ it.cardLastFour }}</div>
                    <div v-else-if="it.status === 'processing' || it.status === 'success'">
                      {{ t('batch.card') }}：{{ t('batch.cardPending') }}
                    </div>
                    <div v-if="it.redemptionToken" class="font-mono truncate">
                      token：{{ it.redemptionToken.slice(0, 18) }}…
                    </div>
                    <div>
                      {{
                        it.status === 'verify_fail' || it.status === 'failed'
                          ? it.error || it.progressMsg || it.verifyMsg
                          : it.progressMsg || it.verifyMsg
                      }}
                    </div>
                  </div>
                </li>
              </ul>
            </div>

            <button
              v-if="canExport"
              type="button"
              class="u-btn-secondary w-full"
              style="color: var(--good, #16a34a); border-color: color-mix(in srgb, var(--good, #16a34a) 40%, var(--brd))"
              @click="exportBatchSuccess"
            >
              {{ exportLabel }}
            </button>
            <button type="button" class="u-btn-secondary w-full" :disabled="submitting || autoSubmitting || unknownQuerying" @click="resetAll">
              {{ t('batch.reset') }}
            </button>
            <p v-if="unknownQuerying" class="text-[11px] text-muted text-center">{{ t('batch.resetWaitUnknown') }}</p>
          </template>
        </div>

        <template v-if="isDesktop" #aside><FaqList /></template>
      </UserTwoColumn>

      <UserFooter />
    </main>
  </UserShell>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBusy } from '../../lib/busyState'
import UserPageHeader from '../../components/UserPageHeader.vue'
import UserShell from '../../components/redeem/UserShell.vue'
import UserFooter from '../../components/redeem/UserFooter.vue'
import FaqList from '../../components/redeem/FaqList.vue'
import UserTwoColumn from '../../components/redeem/UserTwoColumn.vue'
import { isDesktop } from '../../lib/layoutMode'
import ExcelImportBlock from '../../components/ExcelImportBlock.vue'
import { POLL_REQUEST_TIMEOUT_MS, chunkTokens, nextPollPlan, parsePollResponse, withAbortTimeout } from '../../lib/batch-poll'
import {
  batchStats,
  claimRedeem,
  hasUnknownQuerying,
  isSubmittable,
  msUntilUnknownDeadline,
  nextPollTarget,
  ownEmailOf,
  pollVerdict,
  readPolledOrder,
  redeemOutcome,
  runSubmitPool,
  successRows,
  unknownExpired,
  unknownRows,
  type PollTargetState,
} from '../../lib/batch-redeem'
import SessionHelp from '../../components/SessionHelp.vue'
import { isGrokCredential } from '../../lib/grok'
import { isClaudePreview } from '../../lib/claude'
// 屏幕上的邮箱只显示打码后的（和单张兑换页同一个函数）；导出表格用条目的 ownEmail（F2-21、FE2 审查重要 2）
import { maskEmail } from '../../lib/sessionCheck'
import {
  AUTO_SUBMIT_CONCURRENCY,
  BATCH_MAX_KEYS,
  VERIFY_CONCURRENCY,
  accessTokenFromSession,
  checkSessionForCdk,
  exportSuccessWorkbook,
  extractCdkSession,
  mapPool,
  parseCdks,
  parseMailboxLine,
  parseMailboxLines,
  parseMailboxesFromSheet,
  parseSessionsFromSheet,
  readWorkbookRows,
  type ImportedMailbox,
  type ImportedSession,
} from '../../lib/batch-session'

const { t } = useI18n({ useScope: 'global' })

type ItemStatus =
  | 'pending_verify'
  | 'verify_ok'
  | 'verify_fail'
  | 'waiting_session'
  | 'submitting'
  | 'processing'
  | 'success'
  | 'failed'
  // 结果未知且到时限还没查到兑换记录：停止查询，提示去卡密查询页核对；不算失败，也绝不再提交（F2-03 裁定 1）
  | 'unknown'

interface BatchItem {
  /** `批次号-序号-卡密`：同一批卡密重置后再验证，新旧条目的 id 也不同 */
  id: string
  /** 这一条属于哪一批（handleVerifyBatch 时的 batchSeq） */
  batch: number
  cardKey: string
  planLabel: string
  status: ItemStatus
  verifyMsg: string
  redemptionToken: string
  progressMsg: string
  /** 屏幕显示用（显示时还会过 maskEmail）；兑换回包、查询结果会用服务端打过码的邮箱覆盖它 */
  email: string
  /**
   * 买家自己提供的完整邮箱（粘贴或导入的登录信息、邮箱密码列表，或核对账号时后端从买家登录信息读出的），
   * 只给导出用，屏幕上不显示；服务端回的打码邮箱不写这里（FE2 审查重要 2）
   */
  ownEmail: string
  cardLastFour: string
  accessToken: string
  gptPassword: string
  emailPassword: string
  error: string
}

function extractCardLastFour(order: any): string {
  const last = String(order?.card_last_four || '').trim()
  if (/^\d{4}$/.test(last)) return last
  const n = String(order?.card_number || '').replace(/\D/g, '')
  return n.length >= 4 ? n.slice(-4) : ''
}

const deviceId = (() => {
  const k = 'cdk_device_id'
  let v = localStorage.getItem(k)
  if (!v) {
    v = 'web-' + Math.random().toString(36).slice(2) + Date.now().toString(36)
    localStorage.setItem(k, v)
  }
  return v
})()

const phase = ref<'input' | 'run'>('input')
const credMode = ref<'session' | 'mailbox'>('session')
const cdkText = ref('')
const mailboxText = ref('')
const items = ref<BatchItem[]>([])
const verifying = ref(false)
const sessionInput = ref('')
const sessionError = ref('')
const submitting = ref(false)
const autoSubmitting = ref(false)
const sessionPool = ref<ImportedSession[]>([])
const mailboxPool = ref<ImportedMailbox[]>([])
const importMsg = ref('')
const importing = ref(false)
const sessionBoxRef = ref<HTMLTextAreaElement | null>(null)
const mailboxFileRef = ref<HTMLInputElement | null>(null)
const lastFilledItemId = ref<string | null>(null)

/** unknownSince：兑换请求结果未知（发出后断线或 5xx）的那一刻，正常受理的条目没有这个字段（F2-03） */
type PollTarget = PollTargetState
const pollTargets = ref<Record<string, PollTarget>>({})
// 还有结果未知、正在查询的卡密：查完（查到记录或到时限）之前不能重置（F2-04b 裁定 3）
const unknownQuerying = computed(() => hasUnknownQuerying(pollTargets.value))
// 兑换进行中（「重新开始」被禁用的那个条件）不弹「系统已更新」提示条，免得点了丢掉进度
useBusy('batch-redeem', () => submitting.value || autoSubmitting.value || unknownQuerying.value)
let pollTimer: ReturnType<typeof setTimeout> | null = null
// 结果未知的时限计时器：按最早一张的时限另排，不跟着查询间隔、429 退避往后拖（FE2 审查次要 1）
let expiryTimer: ReturnType<typeof setTimeout> | null = null
let pollInFlight = false
let pollBackoffMs = 0
// 轮询被限流（429）时显示的提示，恢复后清掉
const pollNotice = ref('')
// 离开页面后置真：在途请求回来不再排下一轮查询（F2-04a）
let unmounted = false
// 批次号：每次开始验证、重置都加一。在途请求回来时批次号对不上就丢掉，不写到新批次上，也不再提交（F2-04b）
let batchSeq = 0
// 本批已经发出兑换请求的卡密（FE2 审查重要 3）：发之前登记，登记过的不再发。开始验证、重置时随批次号换新集合
let redeemClaimed = new Set<string>()

/** 这一批还算数吗：页面还在、且没有换批次。不算数时不再提交还没开始的卡密，回来的结果也不写。 */
function live(gen: number) {
  return !unmounted && gen === batchSeq
}

const parsedCdks = computed(() => parseCdks(cdkText.value))
const parsedMailboxes = computed(() => parseMailboxLines(mailboxText.value))

watch(parsedMailboxes, (rows) => {
  // 文本粘贴优先；Excel 导入写入 mailboxPool 后也会同步到 mailboxText
  if (credMode.value === 'mailbox') mailboxPool.value = rows
})

const verifiedQueue = computed(() =>
  items.value.filter((i) => i.status !== 'pending_verify' && i.status !== 'verify_fail'),
)
// 能提交的只有 isSubmittable 认的那几种状态：结果未知、处理中、成功、失败的都不会再被提交
const pendingSessionItems = computed(() => items.value.filter((i) => isSubmittable(i.status)))
const currentItem = computed(() => pendingSessionItems.value[0] ?? null)
const currentExcelIdx = computed(() => {
  if (!currentItem.value) return -1
  return verifiedQueue.value.findIndex((i) => i.id === currentItem.value!.id)
})
const sessionsDone = computed(() =>
  items.value.filter((i) => ['submitting', 'processing', 'success', 'failed', 'unknown'].includes(i.status)).length,
)
const totalVerified = computed(() => verifiedQueue.value.length)
const currentOrdinal = computed(() =>
  currentExcelIdx.value >= 0 ? currentExcelIdx.value + 1 : sessionsDone.value,
)

const stats = computed(() => batchStats(items.value))
// 有成功的或结果未知的就能导出；结果未知的另起一页，不混进成功页
const canExport = computed(() => stats.value.ok > 0 || stats.value.unknown > 0)
const exportLabel = computed(() =>
  stats.value.unknown > 0
    ? t('batch.exportOkUnknown', { n: stats.value.ok, m: stats.value.unknown })
    : t('batch.exportOk', { n: stats.value.ok }),
)

const allSessionsCollected = computed(
  () =>
    phase.value === 'run' &&
    !verifying.value &&
    totalVerified.value > 0 &&
    pendingSessionItems.value.length === 0,
)

function statusLabel(st: ItemStatus) {
  const map: Record<ItemStatus, string> = {
    pending_verify: t('batch.status.pending_verify'),
    verify_ok: t('batch.status.verify_ok'),
    verify_fail: t('batch.status.verify_fail'),
    waiting_session: t('batch.status.waiting_session'),
    submitting: t('batch.status.submitting'),
    processing: t('batch.status.processing'),
    success: t('batch.status.success'),
    failed: t('batch.status.failed'),
    unknown: t('batch.status.unknown'),
  }
  return map[st] || st
}

function updateItem(id: string, patch: Partial<BatchItem>) {
  items.value = items.value.map((it) => (it.id === id ? { ...it, ...patch } : it))
}

async function api(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers || {})
  headers.set('X-Redemption-Device', deviceId)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const r = await fetch(path, { ...init, headers, credentials: 'include' })
  const text = await r.text()
  let data: any = null
  try {
    data = text ? JSON.parse(text) : null
  } catch {
    data = { raw: text }
  }
  return { r, data }
}

function planLabelOf(data: any): string {
  return String(data?.plan || data?.plan_type || data?.plan_label || data?.data?.plan || '')
}

function stopAllPolls() {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = null
  if (expiryTimer) clearTimeout(expiryTimer)
  expiryTimer = null
  pollInFlight = false
  pollBackoffMs = 0
  pollNotice.value = ''
  pollTargets.value = {}
}

/** 按在途条目数、是否被限流，决定下一轮什么时候查。全部到终态就停。 */
function schedulePoll(delayMs: number) {
  if (unmounted) return
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => void pollBatch(), delayMs)
}

/**
 * 结果未知、到时限还没查到兑换记录的：停止查询，条目标「结果未知」，提示去卡密查询页核对、不要重复提交。
 * 不判失败：判失败容易让买家重提同一张卡密。这一张也不会回到待提交队列（F2-03 裁定 1）。
 */
function expireUnknownTargets(now: number) {
  for (const [id, target] of Object.entries(pollTargets.value)) {
    if (target.terminal || !unknownExpired(target.unknownSince ?? 0, now)) continue
    updateItem(id, { status: 'unknown', progressMsg: t('batch.resultUnknownStopped'), error: '' })
    pollTargets.value[id] = { ...target, terminal: true }
  }
}

/** 按最早一张结果未知的时限排计时器（FE2 审查次要 1）。没有在查的结果未知就不排。 */
function scheduleUnknownExpiry() {
  if (unmounted) return
  if (expiryTimer) clearTimeout(expiryTimer)
  expiryTimer = null
  const wait = msUntilUnknownDeadline(pollTargets.value, Date.now())
  if (wait === null) return
  expiryTimer = setTimeout(onUnknownDeadline, wait)
}

/** 时限到了：到时限的标「结果未知」、停查；全部到终态时，排着的下一轮（可能在 429 退避中）不用再等。 */
function onUnknownDeadline() {
  expiryTimer = null
  if (unmounted) return
  expireUnknownTargets(Date.now())
  if (!Object.values(pollTargets.value).some((v) => !v.terminal)) {
    if (pollTimer) clearTimeout(pollTimer)
    pollTimer = null
    pollNotice.value = ''
    pollBackoffMs = 0
  }
  scheduleUnknownExpiry()
}

function applyPollResult(id: string, target: PollTarget, status: number, data: any) {
  const polled = readPolledOrder(data)
  const verdict = pollVerdict(status, polled, (target.unknownSince ?? 0) > 0)
  // 单个令牌查不到（404 等）、暂时出错，或结果未知的查到「失败、可以重新提交」（可能是这张卡密以前那次的记录）：
  // 保持原状，下一轮再查；结果未知的到时限由 expireUnknownTargets 收尾（FE2 审查重要 1）
  if (verdict === 'pending') return
  const st = polled.status
  const message = polled.message
  const email = polled.email
  const cardLastFour = extractCardLastFour(polled.order)
  const meta: Partial<BatchItem> = {}
  if (email) meta.email = email
  if (cardLastFour) meta.cardLastFour = cardLastFour
  if (verdict === 'success') {
    updateItem(id, { ...meta, status: 'success', progressMsg: message || '开通完成', error: '' })
  } else if (verdict === 'failed') {
    updateItem(id, { ...meta, status: 'failed', progressMsg: message || '兑换失败', error: message || '失败' })
  } else {
    updateItem(id, { ...meta, status: 'processing', progressMsg: message || (st ? `处理中 · ${st}` : '处理中…'), error: '' })
  }
  // 查到了这次的兑换记录：成功、失败停查；处理中去掉 unknownSince，之后不再按「结果未知、到时限就停」处理（FE2 审查次要 6）
  pollTargets.value[id] = nextPollTarget(target, verdict)
}

async function pollBatch() {
  pollTimer = null
  if (unmounted || pollInFlight) return
  const entries = Object.entries(pollTargets.value).filter(([, v]) => !v.terminal)
  if (!entries.length) {
    pollNotice.value = ''
    pollBackoffMs = 0
    return
  }
  const gen = batchSeq
  pollInFlight = true
  let rateLimited = false
  let failed = false
  try {
    // 所有在途令牌合并成一次请求（最多 100 个一组），后端只算一次查询限流
    for (const group of chunkTokens(entries)) {
      let outcome
      try {
        // 15 秒没回就中止：落到下面的 catch，算这一轮失败，下一轮再查
        const { r, data } = await withAbortTimeout(POLL_REQUEST_TIMEOUT_MS, (signal) =>
          api('/api/v1/lookup/cdk/results', {
            method: 'POST',
            body: JSON.stringify({ tokens: group.map(([, v]) => v.token) }),
            signal,
          }),
        )
        outcome = parsePollResponse(r.status, data)
      } catch {
        outcome = parsePollResponse(0, null)
      }
      // 查询途中重置或离开页面：这一轮的结果作废，也不再排下一轮
      if (!live(gen)) return
      if (outcome.rateLimited) {
        rateLimited = true
        break
      }
      if (outcome.failed) {
        failed = true
        continue
      }
      const byToken = new Map(outcome.items.map((it) => [it.token, it]))
      // 查询途中时限计时器已把某张标了「结果未知」：这一轮查到定论照样写上，比「结果未知」更准；
      // 「还没查到」的不改，仍是「结果未知」
      for (const [id, target] of group) {
        const it = byToken.get(target.token)
        if (!it) continue
        try {
          applyPollResult(id, target, it.status, it.data)
        } catch {
          // 这一张读结果、写条目时意外出错：算这一轮没查到，下一轮再查，不影响别的（FE2 审查次要 2）
          failed = true
        }
      }
    }
  } catch {
    // 别处意外出错：算这一轮失败，下面照常检查时限、排下一轮，查询不会就此停下（FE2 审查次要 2）
    failed = true
  } finally {
    // 换了批次时 stopAllPolls 已经复位，新批次可能已有一轮在途，不能替它清
    if (gen === batchSeq) pollInFlight = false
  }
  if (!live(gen)) return
  // 不管这一轮查到 404、5xx 还是整体失败，结果未知的到时限都停下
  expireUnknownTargets(Date.now())
  const remaining = Object.values(pollTargets.value).filter((v) => !v.terminal).length
  if (!remaining) {
    pollNotice.value = ''
    pollBackoffMs = 0
    return
  }
  const plan = nextPollPlan({ rateLimited, failed }, remaining, pollBackoffMs)
  pollBackoffMs = plan.backoffMs
  pollNotice.value = plan.notice
  schedulePoll(plan.delayMs)
}

function startPoll(id: string, token: string, gen: number, unknownSince = 0) {
  if (unmounted) return
  if (gen !== batchSeq) return
  const target: PollTarget = { token, terminal: false }
  if (unknownSince > 0) target.unknownSince = unknownSince
  pollTargets.value = { ...pollTargets.value, [id]: target }
  if (unknownSince > 0) scheduleUnknownExpiry()
  if (!pollTimer && !pollInFlight) void pollBatch()
}

/**
 * 兑换请求发出后断线或 5xx：不知道服务器收没收到。标「处理中」，用只读的批量结果接口查真实结果；
 * 查询只带兑换令牌，不会再提交卡密，也不会重发兑换（F2-03）。
 */
function markResultUnknown(item: BatchItem, token: string, email: string) {
  const patch: Partial<BatchItem> = { status: 'processing', progressMsg: t('batch.resultUnknown'), error: '' }
  if (email) patch.email = email
  updateItem(item.id, patch)
  startPoll(item.id, token, item.batch, Date.now())
}

function fillCredentialFromPool(excelIdx: number) {
  if (credMode.value === 'mailbox') {
    const pool = mailboxPool.value
    if (!pool.length || excelIdx < 0 || excelIdx >= pool.length) {
      sessionInput.value = ''
      return
    }
    const m = pool[excelIdx]
    sessionInput.value = `${m.email}----${m.password}`
    sessionError.value = ''
    return
  }
  const pool = sessionPool.value
  if (!pool.length || excelIdx < 0 || excelIdx >= pool.length) {
    sessionInput.value = ''
    return
  }
  sessionInput.value = pool[excelIdx].session
  sessionError.value = ''
}

watch(
  () => currentItem.value?.id,
  async (id) => {
    if (!id || currentExcelIdx.value < 0) return
    if (lastFilledItemId.value === id) return
    lastFilledItemId.value = id
    const hasPool =
      credMode.value === 'mailbox' ? mailboxPool.value.length > 0 : sessionPool.value.length > 0
    if (hasPool) fillCredentialFromPool(currentExcelIdx.value)
    else sessionInput.value = ''
    await nextTick()
    sessionBoxRef.value?.focus()
  },
)

async function onPickFile(file: File | null) {
  if (!file) return
  importing.value = true
  importMsg.value = ''
  try {
    const rows = await readWorkbookRows(file)
    const { sessions, mode, sessionCol, skippedDup } = parseSessionsFromSheet(rows)
    if (!sessions.length) {
      sessionPool.value = []
      importMsg.value =
        sessionCol < 0
          ? '未识别到 Session 列。请确保有 session 列或单元格为完整 Session JSON'
          : '识别到列但无有效 Session（需完整 JSON 含 sessionToken，或五段 JWE）'
      return
    }
    sessionPool.value = sessions
    importMsg.value =
      `已从「${file.name}」识别 ${sessions.length} 条 Session（${mode}）` +
      (skippedDup > 0 ? `，已去重跳过 ${skippedDup} 条` : '') +
      (sessions[0]?.email ? `，如 ${maskEmail(sessions[0].email)}` : '')
    lastFilledItemId.value = null
    if (phase.value === 'run' && currentExcelIdx.value >= 0) {
      fillCredentialFromPool(currentExcelIdx.value)
    }
  } catch (e) {
    sessionPool.value = []
    importMsg.value = '读取 Excel 失败：' + (e instanceof Error ? e.message : '未知错误')
  } finally {
    importing.value = false
  }
}

async function onPickMailboxFile(e: Event) {
  const input = e.target as HTMLInputElement
  const file = input.files?.[0] ?? null
  input.value = ''
  if (!file) return
  importing.value = true
  importMsg.value = ''
  try {
    const name = (file.name || '').toLowerCase()
    if (name.endsWith('.txt')) {
      const text = await file.text()
      const rows = parseMailboxLines(text)
      if (!rows.length) {
        importMsg.value = '未识别到邮箱密码行（支持 email----password）'
        return
      }
      mailboxText.value = rows.map((r) => `${r.email}----${r.password}`).join('\n')
      mailboxPool.value = rows
      importMsg.value = `已从「${file.name}」识别 ${rows.length} 条邮箱密码`
      return
    }
    const sheetRows = await readWorkbookRows(file)
    const { mailboxes, mode, skippedDup } = parseMailboxesFromSheet(sheetRows)
    if (!mailboxes.length) {
      importMsg.value = '未识别到邮箱/密码列。请使用「邮箱,邮箱密码」或 email----password'
      return
    }
    mailboxText.value = mailboxes.map((r) => `${r.email}----${r.password}`).join('\n')
    mailboxPool.value = mailboxes
    importMsg.value =
      `已从「${file.name}」识别 ${mailboxes.length} 条邮箱密码（${mode}）` +
      (skippedDup > 0 ? `，已去重跳过 ${skippedDup} 条` : '')
    lastFilledItemId.value = null
    if (phase.value === 'run' && currentExcelIdx.value >= 0) {
      fillCredentialFromPool(currentExcelIdx.value)
    }
  } catch (err) {
    importMsg.value = '读取失败：' + (err instanceof Error ? err.message : '未知错误')
  } finally {
    importing.value = false
  }
}

function clearSessionPool() {
  sessionPool.value = []
  importMsg.value = ''
  sessionInput.value = ''
}

async function handleVerifyBatch() {
  if (verifying.value || submitting.value || autoSubmitting.value) return
  const keys = parseCdks(cdkText.value)
  if (!keys.length) return
  if (keys.length > BATCH_MAX_KEYS) {
    sessionError.value = t('batch.cdkOverLimit', { max: BATCH_MAX_KEYS })
    return
  }
  const gen = ++batchSeq
  redeemClaimed = new Set<string>()
  verifying.value = true
  sessionError.value = ''
  stopAllPolls()
  lastFilledItemId.value = null
  if (credMode.value === 'mailbox') {
    mailboxPool.value = parsedMailboxes.value
  }
  const list: BatchItem[] = keys.map((k, i) => ({
    id: `${gen}-${i}-${k}`,
    batch: gen,
    cardKey: k,
    planLabel: '',
    status: 'pending_verify',
    verifyMsg: '验证中…',
    redemptionToken: '',
    progressMsg: '',
    email: '',
    ownEmail: '',
    cardLastFour: '',
    accessToken: '',
    gptPassword: '',
    emailPassword: '',
    error: '',
  }))
  items.value = list
  phase.value = 'run'
  sessionInput.value = ''

  try {
    const results = await mapPool(list, VERIFY_CONCURRENCY, async (item) => {
      try {
        const { r, data } = await api('/api/v1/public/cdk/preview', {
          method: 'POST',
          body: JSON.stringify({ code: item.cardKey }),
        })
        if (!r.ok) {
          const msg = String(data?.error || data?.msg || data?.message || 'CDK 无效或不可用')
          return {
            ...item,
            status: 'verify_fail' as const,
            verifyMsg: msg,
            error: msg,
          }
        }
        const token = String(
          data?.redemption_token || data?.data?.redemption_token || data?.token || '',
        )
        if (!token) {
          return {
            ...item,
            status: 'verify_fail' as const,
            verifyMsg: '未返回 redemption_token',
            error: '未返回 redemption_token',
          }
        }
        const body = data?.data || data || {}
        // Claude 码要填 Organization ID（两次核对、确认免费账号），批量页不配对，交给单张兑换
        if (isClaudePreview(body)) {
          const msg = t('claude.batchSingleOnly')
          return { ...item, status: 'verify_fail' as const, verifyMsg: msg, error: msg }
        }
        return {
          ...item,
          status: 'waiting_session' as const,
          verifyMsg: '有效',
          planLabel: planLabelOf(body),
          redemptionToken: token,
        }
      } catch {
        return {
          ...item,
          status: 'verify_fail' as const,
          verifyMsg: '验证请求失败',
          error: '验证请求失败',
        }
      }
    })
    // 验证途中重置或离开页面：结果作废
    if (!live(gen)) return
    items.value = results
  } finally {
    if (gen === batchSeq) {
      verifying.value = false
      lastFilledItemId.value = null
    }
  }
}

async function submitOne(
  item: BatchItem,
  credential:
    | { mode: 'session'; session: string; imported?: ImportedSession }
    | { mode: 'mailbox'; email: string; password: string },
): Promise<'ok' | 'fail'> {
  if (credential.mode === 'session') {
    const check = checkSessionForCdk(credential.session)
    if (!check.ok) {
      updateItem(item.id, {
        status: 'failed',
        progressMsg: check.error,
        error: check.error,
      })
      return 'fail'
    }
    const session = extractCdkSession(credential.session)
    const imported = credential.imported
    updateItem(item.id, {
      status: 'submitting',
      email: check.email || imported?.email || '',
      ownEmail: ownEmailOf(check.email, imported?.email),
      accessToken: imported?.accessToken || accessTokenFromSession(credential.session),
      gptPassword: imported?.gptPassword || '',
      emailPassword: imported?.emailPassword || '',
      progressMsg: '账号预检…',
      error: '',
    })
    return runPreflightRedeem(item, { mode: 'session', session }, check.email || imported?.email || '')
  }

  const email = credential.email.trim()
  const password = credential.password
  if (!email.includes('@') || !password) {
    updateItem(item.id, {
      status: 'failed',
      progressMsg: '邮箱或密码无效',
      error: '邮箱或密码无效',
    })
    return 'fail'
  }
  updateItem(item.id, {
    status: 'submitting',
    email,
    ownEmail: ownEmailOf(email),
    emailPassword: password,
    progressMsg: '账号预检…',
    error: '',
  })
  return runPreflightRedeem(item, { mode: 'mailbox', email, password }, email)
}

async function runPreflightRedeem(
  item: BatchItem,
  credential:
    | { mode: 'session'; session: string }
    | { mode: 'mailbox'; email: string; password: string },
  fallbackEmail: string,
): Promise<'ok' | 'fail'> {
  let redemptionToken = item.redemptionToken
  // 兑换请求已经发出：之后断线或出错都不知道服务器收没收到，只能查结果，绝不重发（F2-03）
  let redeemSent = false
  try {
    if (!redemptionToken) {
      const preview = await api('/api/v1/public/cdk/preview', {
        method: 'POST',
        body: JSON.stringify({ code: item.cardKey }),
      })
      if (!preview.r.ok) {
        const msg = String(preview.data?.error || preview.data?.msg || 'CDK 预览失败')
        updateItem(item.id, { status: 'failed', progressMsg: msg, error: msg })
        return 'fail'
      }
      redemptionToken = String(
        preview.data?.redemption_token || preview.data?.data?.redemption_token || '',
      )
      if (!redemptionToken) {
        updateItem(item.id, {
          status: 'failed',
          progressMsg: '未返回 redemption_token',
          error: '未返回 redemption_token',
        })
        return 'fail'
      }
      updateItem(item.id, { redemptionToken })
    }

    const { r, data } = await api('/api/v1/public/cdk/preflight', {
      method: 'POST',
      body: JSON.stringify({
        code: item.cardKey,
        redemption_token: redemptionToken,
        credential,
      }),
    })
    if (!r.ok || (data && typeof data.code === 'number' && data.code !== 0)) {
      const msg = String(data?.error || data?.msg || data?.message || '预检失败')
      updateItem(item.id, { status: 'failed', progressMsg: msg, error: msg })
      return 'fail'
    }
    const body = data?.data && typeof data.data === 'object' ? data.data : data || {}
    const preflightToken = String(body.preflight_token || data?.preflight_token || '')
    // Grok 码：预检不产票，兑换直接带登录态；后端给出的拦截原因（登录态失效/已有订阅）直接判失败。
    const grok = credential.mode === 'session' && isGrokCredential(credential.session)
    const blocking = grok ? String(body.blocking_reason || '') : ''
    if (blocking) {
      updateItem(item.id, { status: 'failed', progressMsg: blocking, error: blocking })
      return 'fail'
    }
    if (!preflightToken && !grok) {
      updateItem(item.id, {
        status: 'failed',
        progressMsg: '未返回 preflight_token',
        error: '未返回 preflight_token',
      })
      return 'fail'
    }
    const email = String(body.email || body.account_email || fallbackEmail || '')
    // 兑换是唯一会用掉卡密的请求：发出前最后看一次，页面已离开或换了批次就不再提交这一张
    if (!live(item.batch)) return 'fail'
    // 结构性保险：这张卡密本批已经发过兑换请求就不再发，也不改条目（由先发的那一次负责显示结果）
    if (!claimRedeem(redeemClaimed, item.cardKey)) return 'fail'
    updateItem(item.id, {
      email,
      ownEmail: ownEmailOf(fallbackEmail, body.email, body.account_email),
      progressMsg: '预检通过，提交兑换…',
    })

    const client_request_id = `batch-${deviceId.slice(0, 8)}-${Date.now()}-${item.id}`
    redeemSent = true
    const redeem = await api('/api/v1/public/cdk/redeem', {
      method: 'POST',
      body: JSON.stringify(grok
        ? { redemption_token: redemptionToken, credential, client_request_id }
        : { redemption_token: redemptionToken, preflight_token: preflightToken, client_request_id }),
    })
    const order =
      redeem.data?.order || redeem.data?.data?.order || redeem.data?.data || redeem.data || {}
    const st = String(order.status || redeem.data?.status || '')
    const message = String(
      order.message || order.user_message || redeem.data?.message || redeem.data?.msg || '',
    )
    const cardLastFour = extractCardLastFour(order)
    const orderEmail = String(order.account_email || order.email || email).trim()
    const outcome = redeemOutcome(redeem.r.status, st, redeem.data)

    if (outcome === 'unknown') {
      // 5xx（本站后端明确拒绝的 503 除外）：服务器可能已经建单（建单不跟随连接），按结果未知去查，不当失败；
      // 2xx 但认不出单据状态的也一样，到时限标「结果未知」（FE2 审查次要 5）
      markResultUnknown(item, redemptionToken, orderEmail || email)
      return 'ok'
    }
    if (outcome === 'rejected') {
      updateItem(item.id, {
        status: 'failed',
        email: orderEmail || email,
        cardLastFour: cardLastFour || item.cardLastFour,
        progressMsg: message || redeem.data?.error || '兑换被拒绝',
        error: message || redeem.data?.error || '兑换被拒绝',
      })
      return 'fail'
    }

    if (outcome === 'success') {
      updateItem(item.id, {
        status: 'success',
        email: orderEmail || email,
        cardLastFour,
        progressMsg: message || '开通完成',
        error: '',
      })
    } else if (outcome === 'failed') {
      updateItem(item.id, {
        status: 'failed',
        email: orderEmail || email,
        cardLastFour,
        progressMsg: message || '兑换失败',
        error: message || '失败',
      })
      return 'fail'
    } else {
      updateItem(item.id, {
        status: 'processing',
        email: orderEmail || email,
        cardLastFour,
        progressMsg: message || '处理中…',
        error: '',
      })
      startPoll(item.id, redemptionToken, item.batch)
    }
    return 'ok'
  } catch {
    if (redeemSent) {
      markResultUnknown(item, redemptionToken, '')
      return 'ok'
    }
    // 兑换请求还没发出（预览、核对账号时断线）：卡密没有用掉，仍算失败
    updateItem(item.id, {
      status: 'failed',
      progressMsg: '网络异常，请稍后重试或联系客服确认是否已受理',
      error: '网络异常',
    })
    return 'fail'
  }
}

async function handleSubmitSession() {
  if (!currentItem.value || submitting.value || autoSubmitting.value) return
  const raw = sessionInput.value.trim()
  if (credMode.value === 'mailbox') {
    const parsed = parseMailboxLine(raw)
    if (!parsed) {
      sessionError.value = '请填写 email----password（或 email:password）'
      return
    }
    sessionError.value = ''
    submitting.value = true
    const item = currentItem.value
    sessionInput.value = ''
    lastFilledItemId.value = null
    try {
      await submitOne(item, { mode: 'mailbox', email: parsed.email, password: parsed.password })
    } finally {
      submitting.value = false
    }
    return
  }

  const check = checkSessionForCdk(raw)
  if (!check.ok) {
    sessionError.value = check.error
    return
  }
  sessionError.value = ''
  submitting.value = true
  const item = currentItem.value
  sessionInput.value = ''
  lastFilledItemId.value = null
  try {
    const imported =
      currentExcelIdx.value >= 0 ? sessionPool.value[currentExcelIdx.value] : undefined
    await submitOne(item, {
      mode: 'session',
      session: raw,
      imported: imported?.session.trim() === raw ? imported : undefined,
    })
  } finally {
    submitting.value = false
  }
}

/**
 * 一键自动提交里的一张。意外抛错时（理论情况：submitOne 自己会接住请求出错）不知道兑换请求发没发出，
 * 按结果未知收尾，提示去卡密查询页核对、不要重复提交；错误不往外抛，并发循环照常取下一张（FE2 审查次要 3）。
 */
async function autoSubmitOne(item: BatchItem, run: () => Promise<unknown>) {
  try {
    await run()
  } catch {
    updateItem(item.id, { status: 'unknown', progressMsg: t('batch.resultUnknownStopped'), error: '' })
  }
}

async function handleAutoSubmitAll() {
  if (autoSubmitting.value || submitting.value || verifying.value) return
  const gen = batchSeq
  const verifiedSnap = items.value.filter(
    (i) => i.status !== 'pending_verify' && i.status !== 'verify_fail',
  )
  const pendingSnap = items.value.filter((i) => isSubmittable(i.status))
  if (!pendingSnap.length) {
    sessionError.value = '没有待提交的卡密'
    return
  }

  if (credMode.value === 'mailbox') {
    const pool = mailboxPool.value.length ? mailboxPool.value : parsedMailboxes.value
    if (!pool.length) {
      sessionError.value = '请先粘贴或导入邮箱密码列表'
      return
    }
    autoSubmitting.value = true
    sessionError.value = ''
    const verifiedPositions = new Map(verifiedSnap.map((item, index) => [item.id, index]))
    try {
      // 离开页面后不再取下一张（重置在自动提交时是禁用的）；已经在提交的那张照常等它返回
      await runSubmitPool(pendingSnap, AUTO_SUBMIT_CONCURRENCY, () => live(gen), (item) => autoSubmitOne(item, async () => {
        const excelIdx = verifiedPositions.get(item.id) ?? -1
        const mb = excelIdx >= 0 ? pool[excelIdx] : undefined
        if (!mb) {
          updateItem(item.id, {
            status: 'failed',
            progressMsg: `邮箱列表只有 ${pool.length} 条，第 ${excelIdx + 1} 张无对应`,
            error: '邮箱密码不足',
          })
          return
        }
        await submitOne(item, { mode: 'mailbox', email: mb.email, password: mb.password })
      }))
    } finally {
      sessionInput.value = ''
      autoSubmitting.value = false
    }
    return
  }

  const pool = sessionPool.value
  if (!pool.length) {
    sessionError.value = '请先导入 Excel Session'
    return
  }
  autoSubmitting.value = true
  sessionError.value = ''
  const verifiedPositions = new Map(verifiedSnap.map((item, index) => [item.id, index]))
  try {
    await runSubmitPool(pendingSnap, AUTO_SUBMIT_CONCURRENCY, () => live(gen), (item) => autoSubmitOne(item, async () => {
      const excelIdx = verifiedPositions.get(item.id) ?? -1
      const sess = excelIdx >= 0 ? pool[excelIdx] : undefined
      if (!sess) {
        updateItem(item.id, {
          status: 'failed',
          progressMsg: `Excel 只有 ${pool.length} 条 Session，第 ${excelIdx + 1} 张无对应`,
          error: 'Session 不足',
        })
        return
      }
      await submitOne(item, { mode: 'session', session: sess.session, imported: sess })
    }))
  } finally {
    sessionInput.value = ''
    autoSubmitting.value = false
  }
}

async function exportBatchSuccess() {
  if (!canExport.value) {
    sessionError.value = '本批尚无成功记录'
    return
  }
  sessionError.value = ''
  try {
    await exportSuccessWorkbook(
      successRows(items.value),
      'batch_success',
      unknownRows(items.value, t('batch.resultUnknownStopped')),
    )
  } catch {
    sessionError.value = '导出失败，请重试'
  }
}

function resetAll() {
  if (submitting.value || autoSubmitting.value || unknownQuerying.value) return
  // 换批次：还在路上的验证、查询回来都对不上，不会写到下一批上
  batchSeq++
  redeemClaimed = new Set<string>()
  stopAllPolls()
  verifying.value = false
  phase.value = 'input'
  items.value = []
  cdkText.value = ''
  mailboxText.value = ''
  mailboxPool.value = []
  sessionPool.value = []
  importMsg.value = ''
  sessionInput.value = ''
  sessionError.value = ''
  lastFilledItemId.value = null
}

onUnmounted(() => {
  unmounted = true
  stopAllPolls()
})
</script>

<style scoped>
/* 768px 限宽只在窄屏；电脑布局交给 .u-page--split 的 1120px */
@media (max-width: 1023px) {
  .user-ui .batch-page { max-width: 768px; }
}
/* 本页还在用旧变量名：这里把它们接到用户端配色（--u-*，颜色取自当前皮肤） */
.user-ui .batch-page {
  --primary: var(--u-primary); --soft: var(--u-tint); --muted: var(--u-muted); --ink: var(--u-ink); --ink-2: var(--u-muted);
  --brd: var(--u-line); --err: var(--u-error-ink); --warn: var(--u-warn-ink); --good: var(--u-primary);
}
.text-good { color: var(--good, #16a34a); }
.text-warn { color: var(--warn, #d97706); }
.text-err { color: var(--err, #dc2626); }
.status-pill[data-st='pending_verify'] { background: var(--soft); color: var(--muted); }
.status-pill[data-st='verify_ok'],
.status-pill[data-st='success'] {
  background: color-mix(in srgb, var(--good, #16a34a) 16%, transparent);
  color: var(--good, #16a34a);
}
.status-pill[data-st='verify_fail'],
.status-pill[data-st='failed'] {
  background: color-mix(in srgb, var(--err, #dc2626) 14%, transparent);
  color: var(--err, #dc2626);
}
.status-pill[data-st='waiting_session'],
.status-pill[data-st='unknown'] {
  background: color-mix(in srgb, var(--warn, #d97706) 14%, transparent);
  color: var(--warn, #d97706);
}
.status-pill[data-st='submitting'],
.status-pill[data-st='processing'] {
  background: color-mix(in srgb, var(--primary) 14%, transparent);
  color: var(--primary);
}
</style>
