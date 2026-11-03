<template>
  <div
    class="app-shell"
    :class="{ 'is-collapsed': railMode, 'is-mobile': isMobile, 'is-drawer-open': drawerOpen }"
  >
    <div v-if="isMobile && drawerOpen" class="modal-mask sn-mask" @click="drawerOpen = false" />

    <aside
      id="admin-sidenav"
      class="sidenav"
      :class="{ 'is-collapsed': railMode }"
      :aria-hidden="isMobile && !drawerOpen ? 'true' : undefined"
      :inert="isMobile && !drawerOpen ? true : undefined"
      @keydown.esc="drawerOpen = false"
    >
      <div class="sn-brand" @click="go('/ops')">
        <svg class="sn-logo" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linejoin="round" aria-hidden="true">
          <path d="M12 2.5 21 9l-9 12.5L3 9z" /><path d="M3 9h18M8 9l4 12.5M16 9l-4 12.5M8 9l4-6.5L16 9" />
        </svg>
        <span v-if="!railMode" class="sn-brand-text">
          <span class="sn-brand-name">{{ brand.name || '运营控制台' }}</span>
          <span class="sn-brand-sub">运营控制台</span>
        </span>
      </div>

      <nav class="sn-nav" aria-label="后台导航">
        <template v-for="g in groups" :key="g.name || '_root'">
          <div v-if="g.name && !railMode" class="sn-group" :class="{ 'has-active': g.items.some((i) => isActive(i.path)) }">{{ g.name }}</div>
          <div v-else-if="g.name && railMode" class="sn-sep" />
          <el-tooltip
            v-for="it in g.items"
            :key="it.path"
            :content="it.title"
            placement="right"
            :disabled="!railMode"
          >
            <router-link
              :to="it.path"
              class="sn-item"
              :class="{ active: isActive(it.path) }"
              :aria-label="it.title"
              :aria-current="isActive(it.path) ? 'page' : undefined"
              @click="drawerOpen = false"
            >
              <el-icon class="sn-icon"><component :is="it.icon" /></el-icon>
              <span v-if="!railMode" class="sn-label">{{ it.title }}</span>
            </router-link>
          </el-tooltip>
        </template>
      </nav>

      <div class="sn-foot">
        <div class="sn-tools">
          <ThemeToggle />
          <button
            v-if="!isMobile"
            type="button"
            class="btn-ghost !px-2.5 sn-collapse"
            :title="railMode ? '展开菜单' : '折叠菜单'"
            :aria-label="railMode ? '展开菜单' : '折叠菜单'"
            :aria-expanded="!railMode"
            aria-controls="admin-sidenav"
            @click="toggleCollapse"
          >
            <el-icon :size="18"><component :is="railMode ? 'Expand' : 'Fold'" /></el-icon>
          </button>
        </div>
        <el-dropdown trigger="click" placement="top-start" @command="onUserCommand">
          <button type="button" class="sn-user" :title="username">
            <span class="sn-avatar">{{ username.slice(0, 1).toUpperCase() }}</span>
            <span v-if="!railMode" class="sn-user-name">{{ username }}</span>
            <el-icon v-if="!railMode" class="sn-user-caret"><ArrowDown /></el-icon>
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item command="pwd">修改密码</el-dropdown-item>
              <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </div>
    </aside>

    <div class="main-col">
      <header class="topbar">
        <button
          v-if="isMobile"
          type="button"
          class="btn-ghost !px-2.5 sn-burger"
          aria-label="打开菜单"
          :aria-expanded="drawerOpen"
          aria-controls="admin-sidenav"
          @click="openDrawer"
        >
          <el-icon :size="20"><Menu /></el-icon>
        </button>
        <nav class="crumbs" aria-label="面包屑">
          <template v-if="currentMeta.group">
            <span class="crumb-group">{{ currentMeta.group }}</span>
            <span class="crumb-sep">/</span>
          </template>
          <span class="crumb-page" :class="{ 'crumb-link': !!crumbTab }">{{ currentMeta.title }}</span>
          <template v-if="crumbTab">
            <span class="crumb-sep">/</span>
            <span class="crumb-page crumb-tab">{{ crumbTab }}</span>
          </template>
        </nav>
        <div class="env-badges" aria-label="当前对接环境">
          <el-tooltip v-for="b in envBadges" :key="b.name" :content="b.url || '环境未知'" placement="bottom">
            <span class="env-badge" :class="`env-${b.kind}`" :data-env="b.name">{{ b.name }}·{{ b.label }}</span>
          </el-tooltip>
        </div>
      </header>

      <main class="page"><router-view /></main>
    </div>

    <el-dialog v-model="pwdOpen" title="修改密码" width="420px" append-to-body align-center @closed="resetPwd">
      <el-form label-position="top" @submit.prevent>
        <el-form-item label="旧密码">
          <el-input v-model="pwdForm.old_password" type="password" show-password autocomplete="current-password" />
        </el-form-item>
        <el-form-item label="新密码（至少 12 位）">
          <el-input v-model="pwdForm.new_password" type="password" show-password autocomplete="new-password" />
        </el-form-item>
        <el-form-item label="确认新密码">
          <el-input v-model="pwdForm.confirm" type="password" show-password autocomplete="new-password" @keyup.enter="submitPwd" />
        </el-form-item>
        <div v-if="pwdError" class="alert alert-error">{{ pwdError }}</div>
      </el-form>
      <template #footer>
        <el-button @click="pwdOpen = false">取消</el-button>
        <el-button type="primary" :loading="pwdLoading" @click="submitPwd">保存</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { authFetch, serverLogout } from '../lib/api'
