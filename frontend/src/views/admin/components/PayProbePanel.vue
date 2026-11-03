<template>
  <div class="space-y-4" data-panel="pay-probe">
    <div class="alert alert-info text-sm" role="note">
      用测试账号的会话，在服务器上确认升级、点数、开通结账页的实际行为。<b>全部不付款</b>：大部分只读，部分只建结账会话；
      浏览器探测只打开、等待、截图、读文字，不点击、不填表。同一时刻只能运行一个探测。
    </div>

    <div class="space-y-3 max-w-3xl">
      <div>
        <div class="text-sm mb-1">测试账号的 ChatGPT 会话</div>
        <el-input
          v-model="session" type="textarea" :rows="4" autocomplete="off" spellcheck="false"
          placeholder="粘贴 ChatGPT 会话（JSON 文本）。提交后会立即清空，不会保存在浏览器里，结果里也不会出现"
        />
      </div>
      <div class="flex flex-wrap items-center gap-3">
        <el-select v-model="region" clearable placeholder="地区（可不选）" style="width: 180px">
          <el-option v-for="r in PROBE_REGIONS" :key="r" :label="r" :value="r" />
        </el-select>
        <span class="text-xs text-muted">决定走哪个地区代理；建开通会话时也按它定币种，不选则直连、按 US 建会话</span>
      </div>
      <div>
        <div class="flex items-center justify-between mb-1">
          <div class="text-sm">探测项</div>
          <div class="text-xs">
            <el-button link type="primary" size="small" @click="checks = PROBE_CHECKS.map((c) => c.key)">全选</el-button>
            <el-button link type="primary" size="small" @click="checks = PROBE_CHECKS.filter((c) => c.kind === 'readonly').map((c) => c.key)">只选只读</el-button>
            <el-button link size="small" @click="checks = []">清空</el-button>
          </div>
        </div>
        <div class="probe-checks">
          <el-checkbox v-for="c in PROBE_CHECKS" :key="c.key" :model-value="checks.includes(c.key)" class="probe-check" @change="toggleCheck(c.key, $event)">
            <span class="font-medium">{{ c.title }}</span>
            <el-tag size="small" class="ml-2" :type="c.kind === 'readonly' ? 'info' : c.kind === 'create' ? 'warning' : 'primary'">{{ probeKindLabel(c.kind) }}</el-tag>
            <div class="text-xs text-muted probe-desc">{{ c.desc }}</div>
          </el-checkbox>
        </div>
      </div>
      <div v-if="blockReason" class="text-sm text-warn">{{ blockReason }}</div>
      <div v-if="needsBrowser && running" class="text-sm text-muted">含浏览器探测，可能要几分钟，结果会逐项出现；离开本页不会中断探测，回来后会接着显示。</div>
      <div>
        <el-button type="primary" :loading="running" :disabled="!!blockReason" @click="run">开始探测</el-button>
      </div>
    </div>

    <section v-if="current" class="card space-y-3" aria-live="polite">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h3 class="text-base font-semibold">本次结果 <span class="text-muted text-sm">#{{ current.id }}</span>
          <el-tag v-if="current.status === 'running'" size="small" type="warning">运行中，已完成 {{ current.results.length }} 项</el-tag></h3>
        <div class="flex items-center gap-2 text-sm">
          <span>成功 {{ counts.ok }}</span><span>失败 {{ counts.fail }}</span><span>跳过 {{ counts.skipped }}</span>
          <el-button size="small" @click="copyText(current)">复制为文本</el-button>
        </div>
      </div>
      <ProbeResultList :results="current.results" @shot="openShot" />
    </section>

    <section class="card space-y-3">
      <div class="flex items-center justify-between">
        <h3 class="text-base font-semibold">历史记录 <span class="text-muted text-sm">最近 50 条</span></h3>
        <el-button size="small" :loading="histLoading" @click="loadHistory">刷新</el-button>
      </div>
      <div v-if="histError" class="alert alert-error text-sm" role="alert">{{ histError }}</div>
      <el-table :data="history" size="small" border empty-text="暂无记录" v-loading="histLoading">
        <el-table-column type="expand">
          <template #default="{ row }">
            <div class="p-3 space-y-2">
              <div><el-button size="small" @click="copyText(row)">复制为文本</el-button></div>
              <ProbeResultList :results="row.results" @shot="openShot" />
            </div>
          </template>
        </el-table-column>
        <el-table-column prop="id" label="#" width="70" />
        <el-table-column label="时间" min-width="160"><template #default="{ row }">{{ fmtTs(row.created_at) }}</template></el-table-column>
        <el-table-column prop="admin" label="操作人" min-width="90" />
        <el-table-column label="地区" width="80"><template #default="{ row }">{{ row.region || '—' }}</template></el-table-column>
        <el-table-column label="结果" min-width="200">
          <template #default="{ row }">
            <span v-for="r in row.results" :key="r.key" class="mr-1">
              <el-tag size="small" :type="probeStatusTag(r.status)" :title="r.title">{{ r.key }}</el-tag>
            </span>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <el-dialog v-model="shotOpen" title="截图" width="min(900px, 96vw)" append-to-body @closed="clearShot">
      <img v-if="shotUrl" :src="shotUrl" alt="探测截图" style="max-width: 100%" />
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, defineComponent, h, onMounted, onUnmounted, ref } from 'vue'
import { ElTag } from 'element-plus/es/components/tag/index.mjs'
import { authFetch } from '../../../lib/api'
import { dialog } from '../../../lib/dialog'
import { fmtTs } from '../../../lib/cardpool'
import { payApi } from '../../../lib/pay'
import {
  PROBE_CHECKS, PROBE_REGIONS, pollProbe, probeBlockReason, probeCounts, probeErrorText, probeKindLabel, probeNeedsBrowser,
  probeStatusLabel, probeStatusTag, probeToText,
  type ProbeRecord, type ProbeResult,
} from '../../../lib/payprobe'

