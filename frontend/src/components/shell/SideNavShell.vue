<template>
  <div
    class="app-shell"
    :class="{ 'is-collapsed': railMode, 'is-mobile': isMobile, 'is-drawer-open': drawerOpen }"
  >
    <div v-if="isMobile && drawerOpen" class="modal-mask sn-mask" @click="drawerOpen = false" />

    <aside
      :id="navId"
      ref="asideEl"
      class="sidenav"
      :class="{ 'is-collapsed': railMode }"
      :aria-hidden="isMobile && !drawerOpen ? 'true' : undefined"
      :inert="isMobile && !drawerOpen ? true : undefined"
      @keydown.esc="drawerOpen = false"
    >
      <div class="sn-brand" @click="go(homePath)">
        <span v-if="logoSrc && !logoFailed" class="sn-logo-tile">
          <img :src="logoSrc" alt="" @error="logoFailed = true" />
        </span>
        <span v-if="!railMode" class="sn-brand-text">
          <span class="sn-brand-name">{{ brandName }}</span>
          <span v-if="subLabel" class="sn-brand-sub">{{ subLabel }}</span>
        </span>
        <span v-else-if="!logoSrc || logoFailed" class="sn-initial" aria-hidden="true">{{ brandName.slice(0, 1) }}</span>
      </div>

      <nav class="sn-nav" :aria-label="navLabel">
        <template v-for="g in groups" :key="g.name || '_root'">
          <div v-if="g.name && !railMode" class="sn-group" :class="{ 'has-active': g.items.some((i) => isActive(i.path)) }">{{ g.name }}</div>
          <div v-else-if="g.name && railMode" class="sn-sep" />
          <el-tooltip
            v-for="it in g.items"
            :key="it.path"
            :content="it.fullTitle"
            placement="right"
            :disabled="!railMode"
          >
            <router-link
              :to="it.path"
              class="sn-item"
              :class="{ active: isActive(it.path) }"
              :aria-label="it.fullTitle"
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
        <button
          v-if="!isMobile"
          type="button"
          class="sn-collapse"
          :title="railMode ? '展开菜单' : '折叠菜单'"
          :aria-label="railMode ? '展开菜单' : '折叠菜单'"
          :aria-expanded="!railMode"
          :aria-controls="navId"
          @click="toggleCollapse"
        >
          <el-icon :size="18"><component :is="railMode ? 'Expand' : 'Fold'" /></el-icon>
        </button>
        <slot name="foot" :rail="railMode" :mobile="isMobile" />
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
          :aria-controls="navId"
          @click="openDrawer"
        >
          <el-icon :size="20"><Menu /></el-icon>
        </button>
        <slot name="topbar" />
      </header>

      <main class="page"><slot /></main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { readNavPref, writeNavPref, isCollapsed, MOBILE_MAX, type NavGroup, type NavPref } from '../../lib/adminNav'

const props = defineProps<{
  groups: NavGroup[]
  isActive: (path: string) => boolean
  brandName: string
  /** 空串表示没有 Logo */
  logoSrc: string
  subLabel: string
  navLabel: string
  navId: string
  prefKey: string
  homePath: string
}>()

const route = useRoute()
const router = useRouter()

// Logo 加载失败后隐藏图片、只显示站名；换了 Logo 就重新试
const logoFailed = ref(false)
watch(() => props.logoSrc, () => { logoFailed.value = false })

function go(p: string) {
  drawerOpen.value = false
  router.push(p)
}

// ── 折叠 / 抽屉 ──
const asideEl = ref<HTMLElement | null>(null)
const vw = ref(typeof window === 'undefined' ? 1440 : window.innerWidth)
const pref = ref<NavPref>(readNavPref(props.prefKey))
const drawerOpen = ref(false)
const isMobile = computed(() => vw.value <= MOBILE_MAX)
const collapsed = computed(() => isCollapsed(pref.value, vw.value))
// 抽屉里始终显示完整菜单
const railMode = computed(() => !isMobile.value && collapsed.value)

