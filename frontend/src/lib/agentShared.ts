// 对接异次元（acg-faka）：页面用的文案和小工具。商户密钥不在这里保存。
import { fmtUsd, parseSignedUsd } from './agentMoney'

export type SharedStatus = 'inactive' | 'active' | 'admin_disabled'

export const SHARED_STATUS_TEXT: Record<SharedStatus, string> = {
  inactive: '未开通',
  active: '已开通',
  admin_disabled: '已被管理员关闭',
}

export const SOURCE_LABELS: Record<string, string> = { web: '后台', acg: '异次元' }

/** 订单、码、流水的来源中文名；未知或空返回空串。 */
export function sourceLabel(s?: string | null): string {
  return s && Object.prototype.hasOwnProperty.call(SOURCE_LABELS, s) ? SOURCE_LABELS[s] : ''
}

export const FAILURE_REASON_TEXT: Record<string, string> = {
  insufficient_balance: '余额不足',
  not_ready: '暂时缺货',
  product_unavailable: '商品停售',
  request_conflict: '请求号冲突',
  rate_limited: '请求太频繁',
  busy: '系统繁忙',
  invalid_request: '参数不正确',
}

export function failureBanner(f?: { total: number; insufficient_balance: number } | null): string {
  if (!f || !(f.total > 0)) return ''
  return `最近 24 小时有 ${f.total} 笔异次元订单取码失败（余额不足 ${f.insufficient_balance || 0} 笔），客户可能已付款但没收到码`
}

export function lowBalanceBanner(lb?: { threshold_cents: number; below: boolean } | null): string {
  if (!lb || !lb.below) return ''
  return `余额低于提醒线 ${fmtUsd(lb.threshold_cents)}`
}

/** 按逗号、空白、换行切分，去空、去重；最多 5 项。 */
export function parseAllowlistInput(text: string): { ips: string[]; error: string } {
  const ips: string[] = []
  for (const t of String(text ?? '').split(/[,\s]+/)) {
    if (t && !ips.includes(t)) ips.push(t)
  }
  return { ips, error: ips.length > 5 ? 'IP 白名单最多 5 项' : '' }
}

const LOW_BALANCE_RANGE_ERROR = '提醒线需为 0 到 10000 美元'

/** 提醒线输入（美元）→ 美分；空或 0 表示关闭，范围 0 到 10000 美元。 */
export function parseLowBalanceInput(text: string): { ok: true; cents: number } | { ok: false; error: string } {
  const s = String(text ?? '').trim()
  if (s === '' || /^0+(\.0*)?$/.test(s)) return { ok: true, cents: 0 }
  const r = parseSignedUsd(s)
  if (!r.ok) return r
  if (r.cents < 0 || r.cents > 1000000) return { ok: false, error: LOW_BALANCE_RANGE_ERROR }
  return { ok: true, cents: r.cents }
}

export const ACG_SETUP_STEPS: string[] = [
  '异次元后台「店铺共享 → 新增店铺」，填上游地址、商户 ID、商户密钥；',
  '在店铺档案里把币种设为 USD，并填好汇率（1 美元 = ? 元）——不设会把 $3.25 当成 ¥3.25 卖，亏本；',
  '「导入商品」时设加价，建议打开价格同步；',
  '本站调价后，到异次元点「同步」，否则下单时会被异次元以「商品价格已变动」拦下。',
]

export const KEY_ONCE_WARNING = '密钥只显示这一次，关掉后看不到，丢了只能重置'
export const RESET_WARNING = '重置后旧密钥立即失效，请马上到异次元里更新'
export const KEY_INVALID_TEXT = '密钥已失效，请重置'