// 结果列表：每项一个状态标签、一句话结论，详情折叠，截图按钮读后端的截图接口。
const ProbeResultList = defineComponent({
  props: { results: { type: Array as () => ProbeResult[], default: () => [] } },
  emits: ['shot'],
  setup(props, { emit }) {
    return () => h('div', { class: 'space-y-2' }, props.results.map((r) => {
      const shot = typeof r.details?.screenshot === 'string' ? (r.details.screenshot as string) : ''
      return h('div', { key: r.key, class: 'probe-item' }, [
        h('div', { class: 'flex flex-wrap items-center gap-2' }, [
          h(ElTag, { size: 'small', type: probeStatusTag(r.status) }, () => probeStatusLabel(r.status)),
          h('span', { class: 'font-medium' }, r.title),
          h('span', { class: 'mono text-xs text-muted' }, r.key),
          shot ? h('a', { class: 'text-sm cursor-pointer underline', onClick: () => emit('shot', shot) }, '查看截图') : null,
        ]),
        h('div', { class: 'text-sm mt-1', style: 'white-space: pre-wrap; word-break: break-word' }, r.summary),
        r.details && Object.keys(r.details).length
          ? h('details', { class: 'mt-1' }, [
            h('summary', { class: 'text-xs text-muted cursor-pointer' }, '详情'),
            h('pre', { class: 'mono text-xs probe-pre' }, JSON.stringify(r.details, null, 2)),
          ])
          : null,
      ])
    }))
  },
})

const session = ref('')
const region = ref('')
const checks = ref<string[]>(PROBE_CHECKS.filter((c) => c.kind === 'readonly').map((c) => c.key))
const running = ref(false)
const current = ref<ProbeRecord | null>(null)

function toggleCheck(key: string, on: unknown) {
  const rest = checks.value.filter((k) => k !== key)
  checks.value = on ? [...rest, key] : rest
}

const blockReason = computed(() => probeBlockReason(session.value, checks.value, running.value))
const needsBrowser = computed(() => probeNeedsBrowser(checks.value))
const counts = computed(() => probeCounts(current.value?.results || []))

