// 功能开关（后台「系统 → 功能开关」）。代理商功能默认关闭：关闭时侧栏不显示代理商分组，
// 直接打开这些页面会被送回总览；代理商登录页只显示「暂未开放」。老的代理换码页不归这个开关管。
import { reactive } from 'vue'
import type { NavItem } from './adminNav'
import { adminApi } from './provider'

export const AGENT_DISABLED_TEXT = '代理商功能暂未开放'

export interface FeatureState { loaded: boolean; agent: boolean }

/** 后台布局和功能开关页共用的开关状态；读到之前 loaded 为 false，代理商菜单先不显示。 */
export const adminFeatures = reactive<FeatureState>({ loaded: false, agent: false })

/** 菜单或页面的 meta.feature：空表示不受开关控制；不认识的功能名当作关闭。 */
export function featureOn(state: FeatureState, feature?: string): boolean {
  if (!feature) return true
  if (feature === 'agent') return state.agent
  return false
}

export function visibleNavItems(items: NavItem[], state: FeatureState): NavItem[] {
  return items.filter((it) => featureOn(state, it.feature))
}

/** 已经确认开关关闭时，受控页面要离开；还没读到开关时不动（读接口失败不该把人踢走）。 */
export function shouldLeaveFeaturePage(meta: Record<string, unknown> | undefined, state: FeatureState): boolean {
  const f = typeof meta?.feature === 'string' ? meta.feature : ''
  return !!f && state.loaded && !featureOn(state, f)
}

/** 后端在代理商功能关闭时对受控接口返回 404 agent_disabled。 */
export function isAgentDisabled(status: number, data: any): boolean {
  return status === 404 && !!data && data.error === 'agent_disabled'
}

/** 读开关；失败时保持原状（loaded 仍为 false 就不显示代理商菜单）。 */
export async function loadAdminFeatures(): Promise<void> {
  const r = await adminApi<{ agent_enabled?: boolean }>('/features')
  if (r.ok) {
    adminFeatures.agent = r.data?.agent_enabled === true
    adminFeatures.loaded = true
  }
}

/** 保存代理商功能开关；成功后更新共用状态，返回错误文字（成功为空串）。 */
export async function saveAgentFeature(on: boolean): Promise<string> {
  const r = await adminApi<{ agent_enabled?: boolean; error?: string }>('/features', 'PUT', { agent_enabled: on })
  if (!r.ok) return (r.data && typeof r.data.error === 'string' && r.data.error) || '保存失败，请稍后再试'
  adminFeatures.agent = r.data?.agent_enabled === true
  adminFeatures.loaded = true
  return ''
}
