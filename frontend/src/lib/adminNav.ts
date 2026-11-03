// 后台侧栏菜单的纯逻辑：菜单由 /ops 子路由的 meta（title / group / icon）生成，折叠状态可持久化。
import type { RouteRecordRaw } from 'vue-router'

export const OPS = '/ops'
export const NAV_STORE_KEY = 'ops-sidenav'
export const MOBILE_MAX = 900
export const NARROW_MAX = 1200

export interface NavItem { path: string; title: string; group: string; icon: string }
export interface NavGroup { name: string; items: NavItem[] }
export type NavPref = 'expanded' | 'collapsed' | ''

export function buildNavItems(routes: readonly RouteRecordRaw[]): NavItem[] {
  const root = routes.find((r) => r.path === OPS)
  const out: NavItem[] = []
  for (const c of root?.children || []) {
    const m = (c.meta || {}) as Record<string, string>
    if (!m.title) continue
    out.push({ path: c.path ? `${OPS}/${c.path}` : OPS, title: m.title, group: m.group || '', icon: m.icon || 'Document' })
  }
  return out
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
