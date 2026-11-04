<template>
  <div class="relative min-h-screen flex items-center justify-center px-6 py-10">
    <div class="absolute right-5 top-5 flex items-center gap-3">
      <ThemeToggle />
    </div>
    <div class="w-full max-w-md animate-slideInUp">
      <div class="card space-y-6 !p-7">
        <div class="text-center">
          <router-link to="/" class="text-sm app-link">{{ t('common.backHome') }}</router-link>
          <h1 class="mt-4 text-3xl font-bold text-ink">{{ t('login.title') }}</h1>
          <p class="mt-2 text-sm text-muted">{{ t('login.subtitle') }}</p>
        </div>

        <div class="form-group">
          <label>{{ t('login.username') }}</label>
          <input v-model="form.username" class="input" :placeholder="t('login.usernamePlaceholder')" autocomplete="username" @keyup.enter="submit" />
        </div>

        <div class="form-group">
          <label>{{ t('login.password') }}</label>
          <input v-model="form.password" class="input" type="password" :placeholder="t('login.passwordPlaceholder')" autocomplete="current-password" @keyup.enter="submit" />
        </div>

        <div v-if="errorMessage" class="alert alert-error">
          {{ errorMessage }}
        </div>

        <button class="btn-primary w-full disabled:opacity-50" :disabled="loading" @click="submit">
          <span v-if="!loading">{{ t('login.loginBtn') }}</span>
          <span v-else class="flex items-center justify-center gap-2"><span class="spinner"></span>{{ t('login.loggingIn') }}</span>
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../../stores/auth'
import { clearSetupLeftovers, safeAdminRedirect } from '../../lib/session'
import ThemeToggle from '../../components/ThemeToggle.vue'
import zh from '../../i18n/locales/zh'

// 后台只做中文：直接从中文词条取值，不随用户端保存的语言变化，也不改全局语言
const t = (key: string): string =>
  key.split('.').reduce<any>((o, k) => (o == null ? undefined : o[k]), zh) ?? key
const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()

const form = reactive({
  username: '',
  password: '',
})

const loading = ref(false)
const errorMessage = ref('')

// 旧版本安装向导往 sessionStorage 写过明文密码，进登录页时清掉（审计 B-4）
clearSetupLeftovers(typeof sessionStorage === 'undefined' ? undefined : sessionStorage)

const submit = async () => {
  loading.value = true
  errorMessage.value = ''

  try {
    const payload = {
      username: form.username.trim(),
      password: form.password.trim(),
    }
    const response = await fetch('/api/v1/auth/admin/login', {
      method: 'POST',
      credentials: 'include',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
    const data = await response.json()

    if (!response.ok) {
      errorMessage.value = data.error || t('login.errLoginFailed')
      return
    }

    // 认证靠 HttpOnly cookie；本地只存展示用元数据（登录接口也不再返回令牌）
    authStore.save({
      username: data.username,
      name: data.name,
      expiresAt: data.expires_at,
    })

    // 只跳回后台内的路径，外站、协议相对地址一律回 /ops（审计 B-6）
    router.push(safeAdminRedirect(route.query.redirect))
  } catch (error) {
    errorMessage.value = t('login.errNetwork')
  }

  loading.value = false
}
</script>