import { dialog } from '../lib/dialog'
import { siteBrand } from '../theme'
import ThemeToggle from '../components/ThemeToggle.vue'
import { PAGE_TABS, tabCrumb, zovoEnv, marsEnv, type EnvBadge } from '../lib/adminui'
import {
  buildNavItems, groupNavItems, readNavPref, writeNavPref, isCollapsed, MOBILE_MAX, OPS, type NavPref,
} from '../lib/adminNav'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const brand = siteBrand
const username = computed(() => auth.username || 'admin')

// 菜单唯一来源：/ops 子路由的 meta（title / group / icon），按路由定义顺序排列。
const navItems = computed(() => buildNavItems(router.options.routes))
const groups = computed(() => groupNavItems(navItems.value))
const currentMeta = computed(() => {
  const m = route.meta as Record<string, string>
  return { title: m.title || brand.value.name || '控制台', group: m.group || '' }
})

// 面包屑第三级：页内标签（?tab=）
const crumbTab = computed(() => tabCrumb(PAGE_TABS, route.path, route.query.tab))

// 环境徽章：资金系统要一眼看出连的是生产还是测试。读不出来就写「未知」，不能默认当生产。
const envBadges = ref<EnvBadge[]>([])
async function loadEnv() {
  const unknown = (name: string): EnvBadge => ({ name, kind: 'unknown', label: '未知', url: '' })
  let zovo = unknown('卡台')
  let mars = unknown('MarsWallex')
  try {
    const r = await authFetch('/api/v1/admin/settings')
    if (r.ok) { const d = await r.json(); zovo = zovoEnv(String(d.card_api_base || '')) }
  } catch { /* 保持未知 */ }
  try {
    const r = await authFetch('/api/v1/admin/marswallex/config')
    if (r.ok) { const d = await r.json(); mars = marsEnv(String(d.base_url || ''), d.base_url_presets) }
  } catch { /* 保持未知 */ }
  envBadges.value = [zovo, mars]
}
onMounted(loadEnv)

function isActive(p: string) {
  return p === OPS ? route.path === OPS : route.path === p || route.path.startsWith(p + '/')
}
function go(p: string) {
  drawerOpen.value = false
  router.push(p)
}

// ── 折叠 / 抽屉 ──
const vw = ref(typeof window === 'undefined' ? 1440 : window.innerWidth)
const pref = ref<NavPref>(readNavPref())
const drawerOpen = ref(false)
const isMobile = computed(() => vw.value <= MOBILE_MAX)
const collapsed = computed(() => isCollapsed(pref.value, vw.value))
// 抽屉里始终显示完整菜单
const railMode = computed(() => !isMobile.value && collapsed.value)

