// 升级后的整页跳转守卫和分包加载失败的兜底，从 router/index.ts 里抽出来，依赖都可以注入，便于测试。
import { START_LOCATION, type Router } from 'vue-router'
import { shouldFullReload } from '../lib/newVersionWatch'
import { clearReloadMark, isChunkLoadError, recoverFromChunkError } from '../lib/chunkReload'

export interface NewVersionGuardDeps {
  hasNew: () => boolean
  setHasNew: () => void
  hardNavigate: (href: string) => void
  storage: () => Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>
  toast: () => void
  isOnline: () => boolean
  /** 派发 vite:preloadError 的对象，生产里是 window */
  events: Pick<EventTarget, 'addEventListener'> | null
}

/**
 * 必须在鉴权守卫之前调用（它注册的 beforeEach 要排在最前）。
 * - 已检测到新版本、不是首次导航、真正换页（路径变了；只改 query / hash 不算）：整页跳过去拿新入口和新分包。
 *   全站只有后台代理商详情页有离开确认（onBeforeRouteLeave，先于全局 beforeEach，被拦下的导航到不了这里）；
 *   其他页面换页本来就会卸载，整页刷新和原来的换页一样。
 * - 分包加载失败：换页进行中整页跳到目标地址；标记还在（上一次刷新后又失败）或断网只提示。
 *
 * 已知限制：整页跳转若被 beforeunload 拦下且用户选了「留下」，这次导航会一直挂着，直到下一次导航取代它。
 * 只在代理商下单、管理员调账且浏览器存储不可用时才会注册 beforeunload，所以极少出现；跳转前先清掉 pendingHref，避免残留。
 */
export function installNewVersionGuard(router: Router, deps: NewVersionGuardDeps): void {
  // 正在进行的导航目标：vite:preloadError 事件里拿不到目标页，靠它判断是不是换页中
  let pendingHref: string | null = null

  router.beforeEach((to, from) => {
    const href = router.resolve(to).href
    if (shouldFullReload({ hasNew: deps.hasNew(), initial: from === START_LOCATION, toPath: to.path, fromPath: from.path })) {
      pendingHref = null
      deps.hardNavigate(href)
      // 不能 return false：后退、前进触发的导航被中止时，vue-router 会 history.go(-delta) 把地址栏退回去，
      // 这可能把刚发起的整页跳转取消掉。让这次导航一直挂着，页面马上就会被新页面替换。
      return new Promise<never>(() => {})
    }
    pendingHref = href
  })

  router.afterEach((_to, _from, failure) => {
    pendingHref = null
    if (!failure) clearReloadMark(deps.storage)
  })

  const recover = (target: string) =>
    recoverFromChunkError({ storage: deps.storage, reload: () => deps.hardNavigate(target), toast: deps.toast, target, isOnline: deps.isOnline })

  router.onError((err, to) => {
    pendingHref = null
    if (isChunkLoadError(err)) recover(router.resolve(to).href)
  })

  // Vite 的动态导入预加载失败（含样式文件）会先派发这个事件，页面内的 import()（如 xlsx、qrcode）也会触发。
  // 只有换页进行中才拦截并整页跳转；否则不拦截、不刷新（以免丢掉页面上进行中的内容），只出提示条，调用方原有的错误处理照常显示。
  deps.events?.addEventListener('vite:preloadError', (e) => {
    if (!pendingHref) {
      deps.setHasNew()
      return
    }
    e.preventDefault()
    recover(pendingHref)
  })
}
