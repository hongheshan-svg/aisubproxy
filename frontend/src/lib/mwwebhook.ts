// MarsWallex 接入配置页「最近一次收到 Webhook」的显示规则。

/** 超过这么久（秒）没收到 Webhook，或从未收到，就显示说明行；与后端 cardpool.WebhookQuietWindow（7 天）一致。 */
export const WEBHOOK_QUIET_SECONDS = 7 * 86400

/** 没收到 Webhook 时显示的说明。 */
export const WEBHOOK_HINT = '没配置通知地址时，收不到 3DS 验证码；外部冻结、持卡人审核结果已改为每 10 分钟定时查询。'

const p2 = (n: number) => String(n).padStart(2, '0')

/** Unix 秒转北京时间 YYYY-MM-DD HH:mm:ss（后台所有时间都用它），固定按 UTC+8 算，与浏览器所在时区无关。 */
export function fmtBeijing(ts: number): string {
  const d = new Date((ts + 8 * 3600) * 1000)
  return `${d.getUTCFullYear()}-${p2(d.getUTCMonth() + 1)}-${p2(d.getUTCDate())} ${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}:${p2(d.getUTCSeconds())}`
}

// 后端 SQLite CURRENT_TIMESTAMP / strftime 给出的时间：UTC，`YYYY-MM-DD HH:MM:SS`，没有时区标记。
// 浏览器会把它当本地时间（Safari 干脆解析失败），所以先按 UTC 手动拆开。
const SQLITE_UTC = /^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})(?:\.\d+)?$/

/** 服务端时间串转毫秒：无时区标记的 SQLite 串按 UTC，其余（RFC3339 等）交给 Date.parse；解析不了返回 NaN。 */
export function parseServerTimeMs(raw: string): number {
  const s = raw.trim()
  const m = SQLITE_UTC.exec(s)
  return m ? Date.UTC(Number(m[1]), Number(m[2]) - 1, Number(m[3]), Number(m[4]), Number(m[5]), Number(m[6])) : Date.parse(s)
}

/** 后台通用：秒数（number）或服务端时间串都按北京时间显示；空值显示 —，解析不了的串原样返回。 */
export function fmtBeijingAny(v: unknown): string {
  if (v === null || v === undefined || v === '' || v === 0) return '—'
  const ms = typeof v === 'number' ? v * 1000 : parseServerTimeMs(String(v))
  if (!Number.isFinite(ms)) return String(v)
  return fmtBeijing(Math.floor(ms / 1000))
}

/** 「最近一次收到 Webhook：」后面的文字：没收到过（null、0）显示「从未」，否则显示北京时间。 */
export function lastWebhookText(at?: number | null): string {
  if (!at || at <= 0) return '从未'
  return `${fmtBeijing(at)}（北京时间）`
}

/** 是否显示说明行：从未收到，或最近一次收到已超过 7 天。nowSec 为当前 Unix 秒。 */
export function showWebhookHint(at: number | null | undefined, nowSec: number): boolean {
  if (!at || at <= 0) return true
  return nowSec - at > WEBHOOK_QUIET_SECONDS
}