function toggleCollapse() {
  pref.value = railMode.value ? 'expanded' : 'collapsed'
  writeNavPref(pref.value)
}
async function openDrawer() {
  drawerOpen.value = true
  await nextTick()
  document.querySelector<HTMLElement>('#admin-sidenav .sn-item')?.focus()
}
function onResize() {
  vw.value = window.innerWidth
  if (!isMobile.value) drawerOpen.value = false
}
function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape') drawerOpen.value = false
}
onMounted(() => {
  window.addEventListener('resize', onResize)
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  window.removeEventListener('resize', onResize)
  window.removeEventListener('keydown', onKey)
})
watch(() => route.path, () => { drawerOpen.value = false })
watch(
  () => [currentMeta.value.title, brand.value.name],
  () => { if (typeof document !== 'undefined') document.title = `${currentMeta.value.title} · ${brand.value.name || '控制台'}` },
  { immediate: true },
)

// ── 用户菜单 / 修改密码 ──
const pwdOpen = ref(false)
const pwdLoading = ref(false)
const pwdError = ref('')
const pwdForm = reactive({ old_password: '', new_password: '', confirm: '' })
function resetPwd() {
  pwdError.value = ''
  pwdForm.old_password = ''
  pwdForm.new_password = ''
  pwdForm.confirm = ''
}
async function onUserCommand(cmd: string) {
  if (cmd === 'pwd') {
    resetPwd()
    pwdOpen.value = true
  } else if (cmd === 'logout') {
    await serverLogout()
    router.push('/ops/login')
  }
}
async function submitPwd() {
  pwdError.value = ''
  if (pwdForm.new_password !== pwdForm.confirm) {
    pwdError.value = '两次新密码不一致'
    return
  }
  pwdLoading.value = true
  try {
    const r = await authFetch('/api/v1/auth/admin/change-password', {
      method: 'POST',
      body: JSON.stringify({ old_password: pwdForm.old_password, new_password: pwdForm.new_password }),
    })
    const d = await r.json().catch(() => ({}))
    if (!r.ok) {
      pwdError.value = d.error || '修改失败'
      return
    }
    pwdOpen.value = false
    dialog.toast('密码已修改', 'ok')
  } finally {
    pwdLoading.value = false
  }
}
</script>

<style scoped>
.app-shell {
  min-height: 100vh;
  display: flex;
  flex-direction: row;
  align-items: stretch;
  background: transparent;
  color: var(--ink);
}
.main-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  min-height: 100vh;
}

/* ── 侧栏：与内容区同底色，只靠右边框分隔 ── */
.sidenav {
  width: var(--side-w, 232px);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
  z-index: 60;
  background: var(--side-bg, var(--surface-solid));
  border-right: 1px solid var(--side-brd, var(--brd));
  transition: width .18s ease;
}
.sidenav.is-collapsed { width: var(--side-w-collapsed, 64px); }

