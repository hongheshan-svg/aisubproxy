// 发码幂等键：同一次发码的重试必须沿用同一个键，否则超时重点「购买」会再发一批。
// 键在点击时生成；请求内容变了（档位、数量、地区等）才换新的；确认成功后清掉，下一次购买用新键。

export function newIdempotencyKey(): string {
  const c: Crypto | undefined = typeof crypto !== 'undefined' ? crypto : undefined
  // 非 https 页面没有 randomUUID，退回 getRandomValues 手拼 v4
  if (c && typeof c.randomUUID === 'function') return c.randomUUID()
  const b = new Uint8Array(16)
  if (c && typeof c.getRandomValues === 'function') c.getRandomValues(b)
  else for (let i = 0; i < b.length; i++) b[i] = Math.floor(Math.random() * 256)
  b[6] = (b[6] & 0x0f) | 0x40
  b[8] = (b[8] & 0x3f) | 0x80
  const h = Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`
}

// 结果未知（超时、断连、5xx、成功但没有码）的那一批要在刷新页面、离开再进入后仍能找回：
// 键和请求指纹落到 sessionStorage，只存「键 + 请求内容（档位、数量、地区这类参数）」，从不存码。
// 结果未知期间锁定：只许用原样的请求重试（沿用同一个键，服务端按键找回或拒绝重发）；
// 内容变了就拒绝，免得换键多发一批。确认放弃（discard）或明确成功（done）才解锁。
const LOCK_TTL_MS = 24 * 3600 * 1000

export class IdempotencyLockedError extends Error {
  constructor() {
    super('上一次发码的结果还不明，请先原样再点一次发码找回；确认不要了再选「放弃上一次」')
    this.name = 'IdempotencyLockedError'
  }
}

export interface KeeperStore {
  getItem(k: string): string | null
  setItem(k: string, v: string): void
  removeItem(k: string): void
}

function browserStore(): KeeperStore | undefined {
  try {
    return typeof sessionStorage !== 'undefined' ? sessionStorage : undefined
  } catch {
    return undefined
  }
}

export class IdempotencyKeeper {
  private fp = ''
  private key = ''
  private locked = false

  constructor(
    private storageKey = '',
    private store: KeeperStore | undefined = undefined,
    private now: () => number = Date.now,
  ) {
    this.restore()
  }

  private restore() {
    if (!this.storageKey || !this.store) return
    try {
      const raw = this.store.getItem(this.storageKey)
      if (!raw) return
      const v = JSON.parse(raw)
      if (v && typeof v.fp === 'string' && typeof v.key === 'string' && v.fp && v.key && this.now() - Number(v.at) < LOCK_TTL_MS) {
        this.fp = v.fp
        this.key = v.key
        this.locked = true
      } else {
        this.store.removeItem(this.storageKey)
      }
    } catch {
      /* 存储不可用或内容损坏：当作没有 */
    }
  }

  private persist() {
    if (!this.storageKey || !this.store) return
    try {
      if (this.locked) this.store.setItem(this.storageKey, JSON.stringify({ fp: this.fp, key: this.key, at: this.now() }))
      else this.store.removeItem(this.storageKey)
    } catch {
      /* ignore */
    }
  }

  // 请求内容没变就沿用上一次的键（重试），变了才生成新的；结果未知锁定期间内容变了抛 IdempotencyLockedError
  keyFor(payload: unknown): string {
    const fp = JSON.stringify(payload)
    if (this.locked && this.key && fp !== this.fp) throw new IdempotencyLockedError()
    if (!this.key || fp !== this.fp) {
      this.fp = fp
      this.key = newIdempotencyKey()
    }
    return this.key
  }

  // 这一次的结果不明：锁定并落盘，之后只许原样重试
  markUncertain(): void {
    if (!this.key) return
    this.locked = true
    this.persist()
  }

  isLocked(): boolean {
    return this.locked
  }

  // 本次发码已明确成功：下一次购买是新的一笔
  done(): void {
    this.discard()
  }

  // 管理员确认放弃上一次结果不明的请求：清键解锁，下一次用新键
  discard(): void {
    this.fp = ''
    this.key = ''
    this.locked = false
    this.persist()
  }
}

// 模块级单例：组件重建（切换标签、离开再进入）也拿到同一个，页面刷新则由 sessionStorage 恢复。
const shared = new Map<string, IdempotencyKeeper>()
export function sharedKeeper(name: string): IdempotencyKeeper {
  let k = shared.get(name)
  if (!k) {
    k = new IdempotencyKeeper('idem-keeper:' + name, browserStore())
    shared.set(name, k)
  }
  return k
}
