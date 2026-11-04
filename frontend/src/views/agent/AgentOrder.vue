<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">下单</h1>
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
          <button type="button" class="btn-primary" :disabled="busy" @click="submit">原样重试</button>
          <button type="button" class="btn-ghost" :disabled="busy" @click="abandon">放弃并重新下单</button>
        </div>
      </div>
      <button v-else class="btn-primary w-full sm:w-auto" type="submit" :disabled="busy || confirming || !canSubmit">下单</button>
    </form>

    <div v-if="confirming" class="card p-4 space-y-3 border-2">
      <div>确认下单：{{ product?.name }} × {{ qty }}，总价 <b>{{ fmtUsd(preview.totalCents) }}</b>，下单后余额 <b>{{ fmtUsd(preview.afterCents) }}</b>。</div>
      <p v-if="!preview.enough" class="text-sm text-red-500">余额不足，还差 {{ fmtUsd(-preview.afterCents) }}</p>
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
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { codesTxt, downloadText } from '../../lib/agentExport'
import { keepPendingOnRateLimit, orderFailureKind, NOTE_MAX, orderKeeper as keeper, orderLockState, orderPreview, orderRetry, validQuantity, type PendingOrder } from '../../lib/agentViews'
import { copyToClipboard } from '../../lib/clipboard'

const products = ref<any[]>([])
const balance = ref(0)
// 回到页面时若有待重试的订单，恢复内容并锁定
const pending = ref<PendingOrder | null>(orderRetry.pending)
const productCode = ref(pending.value?.product_code || '')
const qtyText = ref(String(pending.value?.quantity ?? 1))
const note = ref(pending.value?.note || '')
const busy = ref(false)
const confirming = ref(false)
const err = ref('')
const notice = ref('')
// 结果里的码只放内存，不写 localStorage / sessionStorage
const result = ref<any>(null)

const lock = computed(() => orderLockState({ busy: busy.value, confirming: confirming.value, hasPending: !!pending.value }))
const product = computed(() => products.value.find((p) => p.product_code === productCode.value))
const qty = computed(() => validQuantity(qtyText.value))
const preview = computed(() => orderPreview(product.value?.unit_price_cents || 0, qty.value || 0, balance.value))
const canSubmit = computed(() => !!product.value?.ready && !!qty.value && note.value.length <= NOTE_MAX)
const txt = computed(() => codesTxt(result.value?.codes || []))
const skipped = computed(() => txt.value.skipped)

async function loadAll() {
  try {
    const [p, m] = await Promise.all([agentApi('/products'), agentApi('/me')])
    if (p.ok) products.value = p.data.items || []
    else err.value = agentErrorText(p.data, p.status)
    if (m.ok) balance.value = Number(m.data.balance_cents) || 0
    if (productCode.value && !pending.value && !product.value?.ready) productCode.value = ''
  } catch { err.value = '网络异常，请稍后再试' }
}
onMounted(loadAll)

function askConfirm() {
  err.value = ''; notice.value = ''
  if (!canSubmit.value) { err.value = '请选择可下单的商品，数量填 1–200 的整数'; return }
  confirming.value = true
}

function setPending(p: PendingOrder | null) { pending.value = p; orderRetry.pending = p }

function abandon() {
  if (!window.confirm('放弃后将用新请求号重新下单；如果上一笔其实已成功，会被重复扣款。确定放弃吗？')) return
  keeper.done(); setPending(null); err.value = ''
}

async function submit() {
  if (busy.value) return
  const wasRetry = !!pending.value
  let p = pending.value
  if (!p) {
    if (!product.value || !qty.value) return
    p = { product_code: product.value.product_code, quantity: qty.value, note: note.value, expected_unit_price_cents: product.value.unit_price_cents }
  }
  busy.value = true; err.value = ''; notice.value = ''
  // 请求发出前先记下内容：中途切走页面也能原样重试
  setPending(p)
  const key = keeper.keyFor({ product_code: p.product_code, quantity: p.quantity, note: p.note })
  const fail = (text: string) => { err.value = text; confirming.value = false }
  try {
    const r = await agentApi('/orders', 'POST', { ...p, request_no: key, expected_unit_price_cents: p.expected_unit_price_cents })
    if (r.ok) {
      result.value = { order: r.data.order, codes: r.data.codes || [], replayed: !!r.data.replayed }
      keeper.done(); setPending(null); confirming.value = false
      loadAll()
    } else if (orderFailureKind(r.status, r.data?.error || '') === 'definite') {
      keeper.done(); setPending(null)
      if (r.data.error === 'price_changed') { confirming.value = false; notice.value = '价格已变化，已刷新'; await loadAll() }
      else fail(agentErrorText(r.data, r.status))
    } else if (orderFailureKind(r.status, r.data?.error || '') === 'rate_limited') {
      // 限流时后端没处理订单：保留请求号，不锁表单
      if (!keepPendingOnRateLimit(wasRetry)) setPending(null)
      fail(wasRetry ? '操作太频繁，请稍后再原样重试' : agentErrorText(r.data, r.status))
    } else {
      // 503、未知错误：结果未知，保留请求号并锁定
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
