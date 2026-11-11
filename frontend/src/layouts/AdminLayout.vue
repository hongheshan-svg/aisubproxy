<template>
  <SideNavShell
    :groups="groups"
    :is-active="isActive"
    :brand-name="brand.name || '运营控制台'"
    :logo-src="logoSrc"
    sub-label="运营控制台"
    nav-label="后台导航"
    nav-id="admin-sidenav"
    pref-key="ops-sidenav"
    :home-path="OPS"
  >
    <template #topbar>
      <nav class="crumbs" aria-label="面包屑">
        <template v-if="currentMeta.group">
          <span class="crumb-group">{{ currentMeta.group }}</span>
          <span class="crumb-sep">/</span>
        </template>
        <span class="crumb-page" :class="{ 'crumb-link': !!crumbTab, 'crumb-short': currentMeta.hasFull }">{{ currentMeta.title }}</span>
        <span v-if="currentMeta.hasFull" class="crumb-page crumb-full" :class="{ 'crumb-link': !!crumbTab }">{{ currentMeta.fullTitle }}</span>
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
    </template>

    <template #foot="{ rail }">
      <div v-if="!themeLocked" class="sn-tools"><ThemeToggle /></div>
      <el-dropdown class="sn-user-menu" trigger="click" placement="top-start" @command="onUserCommand">
        <button type="button" class="sn-user" :title="username">
          <span class="sn-avatar">{{ username.slice(0, 1).toUpperCase() }}</span>
          <span v-if="!rail" class="sn-user-name">{{ username }}</span>
          <el-icon v-if="!rail" class="sn-user-caret"><ArrowDown /></el-icon>
        </button>
        <template #dropdown>
          <el-dropdown-menu>
            <el-dropdown-item command="pwd">修改密码</el-dropdown-item>
            <el-dropdown-item command="logout" divided>退出登录</el-dropdown-item>
          </el-dropdown-menu>
        </template>
      </el-dropdown>
    </template>

    <router-view />

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
  </SideNavShell>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { authFetch, serverLogout } from '../lib/api'
import { dialog } from '../lib/dialog'
import { adjustHasAnyPending, clearAdjustRetries } from '../lib/agentAdmin'
import { siteBrand, themeLocked } from '../theme'
import ThemeToggle from '../components/ThemeToggle.vue'
import SideNavShell from '../components/shell/SideNavShell.vue'
import { PAGE_TABS, tabCrumb, marsEnv, type EnvBadge } from '../lib/adminui'
import {
  buildNavItems, groupNavItems, pageTitle, OPS,
} from '../lib/adminNav'
import { adminFeatures, loadAdminFeatures, shouldLeaveFeaturePage, visibleNavItems } from '../lib/features'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const brand = siteBrand
const logoSrc = computed(() => {
  const v = brand.value.logoVersion
  return v ? `/api/v1/public/site/logo?v=${encodeURIComponent(v)}` : ''
})
const username = computed(() => auth.username || 'admin')

// 菜单唯一来源：/ops 子路由的 meta（title / group / icon），按路由定义顺序排列。
// 受功能开关控制的菜单（meta.feature，如代理商）在开关关闭或还没读到时不显示。
const navItems = computed(() => visibleNavItems(buildNavItems(router.options.routes), adminFeatures))
const groups = computed(() => groupNavItems(navItems.value))
const currentMeta = computed(() => {
  const m = route.meta as Record<string, string>
  const title = m.title || brand.value.name || '控制台'
  const fullTitle = m.fullTitle || title
  return { title, fullTitle, hasFull: fullTitle !== title, group: m.group || '' }
})

// 面包屑第三级：页内标签（?tab=）
const crumbTab = computed(() => tabCrumb(PAGE_TABS, route.path, route.query.tab))

// 环境徽章：资金系统要一眼看出连的是生产还是测试。读不出来就写「未知」，不能默认当生产。
const envBadges = ref<EnvBadge[]>([])
async function loadEnv() {
  const unknown = (name: string): EnvBadge => ({ name, kind: 'unknown', label: '未知', url: '' })
  let mars = unknown('MarsWallex')
  try {
    const r = await authFetch('/api/v1/admin/marswallex/config')
    if (r.ok) {
      const d = await r.json()
      const hasCreds = !!(d.developer_id || d.api_token_set || d.private_key_set || d.platform_public_key_set)
      mars = marsEnv(String(d.base_url || ''), d.base_url_presets, hasCreds)
    }
  } catch { /* 保持未知 */ }
  envBadges.value = [mars]
}
onMounted(loadEnv)

// 功能开关：读到「关闭」后，停在受控页面（如直接输入 /ops/agents）就送回总览
onMounted(() => { loadAdminFeatures() })
watch(
  () => [route.path, adminFeatures.loaded, adminFeatures.agent],
  () => { if (shouldLeaveFeaturePage(route.meta as Record<string, unknown>, adminFeatures)) router.replace(OPS) },
  { immediate: true },
)

