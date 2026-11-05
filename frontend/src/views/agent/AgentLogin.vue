<template>
  <div class="min-h-screen flex items-center justify-center px-4 py-10 agent-login">
    <div class="w-full max-w-md card p-6 space-y-5">
      <div>
        <h1 class="text-xl font-semibold">代理商登录</h1>
        <p v-if="!disabled" class="text-sm opacity-70 mt-1">{{ stepHint }}</p>
      </div>

      <!-- 代理商功能关闭时（后台「系统 → 功能开关」）只显示提示，不显示登录框 -->
      <p v-if="disabled" class="text-sm" role="status">{{ AGENT_DISABLED_TEXT }}</p>
      <template v-else>
      <form v-if="step === 'password'" class="space-y-4" autocomplete="on" @submit.prevent="submitPassword">
        <label class="block">
          <span class="block text-sm mb-1">登录名</span>
          <input v-model.trim="username" class="input w-full" type="text" name="username" autocomplete="username" autocapitalize="none" required />
        </label>
        <label class="block">
          <span class="block text-sm mb-1">密码</span>
          <input v-model="password" class="input w-full" type="password" name="password" autocomplete="current-password" required />
        </label>
        <button class="btn-primary w-full" type="submit" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button>
      </form>

      <form v-else-if="step === 'change_password'" class="space-y-4" @submit.prevent="submitNewPassword">
        <label class="block">
          <span class="block text-sm mb-1">新密码（至少 12 位）</span>
          <input v-model="newPassword" class="input w-full" type="password" autocomplete="new-password" minlength="12" required />
        </label>
        <label class="block">
          <span class="block text-sm mb-1">再输一次新密码</span>
          <input v-model="newPassword2" class="input w-full" type="password" autocomplete="new-password" minlength="12" required />
        </label>
        <button class="btn-primary w-full" type="submit" :disabled="busy">{{ busy ? '提交中…' : '修改密码' }}</button>
      </form>

      <form v-else-if="step === 'setup_totp'" class="space-y-4" @submit.prevent="submitTotpSetup">
        <p class="text-sm">用验证器 App（如 Google Authenticator）扫描二维码，再输入显示的 6 位动态码完成绑定。</p>
        <div class="flex flex-col sm:flex-row gap-4 items-center">
          <img v-if="qrDataUrl" :src="qrDataUrl" alt="两步验证二维码" width="180" height="180" class="bg-white p-2 rounded" />
          <div v-else class="text-sm opacity-70">{{ setupLoading ? '正在生成二维码…' : '' }}</div>
          <div class="min-w-0 text-sm">
            <div class="mb-1">无法扫码时，手动输入密钥：</div>
            <code class="break-all select-all">{{ secret }}</code>
          </div>
        </div>
        <label class="block">
          <span class="block text-sm mb-1">动态码</span>
          <input v-model.trim="totpCode" class="input w-full" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="6" pattern="[0-9]{6}" required />
        </label>
        <button class="btn-primary w-full" type="submit" :disabled="busy || !secret">{{ busy ? '验证中…' : '确认绑定并登录' }}</button>
      </form>

      <form v-else class="space-y-4" @submit.prevent="submitTotp">
        <label class="block">
          <span class="block text-sm mb-1">动态码</span>
          <input v-model.trim="totpCode" class="input w-full" type="text" inputmode="numeric" autocomplete="one-time-code" maxlength="6" pattern="[0-9]{6}" required />
        </label>
        <button class="btn-primary w-full" type="submit" :disabled="busy">{{ busy ? '验证中…' : '登录' }}</button>
      </form>

      <p v-if="errorText" class="text-sm text-red-500" role="alert">{{ errorText }}</p>
      <button v-if="step !== 'password'" type="button" class="text-sm underline opacity-70" @click="restart">返回重新输入密码</button>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { agentApi, agentErrorText, safeAgentRedirect } from '../../lib/agentApi'
