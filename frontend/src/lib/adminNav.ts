// 后台侧栏菜单的纯逻辑：菜单由 /ops 子路由的 meta（title / group / icon）生成，折叠状态可持久化。
// 服务商分组里的菜单项用短名（如「选卡规则」），meta.fullTitle 是带服务商名的完整页名（如「ZovoCard 选卡规则」），
// 用于浏览器标签页标题、读屏标签、折叠时的提示和手机上的面包屑（这些地方看不到分组名）。
import type { RouteRecordRaw } from 'vue-router'

export const OPS = '/ops'
export const NAV_STORE_KEY = 'ops-sidenav'
export const MOBILE_MAX = 900
export const NARROW_MAX = 1200

// feature：受哪个功能开关控制（如 'agent'），空串表示总是显示
export interface NavItem { path: string; title: string; fullTitle: string; group: string; icon: string; feature: string }
export interface NavGroup { name: string; items: NavItem[] }
export type NavPref = 'expanded' | 'collapsed' | ''

export function buildNavItems(routes: readonly RouteRecordRaw[]): NavItem[] {
  const root = routes.find((r) => r.path === OPS)
  const out: NavItem[] = []
  for (const c of root?.children || []) {
    const m = (c.meta || {}) as Record<string, string>
    // hideInNav：有标题（浏览器标签页、面包屑用）但不进侧栏，比如详情页
    if (!m.title || (c.meta as Record<string, unknown> | undefined)?.hideInNav) continue
    out.push({ path: c.path ? `${OPS}/${c.path}` : OPS, title: m.title, fullTitle: m.fullTitle || m.title, group: m.group || '', icon: m.icon || 'Document', feature: m.feature || '' })
  }
  return out
}

// 浏览器标签页标题：完整页名 · 站点名
export function pageTitle(meta: { title?: string; fullTitle?: string }, brandName: string): string {
  const brand = brandName || '控制台'
  return `${meta.fullTitle || meta.title || brand} · ${brand}`
}

export function groupNavItems(items: NavItem[]): NavGroup[] {
  const gs: NavGroup[] = []
  for (const it of items) {
    let g = gs.find((x) => x.name === it.group)
    if (!g) gs.push((g = { name: it.group, items: [] }))
    g.items.push(it)
  }
  return gs
}

export function readNavPref(): NavPref {
  try {
    const v = localStorage.getItem(NAV_STORE_KEY)
    return v === 'expanded' || v === 'collapsed' ? v : ''
  } catch {
    return ''
  }
}

export function writeNavPref(v: 'expanded' | 'collapsed') {
  try { localStorage.setItem(NAV_STORE_KEY, v) } catch { /* ignore */ }
}

/** 有手动偏好就按偏好；没有则窄屏（≤1200）默认折叠 */
export function isCollapsed(pref: NavPref, width: number): boolean {
  return pref ? pref === 'collapsed' : width <= NARROW_MAX
}
