<template>
  <div class="space-y-4">
    <h1 class="text-xl font-semibold">下单</h1>
    <form class="card p-4 space-y-4" @submit.prevent="askConfirm">
      <label class="block">
        <span class="block text-sm mb-1">商品</span>
        <select v-model="productCode" class="input w-full">
          <option value="" disabled>请选择商品</option>
          <option v-for="p in products" :key="p.product_code" :value="p.product_code" :disabled="!p.ready">
            {{ p.name }} · {{ fmtUsd(p.unit_price_cents) }}{{ p.ready ? '' : '（暂时无法出码）' }}
          </option>
        </select>
      </label>
      <label class="block">
        <span class="block text-sm mb-1">数量（1–200）</span>
        <input v-model="qtyText" class="input w-full" type="number" inputmode="numeric" min="1" max="200" step="1" />
      </label>
      <label class="block">
        <span class="block text-sm mb-1">备注（可选，最多 200 字）</span>
        <input v-model="note" class="input w-full" type="text" maxlength="200" />
      </label>
      <div v-if="product && qty" class="text-sm">
        合计 {{ fmtUsd(preview.totalCents) }}，下单后余额 {{ fmtUsd(preview.afterCents) }}
        <span v-if="!preview.enough" class="text-red-500">（余额不足，还差 {{ fmtUsd(-preview.afterCents) }}）</span>
      </div>
      <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
      <p v-if="notice" class="text-sm text-amber-600" role="status">{{ notice }}</p>
      <button class="btn-primary w-full sm:w-auto" type="submit" :disabled="busy || !canSubmit">下单</button>
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
import { NOTE_MAX, orderPreview, validQuantity } from '../../lib/agentViews'
import { IdempotencyKeeper } from '../../lib/idempotency'
import { copyToClipboard } from '../../lib/clipboard'

const keeper = new IdempotencyKeeper()
const products = ref<any[]>([])
const balance = ref(0)
const productCode = ref('')
const qtyText = ref('1')
const note = ref('')
const busy = ref(false)
const confirming = ref(false)
const err = ref('')
const notice = ref('')
// 结果里的码只放内存，不写 localStorage / sessionStorage
const result = ref<any>(null)

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
    if (productCode.value && !product.value?.ready) productCode.value = ''
  } catch { err.value = '网络异常，请稍后再试' }
}
onMounted(loadAll)

function askConfirm() {
  err.value = ''; notice.value = ''
  if (!canSubmit.value) { err.value = '请选择可下单的商品，数量填 1–200 的整数'; return }
  confirming.value = true
}

async function submit() {
  if (!product.value || !qty.value) return
  busy.value = true; err.value = ''; notice.value = ''
  const payload = { product_code: product.value.product_code, quantity: qty.value, note: note.value }
  try {
    const r = await agentApi('/orders', 'POST', {
      ...payload,
      request_no: keeper.keyFor(payload),
      expected_unit_price_cents: product.value.unit_price_cents,
    })
    if (r.ok) {
      result.value = { order: r.data.order, codes: r.data.codes || [], replayed: !!r.data.replayed }
      keeper.done()
      confirming.value = false
      loadAll()
    } else if (r.data?.error === 'price_changed') {
      confirming.value = false
      notice.value = '价格已变化，已刷新'
      await loadAll()
    } else {
      err.value = agentErrorText(r.data, r.status)
      // 余额不足、参数错误等明确失败：不会再有同一请求的结果，换新请求号
      if (['insufficient_balance', 'invalid_request', 'product_unavailable', 'product_not_ready'].includes(r.data?.error)) { keeper.done(); confirming.value = false }
    }
  } catch {
    err.value = '网络异常，结果未知：请直接再点确认（沿用同一请求号，不会重复扣款）'
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