// 轮询：提交后每 2 秒读一次，逐项显示；跑完、出错或离开页面都会停止
let pollToken = 0
function stopPolling() { pollToken++ }

async function follow(id: number) {
  const token = ++pollToken
  running.value = true
  const how = await pollProbe(
    () => payApi<ProbeRecord>('/probe/' + id),
    (rec) => { current.value = rec },
    () => token !== pollToken,
  )
  if (token !== pollToken) return // 被新的轮询取代，或页面已离开
  running.value = false
  if (how === 'error') dialog.toast('读取探测进度失败，可稍后在历史记录里查看', 'err')
  else {
    const st = current.value?.status
    dialog.toast(st === 'done' ? '探测完成' : `探测未完整结束${current.value?.error ? '：' + current.value.error : ''}`, st === 'done' ? 'ok' : 'warn')
  }
  loadHistory()
}

async function run() {
  if (blockReason.value) return
  running.value = true
  const body = { session: session.value, checks: checks.value, region: region.value || undefined }
  // 会话一发出就清空：它不该在页面上多停留
  session.value = ''
  const r = await payApi<{ id: number; status: string }>('/probe', 'POST', body)
  if (!r.ok) {
    running.value = false
    dialog.toast(probeErrorText(r.status, r.data), 'err')
    return
  }
  current.value = { id: r.data.id, status: 'running', created_at: Math.floor(Date.now() / 1000), results: [] }
  follow(r.data.id)
}

// ---------- 历史 ----------
const history = ref<ProbeRecord[]>([])
const histLoading = ref(false)
const histError = ref('')

async function loadHistory() {
  histLoading.value = true
  histError.value = ''
  const r = await payApi<{ probes: ProbeRecord[] }>('/probes')
  histLoading.value = false
  if (!r.ok) { histError.value = (r.data as any)?.error || '历史记录读取失败'; return }
  history.value = r.data.probes || []
  // 页面刷新或切走再回来时，若有仍在跑的探测，接着轮询
  const live = history.value.find((p) => p.status === 'running')
  if (live && !running.value) { current.value = live; follow(live.id) }
}

async function copyText(rec: ProbeRecord) {
  const text = probeToText(rec)
  try {
    await navigator.clipboard.writeText(text)
    dialog.toast('已复制', 'ok')
  } catch {
    dialog.toast('复制失败，请手动选中文字复制', 'warn')
  }
}

// ---------- 截图 ----------
const shotOpen = ref(false)
const shotUrl = ref('')

function clearShot() {
  shotOpen.value = false
  if (shotUrl.value) { URL.revokeObjectURL(shotUrl.value); shotUrl.value = '' }
}

async function openShot(path: string) {
  const [dir, name] = path.split('/')
  if (!dir || !name) return
  try {
    const r = await authFetch(`/api/v1/admin/pay/probe-shots/${encodeURIComponent(dir)}/${encodeURIComponent(name)}`)
    if (!r.ok) { dialog.toast('截图读取失败（可能已被清理）', 'err'); return }
    const blob = await r.blob()
    if (shotUrl.value) URL.revokeObjectURL(shotUrl.value)
    shotUrl.value = URL.createObjectURL(blob)
    shotOpen.value = true
  } catch {
    dialog.toast('截图读取失败', 'err')
  }
}

onMounted(loadHistory)
onUnmounted(() => { stopPolling(); clearShot() })
</script>

<style scoped>
.probe-checks { display: grid; gap: 8px; }
.probe-check { height: auto; align-items: flex-start; margin-right: 0; white-space: normal; }
.probe-desc { white-space: normal; line-height: 1.4; }
.probe-item { padding: 8px 0; border-bottom: 1px solid var(--el-border-color-lighter, #eee); }
.probe-pre { max-height: 320px; overflow: auto; background: var(--el-fill-color-light, #f5f5f5); padding: 8px; border-radius: 4px; white-space: pre-wrap; word-break: break-word; }
</style>
