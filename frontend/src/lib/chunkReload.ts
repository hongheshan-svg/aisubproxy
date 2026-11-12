// 分包文件加载失败的兜底：升级后旧分包文件已不存在，服务器回退成 index.html（HTML），动态 import 就会失败。
// 识别出这类错误后整页刷新一次拿到新版。防反复刷新靠 sessionStorage 里的标记：刷新前写入，
// 换页成功（router.afterEach）后清掉；标记还在说明上一次刷新后同样的失败又出现了，这次只提示，不再刷新。
// 这样不依赖时间窗口，网络再慢也不会无限刷新。

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

export const RELOAD_MARK_KEY = 'chunk-reload-target'

type Store = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>

export interface ChunkRecoverDeps {
  /** 返回 sessionStorage 之类的存储；读写都可能抛错 */
  storage: () => Store
  /** 整页跳到目标地址 */
  reload: () => void
  toast: () => void
  /** 目标地址，写进标记里便于排查 */
  target: string
  /** 断网时分包也会取不到，不是升级：只提示，不刷新 */
  isOnline: () => boolean
}

/** 返回 'reloaded'（已触发刷新）或 'toasted'（只提示）。存储不可用时也只提示，因为没法防止死循环。 */
export function recoverFromChunkError(deps: ChunkRecoverDeps): 'reloaded' | 'toasted' {
  if (!deps.isOnline()) {
    deps.toast()
    return 'toasted'
  }
  try {
    const store = deps.storage()
    if (store.getItem(RELOAD_MARK_KEY) !== null) {
      deps.toast()
      return 'toasted'
    }
    store.setItem(RELOAD_MARK_KEY, deps.target || '1')
  } catch {
    deps.toast()
    return 'toasted'
  }
  deps.reload()
  return 'reloaded'
}

/** 换页成功后清掉标记（读写失败忽略）。 */
export function clearReloadMark(storage: () => Store): void {
  try {
    storage().removeItem(RELOAD_MARK_KEY)
  } catch {
    /* 忽略 */
  }
}
