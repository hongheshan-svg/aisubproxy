<template>
  <div class="space-y-4">
    <AdminPageHeader
      title="MarsWallex 选卡规则"
      description="只对 MarsWallex 供卡的兑换生效：有哪些卡头、各套餐按什么顺序用卡、什么时候自动停用"
      class="!mb-0"
    />

    <CardHeadsPanel provider="marswallex" @loaded="(items) => (heads = items)" />

    <div class="card text-sm text-muted space-y-1">
      <div>· 只选<strong>美元卡</strong>：注资按美元计，其他币种的卡不会被选中。</div>
      <div>
        · 支持 3DS 的卡，付款时可能要输入验证码：要在
        <router-link to="/ops/marswallex" class="text-primary">MarsWallex 接入配置</router-link>
        里配通 Webhook，系统才能收到验证码并自动填写。
      </div>
    </div>

    <div v-if="sel" class="card">
      <h2 class="text-xl font-bold text-ink">各套餐的卡产品优先级</h2>
      <p class="text-sm text-muted mt-1 mb-3">
        兑换某个套餐时，按从上到下的顺序找有库存的卡；套餐没单独配置时用「default」。
      </p>
      <div v-for="key in planKeys" :key="key" class="plan-box">
        <div class="flex items-center gap-2 mb-2">
          <b class="mono">{{ key }}</b>
          <span v-if="key === 'default'" class="text-xs text-subtle">兜底</span>
          <el-button v-if="key !== 'default'" size="small" text type="danger" @click="removePlan(key)">删除套餐</el-button>
        </div>
        <div v-for="(pid, i) in sel.plan_products[key]" :key="pid" class="flex items-center gap-2 mb-1">
          <span class="text-xs text-muted w-5">{{ i + 1 }}.</span>
          <span class="flex-1 text-sm min-w-0 break-all">{{ productLabel(pid) }}</span>
          <el-button size="small" text :disabled="i === 0" @click="moveProduct(key, i, -1)">上移</el-button>
          <el-button size="small" text :disabled="i === sel.plan_products[key].length - 1" @click="moveProduct(key, i, 1)">下移</el-button>
          <el-button size="small" text type="danger" @click="sel.plan_products[key].splice(i, 1)">移除</el-button>
        </div>
        <el-select :model-value="''" placeholder="添加产品" class="add-select" size="small" @change="(v: string) => addProduct(key, v)">
          <el-option
            v-for="p in productOptions.filter((x) => !sel!.plan_products[key].includes(x.product_id))"
            :key="p.product_id"
            :label="productLabel(p.product_id)"
            :value="p.product_id"
          />
        </el-select>
      </div>
      <div class="flex flex-wrap items-center gap-2 mt-2">
        <el-input v-model="newPlanKey" size="small" placeholder="新增套餐 key" style="width: 200px" class="mono" />
        <el-button size="small" @click="addPlan">添加套餐</el-button>
      </div>

      <h2 class="text-xl font-bold text-ink mt-6">停用规则</h2>
      <p class="text-sm text-muted mt-1 mb-3">
        在统计窗口内，某产品在某套餐上失败达到阈值时，开启自动暂停后这个产品会暂停用于该套餐；单张卡失败达到阈值时停用这张卡。
      </p>
      <el-form label-position="top" @submit.prevent>
        <div class="grid-form">
          <el-form-item label="单卡最大使用次数（1-100）">
            <el-input-number v-model="sel.max_uses_per_card" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="健康度统计窗口（小时，1-720）">
            <el-input-number v-model="sel.health_window_hours" :min="1" :max="720" />
          </el-form-item>
          <el-form-item label="产品失败阈值（1-100）">
            <el-input-number v-model="sel.product_fail_threshold" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="单卡失败阈值（1-100）">
            <el-input-number v-model="sel.card_fail_threshold" :min="1" :max="100" />
          </el-form-item>
          <el-form-item label="自动暂停">
            <el-switch v-model="sel.auto_suspend" />
          </el-form-item>
        </div>
      </el-form>

      <p v-if="saveError" class="field-error">{{ saveError }}</p>
      <div class="flex gap-2">
        <el-button type="primary" :loading="saving" @click="save">保存选卡规则</el-button>
        <el-button @click="load">重新加载</el-button>
      </div>
    </div>

    <div class="card">
      <div class="flex items-center justify-between mb-2">
        <h2 class="text-xl font-bold text-ink">健康度</h2>
        <el-button size="small" :loading="healthLoading" @click="loadHealth">刷新</el-button>
      </div>
      <el-table :data="health" size="small" border empty-text="暂无数据" v-loading="healthLoading">
        <el-table-column label="产品" min-width="220">
          <template #default="{ row }">{{ productLabel(row.product_id) }}</template>
        </el-table-column>
        <el-table-column prop="plan_key" label="套餐" width="110" />
        <el-table-column label="24h 成功/失败" width="130">
          <template #default="{ row }">{{ row.success_24h }} / {{ row.failed_24h }}</template>
        </el-table-column>
        <el-table-column label="7d 成功/失败" width="130">
          <template #default="{ row }">{{ row.success_7d }} / {{ row.failed_7d }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag size="small" :type="row.suspended ? 'danger' : 'success'">{{ row.suspended ? '已暂停' : '正常' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="reason" label="原因" min-width="160" show-overflow-tooltip />
        <el-table-column label="暂停时间" min-width="150">
          <template #default="{ row }">{{ fmtTs(row.suspended_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90">
          <template #default="{ row }">
            <el-button v-if="row.suspended" size="small" type="success" plain @click="resumeProduct(row)">恢复</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import CardHeadsPanel from './components/CardHeadsPanel.vue'
