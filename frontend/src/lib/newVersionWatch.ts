// 检测站点是否已在线升级（F: 升级后其他开着的页面不再取旧分包）。
// 做法：后端在线升级会把前端目录整个换掉，index.html 里 Vite 入口脚本的文件名（带内容哈希）跟着变。
// 拿「当前页面的入口（启动时固定下来）」和「服务器现在的 index.html（当前页面路径）里的入口」比，不一样就是有新版本。
// 不需要后端接口，也不对外暴露版本号。
import { computed, ref } from 'vue'
import { withTimeout } from './updateWatch'
import { isBusy } from './busyState'

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

/** 从 index.html 文本里找所有 type="module" 且有 src 的脚本，返回规范化后的路径名（属性顺序不限）。 */
export function moduleScriptsOf(html: string): string[] {
  const out: string[] = []
  for (const tag of html.match(/<script\b[^>]*>/gi) || []) {
    const attr = (name: string) => {
      const m = new RegExp(`\\s${name}\\s*=\\s*(?:"([^"]*)"|'([^']*)'|([^\\s>]+))`, 'i').exec(tag)
      return m ? (m[1] ?? m[2] ?? m[3] ?? '') : null
    }
    if (attr('type')?.toLowerCase() !== 'module') continue
    const src = attr('src')
    const n = src ? normalizeSrc(src) : null
    if (n) out.push(n)
  }
  return out
}

/** 从 index.html 文本里找 Vite 的入口脚本（第一个 type="module" 且有 src 的脚本）。 */
export function entryScriptOf(html: string): string | null {
  return moduleScriptsOf(html)[0] ?? null
}

const dirOf = (p: string) => p.slice(0, p.lastIndexOf('/') + 1)

/**
 * 服务器返回的 HTML 相对「我的入口」是不是新版本：
 * 至少有一个和我的入口同目录（如 /assets/）的模块脚本，并且这些脚本里没有我的入口。
 * 多出一个别处注入的脚本（别的目录），或者仍然包含我的入口，都不算。
 */
export function isNewVersionHtml(mine: string, html: string): boolean {
  const scripts = moduleScriptsOf(html)
  if (scripts.includes(mine)) return false
  const dir = dirOf(mine)
  return scripts.some((s) => dirOf(s) === dir)
}

/**
 * 检查一次：取「当前页面的路径」（不带 query 和 hash），和启动时固定下来的入口比；任何失败都当作没有新版本。
 * 取当前路径而不是固定取根路径：CDN、反向代理可能只缓存了其中一个。
 */
export async function checkNewVersionOnce(mine: string): Promise<boolean> {
  return withTimeout(async (signal) => {
    // 带上 origin：路径以 // 开头时（如 //evil.example/x），单独的 pathname 会被 fetch 当成协议相对地址发到别的域名
    const r = await fetch(location.origin + location.pathname, { cache: 'no-store', signal })
    if (r.status !== 200) return false
    return isNewVersionHtml(mine, await r.text())
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

/** 提示条是否显示：有新版本，并且页面不在忙（批量兑换进行中、更新进行中等）。 */
export const showNewVersionBanner = computed(() => hasNewVersion.value && !isBusy.value)

let started = false
let watchedEntry = ''
/** 只在生产构建里启用；整个页面生命周期只启动一次。 */
export function startNewVersionWatch(entry: string): void {
  if (!import.meta.env.PROD || started || typeof document === 'undefined' || !entry) return
  started = true
  watchedEntry = entry
  createVersionWatcher({
    check: () => checkNewVersionOnce(entry),
    isVisible: () => document.visibilityState === 'visible',
    onVisibilityChange: (cb) => {
      document.addEventListener('visibilitychange', cb)
      return () => document.removeEventListener('visibilitychange', cb)
    },
    onFound: () => { hasNewVersion.value = true },
  }).start()
}

/** 立即核对一次（页内动态加载失败时用）；检测没启动（开发模式）时当作没有。 */
export function checkNewVersionNow(): Promise<boolean> {
  return watchedEntry ? checkNewVersionOnce(watchedEntry) : Promise.resolve(false)
}

/**
 * 路由切换时要不要整页跳转：已有新版本、不是首次导航、真正换页（路径变了；只改 query / hash 不算）。
 * 全站只有后台代理商详情页有离开确认（onBeforeRouteLeave，先于全局 beforeEach 执行，被它拦下的导航到不了这里）；
 * 其他页面换页本来就会卸载，整页刷新和原来的换页一样。
 */
export function shouldFullReload(o: { hasNew: boolean; initial: boolean; toPath: string; fromPath: string }): boolean {
  return o.hasNew && !o.initial && o.toPath !== o.fromPath
}
