// 结果未知的请求（代理商下单、管理员调账）落 localStorage：刷新、关标签页重开、新标签页都能找回同一个请求号，
// 原样重试时服务端按请求号找回或拒绝重做，不会再扣一次钱、再入一次账。
// - 只存「归属 + 请求号 + 请求内容（商品编码、数量、备注、期望单价；调账金额、原因）+ 时间」，从不存码、密码、令牌。
// - 多个标签页共用一份：每次用之前都重读存储，别的标签页写入的锁定、清掉的锁定立刻生效。
//   锁定的「重读 + 写入」有 Web Locks 就在互斥锁里做；写入后读回、稍等片刻再读回，读到的不是自己的请求号
//   （两个标签页同一瞬间锁定，后写的覆盖了先写的）就不发请求，改显示对方那一笔。
// - 归属不符（同一浏览器里另一个代理商的记录）只是看不到，不删；只在核对登录身份时（discardOthers）丢弃，
//   免得还开着的旧标签页把新登录的代理商刚写入的记录删掉。
// - 锁定 24 小时后过期（和 lib/idempotency.ts 的发码锁一致）。
// - 存储不可用（隐私模式、被禁用、写不进去）时退回内存：只在本页面有效，页面不报错；
//   这时才注册 beforeunload 拦截刷新、关标签页（记录落进了存储就找得回，不用拦）。
// 没有直接用 IdempotencyKeeper：它只在构造时读一次存储，且只存键和指纹；这里要每次重读、并把归属和请求内容跟键存在同一条里。
import { IdempotencyLockedError, newIdempotencyKey, type KeeperStore } from './idempotency'

export const RETRY_TTL_MS = 24 * 3600 * 1000
/** 代理商下单的锁定记录（每个浏览器一条，记着归属的代理商登录名）。 */
export const ORDER_RETRY_KEY = 'agent-order-retry'
/** 管理员调账的锁定记录前缀，后面接代理商 ID。 */
export const ADJUST_RETRY_PREFIX = 'agent-adjust-retry:'

export type RetryStore = KeeperStore & { readonly length?: number; key?(index: number): string | null }

/** 跨标签页互斥：拿到名为 name 的锁后执行 fn，执行完放锁。 */
export type Mutex = <R>(name: string, fn: () => Promise<R>) => Promise<R>
/** 锁定写入后等多久再读回校验（等别的标签页同一瞬间的写入传过来）。 */
export const RETRY_SETTLE_MS = 50
export type RetryLockOptions = { mutex?: Mutex; settleMs?: number; sleep?: (ms: number) => Promise<void> }

/** 浏览器的 Web Locks（navigator.locks）；没有（非 https、旧浏览器、服务端渲染）时返回 undefined。 */
export function browserMutex(): Mutex | undefined {
  try {
    const locks = typeof navigator !== 'undefined' ? (navigator as any).locks : undefined
    if (!locks || typeof locks.request !== 'function') return undefined
    return <R>(name: string, fn: () => Promise<R>) => locks.request(name, fn) as Promise<R>
  } catch {
    return undefined
  }
}

const defaultSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms))

/** 浏览器的 localStorage；没有、访问或读写抛错（隐私模式、被禁用）时返回 undefined。 */
export function localStore(): RetryStore | undefined {
  try {
    if (typeof localStorage === 'undefined' || !localStorage) return undefined
    const probe = 'retry-lock:probe'
    localStorage.setItem(probe, '1')
    localStorage.removeItem(probe)
    return localStorage
  } catch {
    return undefined
  }
}

type Slot<T> = { owner: string; key: string; payload: T; at: number }

/**
 * 一条结果未知的请求锁。owner 是归属（代理商登录名；调账不分管理员，用空串）。
 * 锁定期间只许原样重试：内容相同沿用原请求号，不同抛 IdempotencyLockedError；归属不符的记录直接丢弃。
 */
export class RetryLock<T> {
  private mem: Slot<T> | null = null // 最近一次读到或写入的；存储不可用时就靠它
  private draft: { fp: string; key: string } | null = null // 还没锁定的请求号：内容不变沿用
  private durable: boolean