function isActive(p: string) {
  return p === OPS ? route.path === OPS : route.path === p || route.path.startsWith(p + '/')
}
watch(
  () => [currentMeta.value.fullTitle, brand.value.name],
  () => { if (typeof document !== 'undefined') document.title = pageTitle(currentMeta.value, brand.value.name) },
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
    // 从存储判断（别的标签页留下的未决也算），不只看本页内存
    if (adjustHasAnyPending() && !(await dialog.confirm('有调账结果未知，退出后这台电脑上不能再原样重试，请先到代理商的「余额流水」核对。确定退出吗？', { title: '调账结果未知', okText: '仍然退出', danger: true }))) return
    // 服务端没确认退出时留在后台：HttpOnly 的会话 cookie 还在，回登录页会让人以为已经退了
    const out = await serverLogout()
    if (out !== 'done') {
      // csrf_token cookie 丢了：后端没有补发，重试、关标签页都没用（cookie 和会话同一有效期），只能清本站 Cookie
      if (out === 'csrf') dialog.toast('退出失败：浏览器里本站的校验 Cookie 丢了，服务器拒绝退出，重试也不会成功。请在浏览器设置里清除本站的 Cookie，清除后这台电脑上就退出了', 'err', 10000)
      else dialog.toast('退出失败，服务器上的登录状态还在，请重试', 'err')
      return
    }
    clearAdjustRetries() // 退出登录清掉调账的待重试记录（只有幂等键、金额和原因）
    router.push('/ops/login')
  }
}
async function submitPwd() {
  if (pwdLoading.value) return
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
    // 改密后服务端已作废所有旧会话（含本机）并清了 cookie：本地也退出，回登录页用新密码登录
    auth.logout()
    dialog.toast('密码已修改，请用新密码重新登录', 'ok')
    router.push('/ops/login')
  } catch {
    pwdError.value = '网络错误，不确定密码是否已修改，请重新登录确认'
  } finally {
    pwdLoading.value = false
  }
}
</script>

<style scoped>
/* 侧栏底部（插槽内容，样式在这里）：颜色全用侧栏变量 */
.sn-tools { display: flex; gap: 6px; }
.sn-tools :deep(.btn-ghost) { color: var(--side-ink); background: transparent; border-color: transparent; box-shadow: none; }
.sn-tools :deep(.btn-ghost:hover) { color: var(--side-ink); background: color-mix(in srgb, var(--side-ink) 12%, transparent); border-color: transparent; }
.sn-tools :deep(.btn-ghost:focus-visible),
.sn-user:focus-visible { outline: 2px solid var(--side-ink); outline-offset: -2px; }
.is-collapsed .sn-tools { justify-content: center; }
.sn-user-menu { width: 100%; }
.sn-user {
  width: 100%; display: flex; align-items: center; gap: 8px;
  padding: 6px 8px; border-radius: var(--radius-md);
  background: transparent; border: 1px solid transparent; color: var(--side-ink); cursor: pointer;
}
.is-collapsed .sn-user { justify-content: center; padding: 6px 0; }
.sn-user:hover { background: color-mix(in srgb, var(--side-ink) 12%, transparent); }
.sn-avatar {
  width: 28px; height: 28px; border-radius: 50%; flex-shrink: 0;
  display: inline-flex; align-items: center; justify-content: center;
  background: var(--side-active); color: var(--side-active-on); font-weight: 700; font-size: 13px;
}
.sn-user-name { flex: 1; text-align: left; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sn-user-caret { color: var(--side-muted); }

/* ── 顶栏内容：面包屑与环境徽章 ── */
.crumbs { display: flex; align-items: center; gap: 8px; font-size: 14px; min-width: 0; }
.crumb-group { color: var(--ink-3); white-space: nowrap; }
.crumb-sep { color: var(--ink-3); }
.env-badges { margin-left: auto; display: flex; gap: 6px; align-items: center; flex-wrap: nowrap; }
.env-badge {
  font-size: 12px; line-height: 1; padding: 5px 8px; border-radius: 999px; white-space: nowrap;
  border: 1px solid var(--brd); color: var(--ink-2); background: var(--surface); cursor: default;
}
.env-badge.env-prod { color: var(--good-ink); border-color: currentColor; }
.env-badge.env-test { color: var(--warn-ink); border-color: currentColor; }
.env-badge.env-custom, .env-badge.env-unknown { color: var(--err-ink); border-color: currentColor; }
.env-badge.env-unconfigured { color: var(--ink-3); }
.is-mobile .crumb-group, .is-mobile .crumb-group + .crumb-sep { display: none; }
/* 手机上不显示分组，页面名换成带服务商名的完整页名 */
.crumb-full { display: none; }
.is-mobile .crumb-short { display: none; }
.is-mobile .crumb-full { display: inline; }
.is-mobile .env-badge { padding: 4px 6px; font-size: 11px; }
.crumb-link { font-weight: 500; color: var(--ink-2); }
.crumb-tab { font-weight: 600; }
.crumb-page { font-weight: 600; color: var(--ink); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
</style>
