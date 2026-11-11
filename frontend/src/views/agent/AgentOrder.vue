<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">下单</h1>
    <!-- 最近一笔订单（服务端）：换了浏览器、电脑后本地没有锁定记录，重下前先确认上一笔有没有成功 -->
    <div v-if="latest" class="card p-3 text-sm border-l-4 border-amber-500" data-testid="latest-order">
      <span class="font-semibold">最近一笔订单</span>：
      <router-link :to="`/agent/orders/${latest.id}`" class="underline">#{{ latest.id }}</router-link>
      · {{ fmtBeijing(latest.created_at) }}（北京时间） · {{ latest.product_name }} × {{ latest.quantity }} · {{ fmtUsd(latest.total_cents) }}
    </div>
    <form class="card p-4 space-y-4" @submit.prevent="askConfirm">
      <label class="block">
        <span class="block text-sm mb-1">商品</span>
        <select v-model="productCode" class="input w-full" :disabled="lock.inputsDisabled">
          <option value="" disabled>请选择商品</option>
          <option v-for="p in products" :key="p.product_code" :value="p.product_code" :disabled="!p.ready">
            {{ p.name }} · {{ fmtUsd(p.unit_price_cents) }}{{ p.ready ? '' : '（暂时无法出码）' }}
          </option>
        </select>
      </label>
      <label class="block">
        <span class="block text-sm mb-1">数量（1–200）</span>
        <input v-model="qtyText" :disabled="lock.inputsDisabled" class="input w-full" type="number" inputmode="numeric" min="1" max="200" step="1" />
      </label>
      <label class="block">
        <span class="block text-sm mb-1">备注（可选，最多 200 字）</span>
        <input v-model="note" :disabled="lock.inputsDisabled" class="input w-full" type="text" maxlength="200" />
      </label>
      <div v-if="product && qty" class="text-sm">
        合计 {{ fmtUsd(preview.totalCents) }}，下单后余额 {{ fmtUsd(preview.afterCents) }}
        <span v-if="!preview.enough" class="text-red-500">（余额不足，还差 {{ fmtUsd(-preview.afterCents) }}）</span>
      </div>
      <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
      <p v-if="notice" class="text-sm text-amber-600" role="status">{{ notice }}</p>
      <div v-if="lock.unknown" class="space-y-2">
        <p class="text-sm text-red-500" role="alert">上一笔订单结果未知，请原样重试以取回结果</p>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn-primary" :disabled="busy || !ready" @click="submit">原样重试</button>
          <button type="button" class="btn-ghost" :disabled="busy" @click="abandon">放弃并重新下单</button>
        </div>
      </div>
      <button v-else class="btn-primary w-full sm:w-auto" type="submit" :disabled="busy || confirming || !ready || !canSubmit">下单</button>
    </form>

    <div v-if="confirming" class="card p-4 space-y-3 border-2">
      <div>确认下单：{{ product?.name }} × {{ qty }}，总价 <b>{{ fmtUsd(preview.totalCents) }}</b>，下单后余额 <b>{{ fmtUsd(preview.afterCents) }}</b>。</div>
      <p v-if="!preview.enough" class="text-sm text-red-500">余额不足，还差 {{ fmtUsd(-preview.afterCents) }}</p>
      <p v-if="dupWarning" class="text-sm font-semibold text-red-500" role="alert">{{ dupWarning }}</p>
      <div class="flex gap-2">
        <button type="button" class="btn-primary" :disabled="busy || !preview.enough" @click="submit">{{ busy ? '提交中…' : '确认下单' }}</button>
        <button type="button" class="btn-ghost" :disabled="busy" @click="confirming = false">取消</button>
      </div>
    </div>

    <div v-if="result" class="card p-4 space-y-3">
      <p v-if="result.replayed" class="text-sm text-amber-600">这是之前同一笔订单的结果</p>
      <div class="text-sm">订单 <router-link :to="`/agent/orders/${result.order.id}`" class="underline">#{{ result.order.id }}</router-link>，共 {{ result.codes.length }} 张码（仅在本页显示，离开后请到订单详情重新导出）</div>
      <div class="flex flex-wrap gap-2">
        <button type="button" class="btn-ghost" @click="copyAll">复制全部</button>
        <button type="button" class="btn-ghost" @click="downloadTxt">下载 .txt</button>
      </div>
      <p v-if="skipped" class="text-sm text-amber-600">有 {{ skipped }} 张无法取回，请联系管理员</p>
      <textarea class="input w-full font-mono text-xs" rows="8" readonly :value="txt.text"></textarea>
      <AgentOrderCodes :codes="result.codes" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { codesTxt, downloadText } from '../../lib/agentExport'