  constructor(
    readonly storageKey: string,
    private store: RetryStore | undefined,
    private parse: (v: unknown) => T | null,
    private now: () => number = Date.now,
    private opts: RetryLockOptions = {},
  ) {
    this.durable = !!store
  }

  /** 记录能落进存储（刷新、关标签页后找得回）；false 表示只在本页面内存里。 */
  get persistent(): boolean {
    return this.durable
  }

  private fp(payload: T): string {
    return JSON.stringify(this.parse(payload) ?? payload)
  }

  private fresh(s: Slot<T> | null): Slot<T> | null {
    return s && this.now() - s.at < RETRY_TTL_MS ? s : null
  }

  private decode(raw: string): Slot<T> | null {
    try {
      const v = JSON.parse(raw)
      if (!v || typeof v !== 'object' || typeof v.owner !== 'string' || typeof v.key !== 'string') return null
      if (!v.key || v.key.length > 100 || !Number.isFinite(v.at)) return null
      const payload = this.parse(v.payload)
      return payload ? this.fresh({ owner: v.owner, key: v.key, payload, at: v.at }) : null
    } catch {
      return null
    }
  }

  private load(): Slot<T> | null {
    if (this.durable) {
      try {
        const raw = this.store!.getItem(this.storageKey)
        const s = raw ? this.decode(raw) : null
        if (raw && !s) this.store!.removeItem(this.storageKey) // 过期或损坏
        this.mem = s
        return s
      } catch {
        this.durable = false // 读不了：本页面改用内存
      }
    }
    this.mem = this.fresh(this.mem)
    return this.mem
  }

  private save(s: Slot<T>) {
    this.mem = s
    if (!this.durable) return
    const raw = JSON.stringify(s)
    let back: string | null = null
    try {
      this.store!.setItem(this.storageKey, raw)
      back = this.store!.getItem(this.storageKey)
      if (back === raw) return
    } catch {
      /* 写不进去 */
    }
    this.takenOver(back, s.key)
    this.durable = false // 本页面改用内存，锁定照样生效，只是刷新后找不回
  }

  /** 读回的是另一笔有效记录（别的标签页同一瞬间锁定，覆盖了这一笔）：以那边为准，这一笔不发。 */
  private takenOver(back: string | null, key: string) {
    const other = back ? this.decode(back) : null
    if (other && other.key !== key) {
      this.mem = other
      throw new IdempotencyLockedError()
    }
  }

  private drop() {
    this.mem = null
    if (!this.durable) return
    try {
      this.store!.removeItem(this.storageKey)
    } catch {
      this.durable = false
    }
  }

  /** 当前锁定的那笔（每次重读存储）。归属不是 owner 的只是看不到（返回 null），不删。 */
  current(owner = ''): { key: string; payload: T } | null {
    const s = this.load()
    if (!s || s.owner !== owner) return null
    return { key: s.key, payload: s.payload }
  }

  /** 核对登录身份那一刻调用：存储里是别人的记录（同一浏览器换了账号）就丢弃。 */
  discardOthers(owner: string): void {
    const s = this.load()
    if (s && s.owner !== owner) this.drop()
  }

  /**
   * 取请求号：锁定时内容相同沿用原号、不同抛 IdempotencyLockedError；没锁定时内容不变沿用上次的号，变了换新号。
   * 存储里有别人（另一个代理商）的锁定也抛 IdempotencyLockedError：不覆盖别人的记录。
   */
  keyFor(payload: T, owner = ''): string {
    const fp = this.fp(payload)
    const s = this.load()
    if (s && s.owner !== owner) throw new IdempotencyLockedError()
    const cur = s ? { key: s.key, payload: s.payload } : null
    if (cur) {
      if (this.fp(cur.payload) !== fp) throw new IdempotencyLockedError()
      return cur.key
    }
    if (!this.draft || this.draft.fp !== fp) this.draft = { fp, key: newIdempotencyKey() }
    return this.draft.key
  }

  /** 锁定并落盘（同步，写入后立刻读回），之后只许原样重试。返回请求号。页面发请求前用 acquire。 */
  lock(payload: T, owner = ''): string {
    const key = this.keyFor(payload, owner)
    this.save({ owner, key, payload: this.parse(payload) ?? payload, at: this.now() })
    return key
  }

