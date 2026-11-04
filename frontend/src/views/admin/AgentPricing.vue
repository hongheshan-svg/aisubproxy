<template>
  <div class="space-y-4" data-page="agent-pricing">
    <div class="flex items-center justify-between gap-3">
      <h1 class="text-xl font-semibold">批发商品与价格</h1>
      <el-button :loading="loading" @click="loadAll">刷新</el-button>
    </div>
    <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>

    <!-- 等级 -->
    <section class="card p-4 space-y-3">
      <div class="flex items-center justify-between"><h2 class="font-semibold">价格等级</h2><el-button size="small" type="primary" @click="openTier(null)">新增等级</el-button></div>
      <el-table :data="tiers" size="small" stripe empty-text="还没有等级">
        <el-table-column prop="name" label="名称" min-width="140" />
        <el-table-column prop="note" label="备注" min-width="160" show-overflow-tooltip />
        <el-table-column prop="agent_count" label="代理商数" width="100" />
        <el-table-column label="操作" width="140">
          <template #default="{ row }">
            <el-button size="small" link type="primary" @click="openTier(row)">编辑</el-button>
            <el-button size="small" link type="danger" :disabled="row.agent_count > 0" @click="deleteTier(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 商品 -->
    <section class="card p-4 space-y-3">
      <div class="flex items-center justify-between"><h2 class="font-semibold">批发商品</h2><el-button size="small" type="primary" @click="openProduct">新增商品</el-button></div>
      <el-table :data="products" size="small" stripe empty-text="还没有商品">
        <el-table-column prop="product_code" label="编码" min-width="150" class-name="mono" />
        <el-table-column prop="name" label="名称" min-width="150" show-overflow-tooltip />
        <el-table-column label="套餐 / 地区" min-width="130"><template #default="{ row }">{{ row.plan }} / {{ row.region }}</template></el-table-column>
        <el-table-column label="上架" width="80"><template #default="{ row }"><el-tag size="small" :type="row.enabled ? 'success' : 'info'">{{ row.enabled ? '上架' : '下架' }}</el-tag></template></el-table-column>
        <el-table-column prop="sort" label="排序" width="70" />
        <el-table-column label="操作" width="80"><template #default="{ row }"><el-button size="small" link type="primary" @click="editProduct(row)">编辑</el-button></template></el-table-column>
      </el-table>
    </section>

    <!-- 价格矩阵 -->
    <section class="card p-4 space-y-3">
      <h2 class="font-semibold">等级价格（美元 / 张）</h2>
      <p class="text-sm opacity-70">留空或填 0 表示这个等级不卖这个商品。改完统一点保存，整批生效。</p>
      <div class="overflow-x-auto">
        <table class="text-sm" data-testid="price-matrix">
          <thead><tr><th class="p-2 text-left">商品</th><th v-for="t in tiers" :key="t.id" class="p-2 text-left">{{ t.name }}</th></tr></thead>
          <tbody>
            <tr v-for="p in products" :key="p.id" class="border-t">
              <td class="p-2 whitespace-nowrap">{{ p.name }} <span class="opacity-60 mono">{{ p.product_code }}</span><el-tag v-if="!p.enabled" size="small" type="info" class="ml-1">下架</el-tag></td>
              <td v-for="t in tiers" :key="t.id" class="p-1"><el-input v-model="edited[`${t.id}:${p.id}`]" size="small" class="!w-[100px]" placeholder="不卖" /></td>
            </tr>
            <tr v-if="!products.length"><td class="p-3 opacity-70" :colspan="tiers.length + 1">还没有商品</td></tr>
          </tbody>
        </table>
      </div>
      <p v-if="matrixErr" class="text-sm text-red-500" role="alert">{{ matrixErr }}</p>
      <el-button type="primary" :loading="savingMatrix" :disabled="!products.length || !tiers.length" @click="saveMatrix">保存价格</el-button>
    </section>

    <!-- 等级对话框 -->
    <el-dialog v-model="tierOpen" :title="tierForm.id ? '编辑等级' : '新增等级'" width="min(420px, 96vw)" append-to-body :close-on-click-modal="false">
      <div class="space-y-3">
        <label class="block text-sm"><span class="block mb-1">名称</span><el-input v-model="tierForm.name" maxlength="50" /></label>
        <label class="block text-sm"><span class="block mb-1">备注</span><el-input v-model="tierForm.note" maxlength="200" /></label>
        <p v-if="tierErr" class="text-sm text-red-500" role="alert">{{ tierErr }}</p>
      </div>
      <template #footer><el-button @click="tierOpen = false">取消</el-button><el-button type="primary" :loading="tierBusy" @click="saveTier">保存</el-button></template>
    </el-dialog>

    <!-- 商品对话框 -->
    <el-dialog v-model="prodOpen" :title="prodForm.id ? '编辑商品' : '新增商品'" width="min(480px, 96vw)" append-to-body :close-on-click-modal="false">
      <div class="space-y-3">
        <template v-if="!prodForm.id">
          <label class="block text-sm"><span class="block mb-1">套餐 × 地区（已建过的不能再选）</span>
            <el-select v-model="prodForm.combo" class="w-full" placeholder="选择组合" @change="onCombo">
              <el-option v-for="o in options" :key="o.plan + '|' + o.region" :value="o.plan + '|' + o.region" :disabled="o.exists" :label="`${o.plan} · ${o.region}${o.exists ? '（已存在）' : ''}`" />
            </el-select>
          </label>
          <label class="block text-sm"><span class="block mb-1">商品编码（小写字母、数字、连字符，2-48 位，建好后不能改）</span><el-input v-model="prodForm.product_code" maxlength="48" class="mono" /></label>
        </template>
        <div v-else class="text-sm">编码：<b class="mono">{{ prodForm.product_code }}</b>（不能改）</div>
        <label class="block text-sm"><span class="block mb-1">名称</span><el-input v-model="prodForm.name" maxlength="100" /></label>
        <label class="block text-sm"><span class="block mb-1">排序（小的在前）</span><el-input-number v-model="prodForm.sort" :min="0" :max="9999" class="!w-full" /></label>
        <label v-if="prodForm.id" class="flex items-center gap-2 text-sm"><el-switch v-model="prodForm.enabled" />上架（下架后代理商看不到，也不能下单）</label>
        <p v-if="prodErr" class="text-sm text-red-500" role="alert">{{ prodErr }}</p>
      </div>
      <template #footer><el-button @click="prodOpen = false">取消</el-button><el-button type="primary" :loading="prodBusy" @click="saveProduct">保存</el-button></template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { adminApi, centsToUsd } from '../../lib/provider'