import {
  keepPendingOnRateLimit, orderFailureKind, NOTE_MAX, orderLockState, orderPreview, orderRetry, pickLatestOrder, recentOrderWarning, samePendingOrder, validQuantity,
  type PendingOrder, type RecentOrder,
} from '../../lib/agentViews'
import { ORDER_RETRY_KEY } from '../../lib/retryLock'
import { IdempotencyLockedError } from '../../lib/idempotency'
import { fmtBeijing } from '../../lib/mwwebhook'
import { copyToClipboard } from '../../lib/clipboard'
import { dialog } from '../../lib/dialog'
import AgentOrderCodes from '../../components/AgentOrderCodes.vue'

const products = ref<any[]>([])
const balance = ref(0)
// 结果未知的订单（lib/agentViews 的 orderRetry，落 localStorage）：核对当前代理商后才恢复内容并锁定
const pending = ref<PendingOrder | null>(null)
const productCode = ref('')
const qtyText = ref('1')
const note = ref('')
// 已用 /me 核对当前代理商：之前不许提交，免得把上一个账号的待重试订单拿来「原样重试」
const ready = ref(false)
const latest = ref<RecentOrder | null>(null)
const dupWarning = ref('')
const busy = ref(false)
const confirming = ref(false)
const err = ref('')
const notice = ref('')
// 结果里的码只放内存，不写 localStorage / sessionStorage
const result = ref<any>(null)

const lock = computed(() => orderLockState({ busy: busy.value, confirming: confirming.value, hasPending: !!pending.value, ready: ready.value }))
const product = computed(() => products.value.find((p) => p.product_code === productCode.value))
const qty = computed(() => validQuantity(qtyText.value))
const preview = computed(() => orderPreview(product.value?.unit_price_cents || 0, qty.value || 0, balance.value))
const canSubmit = computed(() => !!product.value?.ready && !!qty.value && note.value.length <= NOTE_MAX)
const txt = computed(() => codesTxt(result.value?.codes || []))
const skipped = computed(() => txt.value.skipped)

// 显示待重试的订单：有就恢复内容并锁定；没有了（别处已处理完）只解锁，表单内容留着方便核对
function showPending(p: PendingOrder | null) {
  pending.value = p
  if (p) { productCode.value = p.product_code; qtyText.value = String(p.quantity); note.value = p.note }
}

const SETTLED_ELSEWHERE = '这笔订单已在别的页面处理完（成功或已放弃），请先到「订单」核对，再决定是否重新下单'
const LOCKED_ELSEWHERE = '有一笔订单结果未知（可能是在别的页面下的），请先原样重试或放弃'
const OTHER_ACCOUNT = '这个浏览器里另一个代理商账号有一笔结果未知的订单，请刷新页面，确认当前登录的账号后再下单'

async function loadAll() {
  try {
    const [p, m] = await Promise.all([agentApi('/products'), agentApi('/me')])
    if (p.ok) products.value = p.data.items || []
    else err.value = agentErrorText(p.data, p.status)
    if (m.ok) {
      balance.value = Number(m.data.balance_cents) || 0
      // 核对身份：存储里是别的代理商的待重试订单就丢弃；请求在途时不动表单，等它自己结算
      const cur = orderRetry.bind(String(m.data?.username || ''))
      if (!busy.value) showPending(cur)
      ready.value = orderRetry.bound
    } else if (!err.value) err.value = agentErrorText(m.data, m.status)
    if (productCode.value && !pending.value && !product.value?.ready) productCode.value = ''
  } catch { err.value = '网络异常，请稍后再试' }
}
// 最近一笔订单：取不到就不显示，不影响下单
async function loadLatest() {
  try {
    const r = await agentApi('/orders?page=1&page_size=1')
    latest.value = r.ok ? pickLatestOrder(r.data) : null
  } catch { latest.value = null }
}
// 别的标签页锁定或处理完一笔：实时刷新锁定界面（提交前还会再重读一次）
function onStorage(e: StorageEvent) {
  if (busy.value || !ready.value || (e.key !== null && e.key !== ORDER_RETRY_KEY)) return
  const rc = orderRetry.recheck(pending.value)
  if (rc.kind === 'go') return
  confirming.value = false
  showPending(rc.kind === 'locked' ? rc.pending : null)
  if (rc.kind === 'settled') { err.value = ''; notice.value = SETTLED_ELSEWHERE; loadLatest() }
}
onMounted(() => {
  loadAll()
  loadLatest()
  window.addEventListener('storage', onStorage)
})
onBeforeUnmount(() => window.removeEventListener('storage', onStorage))

