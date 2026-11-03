// MarsWallex 兑换（付款引擎）后台页面的公共函数。
import { authFetch } from './api'
import type { Resp } from './cardpool'

/** 调用 /api/v1/admin/pay 下的接口，写法同 poolApi。 */
export async function payApi<T = any>(path: string, method = 'GET', body?: unknown): Promise<Resp<T>> {
  try {
    const r = await authFetch('/api/v1/admin/pay' + path, {
      method,
      body: body === undefined ? undefined : JSON.stringify(body),
    })
    const data = await r.json().catch(() => ({}))
    return { ok: r.ok, status: r.status, data }
  } catch {
    return { ok: false, status: 0, data: { error: '网络错误' } as any }
  }
}

const STATE_LABEL: Record<string, string> = {
  queued: '已受理', checking: '预检中', link_created: '已生成链接', opening: '打开支付页',
  funding: '备卡中', filling: '填表中', submitted: '已提交', requires_3ds: '等待 3DS',
  verifying: '确认中', succeeded: '成功', failed: '失败', needs_review: '待人工', previewed: '试运行完成',
}

export const STATES = Object.keys(STATE_LABEL)

export function stateLabel(state: string): string {
  return STATE_LABEL[state] ?? state
}

const FAILURE_LABEL: Record<string, string> = {
  not_submitted: '提交前放弃，未扣款',
  declined: '提交后被拒',
}

export function failureLabel(f: string): string {
  return FAILURE_LABEL[f] ?? f
}

/** 订单详情「失败原因」的类别文字。人工定论的单显示定论本身：管理员判定没扣款时，系统记的 declined（提交后被拒）会误导。 */
export function failureText(failure: string, resolvedBy: string): string {
  return resolvedBy ? '人工定论为失败' : failureLabel(failure)
}

const PAGE_OUTCOME_LABEL: Record<string, string> = {
  success: '支付页显示成功',
  declined: '支付页显示被拒',
  form_rejected: '表单校验未通过',
  unknown: '没有明确结果',
}

export function pageOutcomeLabel(o: string): string {
  if (!o) return '—'
  return PAGE_OUTCOME_LABEL[o] ?? o
}

const TERMINAL = new Set(['succeeded', 'failed', 'needs_review', 'previewed'])

export function isTerminal(state: string): boolean {
  return TERMINAL.has(state)
}

/** 币种小数位，与后端一致：JPY、CLP 为 0，其余为 2。 */
export function currencyDigits(currency: string): number {
  return currency === 'JPY' || currency === 'CLP' ? 0 : 2
}

/** 主单位金额转最小单位（Math.round 避免 19.99 × 100 的浮点误差）。 */
export function toMinor(amount: number, currency: string): number {
  return Math.round(amount * 10 ** currencyDigits(currency))
}

/** 最小单位转主单位。 */
export function fromMinor(minor: number, currency: string): number {
  return minor / 10 ** currencyDigits(currency)
}

/** 最小单位金额按币种小数位、千分位显示，后接币种代码，如 1,120.00 PHP。 */
export function formatMinor(minor: number, currency: string): string {
  const d = currencyDigits(currency)
  const neg = minor < 0
  const abs = Math.abs(minor)
  const unit = 10 ** d
  const whole = Math.floor(abs / unit)
  const frac = abs - whole * unit
  const w = String(whole).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const text = d > 0 ? `${w}.${String(frac).padStart(d, '0')}` : w
  return `${neg ? '-' : ''}${text} ${currency}`
}

/** 点数档（kind=credit）：没有 ChatGPT 套餐名，按点数数量下单。 */
export function isCreditPlan(p: { kind?: string } | null | undefined): boolean {
  return p?.kind === 'credit'
}

/** 接口返回的内置价格（后端 BuiltinRegionPrice）：套餐键 → 地区 → 价格，只含美国、菲律宾。 */
export interface BuiltinPrice { currency: string; expect_minor: number; floor_minor: number; cap_minor: number }
export type BuiltinPrices = Record<string, Record<string, BuiltinPrice>>

// ---------- 付款配置：套餐表与编辑表单互转 ----------

export interface RegionConf { enabled: boolean; currency: string; expect_minor?: number; floor_minor?: number; cap_minor: number; fund_cents: number }
export interface PlanConf {
  key: string; label: string; chatgpt_plan_name: string; expect_plan_type: string
  kind?: string; credit_quantity?: number
  regions: Record<string, RegionConf>
}
/** 表单里的金额是主单位（PHP 565.00 填 565），保存时再换回最小单位。 */
export interface FormRow {
  region: string; enabled: boolean; initEnabled: boolean; currency: string
  expect: number | null; floor: number | null; cap: number | null; fund: number | null
}
export interface FormPlan {
  key: string; label: string; chatgpt_plan_name: string; expect_plan_type: string
  kind: string; credit_quantity: number; rows: FormRow[]
}

export function planToForm(p: PlanConf, regions: readonly string[]): FormPlan {
  return {
    key: p.key, label: p.label, chatgpt_plan_name: p.chatgpt_plan_name, expect_plan_type: p.expect_plan_type,
    kind: p.kind || '', credit_quantity: p.credit_quantity || 0,
    rows: regions.map((r) => {
      const rc = p.regions?.[r] || { enabled: false, currency: '', expect_minor: 0, floor_minor: 0, cap_minor: 0, fund_cents: 0 }
      return {
        region: r, enabled: rc.enabled, initEnabled: rc.enabled, currency: rc.currency,
        expect: fromMinor(rc.expect_minor || 0, rc.currency), floor: fromMinor(rc.floor_minor || 0, rc.currency), cap: fromMinor(rc.cap_minor, rc.currency), fund: rc.fund_cents / 100,
      }
    }),
  }
}

