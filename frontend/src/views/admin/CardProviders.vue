<template>
  <div class="space-y-4">
    <AdminPageHeader title="卡服务商" description="按产品选择新发的 CDK 由哪家卡服务商处理，并查看各家是否就绪。" class="!mb-0" />

    <div class="alert alert-info text-sm">
      「卡台选卡规则」管的是 ZovoCard 内部用哪个卡头，和这里的发码服务商不是一回事。
      <router-link to="/ops/card-selection" class="underline">去看卡台选卡规则</router-link>
    </div>

    <ApiErrorNotice :message="loadError" />
    <el-button v-if="loadError" size="small" :loading="loading" @click="load">重试</el-button>

    <div v-for="w in reg?.warnings || []" :key="w" class="alert alert-warning text-sm" data-testid="registry-warning">{{ w }}</div>

    <section v-if="reg" class="card space-y-4" data-testid="config-section">
      <div class="space-y-2">
        <h2 class="text-base font-semibold text-ink">全局默认</h2>
        <p class="text-sm text-muted">没有单独选服务商的产品，都用这一家。只影响之后新发的码，已发的码、在途单都不变。</p>
        <el-radio-group v-model="draftDefault" :disabled="saving || loading">
          <el-radio-button v-for="p in reg.providers" :key="p.name" :value="p.name">{{ p.display_name || p.name }}</el-radio-button>
        </el-radio-group>
        <span v-if="loading" class="text-sm text-muted ml-2">读取中…</span>
      </div>

      <div class="space-y-2">
        <h2 class="text-base font-semibold text-ink">按产品选择服务商</h2>
        <p class="text-sm text-muted">每个产品可以单独指定一家；选「跟随全局默认」就用上面的默认。服务商不支持的档位发码会被直接拒绝，不会自动改用另一家。</p>
        <ProductProviderTable v-model="draftByProduct" :reg="reg" :default-name="draftDefault" :disabled="saving || loading" />
      </div>

      <div class="flex flex-wrap items-center gap-3">
        <el-button type="primary" :loading="saving" :disabled="!dirty || loading" data-testid="save" @click="save">保存</el-button>
        <el-button v-if="dirty" :disabled="saving" @click="resetDraft">放弃修改</el-button>
        <span v-if="dirty" class="text-xs text-muted">有未保存的修改</span>
      </div>

      <div v-if="notReady.length" class="alert alert-warning text-sm" role="alert" data-testid="not-ready">
        已保存，但下面被选用的服务商目前未就绪，发码会被拒绝：
        <div v-for="n in notReady" :key="n.provider" class="mt-1">
          <b>{{ providerLabel(n.provider) }}</b>
          <ul class="list-disc pl-5"><li v-for="p in n.problems" :key="p">{{ p }}</li></ul>
        </div>
      </div>
    </section>

    <section class="grid gap-4 lg:grid-cols-2">
      <ProviderCard v-for="p in reg?.providers || []" :key="p.name" class="min-w-0" :info="p" :readiness="rd?.providers?.[p.name]" :loading="loading" />
    </section>

    <section class="card space-y-3">
      <h2 class="text-base font-semibold text-ink">能力矩阵</h2>
      <p class="text-sm text-muted">哪家服务商支持哪个产品和档位。不支持的组合发码会被直接拒绝，不会自动改用另一家。</p>
      <el-table :data="rd?.capabilities?.length ? rd.capabilities : (reg?.capabilities || [])" size="small" border :empty-text="reg ? '没有数据' : '读取失败或读取中'">
        <el-table-column prop="product" label="产品" width="110" />
        <el-table-column label="档位" min-width="160">
          <template #default="{ row }">{{ row.plan }}<span v-if="row.comment" class="text-xs text-muted ml-1">{{ row.comment }}</span></template>
        </el-table-column>
        <el-table-column v-for="p in reg?.providers || []" :key="p.name" :label="p.display_name || p.name" min-width="150">
          <template #default="{ row }">
            <span :class="row.support?.[p.name] ? 'text-ok' : 'text-muted'">{{ row.support?.[p.name] ? '支持' : '不支持' }}</span>
            <el-tag v-if="row.support?.[p.name] && row.notes?.[p.name]" size="small" type="warning" class="ml-1">{{ row.notes[p.name] }}</el-tag>
          </template>
        </el-table-column>
      </el-table>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onServerPrefetch, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import ApiErrorNotice from '../../components/ApiErrorNotice.vue'