import { dialog } from '../../lib/dialog'
import { mapApiError } from '../../lib/errors'
import { errText, fmtTs, poolApi as api, splitSelectionSaved, usedUpNotice, type Selection, type SelectionSaved } from '../../lib/cardpool'
import { cardHeadOptionLabel, cardHeadsByProduct, type CardHead } from '../../lib/cardheads'

interface Product { product_id: string; product_name: string; card_label: string }
interface Health {
  product_id: string; plan_key: string; success_24h: number; failed_24h: number
  success_7d: number; failed_7d: number; suspended: boolean; suspended_at: number | null; reason: string
}

const sel = ref<Selection | null>(null)
const saving = ref(false)
const saveError = ref('')
const newPlanKey = ref('')
const products = ref<Product[]>([])
const heads = ref<CardHead[]>([])
const health = ref<Health[]>([])
const healthLoading = ref(false)

const headsById = computed(() => cardHeadsByProduct(heads.value))

// 可选产品：以 MarsWallex 卡产品接口为准；接口读不到时用已同步的卡头
const productOptions = computed<Product[]>(() =>
  products.value.length
    ? products.value
    : heads.value.filter((h) => h.listed).map((h) => ({ product_id: h.product_id, product_name: h.name, card_label: h.network })),
)

// 产品显示为「卡头 · 3DS … · 产品名」；没有卡头信息时为「产品名（产品 ID）」
function productLabel(id: string): string {
  const h = headsById.value[id]
  if (h) return h.name && h.card_heads.length ? `${cardHeadOptionLabel(h)} · ${h.name}` : `${cardHeadOptionLabel(h)}（${id}）`
  const p = products.value.find((x) => x.product_id === id)
  return `${p?.product_name || p?.card_label || id}（${id}）`
}

const planKeys = computed(() => {
  const keys = Object.keys(sel.value?.plan_products || {})
  return keys.sort((a, b) => (a === 'default' ? -1 : b === 'default' ? 1 : a.localeCompare(b)))
})

function addProduct(key: string, pid: string) {
  if (pid && sel.value && !sel.value.plan_products[key].includes(pid)) sel.value.plan_products[key].push(pid)
}
function moveProduct(key: string, i: number, d: number) {
  const list = sel.value!.plan_products[key]
  const j = i + d
  if (j < 0 || j >= list.length) return
  ;[list[i], list[j]] = [list[j], list[i]]
}
function addPlan() {
  const k = newPlanKey.value.trim()
  if (!k || !sel.value) return
  if (!sel.value.plan_products[k]) sel.value.plan_products[k] = []
  newPlanKey.value = ''
}
function removePlan(key: string) {
  if (sel.value) delete sel.value.plan_products[key]
}

async function load() {
  saveError.value = ''
  const r = await api<Selection>('/selection')
  if (!r.ok) return dialog.toast(errText(r, '加载选卡规则失败'), 'err')
  r.data.plan_products = r.data.plan_products || {}
  sel.value = r.data
}

async function loadProducts() {
  const r = await api<{ items: Product[] }>('/products')
  if (r.ok) products.value = r.data.items || []
  else if (!mapApiError(errText(r, '')).config) dialog.toast(errText(r, '加载产品失败'), 'warn')
}

async function save() {
  if (!sel.value) return
  saveError.value = ''
  saving.value = true
  try {
    const r = await api<SelectionSaved>('/selection', 'PUT', sel.value)
    if (!r.ok) {
      saveError.value = errText(r, '保存失败')
      return
    }
    // 调低单卡最大使用次数时，后端带回将不再可用的卡数：只提示，由管理员手动处理
    const { selection, unusable } = splitSelectionSaved(r.data)
    sel.value = selection
    dialog.toast('选卡规则已保存', 'ok')
    if (unusable > 0) await dialog.alert(usedUpNotice(unusable), { title: '有卡将不再可用' })
  } finally {
    saving.value = false
  }
}

async function loadHealth() {
  healthLoading.value = true
  try {
    const r = await api<{ items: Health[] }>('/health')
    if (!r.ok) return dialog.toast(errText(r, '加载健康度失败'), 'err')
    health.value = r.data.items
  } finally {
    healthLoading.value = false
  }
}
async function resumeProduct(row: Health) {
  const ok = await dialog.confirm(`恢复产品 ${productLabel(row.product_id)}（套餐 ${row.plan_key || '—'}）？`, { title: '恢复', okText: '恢复' })
  if (!ok) return
  const r = await api('/health/resume', 'POST', { product_id: row.product_id, plan_key: row.plan_key })
  if (!r.ok) return dialog.toast(errText(r, '恢复失败'), 'err')
  dialog.toast('已恢复', 'ok')
  loadHealth()
}

onMounted(() => {
  load()
  loadProducts()
  loadHealth()
})
</script>

<style scoped>
.field-error {
  margin-bottom: 8px;
  font-size: 13px;
  color: #dc2626;
}
.grid-form {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  column-gap: 16px;
}
.plan-box {
  padding: 10px 12px;
  margin-bottom: 8px;
  border: 1px solid rgba(127, 127, 127, 0.25);
  border-radius: 8px;
}
.add-select {
  width: 100%;
  max-width: 420px;
}
</style>
