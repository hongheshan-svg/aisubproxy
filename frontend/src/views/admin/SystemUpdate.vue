<template>
  <div class="space-y-4">
    <AdminPageHeader class="!mb-0" title="版本更新" description="GitHub 预编译包 · 签名校验 · 自动备份 · 一键无痕热更" />
    <section class="card space-y-3 max-w-3xl">
      <div class="flex justify-end">
        <el-button :loading="versionLoading" @click="loadVersion(true)">检查更新</el-button>
      </div>
      <div v-if="versionError" class="text-sm" style="color: var(--err)">{{ versionError }}</div>
      <div v-if="versionInfo.update_hint" class="text-xs" style="color: var(--warn)">{{ versionInfo.update_hint }}</div>
      <div class="space-y-2 text-sm">
        <div class="flex justify-between gap-2">
          <span class="text-muted">当前版本</span>
          <span class="mono font-semibold text-ink">v{{ versionInfo.current || '—' }}</span>
        </div>
        <div class="flex justify-between gap-2">
          <span class="text-muted">GitHub 最新</span>
          <span class="mono font-semibold" :style="versionInfo.update_available ? 'color: var(--warn)' : 'color: var(--good)'">
            {{ versionInfo.latest ? `v${versionInfo.latest}` : '—' }}
          </span>
        </div>
        <div class="flex justify-between gap-2">
          <span class="text-muted">状态</span>
          <el-tag size="small" :type="versionInfo.update_available ? 'warning' : 'success'">
            {{ versionInfo.update_available ? '有新版本' : (versionInfo.latest ? '已是最新' : '未取到远端') }}
          </el-tag>
        </div>
        <div class="flex justify-between gap-2">
          <span class="text-muted">发布包</span>
          <el-tag size="small" :type="versionInfo.has_bundle ? 'success' : 'info'">
            {{ versionInfo.has_bundle ? 'cdk-bundle 可用' : '无预编译包' }}
          </el-tag>
        </div>
        <div v-if="versionInfo.github_repo" class="text-xs text-subtle break-all">
          仓库 {{ versionInfo.github_repo }}
          <span v-if="versionInfo.checked_at"> · 检查于 {{ formatChecked(versionInfo.checked_at) }}</span>
        </div>
      </div>

      <!-- 更新说明：取自 GitHub Release（CHANGELOG.md 中这一版的小节），按纯文本显示 -->
      <details
        v-if="versionInfo.release_notes"
        :key="versionInfo.latest"
        :open="!!versionInfo.update_available"
        class="rounded-xl p-3 text-sm"
        style="background: var(--surface-2)"
      >
        <summary class="cursor-pointer text-ink font-semibold">
          v{{ versionInfo.latest }} 更新说明
        </summary>
        <div class="mt-2 text-xs text-muted leading-relaxed" style="white-space: pre-wrap; max-height: 320px; overflow-y: auto">{{ releaseNotesText(versionInfo.release_notes) }}</div>
      </details>

      <!-- 热更进度 -->
      <div v-if="updating || updatePhase" class="rounded-xl p-3 space-y-2" style="background: var(--surface-2)">
        <div class="flex justify-between text-xs text-muted">
          <span>{{ updateMessage || updatePhase }}</span>
          <span class="mono">{{ updateProgress }}%</span>
        </div>
        <div class="h-2 rounded-full overflow-hidden" style="background: var(--brd)">
          <div
            class="h-2 rounded-full transition-all duration-300"
            :style="{ width: `${updateProgress}%`, background: updateFailed ? 'var(--err)' : 'var(--primary)' }"
          />
        </div>
        <div v-if="updateFailed" class="text-xs" style="color: var(--err)">{{ updateError }}</div>
      </div>

      <div class="flex flex-wrap gap-2 pt-1">
        <el-button
          type="primary"
          :loading="updating"
          :disabled="!canOneClickUpdate"
          @click="oneClickUpdate"
        >
          {{ updating ? '更新中…' : '一键无痕更新' }}
        </el-button>
        <a
          v-if="versionInfo.release_url"
          class="btn-secondary !px-4 !py-2 text-sm"
          :href="versionInfo.release_url"
          target="_blank"
          rel="noopener noreferrer"
        >打开 Release</a>
        <a
          v-if="versionInfo.tags_url"
          class="btn-secondary !px-4 !py-2 text-sm"
          :href="versionInfo.tags_url"
          target="_blank"
          rel="noopener noreferrer"
        >查看 Tags</a>
      </div>
      <p class="text-xs text-subtle leading-relaxed">
        在线下载发布包 → 热替换二进制与前端 → 进程 re-exec 重载。
        <b>不修改</b> app.env / 数据库。需 Release 挂有 <code>cdk-bundle-linux-amd64.tgz</code>（git tag 触发 CI 自动上传）。
      </p>
      <p v-if="isDocker" class="text-xs text-subtle leading-relaxed">
        Docker 部署：新版本保存在数据卷里，删掉容器重建后仍然生效；连续 3 次启动失败会自动退回镜像自带的版本。
        也可以换镜像升级：在部署目录运行 <code>deploy/docker-deploy.sh --version X.Y.Z</code>（版本号不带 v）。
        回滚在线升级：<code>docker exec cdk-recharge /app/cdk-recharge rollback &amp;&amp; docker restart cdk-recharge</code>
      </p>
    </section>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { authFetch } from '../../lib/api'

