// MarsWallex 接入配置页「最近一次收到 Webhook」的显示规则。

/** 超过这么久（秒）没收到 Webhook，或从未收到，就显示说明行；与后端 cardpool.WebhookQuietWindow（7 天）一致。 */
export const WEBHOOK_QUIET_SECONDS = 7 * 86400

/** 没收到 Webhook 时显示的说明。 */
export const WEBHOOK_HINT = '没配置通知地址时，收不到 3DS 验证码；外部冻结、持卡人审核结果已改为每 10 分钟定时查询。'

const p2 = (n: number) => String(n).padStart(2, '0')

/** Unix 秒转北京时间 YYYY-MM-DD HH:mm:ss（格式同 payprobe 的 fmtDate），固定按 UTC+8 算，与浏览器所在时区无关。 */
export function fmtBeijing(ts: number): string {
  const d = new Date((ts + 8 * 3600) * 1000)
  return `${d.getUTCFullYear()}-${p2(d.getUTCMonth() + 1)}-${p2(d.getUTCDate())} ${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}:${p2(d.getUTCSeconds())}`
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