import { adminErrorText, matrixChanges, validProductCode, type PriceCell } from '../../lib/agentAdmin'
import { dialog } from '../../lib/dialog'

const loading = ref(false)
const err = ref('')
const tiers = ref<any[]>([])
const products = ref<any[]>([])
const options = ref<any[]>([])
const cells = ref<PriceCell[]>([])
const edited = reactive<Record<string, string>>({})

function fillMatrix(d: any) {
  tiers.value = d.tiers || []
  cells.value = d.cells || []
  for (const k of Object.keys(edited)) delete edited[k]
  for (const t of tiers.value) for (const p of d.products || []) edited[`${t.id}:${p.id}`] = ''
  for (const c of cells.value) if (c.price_cents > 0) edited[`${c.tier_id}:${c.product_id}`] = centsToUsd(c.price_cents)
}

async function loadAll() {
  loading.value = true
  err.value = ''
  const [t, p, m] = await Promise.all([adminApi<any>('/agent-tiers'), adminApi<any>('/agent-products'), adminApi<any>('/agent-prices')])
  loading.value = false
  const bad = [t, p, m].find((r) => !r.ok)
  if (bad) err.value = '读取失败：' + adminErrorText(bad.data, bad.status)
  if (t.ok) tiers.value = t.data.items || []
  if (p.ok) products.value = p.data.items || []
  if (m.ok) { fillMatrix(m.data); products.value = p.ok ? products.value : (m.data.products || []) }
}

