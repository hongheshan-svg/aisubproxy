// 发码幂等键：同一次发码的重试必须沿用同一个键，否则超时重点「购买」会让卡台再发一批。
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

export class IdempotencyKeeper {
  private fp = ''
  private key = ''

  // 请求内容没变就沿用上一次的键（重试），变了才生成新的
  keyFor(payload: unknown): string {
    const fp = JSON.stringify(payload)
    if (!this.key || fp !== this.fp) {
      this.fp = fp
      this.key = newIdempotencyKey()
    }
    return this.key
  }

  // 本次发码已明确成功：下一次购买是新的一笔
  done(): void {
    this.fp = ''
    this.key = ''
  }
}
