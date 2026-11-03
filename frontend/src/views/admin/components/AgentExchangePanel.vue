<template>
  <div class="space-y-4" data-panel="agent-exchange">
    <section class="card space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 class="text-base font-semibold text-ink">代理换码核对</h2>
          <p class="text-sm text-muted mt-0.5">
            「结果不明」的换码要先到卡台核对有没有真的发出新码，再在这里定论；「旧码待禁用」是新码已发、旧码还没禁掉。
          </p>
        </div>
        <div class="flex items-center gap-2">
          <el-select v-model="stateFilter" class="!w-[200px]" @change="load">
            <el-option label="需要处理（不明 + 待禁用）" value="todo" />
            <el-option v-for="(label, k) in SWAP_STATE_LABEL" :key="k" :label="label" :value="k" />
            <el-option label="全部" value="" />
          </el-select>
          <el-button :loading="loading" @click="load">刷新</el-button>
        </div>
      </div>

      <div v-if="loadError" class="alert alert-error text-sm" role="alert">
        换码记录读取失败，下面的表格不可信，不能据此认为没有待核对的记录：{{ loadError }}
        <el-button size="small" link type="primary" @click="load">重试</el-button>
      </div>

      <el-table :data="rows" size="small" stripe v-loading="loading" :empty-text="loadError ? '读取失败' : '没有记录'">
        <el-table-column prop="id" label="ID" width="64" />
        <el-table-column label="状态" min-width="240">
          <template #default="{ row }">
            <el-tag size="small" :type="row.state === 'unknown' ? 'danger' : row.state === 'disabling' ? 'warning' : row.state === 'done' ? 'success' : 'info'">
              {{ swapStateLabel(row.state) }}
            </el-tag>
            <div v-if="row.state === 'disabling'" class="text-xs text-warn mt-1" style="white-space: normal">{{ DISABLING_HINT }}</div>
          </template>
        </el-table-column>
        <el-table-column label="旧码 → 新码" min-width="200">
          <template #default="{ row }">
            <span class="mono">#{{ row.old_cdk_id }} {{ row.old_code_prefix }}…</span> →
            <span class="mono">{{ row.new_cdk_id ? `#${row.new_cdk_id} ${row.new_code_prefix}…` : '（未记录）' }}</span>
          </template>
        </el-table-column>
        <el-table-column prop="plan" label="套餐" width="90" />
        <el-table-column label="付款单" min-width="150"><template #default="{ row }">{{ row.order_id || '—' }} {{ row.order_status }}</template></el-table-column>
        <el-table-column prop="ip" label="IP" min-width="110" />
        <el-table-column prop="note" label="备注" min-width="140" show-overflow-tooltip />
        <el-table-column label="更新时间" min-width="150"><template #default="{ row }">{{ fmtTime(row.updated_at) }}</template></el-table-column>
        <el-table-column label="操作" min-width="120">
          <template #default="{ row }">
            <el-button v-if="canResolveSwap(row.state)" size="small" type="primary" plain @click="openResolve(row)">核对</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <el-dialog v-model="resolveOpen" :title="`核对换码记录 #${cur?.id}`" width="min(520px, 96vw)" append-to-body :close-on-click-modal="false">
      <div v-if="cur" class="space-y-3">
        <div class="alert alert-warning text-sm">
          先到卡台核对：这次换码有没有真的发出新码。核对不清楚不要定论，定论后不能撤回。
        </div>
        <div class="text-sm">旧码 <span class="mono">#{{ cur.old_cdk_id }} {{ cur.old_code_prefix }}…</span>，套餐 {{ cur.plan }}</div>
        <el-radio-group v-model="form.result">
          <el-radio-button value="released">卡台没发码（释放，用户可重试）</el-radio-button>
          <el-radio-button value="issued">卡台已发码（禁用旧码）</el-radio-button>
        </el-radio-group>
        <label v-if="form.result === 'issued'" class="block text-sm space-y-1">
          <span class="text-muted">卡台上新码的 ID（必填，大于 0）</span>
          <el-input-number v-model="form.newId" :min="0" :controls="false" class="!w-full" />
        </label>
        <label class="block text-sm space-y-1">
          <span class="text-muted">备注（必填）：你在卡台看到了什么</span>
          <el-input v-model="form.note" type="textarea" :rows="3" maxlength="200" show-word-limit />
        </label>
        <p v-if="formError" class="text-sm text-err">{{ formError }}</p>
      </div>
      <template #footer>
        <el-button @click="resolveOpen = false">取消</el-button>
        <el-button type="danger" :loading="saving" @click="submitResolve">提交定论</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { dialog } from '../../../lib/dialog'
