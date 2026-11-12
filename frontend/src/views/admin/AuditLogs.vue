<template>
  <div class="pb-2">
    <div class="w-full space-y-5">
      <AdminPageHeader title="审计日志" description="记录管理员和代理商的登录、改密、资金与卡池、付款配置、代理商、系统更新等敏感操作。" class="!mb-0">
        <template #actions>
          <button class="btn-secondary" @click="loadLogs">刷新</button>
        </template>
      </AdminPageHeader>

      <div class="card overflow-hidden !p-0">
        <!-- 手机和平板（< 1024px）：一条记录一张卡片，详情占满整行。表格在窄屏上会把详情和操作挤成一字一行 -->
        <ul class="lg:hidden" data-audit-list>
          <li v-if="statusText" class="py-8 text-center text-sm text-muted">{{ statusText }}</li>
          <li v-for="(item, i) in logs" :key="item.id" class="px-4 py-3" :style="i > 0 || statusText ? 'border-top: 1px solid var(--brd)' : ''">
            <div class="flex items-center justify-between gap-3">
              <span :class="actionClass(item.action)" class="whitespace-nowrap">{{ actionLabel(item.action) }}</span>
              <span class="text-xs text-muted whitespace-nowrap">{{ formatDate(item.created_at) }}</span>
            </div>
            <p v-if="auditDetailLabel(item.detail)" class="mt-2 text-sm text-ink font-mono [overflow-wrap:anywhere]">{{ auditDetailLabel(item.detail) }}</p>
            <div class="mt-1.5 flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-muted">
              <span>管理员 <span class="text-ink">{{ item.username || '-' }}</span></span>
              <span>IP <span class="font-mono [overflow-wrap:anywhere]">{{ item.ip || '-' }}</span></span>
            </div>
          </li>
        </ul>
        <!-- 电脑（>= 1024px）：表格。详情列至少 16rem，长串只在详情列里折行；IPv6 可在任意处折行 -->
        <div class="hidden lg:block overflow-x-auto">
          <table class="data-table">
            <thead>
              <tr>
                <th>时间</th>
                <th>管理员</th>
                <th>操作</th>
                <th>详情</th>
                <th>IP</th>
              </tr>
            </thead>
            <tbody>
              <tr v-if="statusText">
                <td colspan="5" class="py-8 text-center text-muted">{{ statusText }}</td>
              </tr>
              <tr v-for="item in logs" :key="item.id">
                <td class="text-muted whitespace-nowrap">{{ formatDate(item.created_at) }}</td>
                <td class="text-ink">{{ item.username || '-' }}</td>
                <td class="whitespace-nowrap"><span :class="actionClass(item.action)">{{ actionLabel(item.action) }}</span></td>
                <td class="min-w-[16rem] text-muted font-mono [overflow-wrap:anywhere]">{{ auditDetailLabel(item.detail) || '-' }}</td>
                <td class="min-w-[8.5rem] text-muted font-mono [overflow-wrap:anywhere]">{{ item.ip || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
        <div v-if="cursor > 0 || loadingMore" class="p-3 text-center" style="border-top: 1px solid var(--brd)">
          <button class="btn-secondary" :disabled="loadingMore" @click="loadMore">{{ loadingMore ? '加载中...' : '加载更多' }}</button>
        </div>
        <div v-else-if="logs.length > 0 && !loading" class="p-3 text-center text-xs text-muted" style="border-top: 1px solid var(--brd)">已显示全部 {{ logs.length }} 条</div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { computed, onMounted, ref } from 'vue'
import { authFetch } from '../../lib/api'
import { dialog } from '../../lib/dialog'
import { auditActionClass, auditActionLabel, auditDetailLabel } from '../../lib/errors'
import { fmtAuditTime as formatDate } from '../../lib/auditTime'
import { AUDIT_PAGE_SIZE, appendAuditPage, auditLogsUrl, auditPageResult } from '../../lib/auditPaging'

interface AuditLog {
  id: number
  username: string
  action: string
  detail: string
  ip: string
  created_at: string
}

const logs = ref<AuditLog[]>([])
const loading = ref(false)
const loadingMore = ref(false)
const cursor = ref(0) // 下一页的 before_id；0 表示没有更多
// 读取失败（接口报错或网络异常）：表格空着时显示「读取失败」，不能说成「暂无审计记录」
const loadError = ref(false)
// 列表上方的状态行：刷新时显示「加载中」（已有记录照常显示在下面），没有记录时说明是空还是读取失败
const statusText = computed(() => {
  if (loading.value) return '加载中...'
  if (logs.value.length === 0) return loadError.value ? '读取失败，请点刷新重试' : '暂无审计记录'
  return ''
})

// 读一页；失败返回 null。响应体读不出来也算失败。
async function fetchPage(beforeId?: number) {
  try {
    const response = await authFetch(auditLogsUrl(AUDIT_PAGE_SIZE, beforeId))
    const data = await response.json().catch(() => null)
    const page = auditPageResult<AuditLog>(response.ok, data)
    return page.failed ? null : page
  } catch {
    return null
  }
}

const loadLogs = async () => {
  loading.value = true
  const page = await fetchPage()
  loading.value = false
  if (!page) {
    // 保留已有记录和游标，点刷新可以重试
    loadError.value = true
    dialog.toast('读取审计日志失败', 'err')
    return
  }
  loadError.value = false
  logs.value = page.logs
  cursor.value = page.cursor
}

const loadMore = async () => {
  if (loadingMore.value || cursor.value <= 0) return
  loadingMore.value = true
  const page = await fetchPage(cursor.value)
  loadingMore.value = false
  if (!page) {
    // 失败时保留已有记录和游标，可以再点一次
    dialog.toast('读取失败，请再点一次', 'err')
    return
  }
  logs.value = appendAuditPage(logs.value, page.logs)
  cursor.value = page.cursor
}

const actionLabel = auditActionLabel

const actionClass = auditActionClass

onMounted(loadLogs)
</script>