const versionLoading = ref(false)
const versionError = ref('')
const versionInfo = ref<any>({})
// 后端 deploy_mode：docker 或 native
const isDocker = computed(() => versionInfo.value.deploy_mode === 'docker')

const updating = ref(false)
const updatePhase = ref('')
const updateMessage = ref('')
const updateProgress = ref(0)
const updateFailed = ref(false)
const updateError = ref('')
let updatePollTimer: number | null = null

const canOneClickUpdate = computed(() => {
  if (updating.value) return false
  if (versionInfo.value.update_enabled === false) return false
  // 有新版本且有 bundle，或强制允许「重装最新」当 has_bundle
  if (versionInfo.value.one_click_ready) return true
  return !!(versionInfo.value.has_bundle && versionInfo.value.latest && versionInfo.value.update_available)
})

// 把 Release 的 Markdown 简化成可读的纯文本：去掉标题井号、加粗与行内代码标记（不渲染 HTML）
function releaseNotesText(md: string) {
  return String(md || '')
    .replace(/\r\n/g, '\n')
    .replace(/^#{1,6}\s*/gm, '')
    .replace(/\*\*(.+?)\*\*/g, '$1')
    .replace(/`([^`]+)`/g, '$1')
    .trim()
}

function numOrDash(v: unknown) {
  if (v === null || v === undefined || v === '') return '—'
  const n = Number(v)
  return Number.isFinite(n) ? n : '—'
}

function formatChecked(iso: string) {
  try {
    return new Date(iso).toLocaleString()
  } catch {
    return iso
  }
}

function stopUpdatePoll() {
  if (updatePollTimer != null) {
    window.clearInterval(updatePollTimer)
    updatePollTimer = null
  }
}

async function pollUpdateStatus() {
  try {
    const r = await authFetch('/api/v1/admin/system/update/status')
    const d = await r.json().catch(() => ({}))
    const st = d.state || {}
    updatePhase.value = st.phase || ''
    updateMessage.value = st.message || ''
    updateProgress.value = Number(st.progress) || 0
    if (st.phase === 'failed') {
      updateFailed.value = true
      updateError.value = st.error || st.message || '更新失败'
      updating.value = false
      stopUpdatePoll()
      return
    }
    if (st.phase === 'reloading' || st.phase === 'done') {
      updateMessage.value = st.message || '重载中，等待服务恢复…'
      updateProgress.value = Math.max(updateProgress.value, 95)
      // 轮询 health + 新版本
      await waitForReload()
    }
  } catch {
    // 进程 re-exec 瞬间可能短暂断连，继续等 health
    await waitForReload()
  }
}

async function waitForReload() {
  stopUpdatePoll()
  const before = versionInfo.value.current
  for (let i = 0; i < 40; i++) {
    await new Promise((r) => setTimeout(r, 500))
    try {
      const h = await fetch('/health', { credentials: 'include' })
      if (!h.ok) continue
      // 恢复后刷新版本
      await loadVersion(true)
      const after = versionInfo.value.current
      updateProgress.value = 100
      updatePhase.value = 'done'
      updateMessage.value = after && after !== before
        ? `已热更至 v${after}`
        : `服务已恢复（当前 v${after || before || '—'}）`
      updating.value = false
      updateFailed.value = false
      return
    } catch {
      /* keep waiting */
    }
  }
  updateFailed.value = true
  updateError.value = isDocker.value
    ? '重载超时：请手动刷新页面，并用 docker logs cdk-recharge 查看日志'
    : '重载超时：请手动刷新页面并检查 systemctl status cdk-recharge'
  updating.value = false
}

async function oneClickUpdate() {
  if (!canOneClickUpdate.value) return
  const target = versionInfo.value.latest || 'latest'
  const ok = window.confirm(
    `确认一键无痕更新到 v${target}？\n\n` +
      '• 在线下载 GitHub 预编译包\n' +
      '• 热替换二进制与前端（不改 app.env / 数据库）\n' +
      '• 进程 re-exec 亚秒重载\n',
  )
  if (!ok) return

  updating.value = true
  updateFailed.value = false
  updateError.value = ''
  updatePhase.value = 'starting'
  updateMessage.value = '提交更新任务…'
  updateProgress.value = 2
  try {
    const r = await authFetch('/api/v1/admin/system/update', {
      method: 'POST',
      body: JSON.stringify({ target: 'latest', confirm: true }),
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok && r.status !== 202) {
      updateFailed.value = true
      updateError.value = d.error || '启动更新失败'
      updating.value = false
      return
    }
    stopUpdatePoll()
    updatePollTimer = window.setInterval(() => {
      pollUpdateStatus()
    }, 800)
    await pollUpdateStatus()
  } catch (e: any) {
    updateFailed.value = true
    updateError.value = e?.message || '网络错误'
    updating.value = false
  }
}

async function loadVersion(force = false) {
  versionLoading.value = true
  versionError.value = ''
  try {
    const q = force ? '?refresh=1' : ''
    const r = await authFetch(`/api/v1/admin/system/version${q}`)
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      versionError.value = d.error || '版本检查失败'
      return
    }
    versionInfo.value = d
    if (d.github_error && !d.latest) {
      versionError.value = `GitHub: ${d.github_error}`
    }
  } catch (e: any) {
    versionError.value = e?.message || '网络错误'
  } finally {
    versionLoading.value = false
  }
}

onMounted(() => {
  loadVersion(false)
})
onBeforeUnmount(stopUpdatePoll)
</script>
