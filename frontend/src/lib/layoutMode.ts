// 用户端电脑布局状态：窗口宽度 >= 1024px 时为真。样式里的媒体查询必须用同一个断点。
import { ref } from 'vue'

export const DESKTOP_QUERY = '(min-width: 1024px)'
export const isDesktop = ref(false)

export function initLayoutMode(win: { matchMedia?: (q: string) => MediaQueryList } | undefined = typeof window !== 'undefined' ? window : undefined): void {
  if (!win || typeof win.matchMedia !== 'function') return
  const mql = win.matchMedia(DESKTOP_QUERY)
  isDesktop.value = mql.matches
  mql.addEventListener('change', (e) => { isDesktop.value = e.matches })
}
