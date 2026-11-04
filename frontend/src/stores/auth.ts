import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import {
  AUTH_STORAGE_KEY, parseStoredAuth, probeAdminSession, storedAuthAlive, type SessionProbe, type StoredAuth,
} from '../lib/session'

// 认证只靠 HttpOnly cookie + CSRF 头；这里只存展示用的用户名、显示名、到期时间，不存令牌。
// 是否已登录以服务端 /auth/admin/me 为准：每次页面加载由路由守卫问一次（ensureSession），本地记录只是提示。
export const useAuthStore = defineStore('auth', () => {
  const username = ref('')
  const name = ref('')
  const expiresAt = ref('')
  // 本次页面加载里登录态是否已确认：服务端回答过、刚登录或安装成功、刚退出，都算确认，之后不再问服务端
  const confirmed = ref(false)

  const isLoggedIn = computed(() => storedAuthAlive({ username: username.value, expiresAt: expiresAt.value }))

  const apply = (a: StoredAuth | null) => {
    username.value = a?.username || ''
    name.value = a?.name || ''
    expiresAt.value = a?.expiresAt || ''
  }

  // 私密窗口等环境下 localStorage 可能读写都抛错：登录态照样以服务端为准
  const persist = (a: StoredAuth | null) => {
    try {
      if (a) localStorage.setItem(AUTH_STORAGE_KEY, JSON.stringify(a))
      else localStorage.removeItem(AUTH_STORAGE_KEY)
    } catch {
      /* ignore */
    }
  }

  const restore = () => {
    let raw: string | null = null
    try {
      raw = localStorage.getItem(AUTH_STORAGE_KEY)
    } catch {
      raw = null
    }
    if (raw === null) return
    const saved = parseStoredAuth(raw)
    const alive = storedAuthAlive(saved)
    apply(alive ? saved : null)
    // 旧版本连 JWT 一起存过：按新格式写回，令牌从浏览器存储里抹掉；过期或坏数据直接删
    persist(alive ? saved : null)
  }

  const save = (payload: StoredAuth) => {
    const a: StoredAuth = { username: payload.username, name: payload.name || payload.username, expiresAt: payload.expiresAt }
    apply(a)
    persist(a)
    confirmed.value = true
  }

  const logout = () => {
    apply(null)
    persist(null)
    confirmed.value = true
  }

  let inflight: Promise<void> | null = null
  // 向服务端确认登录态。确认过就直接返回；服务端暂时不可达时沿用本地记录，下次跳转再问。
  const ensureSession = (probe: () => Promise<SessionProbe> = () => probeAdminSession()): Promise<void> => {
    if (confirmed.value) return Promise.resolve()
    if (!inflight) {
      inflight = probe()
        .then((r) => {
          if (confirmed.value) return // 等待期间已登录或已退出，以那次为准
          if (r.kind === 'alive') save(r.auth)
          else if (r.kind === 'dead') logout()
        })
        .finally(() => {
          inflight = null
        })
    }
    return inflight
  }

  return {
    username,
    name,
    expiresAt,
    isLoggedIn,
    confirmed,
    restore,
    save,
    logout,
    ensureSession,
  }
})