function toggleCollapse() {
  pref.value = railMode.value ? 'expanded' : 'collapsed'
  writeNavPref(pref.value, props.prefKey)
}
async function openDrawer() {
  drawerOpen.value = true
  await nextTick()
  asideEl.value?.querySelector<HTMLElement>('.sn-item')?.focus()
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

/* ── 侧栏：深色底，颜色全部来自 theme.ts 写在根元素上的侧栏变量 ── */
.sidenav {
  width: var(--side-w, 232px);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
  z-index: 60;
  background: var(--side-bg);
  color: var(--side-ink);
  border-right: 1px solid var(--side-brd, var(--brd));
  transition: width .18s ease;
}
.sidenav.is-collapsed { width: var(--side-w-collapsed, 64px); }

.sn-brand {
  height: var(--topbar-h, 56px);
  flex-shrink: 0;
  display: flex; align-items: center; gap: 10px;
  padding: 0 18px; cursor: pointer;
  border-bottom: 1px solid var(--side-muted);
  overflow: hidden;
}
.is-collapsed .sn-brand { justify-content: center; padding: 0; }
.sn-logo-tile {
  width: 32px; height: 32px; flex-shrink: 0;
  display: inline-flex; align-items: center; justify-content: center;
  background: #FFFFFF; border-radius: 8px; overflow: hidden;
}
.sn-logo-tile img { max-width: 100%; max-height: 100%; object-fit: contain; }
.sn-initial { font-size: 16px; font-weight: 700; color: var(--side-ink); }
.sn-brand-text { display: flex; flex-direction: column; min-width: 0; line-height: 1.2; }
.sn-brand-name {
  font-size: 15px; font-weight: 700; color: var(--side-ink);
  font-family: var(--font-display, var(--font-serif));
  min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.sn-brand-sub { font-size: 11px; color: var(--side-muted); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.sn-nav { flex: 1; overflow-y: auto; overflow-x: hidden; padding: 6px 10px 10px; }
.sn-group {
  font-size: 11px; letter-spacing: .06em; color: var(--side-muted);
  margin: 14px 0 4px; padding: 0 12px;
}
.sn-sep { height: 1px; background: var(--side-muted); opacity: .4; margin: 10px 8px; }
.sn-item {
  display: flex; align-items: center; gap: 10px;
  height: 38px; padding: 0 12px; margin-bottom: 2px;
  border-radius: var(--radius-md);
  color: var(--side-ink); text-decoration: none; font-size: 14px;
  transition: background .15s, color .15s;
}
.is-collapsed .sn-item { justify-content: center; padding: 0; }
.sn-icon { font-size: 18px; flex-shrink: 0; }
.sn-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.sn-item:hover { background: color-mix(in srgb, var(--side-ink) 12%, transparent); }
.sn-item:focus-visible,
.sn-collapse:focus-visible { outline: 2px solid var(--side-ink); outline-offset: -2px; }
.sn-item.active {
  background: var(--side-active); color: var(--side-active-on); font-weight: 700;
  box-shadow: inset 3px 0 0 #FFFFFF;
}

.sn-foot {
  flex-shrink: 0; padding: 10px; border-top: 1px solid var(--side-muted);
  display: flex; flex-direction: column; gap: 8px;
}
.is-collapsed .sn-foot { align-items: center; }
.sn-collapse {
  align-self: flex-start;
  display: inline-flex; align-items: center; justify-content: center;
  height: 34px; padding: 0 10px; border-radius: var(--radius-md);
  background: transparent; border: 1px solid transparent; color: var(--side-ink); cursor: pointer;
}
.is-collapsed .sn-collapse { align-self: center; }
.sn-collapse:hover { background: color-mix(in srgb, var(--side-ink) 12%, transparent); }

/* ── 主列顶栏 ── */
.topbar {
  height: var(--topbar-h, 56px);
  position: sticky; top: 0; z-index: 40;
  display: flex; align-items: center; gap: 10px;
  padding: 0 20px;
  background: var(--surface-solid);
  border-bottom: 1px solid var(--brd);
}
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
