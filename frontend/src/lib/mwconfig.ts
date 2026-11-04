// MarsWallex 接入配置页的纯函数：文案映射、公钥转换、Webhook 地址可达性判断。

/** 「复制为一行 Base64」：去掉 PEM 的首尾行和所有换行、空白。已经是一行 Base64 的原样返回。 */
export function toOneLineBase64(pem: string): string {
  return pem
    .split(/\r?\n/)
    .filter((l) => !l.trim().startsWith('-----'))
    .join('')
    .replace(/\s+/g, '')
}

const OUTCOME_LABEL: Record<string, string> = {
  success: '连接成功',
  rejected: '被拒',
  unknown: '结果未知',
  not_sent: '未发出',
}

/** 测试结果的 outcome 翻成中文；其他值照原样显示。 */
export function outcomeLabel(outcome: string | undefined): string {
  if (!outcome) return ''
  return OUTCOME_LABEL[outcome] ?? outcome
}

export const ACCOUNT_TYPE_LABEL: Record<string, string> = {
  MAIN: '主钱包',
  SUB_ACCOUNT: '子账户',
}

/** 余额表「类型」列：已知的翻成中文，其他值照原样，没有值显示 —。 */
export function accountTypeLabel(t: string | undefined): string {
  if (!t) return '—'
  return ACCOUNT_TYPE_LABEL[t] ?? t
}

/** 401 / 403 时不显示 reason：signature_missing 指平台的错误回复没带签名，会误导人以为我方签名有问题。 */
export function showTestReason(httpStatus: number | undefined): boolean {
  return httpStatus !== 401 && httpStatus !== 403
}

export const WEBHOOK_UNREACHABLE_HINT =
  '这个地址 MarsWallex 访问不到。请用公网域名、通过 HTTPS 打开后台，再复制这里的地址；没有域名可以用 <IP 改横杠>.sslip.io。'

function privateIPv4(host: string): boolean {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host)
  if (!m) return false
  const a = Number(m[1])
  const b = Number(m[2])
  return a === 127 || a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168)
}

function privateIPv6(host: string): boolean {
  const h = host.replace(/^\[|\]$/g, '').toLowerCase()
  if (!h.includes(':')) return false
  if (h === '::1') return true
  return /^f[cd][0-9a-f]{2}:/.test(h) || /^f[cd][0-9a-f]{2}$/.test(h) // fc00::/7
}

/** Webhook 地址 MarsWallex 是否访问不到：协议是 http，或主机是 localhost、本机、内网地址（10/8、172.16/12、192.168/16、::1、fc00::/7）。 */
export function webhookUnreachable(url: string | undefined | null): boolean {
  if (!url) return false
  let u: URL
  try {
    u = new URL(url)
  } catch {
    return false
  }
  if (u.protocol === 'http:') return true
  const host = u.hostname.toLowerCase()
  return host === 'localhost' || privateIPv4(host) || privateIPv6(host)
}

/** 服务器设置了环境代理（HTTPS_PROXY 等）时的提示；host 为空表示没有。 */
export function envProxyWarning(host: string | undefined | null): string {
  if (!host) return ''
  return `服务器设置了环境代理（${host}），访问 MarsWallex 会经过它，白名单要填这个代理的出口 IP，上面两行不是平台看到的地址。`
}
