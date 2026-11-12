// 检测站点是否已在线升级（F: 升级后其他开着的页面不再取旧分包）。
// 做法：后端在线升级会把前端目录整个换掉，index.html 里 Vite 入口脚本的文件名（带内容哈希）跟着变。
// 拿「当前页面加载的入口」和「服务器现在的 index.html（站点根路径）里的入口」比，不一样就是有新版本。
// 不需要后端接口，也不对外暴露版本号。
import { ref } from 'vue'
import { withTimeout } from './updateWatch'

/** 同一页面加载内两次检查至少间隔 */
export const MIN_GAP_MS = 60_000
/** 页面可见期间的定时检查间隔 */
export const POLL_INTERVAL_MS = 5 * 60_000
/** 取 index.html 的超时 */
export const FETCH_TIMEOUT_MS = 5_000

/** 把脚本地址规范成只含路径名的形式（绝对 URL、带查询串都只取 pathname）。 */
function normalizeSrc(src: string): string | null {
  try {
    return new URL(src, 'http://localhost').pathname || null
  } catch {
    return null
  }
}

/** 从 index.html 文本里找 Vite 的入口脚本（type="module" 且有 src），属性顺序不限。 */
export function entryScriptOf(html: string): string | null {
  for (const tag of html.match(/<script\b[^>]*>/gi) || []) {
    const attr = (name: string) => {
      const m = new RegExp(`\\s${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, 'i').exec(tag)
      return m ? (m[1] ?? m[2] ?? m[3] ?? '') : null
    }
    if (attr('type')?.toLowerCase() !== 'module') continue
    const src = attr('src')
    if (!src) continue
    return normalizeSrc(src)
  }
  return null
}

/** 当前页面加载的入口脚本。 */
export function currentEntry(): string | null {
  try {
    const el = document.querySelector('script[type="module"][src]')
    const src = el?.getAttribute('src')
    return src ? normalizeSrc(src) : null
  } catch {
    return null
  }
}

/** 检查一次：只有两边都取到入口、而且不一样才返回 true；任何失败都当作没有新版本。 */
export async function checkNewVersionOnce(): Promise<boolean> {
  const mine = currentEntry()
  if (!mine) return false
  return withTimeout(async (signal) => {
    // 直接取站点根路径：后端对 /index.html 会 301 到 /（http.ServeFile 的规范化），取根路径少绕一趟
    const r = await fetch(import.meta.env.BASE_URL, { cache: 'no-store', signal })
    if (r.status !== 200) return false
    const theirs = entryScriptOf(await r.text())
    return !!theirs && theirs !== mine
  }, FETCH_TIMEOUT_MS, false)
}

export interface VersionWatchDeps {
  check: () => Promise<boolean>
  isVisible: () => boolean
  /** 订阅标签页可见性变化，返回取消订阅 */
  onVisibilityChange: (cb: () => void) => () => void
  onFound: () => void
}

/** 检查时机：标签页变为可见时（60 秒节流）、可见期间每 5 分钟；检测到后全部停止。 */
export function createVersionWatcher(deps: VersionWatchDeps) {
  let timer: ReturnType<typeof setInterval> | undefined
  let off: (() => void) | undefined
  let lastAt: number | null = null
  let busy = false
  let stopped = true

  async function run() {
    if (stopped || busy) return
    if (lastAt !== null && Date.now() - lastAt < MIN_GAP_MS) return
    busy = true
    lastAt = Date.now()
    try {
      if (await deps.check()) {
        stop()
        deps.onFound()
      }
    } catch {
      /* 检查失败不当作新版本 */
    } finally {
      busy = false
    }
  }

  function start() {
    if (!stopped) return
    stopped = false
    off = deps.onVisibilityChange(() => { if (deps.isVisible()) void run() })
    timer = setInterval(() => { if (deps.isVisible()) void run() }, POLL_INTERVAL_MS)
  }

  function stop() {
    stopped = true
    if (timer !== undefined) clearInterval(timer)
    timer = undefined
    off?.()
    off = undefined
  }

  return { start, stop }
}

/** 响应式状态：当前页面是否已有新版本。 */
export const hasNewVersion = ref(false)

let started = false
/** 只在生产构建里启用；整个页面生命周期只启动一次。 */
export function startNewVersionWatch(): void {
  if (!import.meta.env.PROD || started || typeof document === 'undefined') return
  started = true
  createVersionWatcher({
    check: checkNewVersionOnce,
    isVisible: () => document.visibilityState === 'visible',
    onVisibilityChange: (cb) => {
      document.addEventListener('visibilitychange', cb)
      return () => document.removeEventListener('visibilitychange', cb)
    },
    onFound: () => { hasNewVersion.value = true },
  }).start()
}

/**
 * 路由切换时要不要整页跳转：已有新版本、不是首次导航、真正换页（路径变了；只改 query / hash 不算）。
 * 注意：组件自己的 onBeforeRouteLeave 先于全局 beforeEach 执行，被它拦下的导航到不了这里，所以不会被强制刷新。
 */
export function shouldFullReload(o: { hasNew: boolean; initial: boolean; toPath: string; fromPath: string }): boolean {
  return o.hasNew && !o.initial && o.toPath !== o.fromPath
}