/** 表单转回套餐表。每个地区都写出来：服务端要求套餐表完整（11 个套餐 × 5 个地区），缺了会整体拒绝。 */
export function formToPlan(p: FormPlan): PlanConf {
  return {
    key: p.key, label: p.label, chatgpt_plan_name: p.chatgpt_plan_name, expect_plan_type: p.expect_plan_type,
    kind: p.kind, credit_quantity: p.credit_quantity,
    regions: Object.fromEntries(p.rows.map((row) => {
      const cur = row.currency.trim().toUpperCase()
      return [row.region, {
        enabled: row.enabled, currency: cur,
        expect_minor: toMinor(row.expect || 0, cur), floor_minor: toMinor(row.floor || 0, cur), cap_minor: toMinor(row.cap || 0, cur), fund_cents: Math.round((row.fund || 0) * 100),
      }]
    })),
  }
}

/** 把某个地区的行恢复成内置价格（币种、预期金额、最低金额、上限）；不动启用开关和注资金额。订阅和点数都能用，没有内置价返回 false。 */
export function applyBuiltinPrice(p: FormPlan, region: string, builtin: BuiltinPrices | undefined): boolean {
  const row = p.rows.find((r) => r.region === region)
  const b = builtin?.[p.key]?.[region]
  if (!row || !b) return false
  row.currency = b.currency
  row.expect = fromMinor(b.expect_minor, b.currency)
  row.floor = fromMinor(b.floor_minor, b.currency)
  row.cap = fromMinor(b.cap_minor, b.currency)
  return true
}

/** 套餐在哪些地区有内置价（按地区顺序）。 */
export function builtinRegions(p: FormPlan, builtin: BuiltinPrices | undefined): string[] {
  return p.rows.map((r) => r.region).filter((r) => !!builtin?.[p.key]?.[r])
}

/** 表格下方的说明：内置价覆盖哪些地区。 */
export const BUILTIN_NOTE = '美国、菲律宾的预期金额、最低金额、上限已按官方价格内置（可改）；其他地区要自己填。'

/** 金额上限低于预期金额时的提示（两者都大于 0 才比较），与后端保存时的校验一致。 */
export function capBelowExpect(capMinor: number, expectMinor: number): boolean {
  return capMinor > 0 && expectMinor > 0 && capMinor < expectMinor
}

/** 最低金额的保存问题：高于上限或高于预期金额时后端会拒绝（被比较的一方大于 0 才比），没问题返回空串。 */
export function floorProblem(floorMinor: number, expectMinor: number, capMinor: number): string {
  if (!(floorMinor > 0)) return ''
  if (capMinor > 0 && floorMinor > capMinor) return '最低金额高于上限，保存会被拒绝'
  if (expectMinor > 0 && floorMinor > expectMinor) return '最低金额高于预期金额，保存会被拒绝'
  return ''
}

// payBlockReason 返回这个套餐与地区不能提交的原因，可以提交时返回空串。
// 试运行只要求有 ChatGPT 套餐名（点数档不需要）；真实付款还要求地区已启用且金额都已配置。与后端 ResolvePlan 的规则一致。
export function payBlockReason(
  plan: { chatgpt_plan_name: string; kind?: string; regions?: Record<string, { enabled: boolean; cap_minor: number; fund_cents: number; floor_minor?: number }> } | undefined,
  region: string,
  dryRun: boolean,
): string {
  if (!plan) return '请选择套餐'
  if (!isCreditPlan(plan) && !plan.chatgpt_plan_name) return '该套餐没有 ChatGPT 套餐名，无法提交'
  if (dryRun) return ''
  const rc = plan.regions?.[region]
  if (!rc || !rc.enabled) return '该套餐在此地区未启用，不能真实付款（可先试运行）'
  if (!(rc.cap_minor > 0) || !(rc.fund_cents > 0)) return '该套餐在此地区的金额未配置，不能真实付款（可先试运行）'
  if (!((rc.floor_minor || 0) > 0)) return '该套餐在此地区未配置最低金额，不能真实付款（可先试运行）'
  return ''
}

// ---------- 地区代理测试 ----------
export interface ProxyTestResult {
  region: string
  proxy_configured: boolean
  ok: boolean
  ip?: string
  loc?: string
  colo?: string
  latency_ms?: number
  matches_region?: boolean
  error?: string
}

const COUNTRY_NAME: Record<string, string> = { PH: '菲律宾', US: '美国', JP: '日本', CL: '智利', EG: '埃及', SG: '新加坡' }

/** 代理测试结果的展示文案；tone 对应界面颜色：ok 绿、warn 警告色、err 红。 */
export function proxyTestText(r: ProxyTestResult): { tone: 'ok' | 'warn' | 'err'; text: string } {
  if (!r.ok) return { tone: 'err', text: `测试失败：${r.error || '原因未知'}` }
  const loc = r.loc || '未知'
  if (r.matches_region === false) {
    return { tone: 'warn', text: `出口 IP ${r.ip}，识别为 ${loc}，与 ${r.region} 不一致，可能拿不到该地区价格，也更容易被风控（耗时 ${r.latency_ms ?? 0} ms）` }
  }
  const name = COUNTRY_NAME[loc] ? `（${COUNTRY_NAME[loc]}）` : ''
  return { tone: 'ok', text: `出口 IP ${r.ip}，OpenAI 识别为 ${loc}${name}，耗时 ${r.latency_ms ?? 0} ms` }
}
