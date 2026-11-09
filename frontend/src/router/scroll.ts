// 路由滚动：只处理带 hash 的跳转（如页脚的 /#help），其余保持浏览器默认行为
export function hashScroll(to: { hash: string }, reduced: boolean): false | { el: string; behavior: 'smooth' | 'auto' } {
  if (!to.hash) return false
  return { el: to.hash, behavior: reduced ? 'auto' : 'smooth' }
}

export function prefersReducedMotion(): boolean {
  try {
    return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches
  } catch {
    return false
  }
}