function askConfirm() {
  err.value = ''; notice.value = ''
  if (!canSubmit.value) { err.value = '请选择可下单的商品，数量填 1–200 的整数'; return }
  dupWarning.value = recentOrderWarning(latest.value, product.value.product_code, qty.value!, Math.floor(Date.now() / 1000))
  confirming.value = true
}

async function abandon() {
  if (busy.value) return
  const shown = pending.value
  if (!shown) return
  const ok = await dialog.confirm('放弃后将用新请求号重新下单；如果上一笔其实已成功，会被重复扣款。确定放弃吗？', {
    title: '放弃结果未知的订单', okText: '放弃并重新下单', danger: true,
  })
  if (!ok) return
  // 确认框是异步的，等待期间状态可能变了：这页发起了原样重试（请求在路上），或页面上显示的已不是这笔，都不放弃
  if (busy.value || !samePendingOrder(pending.value, shown)) return
  // 再重读存储：别处已处理完、或别的标签页放弃后锁定了另一笔，都不清（只清显示的这笔的请求号）
  const rc = orderRetry.abandonShown(shown)
  if (rc.kind === 'locked') { showPending(rc.pending); notice.value = ''; err.value = LOCKED_ELSEWHERE; return }
  if (rc.kind === 'settled') { showPending(null); err.value = ''; notice.value = SETTLED_ELSEWHERE; loadLatest(); return }
  pending.value = null; err.value = ''
  loadLatest()
}

async function submit() {
  if (busy.value || !ready.value) return
  const fail = (text: string) => { err.value = text; confirming.value = false }
  // 提交前重读锁定（多个标签页共用）：别处刚锁定了一笔就改显示那笔；页面上锁定的那笔别处已处理完就不许再发
  const rc = orderRetry.recheck(pending.value)
  if (rc.kind === 'locked') { showPending(rc.pending); notice.value = ''; fail(LOCKED_ELSEWHERE); return }
  if (rc.kind === 'settled') { showPending(null); fail(''); notice.value = SETTLED_ELSEWHERE; loadLatest(); return }
  const wasRetry = !!pending.value
  let p = pending.value
  if (!p) {
    if (!product.value || !qty.value) return
    p = { product_code: product.value.product_code, quantity: qty.value, note: note.value, expected_unit_price_cents: product.value.unit_price_cents }
  }
  busy.value = true; err.value = ''; notice.value = ''
  // 请求发出前先锁定落盘（多个标签页互斥、写后读回校验）：请求还在路上时刷新、关标签页，回来也沿用同一个请求号
  let key: string
  try { key = await orderRetry.acquire(p) } catch (e) {
    busy.value = false
    if (!(e instanceof IdempotencyLockedError)) { fail('页面出错，请刷新后重试'); return }
    // 别的标签页同一瞬间锁定了另一笔（或同一浏览器里另一个账号有未决订单）：这一笔没发
    const cur = orderRetry.refresh()
    showPending(cur)
    fail(cur ? LOCKED_ELSEWHERE : OTHER_ACCOUNT)
    return
  }
  pending.value = p
  try {
    const r = await agentApi('/orders', 'POST', { ...p, request_no: key, expected_unit_price_cents: p.expected_unit_price_cents })
    if (r.ok) {
      result.value = { order: r.data.order, codes: r.data.codes || [], replayed: !!r.data.replayed }
      orderRetry.settle(key); pending.value = null; confirming.value = false
      loadAll()
      loadLatest()
    } else if (orderFailureKind(r.status, r.data?.error || '') === 'definite') {
      orderRetry.settle(key); pending.value = null
      if (r.data.error === 'price_changed') { confirming.value = false; notice.value = '价格已变化，已刷新'; await loadAll() }
      else fail(agentErrorText(r.data, r.status))
    } else if (orderFailureKind(r.status, r.data?.error || '') === 'rate_limited') {
      // 限流时后端没处理订单：首次提交解锁（下一次换新请求号也不会重复）；原样重试遇限流继续锁定
      if (!keepPendingOnRateLimit(wasRetry)) { orderRetry.settle(key); pending.value = null }
      fail(wasRetry ? '操作太频繁，请稍后再原样重试' : agentErrorText(r.data, r.status))
    } else {
      // 503、未知错误：结果未知，保持锁定（发出前已落盘）
      fail(agentErrorText(r.data, r.status))
    }
  } catch {
    fail('网络异常，结果未知')
  } finally { busy.value = false }
}

async function copyAll() {
  const ok = await copyToClipboard(txt.value.text)
  notice.value = ok ? '已复制' : '复制失败，请手动选择文本'
}
function downloadTxt() {
  downloadText(`order-${result.value.order.id}.txt`, txt.value.text, 'text/plain')
}
</script>