.sn-brand {
  height: var(--topbar-h, 56px);
  flex-shrink: 0;
  display: flex; align-items: center; gap: 10px;
  padding: 0 18px; cursor: pointer;
  border-bottom: 1px solid var(--side-brd, var(--brd));
  overflow: hidden;
}
.is-collapsed .sn-brand { justify-content: center; padding: 0; }
.sn-logo { width: 24px; height: 24px; flex-shrink: 0; color: var(--primary); }
.sn-brand-text { display: flex; flex-direction: column; min-width: 0; line-height: 1.2; }
.sn-brand-name {
  font-size: 15px; font-weight: 700; color: var(--ink);
  font-family: var(--font-display, var(--font-serif));
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.sn-brand-sub { font-size: 11px; color: var(--ink-3); }

.sn-nav { flex: 1; overflow-y: auto; overflow-x: hidden; padding: 6px 10px 10px; }
.sn-group {
  font-size: 11px; letter-spacing: .06em; color: var(--ink-3);
  margin: 14px 0 4px; padding: 0 12px;
}
.sn-group.has-active { color: var(--ink-2); }
.sn-sep { height: 1px; background: var(--side-brd, var(--brd)); margin: 10px 8px; }
.sn-item {
  display: flex; align-items: center; gap: 10px;
  height: 38px; padding: 0 12px; margin-bottom: 2px;
  border-radius: var(--radius-md);
  color: var(--ink-2); text-decoration: none; font-size: 14px;
  transition: background .15s, color .15s;
}
.is-collapsed .sn-item { justify-content: center; padding: 0; }
.sn-icon { font-size: 18px; flex-shrink: 0; }
.sn-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.sn-item:hover { background: var(--surface-2); color: var(--ink); }
.sn-item:focus-visible { outline: 2px solid var(--primary); outline-offset: -2px; }
.sn-item.active {
  background: var(--primary-soft); color: var(--primary); font-weight: 600;
  box-shadow: inset 3px 0 0 var(--primary);
}

.sn-foot {
  flex-shrink: 0; padding: 10px; border-top: 1px solid var(--side-brd, var(--brd));
  display: flex; flex-direction: column; gap: 8px;
}
.sn-tools { display: flex; gap: 6px; }
.is-collapsed .sn-tools { flex-direction: column; align-items: center; }
.sn-foot :deep(.el-dropdown) { width: 100%; }
.sn-user {
  width: 100%; display: flex; align-items: center; gap: 8px;
  padding: 6px 8px; border-radius: var(--radius-md);
  background: transparent; border: 1px solid transparent; color: var(--ink); cursor: pointer;
}
.is-collapsed .sn-user { justify-content: center; padding: 6px 0; }
.sn-user:hover { background: var(--surface-2); }
.sn-avatar {
  width: 28px; height: 28px; border-radius: 50%; flex-shrink: 0;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--primary-soft); color: var(--primary); font-weight: 700; font-size: 13px;
}
.sn-user-name { flex: 1; text-align: left; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sn-user-caret { color: var(--ink-3); }

/* ── 主列顶栏 ── */
.topbar {
  height: var(--topbar-h, 56px);
  position: sticky; top: 0; z-index: 40;
  display: flex; align-items: center; gap: 10px;
  padding: 0 20px;
  background: color-mix(in srgb, var(--surface-solid) 88%, transparent);
  backdrop-filter: blur(10px);
  border-bottom: 1px solid var(--brd);
}
.crumbs { display: flex; align-items: center; gap: 8px; font-size: 14px; min-width: 0; }
.crumb-group { color: var(--ink-3); white-space: nowrap; }
.crumb-sep { color: var(--ink-3); }
.env-badges { margin-left: auto; display: flex; gap: 6px; align-items: center; flex-wrap: nowrap; }
.env-badge {
  font-size: 12px; line-height: 1; padding: 5px 8px; border-radius: 999px; white-space: nowrap;
  border: 1px solid var(--brd); color: var(--ink-2); background: var(--surface); cursor: default;
}
.env-badge.env-prod { color: var(--good, #16a34a); border-color: currentColor; }
.env-badge.env-test { color: var(--warn, #d97706); border-color: currentColor; }
.env-badge.env-custom, .env-badge.env-unknown { color: var(--err, #dc2626); border-color: currentColor; }
.is-mobile .env-badge { padding: 4px 6px; font-size: 11px; }
.crumb-link { font-weight: 500; color: var(--ink-2); }
.crumb-tab { font-weight: 600; }
.crumb-page { font-weight: 600; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.page {
  max-width: var(--content-max, 1500px); width: 100%; margin: 0 auto;
  padding: var(--page-pad, 22px 20px); flex: 1; min-width: 0;
}

/* ── 窄屏：侧栏变抽屉 ── */
.is-mobile .sidenav {
  position: fixed; left: 0; top: 0; bottom: 0;
  width: min(280px, 84vw);
  transform: translateX(-100%);
  transition: transform .2s ease;
  z-index: 9000;
}
.is-mobile.is-drawer-open .sidenav { transform: translateX(0); box-shadow: var(--shadow); }
.sn-mask { position: fixed; inset: 0; z-index: 8999; }
.is-mobile .topbar { padding: 0 12px; }
.is-mobile .page { padding: 16px 14px; }
</style>