import { errText } from '../../../lib/cardpool'
import { adminApi, canResolveSwap, DISABLING_HINT, SWAP_STATE_LABEL, swapStateLabel } from '../../../lib/provider'

const rows = ref<any[]>([])
const loading = ref(false)
const loadError = ref('')
const stateFilter = ref('todo')

function fmtTime(v: unknown) {
  if (!v) return '—'
  const d = typeof v === 'number' ? new Date(v * 1000) : new Date(String(v))
  return Number.isNaN(d.getTime()) ? String(v) : d.toLocaleString()
}

async function load() {
  loading.value = true
  loadError.value = ''
  const states = stateFilter.value === 'todo' ? ['unknown', 'disabling'] : [stateFilter.value]
  const results = await Promise.all(states.map((s) => adminApi<{ list: any[] }>('/cardplatform/agent-exchanges' + (s ? `?state=${s}` : ''))))
  loading.value = false
  const failed = results.find((r) => !r.ok)
  if (failed) { loadError.value = errText(failed, '读取失败'); return }
  rows.value = results.flatMap((r) => r.data.list || []).sort((a, b) => b.id - a.id)
}

const resolveOpen = ref(false)
const cur = ref<any>(null)
const saving = ref(false)
const formError = ref('')
const form = reactive({ result: 'released' as 'released' | 'issued', newId: 0, note: '' })

function openResolve(row: any) {
  cur.value = row
  form.result = 'released'
  form.newId = 0
  form.note = ''
  formError.value = ''
  resolveOpen.value = true
}

async function submitResolve() {
  const row = cur.value
  if (!row || saving.value) return
  formError.value = ''
  const note = form.note.trim()
  if (!note) { formError.value = '备注必填'; return }
  if (form.result === 'issued' && !(form.newId > 0)) { formError.value = '卡台已发码时必须填新码 ID（大于 0）'; return }
  const text = form.result === 'released'
    ? `确认卡台没有发出新码？\n将释放占位，用户可以重新换码。如果卡台其实已经发了码，会多发一张。`
    : `确认卡台已经发出新码 #${form.newId}？\n将记下新码并禁用旧码 #${row.old_cdk_id}（${row.old_code_prefix}…）。`
  if (!(await dialog.confirm(`记录 #${row.id}（套餐 ${row.plan}）\n${text}\n备注：${note}`, { title: '确认定论', okText: '确认定论', danger: true }))) return
  saving.value = true
  const r = await adminApi('/cardplatform/agent-exchanges/' + row.id + '/resolve', 'POST', {
    result: form.result, new_cdk_id: form.result === 'issued' ? form.newId : 0, note,
  })
  saving.value = false
  if (!r.ok) {
    formError.value = r.status === 409 ? '这条记录已经不是「结果不明」，请刷新' : errText(r, '提交失败')
    if (r.status === 409) load()
    return
  }
  resolveOpen.value = false
  if (form.result === 'issued' && r.data.old_disabled === false) {
    dialog.toast('已记录新码，但旧码没有禁用成功：记录停在「旧码待禁用」，系统会每 10 分钟重试，持续失败请到卡台手动禁用旧码', 'warn', 9000)
  } else {
    dialog.toast('已定论')
  }
  load()
}

onMounted(load)
</script>

<style scoped>
.text-err { color: #dc2626; }
.text-warn { color: #d97706; }
</style>