// ---------- 价格矩阵 ----------
const matrixErr = ref('')
const savingMatrix = ref(false)
async function saveMatrix() {
  const { cells: changed, errors } = matrixChanges(cells.value, { ...edited })
  if (errors.length) { matrixErr.value = errors.join('；'); return }
  if (!changed.length) { matrixErr.value = ''; dialog.toast('没有改动', 'info'); return }
  savingMatrix.value = true
  const r = await adminApi<any>('/agent-prices', 'PUT', { cells: changed })
  savingMatrix.value = false
  if (!r.ok) { matrixErr.value = adminErrorText(r.data, r.status); return }
  matrixErr.value = ''
  fillMatrix(r.data)
  dialog.toast(`已保存 ${changed.length} 处改动`)
}

// ---------- 等级 ----------
const tierOpen = ref(false)
const tierBusy = ref(false)
const tierErr = ref('')
const tierForm = reactive({ id: 0, name: '', note: '' })
function openTier(row: any) {
  Object.assign(tierForm, row ? { id: row.id, name: row.name, note: row.note } : { id: 0, name: '', note: '' })
  tierErr.value = ''
  tierOpen.value = true
}
async function saveTier() {
  if (!tierForm.name.trim()) { tierErr.value = '请填写名称'; return }
  tierBusy.value = true
  const body = { name: tierForm.name.trim(), note: tierForm.note.trim() }
  const r = tierForm.id ? await adminApi<any>(`/agent-tiers/${tierForm.id}`, 'PUT', body) : await adminApi<any>('/agent-tiers', 'POST', body)
  tierBusy.value = false
  if (!r.ok) { tierErr.value = adminErrorText(r.data, r.status); return }
  tierOpen.value = false
  loadAll()
}
async function deleteTier(row: any) {
  const ok = await dialog.confirm(`删除等级「${row.name}」？这个等级下的价格也会一并删除。`, { title: '删除等级', okText: '删除', danger: true })
  if (!ok) return
  const r = await adminApi<any>(`/agent-tiers/${row.id}`, 'DELETE')
  if (!r.ok) { dialog.toast(adminErrorText(r.data, r.status), 'err'); return }
  dialog.toast('已删除')
  loadAll()
}

// ---------- 商品 ----------
const prodOpen = ref(false)
const prodBusy = ref(false)
const prodErr = ref('')
const prodForm = reactive({ id: 0, combo: '', product_code: '', name: '', sort: 0, enabled: true })
async function openProduct() {
  const r = await adminApi<any>('/agent-products/options')
  if (!r.ok) { dialog.toast('读取可选组合失败：' + adminErrorText(r.data, r.status), 'err'); return }
  options.value = r.data.items || []
  Object.assign(prodForm, { id: 0, combo: '', product_code: '', name: '', sort: 0, enabled: true })
  prodErr.value = ''
  prodOpen.value = true
}
function onCombo(v: string) {
  const o = options.value.find((x) => x.plan + '|' + x.region === v)
  if (o) {
    prodForm.product_code = o.default_code
    if (!prodForm.name) prodForm.name = `${o.plan} ${o.region}`
  }
}
function editProduct(row: any) {
  Object.assign(prodForm, { id: row.id, combo: '', product_code: row.product_code, name: row.name, sort: row.sort, enabled: !!row.enabled })
  prodErr.value = ''
  prodOpen.value = true
}
async function saveProduct() {
  if (!prodForm.name.trim()) { prodErr.value = '请填写名称'; return }
  let r
  prodBusy.value = true
  if (prodForm.id) {
    r = await adminApi<any>(`/agent-products/${prodForm.id}`, 'PUT', { name: prodForm.name.trim(), enabled: prodForm.enabled, sort: prodForm.sort })
  } else {
    const o = options.value.find((x) => x.plan + '|' + x.region === prodForm.combo)
    if (!o) { prodBusy.value = false; prodErr.value = '请选择套餐和地区'; return }
    const code = prodForm.product_code.trim()
    if (!validProductCode(code)) { prodBusy.value = false; prodErr.value = '商品编码只能用小写字母、数字、连字符，2-48 位'; return }
    r = await adminApi<any>('/agent-products', 'POST', { product_code: code, name: prodForm.name.trim(), plan: o.plan, region: o.region, sort: prodForm.sort })
  }
  prodBusy.value = false
  if (!r.ok) { prodErr.value = adminErrorText(r.data, r.status); return }
  prodOpen.value = false
  loadAll()
}

onMounted(loadAll)
</script>
