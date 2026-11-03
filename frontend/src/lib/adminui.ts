// 后台总览待办、环境徽章、页内标签的纯逻辑。

/** 一项待办的查询状态。查询失败必须显示「获取失败」，不能当成 0，否则会误导运营。 */
export type TodoState<T = number> =
  | { state: 'loading' }
  | { state: 'ok'; value: T }
  | { state: 'fail'; error: string }

export const FAIL_TEXT = '获取失败'

export function todoText<T>(t: TodoState<T>, fmt: (v: T) => string = (v) => String(v)): string {
  if (t.state === 'loading') return '…'
  if (t.state === 'fail') return FAIL_TEXT
  return fmt(t.value)
}

/** 这项待办是否需要运营处理（失败也算：要去看看）。 */
export function todoNeedsAttention(t: TodoState<number>): boolean {
  return t.state === 'fail' || (t.state === 'ok' && t.value > 0)
}

export interface PoolOverview {
  main_balances?: { currency: string; available_balance: string }[]
  balance_error?: string
  products?: { available: number }[] | null
  needs_review?: number
  failed_events?: number
}

/** 卡池可用卡总数。 */
export function availableCards(ov: PoolOverview): number {
  return (ov.products || []).reduce((n, p) => n + (p.available || 0), 0)
}

/** 主钱包美元余额文本。余额取不到（balance_error）或没有美元项时返回 null，由调用方显示「获取失败」。 */
export function mainUsdBalance(ov: PoolOverview): string | null {
  if (ov.balance_error) return null
  const usd = (ov.main_balances || []).find((b) => b.currency === 'USD')
  return usd ? usd.available_balance : null
}

// ---------- 环境徽章 ----------

export type EnvKind = 'prod' | 'test' | 'custom' | 'unknown'
export interface EnvBadge { name: string; kind: EnvKind; label: string; url: string }

const trimUrl = (u: string) => (u || '').trim().replace(/\/+$/, '').toLowerCase()

/** 卡台：zovocard.com = 生产，sandbox.zovocard.com = 沙盒，其余按自定义。未配置时按默认生产。 */
export function zovoEnv(base: string): EnvBadge {
  const b = trimUrl(base)
  if (!b || b === 'https://zovocard.com') return { name: '卡台', kind: 'prod', label: '生产', url: base || 'https://zovocard.com' }
  if (b === 'https://sandbox.zovocard.com') return { name: '卡台', kind: 'test', label: '沙盒', url: base }
  return { name: '卡台', kind: 'custom', label: '自定义', url: base }
}

/** MarsWallex：按后端给的预设（prod/test）比对地址；空地址 = 生产预设。 */
export function marsEnv(baseUrl: string, presets: { id: string; url: string }[] | undefined): EnvBadge {
  const b = trimUrl(baseUrl)
  const prod = presets?.find((p) => p.id === 'prod')
  if (!b) return { name: 'MarsWallex', kind: 'prod', label: '生产', url: prod?.url || '' }
  const hit = presets?.find((p) => trimUrl(p.url) === b)
  if (hit?.id === 'prod') return { name: 'MarsWallex', kind: 'prod', label: '生产', url: baseUrl }
  if (hit?.id === 'test') return { name: 'MarsWallex', kind: 'test', label: '测试', url: baseUrl }
  return { name: 'MarsWallex', kind: 'custom', label: '自定义', url: baseUrl }
}

// ---------- 页内标签 ----------

/** ?tab= 的值合法才用，否则回落默认标签。 */
export function pickTab(q: unknown, valid: readonly string[], fallback: string): string {
  const v = Array.isArray(q) ? q[0] : q
  return typeof v === 'string' && valid.includes(v) ? v : fallback
}

export interface TabDef { name: string; label: string }

/** 有页内标签的后台页：路径 → 标签（第一个是默认）。页面的 useRouteTab 与顶栏面包屑共用。 */
export const PAGE_TABS: Record<string, TabDef[]> = {
  '/ops/cardpool': [
    { name: 'overview', label: '概览' }, { name: 'cards', label: '卡' }, { name: 'holders', label: '持卡人' },
    { name: 'orders', label: '单据' }, { name: 'txns', label: '交易' }, { name: 'policy', label: '策略与健康度' },
  ],
  '/ops/marswallex-wallet': [
    { name: 'accounts', label: '账户' }, { name: 'addresses', label: '充值地址' }, { name: 'conversion', label: '币种兑换' },
    { name: 'transfer', label: '转账' }, { name: 'serials', label: '流水' },
  ],
  '/ops/marswallex-pay': [
    { name: 'test', label: '发起付款' }, { name: 'orders', label: '付款订单' }, { name: 'config', label: '付款配置' },
  ],
  '/ops/cdkeys': [
    { name: 'codes', label: '卡密' }, { name: 'swaps', label: '代理换码核对' },
  ],
}

/** 面包屑第三级：当前页有标签定义且标签合法时返回标签名。 */
export function tabCrumb(tabs: Record<string, TabDef[]> | undefined, path: string, q: unknown): string {
  const defs = tabs?.[path]
  if (!defs?.length) return ''
  const name = pickTab(q, defs.map((t) => t.name), defs[0].name)
  return defs.find((t) => t.name === name)?.label || ''
}
