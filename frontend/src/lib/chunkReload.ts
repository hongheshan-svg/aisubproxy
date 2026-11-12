// 分包文件加载失败的兜底：升级后旧分包文件已不存在，服务器回退成 index.html（HTML），动态 import 就会失败。
// 识别出这类错误后整页刷新一次拿到新版；10 秒内已经刷过一次就不再刷（避免死循环），改为提示。

/** 识别「分包 / 样式文件加载失败」。各浏览器的报错文字不同，集中在这里维护。 */
const CHUNK_ERROR_PATTERNS: RegExp[] = [
  /Failed to fetch dynamically imported module/i, // Chrome / Edge
  /Importing a module script failed/i, // Safari
  /error loading dynamically imported module/i, // Firefox
  /Unable to preload CSS/i, // Vite 预加载样式失败
]

export function isChunkLoadError(err: unknown): boolean {
  const msg = typeof err === 'string' ? err : (err as { message?: unknown } | null | undefined)?.message
  return typeof msg === 'string' && CHUNK_ERROR_PATTERNS.some((re) => re.test(msg))
}

export const RELOAD_GUARD_KEY = 'chunk-reload-at'
export const RELOAD_GUARD_MS = 10_000

export interface ChunkRecoverDeps {
  now: () => number
  /** 返回 sessionStorage 之类的存储；读写都可能抛错 */
  storage: () => Pick<Storage, 'getItem' | 'setItem'>
  /** 整页跳到目标地址 / 刷新 */
  reload: () => void
  toast: () => void
}

/** 返回 'reloaded'（已触发刷新）或 'toasted'（只提示）。存储不可用时也只提示，因为没法防止死循环。 */
export function recoverFromChunkError(deps: ChunkRecoverDeps): 'reloaded' | 'toasted' {
  try {
    const store = deps.storage()
    const last = Number(store.getItem(RELOAD_GUARD_KEY))
    const now = deps.now()
    if (Number.isFinite(last) && last > 0 && now - last < RELOAD_GUARD_MS) {
      deps.toast()
      return 'toasted'
    }
    store.setItem(RELOAD_GUARD_KEY, String(now))
  } catch {
    deps.toast()
    return 'toasted'
  }
  deps.reload()
  return 'reloaded'
}