import { nextLoginStep, type LoginStep } from '../../lib/agentLoginFlow'
import { AGENT_DISABLED_TEXT } from '../../lib/features'

type Step = LoginStep

const route = useRoute()
const router = useRouter()

const step = ref<Step>('password')
const busy = ref(false)
const errorText = ref('')

const username = ref('')
const password = ref('')
const newPassword = ref('')
const newPassword2 = ref('')
const totpCode = ref('')
const secret = ref('')
const qrDataUrl = ref('')
const setupLoading = ref(false)
// 代理商功能是否关闭：读状态接口失败时按「开着」处理，登录接口自己会再拦
const disabled = ref(false)

onMounted(async () => {
  try {
    const r = await agentApi<{ enabled?: boolean }>('/status')
    disabled.value = r.ok && r.data?.enabled === false
  } catch {
    /* 读不到就照常显示登录框 */
  }
})

const stepHint = computed(() => ({
  password: '请输入管理员为你开通的登录名和密码。',
  change_password: '首次登录需要先修改密码。',
  setup_totp: '首次登录需要绑定两步验证。',
  totp: '请输入验证器 App 里的 6 位动态码。',
}[step.value]))

function clearSecrets() {
  password.value = ''
  newPassword.value = ''
  newPassword2.value = ''
  totpCode.value = ''
  secret.value = ''
  qrDataUrl.value = ''
}

function restart() {
  clearSecrets()
  errorText.value = ''
  step.value = 'password'
}

// 按流转结果进入下一步；只有最后两步收到 next === 'done' 才跳转
async function advance(from: Step, res: { ok: boolean; data: any }) {
  const out = nextLoginStep(from, res)
  if (out.kind === 'error') {
    if (out.restart) restart()
    errorText.value = out.text
    return
  }
  errorText.value = ''
  totpCode.value = ''
  if (out.kind === 'step') {
    step.value = out.step
    if (out.step === 'setup_totp') await loadSetup()
    return
  }
  clearSecrets()
  await router.replace(safeAgentRedirect(route.query.redirect))
}

async function loadSetup() {
  setupLoading.value = true
  try {
    const r = await agentApi<{ secret?: string; otpauth_uri?: string }>('/auth/totp/setup')
    if (step.value !== 'setup_totp') return
    if (!r.ok || !r.data.secret || !r.data.otpauth_uri) {
      errorText.value = agentErrorText(r.data)
      return
    }
    // 二维码只在浏览器本地生成，otpauth_uri 不发给任何第三方
    const QRCode = (await import('qrcode')).default
    const url = await QRCode.toDataURL(r.data.otpauth_uri, { margin: 1, width: 180 })
    // 期间用户已返回重新登录：迟到的结果不能把密钥和二维码写回去
    if (step.value !== 'setup_totp') return
    secret.value = r.data.secret
    qrDataUrl.value = url
  } catch {
    errorText.value = '二维码生成失败，请刷新后重试'
  } finally {
    setupLoading.value = false
  }
}

async function call(path: string, body: unknown) {
  if (busy.value) return
  busy.value = true
  errorText.value = ''
  try {
    const from = step.value
    const r = await agentApi<{ next?: string }>(path, 'POST', body)
    await advance(from, r)
  } catch {
    errorText.value = '网络异常，请稍后再试'
  } finally {
    busy.value = false
  }
}

async function submitPassword() {
  await call('/auth/login', { username: username.value, password: password.value })
  password.value = ''
}

async function submitNewPassword() {
  if (newPassword.value.length < 12) { errorText.value = '新密码至少 12 位'; return }
  if (newPassword.value !== newPassword2.value) { errorText.value = '两次输入的新密码不一致'; return }
  await call('/auth/password/initial', { new_password: newPassword.value })
  newPassword.value = ''
  newPassword2.value = ''
}

async function submitTotpSetup() {
  await call('/auth/totp/confirm', { code: totpCode.value })
}

async function submitTotp() {
  await call('/auth/totp', { code: totpCode.value })
}
</script>
