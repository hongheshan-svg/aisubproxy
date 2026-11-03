// 免付款探测页的公共函数：探测项定义、提交前检查、结果转文本。
// 探测全部不付款；结果里不含会话、token、完整邮箱和完整结账链接（后端已脱敏）。

export type ProbeStatus = 'ok' | 'fail' | 'skipped'

export interface ProbeResult {
  key: string
  title: string
  status: ProbeStatus
  summary: string
  details?: Record<string, unknown>
}

export type ProbeRunStatus = 'running' | 'done' | 'failed'

export interface ProbeRecord {
  id: number
  status?: ProbeRunStatus
  finished_at?: number
  error?: string
  created_at: number
  admin?: string
  region?: string
  checks?: string[]
  results: ProbeResult[]
}

export interface ProbeCheckDef {
  key: string
  title: string
  /** readonly 只读；create 建结账会话（不付款）；browser 用浏览器打开页面，只看不点 */
  kind: 'readonly' | 'create' | 'browser'
  desc: string
}

export const PROBE_CHECKS: ProbeCheckDef[] = [
  { key: 'account', title: '账号概况', kind: 'readonly', desc: '套餐、订阅、计费币种、账号结构、欠费、订阅来源、是否续费' },
  { key: 'subscriptions', title: '订阅详情', kind: 'readonly', desc: 'will_renew、renews_at、billing_period、is_delinquent 等字段有没有' },
  { key: 'checkout_modes', title: '开通结账会话形态', kind: 'create', desc: '仅无订阅账号：分别不带、带 redirect 各建一次 Plus 开通会话，不付款' },
  { key: 'credit_checkout', title: '点数结账会话形态', kind: 'create', desc: '仅有订阅账号：分别不带、带 redirect 各建一次 250 点会话，不付款' },
  { key: 'payment_methods', title: '已存付款方式', kind: 'readonly', desc: '已存卡数量、有没有默认卡、备用付款方式开关' },
  { key: 'upgrade_preview', title: '升级预览', kind: 'readonly', desc: '仅有订阅账号：读更高各档的应付金额、币种、续费日' },
  { key: 'customer_portal', title: '账单门户', kind: 'browser', desc: '取门户链接，浏览器打开并截图，看有没有「添加付款方式」入口；只看不点' },
  { key: 'chatgpt_browser', title: '浏览器打开 ChatGPT', kind: 'browser', desc: '注入会话后打开 chatgpt.com 并截图，判断是否被 Cloudflare 拦截；只看不点' },
  { key: 'credit_balance', title: '点数余额接口', kind: 'readonly', desc: '读 /wham/usage，看返回结构里有哪些字段' },
]

export const PROBE_REGIONS = ['PH', 'US', 'JP', 'CL', 'EG']

const KIND_LABEL: Record<string, string> = { readonly: '只读', create: '建会话，不付款', browser: '浏览器，只看不点' }
export function probeKindLabel(kind: string): string {
  return KIND_LABEL[kind] ?? kind
}

const STATUS_LABEL: Record<string, string> = { ok: '成功', fail: '失败', skipped: '已跳过' }
export function probeStatusLabel(s: string): string {
  return STATUS_LABEL[s] ?? s
}

export function probeStatusTag(s: string): 'success' | 'danger' | 'info' {
  return s === 'ok' ? 'success' : s === 'fail' ? 'danger' : 'info'
}

/** 提交前检查：返回不能提交的原因，空串表示可以。 */
export function probeBlockReason(session: string, checks: string[], running: boolean): string {
  if (running) return '已有探测在运行，请等它结束'
  if (!session.trim()) return '请先粘贴测试账号的会话'
  if (!checks.length) return '请至少选一个探测项'
  return ''
}

/** 有浏览器探测项时，提示耗时。 */
export function probeNeedsBrowser(checks: string[]): boolean {
  return PROBE_CHECKS.some((c) => c.kind === 'browser' && checks.includes(c.key))
}

/** 按固定顺序（探测项定义的顺序）排列选中的探测项，请求体里的顺序不影响后端，这里只让展示稳定。 */
export function probeOrderChecks(checks: string[]): string[] {
  return PROBE_CHECKS.map((c) => c.key).filter((k) => checks.includes(k))
}

function fmtDate(ts: number): string {
  const d = new Date(ts * 1000)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

/** 把一次探测记录转成纯文本，用来「复制为文本」发给开发。内容就是后端已脱敏的结果，这里不再加任何会话相关的东西。 */
export function probeToText(rec: ProbeRecord): string {
  const lines = [`免付款探测 #${rec.id || '-'}　${rec.created_at ? fmtDate(rec.created_at) : ''}${rec.region ? `　地区 ${rec.region}` : ''}`]
  for (const r of rec.results || []) {
    lines.push('', `[${probeStatusLabel(r.status)}] ${r.key} ${r.title}`, `  ${r.summary}`)
    if (r.details && Object.keys(r.details).length) {
      lines.push(...JSON.stringify(r.details, null, 2).split('\n').map((l) => '  ' + l))
    }
  }
  return lines.join('\n')
}

/** 接口出错时给管理员看的话。409 是已有探测在跑。 */
export function probeErrorText(status: number, data: any): string {
  if (status === 409) return (data && data.error) || '已有探测正在运行，请等它结束再试'
  if (status === 503) return '付款引擎未启动'
  return (data && data.error) || (status === 0 ? '网络错误' : '探测失败')
}

/** 失败项的数量，用来在结果上方给个总览。 */
export function probeCounts(results: ProbeResult[]): { ok: number; fail: number; skipped: number } {
  const c = { ok: 0, fail: 0, skipped: 0 }
  for (const r of results || []) if (r.status in c) c[r.status as ProbeStatus]++
  return c
}

export const PROBE_POLL_MS = 2000

/**
 * 轮询一次探测直到跑完（status 不再是 running）。每轮先等 intervalMs 再查，逐次回调当前记录，方便边跑边显示。
 * shouldStop 为真（页面离开、被取代）时立刻停止；连续 3 次读取失败也停止，避免一直打接口。
 * getOnce、sleep 可注入，便于不依赖定时器单测。返回结束原因。
 */
export async function pollProbe(
  getOnce: () => Promise<{ ok: boolean; data: any }>,
  onUpdate: (rec: ProbeRecord) => void,
  shouldStop: () => boolean,
  sleep: (ms: number) => Promise<void> = (ms) => new Promise((r) => setTimeout(r, ms)),
  intervalMs = PROBE_POLL_MS,
): Promise<'finished' | 'stopped' | 'error'> {
  let fails = 0
  while (!shouldStop()) {
    await sleep(intervalMs)
    if (shouldStop()) break
    const r = await getOnce()
    if (shouldStop()) break
    if (!r.ok) {
      if (++fails >= 3) return 'error'
      continue
    }
    fails = 0
    onUpdate(r.data as ProbeRecord)
    if (r.data?.status !== 'running') return 'finished'
  }
  return 'stopped'
}