  /**
   * 发请求前调用：锁定落盘，并防住两个标签页同一瞬间锁定（读-改-写不是原子的，后写的会覆盖先写的）。
   * 有 Web Locks 就把「重读 + 写入 + 读回」放进互斥锁；再稍等片刻读回，请求号不是自己的就抛 IdempotencyLockedError（请求还没发，安全）。
   * 读回时记录不见了（极少见，别处刚清掉）就补写回去，照常发。存储不可用时只在内存里锁定，不等待。
   */
  async acquire(payload: T, owner = ''): Promise<string> {
    const step = async () => {
      const key = this.lock(payload, owner)
      const wait = this.opts.settleMs ?? 0
      if (this.durable && wait > 0) {
        await (this.opts.sleep ?? defaultSleep)(wait)
        this.confirm(key)
      }
      return key
    }
    return this.opts.mutex && this.durable ? this.opts.mutex(this.storageKey, step) : step()
  }

  private confirm(key: string) {
    if (!this.durable) return
    let back: string | null
    try {
      back = this.store!.getItem(this.storageKey)
    } catch {
      this.durable = false
      return
    }
    const s = back ? this.decode(back) : null
    if (s && s.key === key) return
    this.takenOver(back, key)
    if (this.mem && this.mem.key === key) this.save(this.mem) // 不见了：补写回去
  }

  /** 有了明确结果或确认放弃：解锁，下一笔用新号。传 key 时只清请求号相同的那笔（别的标签页后来锁定的不动）。 */
  done(key?: string): void {
    this.draft = null
    const s = this.load()
    if (s && key && s.key !== key) return
    this.drop()
  }

  /** 退出登录：不管归属，全部清掉。 */
  clear(): void {
    this.draft = null
    this.drop()
  }
}

/** 存储里所有以 prefix 开头的键名。存储不可用、不能列举时返回空数组。 */
export function storedKeysWithPrefix(prefix: string, store: RetryStore | undefined): string[] {
  if (!store) return []
  try {
    const names: string[] = []
    const n = Number(store.length) || 0
    for (let i = 0; i < n; i++) {
      const name = store.key?.(i)
      if (name && name.startsWith(prefix)) names.push(name)
    }
    return names
  } catch {
    return []
  }
}

/** 清掉存储里所有以 prefix 开头的记录（退出登录时用）。存储不可用时什么都不做。 */
export function clearStoredPrefix(prefix: string, store: RetryStore | undefined): void {
  try {
    for (const name of storedKeysWithPrefix(prefix, store)) store!.removeItem(name)
  } catch {
    /* ignore */
  }
}

/** 要不要拦截刷新、关标签页：有结果未知的请求，且记录只在内存里（存储不可用）才拦；落进存储的找得回，拦了只是打扰。 */
export function needsLeaveGuard(hasPending: boolean, persistent: boolean): boolean {
  return hasPending && !persistent
}

export type LeaveGuard = { set(active: boolean): void; readonly active: boolean }
type ListenerTarget = {
  addEventListener(type: 'beforeunload', h: (e: BeforeUnloadEvent) => void): void
  removeEventListener(type: 'beforeunload', h: (e: BeforeUnloadEvent) => void): void
}

/** 拦截刷新、关标签页：浏览器弹「确定离开」（何时打开见 needsLeaveGuard）。没有 window（服务端渲染、测试）时什么都不做。 */
export function createLeaveGuard(target: ListenerTarget | undefined = typeof window !== 'undefined' ? window : undefined): LeaveGuard {
  let on = false
  const handler = (e: BeforeUnloadEvent) => {
    e.preventDefault()
    e.returnValue = '' // 旧版浏览器要设这个才弹
  }
  return {
    set(active: boolean) {
      if (!target || active === on) return
      on = active
      try {
        if (active) target.addEventListener('beforeunload', handler)
        else target.removeEventListener('beforeunload', handler)
      } catch {
        /* ignore */
      }
    },
    get active() {
      return on
    },
  }
}
