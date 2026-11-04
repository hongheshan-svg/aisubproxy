<template>
  <div class="pb-2">
    <div class="w-full space-y-5">
      <AdminPageHeader title="审计日志" description="记录管理员的登录、改密、卡密增删禁用、任务处理、长链接生成等敏感操作。" class="!mb-0">
        <template #actions>
          <button class="btn-secondary" @click="loadLogs">刷新</button>
        </template>
      </AdminPageHeader>

      <div class="card overflow-hidden !p-0">
        <div class="overflow-x-auto">
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
              <tr v-if="loading">
                <td colspan="5" class="py-8 text-center text-muted">加载中...</td>
              </tr>
              <tr v-else-if="logs.length === 0">
                <td colspan="5" class="py-8 text-center text-muted">暂无审计记录</td>
              </tr>
              <tr v-for="item in logs" :key="item.id">
                <td class="text-muted whitespace-nowrap">{{ formatDate(item.created_at) }}</td>
                <td class="text-ink">{{ item.username || '-' }}</td>
                <td><span :class="actionClass(item.action)">{{ actionLabel(item.action) }}</span></td>
                <td class="text-muted font-mono break-all">{{ auditDetailLabel(item.detail) || '-' }}</td>
                <td class="text-muted font-mono">{{ item.ip || '-' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import AdminPageHeader from '../../components/AdminPageHeader.vue'
import { onMounted, ref } from 'vue'
import { authFetch } from '../../lib/api'
import { auditActionLabel, auditDetailLabel } from '../../lib/errors'
import { fmtAuditTime as formatDate } from '../../lib/auditTime'

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

const loadLogs = async () => {
  loading.value = true
  try {
    const response = await authFetch('/api/v1/admin/audit-logs?limit=300')
    const data = await response.json()
    logs.value = data.logs || []
  } catch (error) {
    logs.value = []
  }
  loading.value = false
}

const actionLabel = auditActionLabel

const actionClass = (action: string) => {
  const classes: Record<string, string> = {
    login: 'pill pill-info',
    change_password: 'pill pill-warn',
    cdk_generate: 'pill pill-good',
    cdk_disable: 'pill pill-warn',
    cardplatform_cdk_note_set: 'pill pill-info',
    cardplatform_cdk_note_clear: 'pill',
    cardplatform_batch_note_set: 'pill pill-info',
    cardplatform_batch_note_clear: 'pill',
    cdk_delete: 'pill pill-err',
    task_approve: 'pill pill-good',
    task_reject: 'pill pill-err',
    checkout_generate: 'pill pill-info',
  }
  return classes[action] || 'pill'
}

onMounted(loadLogs)
</script>
