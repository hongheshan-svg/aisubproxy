<template>
  <div class="card">
    <div class="flex flex-wrap items-start justify-between gap-2 mb-3">
      <div>
        <h2 class="text-xl font-bold text-ink">卡头</h2>
        <p v-if="list.auto" class="text-sm text-muted mt-1">
          由服务商接口自动读取，每 30 分钟同步一次
          <span v-if="list.last_sync_at" class="ml-1 text-subtle">· 上次同步 {{ fmtTs(list.last_sync_at) }}</span>
          <span v-else class="ml-1 text-subtle">· 本次启动后还没同步</span>
        </p>
        <p v-else class="text-sm text-muted mt-1">这家服务商没有卡头接口，请手动添加和维护。</p>
        <p class="text-xs text-subtle mt-1">3DS、Apple Pay 是手动标注的，只用于显示，不影响选卡。</p>
        <p v-if="list.last_sync_error" class="text-sm text-err mt-1" data-test="sync-error">最近一次同步失败：{{ list.last_sync_error }}</p>
      </div>
      <div class="flex gap-2">
        <el-button v-if="list.auto" type="primary" plain :loading="syncing" @click="sync">立即同步</el-button>
        <el-button v-else type="primary" plain @click="openAdd">添加卡头</el-button>
      </div>
    </div>

    <el-table
      :data="list.items"
      size="small"
      border
      v-loading="loading"
      :row-class-name="({ row }: { row: CardHead }) => (row.listed ? '' : 'head-unlisted')"
      :empty-text="list.auto ? '还没有卡头，点「立即同步」读取' : '还没有卡头，点「添加卡头」'"
    >
      <el-table-column label="卡头" min-width="130">
        <template #default="{ row }">
          <span class="mono">{{ cardHeadTitle(row) }}</span>
          <el-tag v-if="!row.listed" size="small" type="info" class="ml-1">接口已不再返回</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="名称" min-width="200" show-overflow-tooltip>
        <template #default="{ row }">
          {{ row.name || '—' }}
          <div class="text-xs text-subtle mono">{{ row.product_id }}</div>
        </template>
      </el-table-column>
      <el-table-column prop="network" label="卡组织" width="90" />
      <el-table-column prop="country" label="国家" width="70" />
      <el-table-column prop="currency" label="币种" width="70" />
      <el-table-column label="3DS" width="80">
        <template #default="{ row }"><TriTag :value="row.three_ds" /></template>
      </el-table-column>
      <el-table-column label="Apple Pay" width="90">
        <template #default="{ row }"><TriTag :value="row.apple_pay" /></template>
      </el-table-column>
      <el-table-column prop="note" label="备注" min-width="140" show-overflow-tooltip />
      <el-table-column label="来源" width="70">
        <template #default="{ row }">{{ row.source === 'api' ? '接口' : '手动' }}</template>
      </el-table-column>
      <el-table-column label="操作" width="130" fixed="right">
        <template #default="{ row }">
          <el-button size="small" link type="primary" @click="openEdit(row)">{{ row.source === 'manual' ? '编辑' : '编辑标注' }}</el-button>
          <el-button v-if="row.source === 'manual'" size="small" link type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="editOpen" :title="editTitle" width="480px" :close-on-click-modal="false" append-to-body>
      <el-form label-width="90px" @submit.prevent>
        <template v-if="editManual">
          <el-form-item label="产品 ID" required>
            <el-input v-model="form.product_id" :disabled="!adding" maxlength="64" placeholder="服务商的产品标识，如 2100050582268325889" />
          </el-form-item>
          <el-form-item label="名称">
            <el-input v-model="form.name" maxlength="100" />
          </el-form-item>
          <el-form-item label="卡头">
            <el-input v-model="form.heads" placeholder="4 到 12 位数字，多个用逗号分隔，如 411111,422222" />
          </el-form-item>
          <el-form-item label="卡组织">
            <el-input v-model="form.network" maxlength="16" placeholder="如 VISA、MASTER" />
          </el-form-item>
          <el-form-item label="国家">
            <el-input v-model="form.country" maxlength="16" placeholder="如 US" />
          </el-form-item>
          <el-form-item label="币种">
            <el-input v-model="form.currency" maxlength="16" placeholder="如 USD" />
          </el-form-item>
        </template>
        <div v-else class="text-sm text-muted mb-3">
          <span class="mono">{{ editing && cardHeadTitle(editing) }}</span> · {{ editing?.name }}
          <div class="text-xs text-subtle mt-1">产品信息由接口同步，这里只改标注。</div>
        </div>
        <el-form-item label="3DS">
          <el-radio-group v-model="form.three_ds">
            <el-radio-button v-for="o in TRI_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="Apple Pay">
          <el-radio-group v-model="form.apple_pay">
            <el-radio-button v-for="o in TRI_OPTIONS" :key="o.value" :value="o.value">{{ o.label }}</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.note" type="textarea" :rows="2" maxlength="200" show-word-limit />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="editOpen = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="save">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, reactive, ref } from 'vue'