import ProviderCard from './components/ProviderCard.vue'
import ProductProviderTable from './components/ProductProviderTable.vue'
import { dialog } from '../../lib/dialog'
import { errText } from '../../lib/cardpool'
import {
  adminApi, loadRegistry, providerLabel, registry, setRegistry,
  type CapabilityRow, type NotReady, type ProviderReadiness,
} from '../../lib/provider'

interface Readiness {
  default: string
  providers: Record<string, ProviderReadiness>
  capabilities: CapabilityRow[]
}

const loading = ref(false)
const loadError = ref('')
const saving = ref(false)
const reg = registry
const rd = ref<Readiness | null>(null)
const notReady = ref<NotReady[]>([])
const draftDefault = ref('')
const draftByProduct = ref<Record<string, string>>({})

/** 把每个产品都补成一个值（空 = 跟随全局默认），便于和草稿比较与提交。 */
function fullByProduct(by: Record<string, string> | undefined): Record<string, string> {
  const out: Record<string, string> = {}
  for (const p of reg.value?.products || []) out[p.key] = by?.[p.key] || ''
  return out
}

function resetDraft() {
  draftDefault.value = reg.value?.default || ''
  draftByProduct.value = fullByProduct(reg.value?.by_product)
}

const dirty = computed(() => {
  if (!reg.value) return false
  if (draftDefault.value !== reg.value.default) return true
  const saved = fullByProduct(reg.value.by_product)
  return Object.keys(saved).some((k) => (draftByProduct.value[k] || '') !== saved[k])
})

async function load() {
  loading.value = true
  loadError.value = ''
  const [g, r] = await Promise.all([loadRegistry(true), adminApi<Readiness>('/providers/readiness')])
  loading.value = false
  if (!g.ok) loadError.value = errText(g, '读取服务商列表失败')
  else resetDraft()
  if (!r.ok) {
    rd.value = null
    loadError.value = loadError.value || errText(r, '读取就绪状态失败')
  } else {
    rd.value = { ...r.data, providers: r.data.providers || {}, capabilities: r.data.capabilities || [] }
  }
}

async function save() {
  if (!reg.value || !dirty.value || saving.value) return
  const lines: string[] = []
  if (draftDefault.value !== reg.value.default) {
    lines.push(`全局默认：${providerLabel(reg.value.default)} → ${providerLabel(draftDefault.value)}`)
  }
  const saved = fullByProduct(reg.value.by_product)
  for (const p of reg.value.products) {
    const a = saved[p.key]; const b = draftByProduct.value[p.key] || ''
    if (a !== b) lines.push(`${p.label}：${a ? providerLabel(a) : '跟随全局默认'} → ${b ? providerLabel(b) : '跟随全局默认'}`)
  }
  const ok = await dialog.confirm(`保存下面的修改：\n- ${lines.join('\n- ')}\n\n只影响之后新发的码；已发的码、在途的付款单和已占用的卡都不变。`, {
    title: '保存发码服务商配置', okText: '保存',
  })
  if (!ok) return
  saving.value = true
  const r = await adminApi('/providers/config', 'PUT', { default: draftDefault.value, by_product: fullByProduct(draftByProduct.value) })
  saving.value = false
  if (!r.ok) { dialog.toast(errText(r, '保存失败'), 'err'); return }
  setRegistry(r.data)
  notReady.value = Array.isArray(r.data.not_ready) ? r.data.not_ready : []
  resetDraft()
  dialog.toast('发码服务商配置已保存')
  const rr = await adminApi<Readiness>('/providers/readiness')
  if (rr.ok) rd.value = { ...rr.data, providers: rr.data.providers || {}, capabilities: rr.data.capabilities || [] }
}

onMounted(load)
// 仅服务端渲染（单元测试）会触发：让测试里渲染出的页面带上数据。浏览器里不会执行。
onServerPrefetch(load)
</script>

<style scoped>
.text-ok { color: #16a34a; }
</style>
