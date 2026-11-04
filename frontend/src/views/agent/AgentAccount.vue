<template>
  <div class="space-y-4 max-w-md">
    <h1 class="text-xl font-semibold">账号</h1>
    <div v-if="me" class="card p-4 text-sm space-y-1">
      <div>登录名：{{ me.username }}</div>
      <div v-if="me.display_name">名称：{{ me.display_name }}</div>
      <div>等级：{{ me.tier_name || '无' }}</div>
      <div>余额：{{ fmtUsd(me.balance_cents || 0) }}</div>
      <div v-if="me.last_login_at">上次登录：{{ fmtBeijing(me.last_login_at) }}</div>
    </div>
    <form class="card p-4 space-y-4" @submit.prevent="submit">
      <h2 class="font-semibold">修改密码</h2>
      <label class="block"><span class="block text-sm mb-1">当前密码</span><input v-model="cur" class="input w-full" type="password" autocomplete="current-password" required /></label>
      <label class="block"><span class="block text-sm mb-1">新密码（至少 12 位）</span><input v-model="next" class="input w-full" type="password" autocomplete="new-password" minlength="12" required /></label>
      <label class="block"><span class="block text-sm mb-1">确认新密码</span><input v-model="next2" class="input w-full" type="password" autocomplete="new-password" minlength="12" required /></label>
      <label class="block"><span class="block text-sm mb-1">动态码</span><input v-model.trim="code" class="input w-full" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="6" required /></label>
      <p v-if="err" class="text-sm text-red-500" role="alert">{{ err }}</p>
      <button class="btn-primary" type="submit" :disabled="busy">{{ busy ? '提交中…' : '修改密码' }}</button>
    </form>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { agentApi, agentErrorText } from '../../lib/agentApi'
import { fmtUsd } from '../../lib/agentMoney'
import { resetAgentSession } from '../../lib/agentSession'
import { fmtBeijing } from '../../lib/mwwebhook'

const router = useRouter()
const me = ref<any>(null)
const cur = ref(''); const next = ref(''); const next2 = ref(''); const code = ref('')
const err = ref(''); const busy = ref(false)

onMounted(async () => {
  try { const r = await agentApi('/me'); if (r.ok) me.value = r.data } catch { /* 账号信息拿不到不影响改密码 */ }
})

async function submit() {
  err.value = ''
  if (next.value !== next2.value) { err.value = '两次输入的新密码不一致'; return }
  busy.value = true
  try {
    const r = await agentApi('/me/password', 'POST', { current_password: cur.value, new_password: next.value, code: code.value })
    if (r.ok) {
      cur.value = next.value = next2.value = code.value = ''
      resetAgentSession()
      router.replace('/agent/login')
    } else err.value = agentErrorText(r.data, r.status)
  } catch { err.value = '网络异常，请稍后再试' } finally { busy.value = false }
}
</script>