import { ElTag } from 'element-plus'
import { authFetch } from '../../../lib/api'
import { dialog } from '../../../lib/dialog'
import { fmtTs } from '../../../lib/cardpool'
import { TRI_OPTIONS, cardHeadTitle, triLabel, type CardHead, type CardHeadList, type TriState } from '../../../lib/cardheads'

const props = defineProps<{ provider: string }>()
const emit = defineEmits<{ loaded: [items: CardHead[]] }>()

// 3DS / Apple Pay 的小标签：支持为绿色，不支持为灰色，未标注只显示文字
const TriTag = defineComponent({
  props: { value: { type: String, default: '' } },
  setup(p) {
    return () =>
      p.value
        ? h(ElTag, { size: 'small', type: p.value === 'yes' ? 'success' : 'info' }, () => triLabel(p.value as TriState))
        : h('span', { class: 'text-subtle text-xs' }, '未标注')
  },
})

const base = computed(() => `/api/v1/admin/providers/${encodeURIComponent(props.provider)}/card-heads`)
const list = ref<CardHeadList>({ provider: props.provider, auto: false, last_sync_at: 0, last_sync_error: '', items: [] })
const loading = ref(false)
const syncing = ref(false)
const saving = ref(false)

async function call(path: string, method = 'GET', body?: unknown): Promise<{ ok: boolean; data: any }> {
  try {
    const r = await authFetch(base.value + path, { method, body: body === undefined ? undefined : JSON.stringify(body) })
    return { ok: r.ok, data: await r.json().catch(() => ({})) }
  } catch {
    return { ok: false, data: { error: '网络错误' } }
  }
}

function apply(data: CardHeadList) {
  list.value = { ...data, items: data.items || [] }
  emit('loaded', list.value.items)
}

async function load() {
  loading.value = true
  const r = await call('')
  loading.value = false
  if (!r.ok) {
    dialog.toast(r.data.error || '读取卡头失败', 'err')
    return
  }
  apply(r.data)
}

async function sync() {
  syncing.value = true
  const r = await call('/sync', 'POST')
  syncing.value = false
  if (!r.ok) {
    dialog.toast(r.data.error || '同步失败', 'err')
    await load()
    return
  }
  apply(r.data)
  dialog.toast(`已同步，共 ${r.data.items?.length ?? 0} 个卡头`, 'ok')
}

const editOpen = ref(false)
const adding = ref(false)
const editing = ref<CardHead | null>(null)
const form = reactive({ product_id: '', name: '', heads: '', network: '', country: '', currency: '', three_ds: '' as TriState, apple_pay: '' as TriState, note: '' })
const editManual = computed(() => adding.value || editing.value?.source === 'manual')
const editTitle = computed(() => (adding.value ? '添加卡头' : editManual.value ? '编辑卡头' : '编辑标注'))

function fillForm(h: CardHead | null) {
  Object.assign(form, {
    product_id: h?.product_id ?? '',
    name: h?.name ?? '',
    heads: (h?.card_heads ?? []).join(','),
    network: h?.network ?? '',
    country: h?.country ?? '',
    currency: h?.currency ?? '',
    three_ds: h?.three_ds ?? '',
    apple_pay: h?.apple_pay ?? '',
    note: h?.note ?? '',
  })
}

function openAdd() {
  adding.value = true
  editing.value = null
  fillForm(null)
  editOpen.value = true
}

function openEdit(h: CardHead) {
  adding.value = false
  editing.value = h
  fillForm(h)
  editOpen.value = true
}

async function save() {
  const body: Record<string, unknown> = { three_ds: form.three_ds, apple_pay: form.apple_pay, note: form.note }
  if (editManual.value) {
    Object.assign(body, {
      product_id: form.product_id.trim(),
      name: form.name,
      card_heads: form.heads.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean),
      network: form.network,
      country: form.country,
      currency: form.currency,
    })
  }
  saving.value = true
  const r = adding.value
    ? await call('', 'POST', body)
    : await call('/' + encodeURIComponent(editing.value!.product_id), 'PUT', body)
  saving.value = false
  if (!r.ok) {
    dialog.toast(r.data.error || '保存失败', 'err')
    return
  }
  apply(r.data)
  editOpen.value = false
  dialog.toast('已保存', 'ok')
}

async function remove(h: CardHead) {
  const ok = await dialog.confirm(`删除卡头「${cardHeadTitle(h)}」？`, { title: '删除卡头', okText: '删除', danger: true })
  if (!ok) return
  const r = await call('/' + encodeURIComponent(h.product_id), 'DELETE')
  if (!r.ok) {
    dialog.toast(r.data.error || '删除失败', 'err')
    return
  }
  apply(r.data)
  dialog.toast('已删除', 'ok')
}

onMounted(load)
defineExpose({ load })
</script>

<style scoped>
:deep(.head-unlisted) {
  color: var(--el-text-color-placeholder);
}
</style>
