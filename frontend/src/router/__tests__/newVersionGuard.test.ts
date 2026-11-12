// 守卫与兜底的行为测试：真实的 vue-router（内存历史）+ 注入的依赖。
import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { createMemoryHistory, createRouter } from 'vue-router'
import { installNewVersionGuard } from '../newVersionGuard'
import { RELOAD_MARK_KEY } from '../../lib/chunkReload'

const stub = { template: '<i/>' }

function setup(
  opts: { hasNew?: boolean; online?: boolean; storageThrows?: boolean; data?: Record<string, string>; serverHasNew?: boolean } = {},
) {
  const state = { hasNew: !!opts.hasNew, online: opts.online !== false }
  // 传入同一个 data 模拟「整页刷新后的新页面」：sessionStorage 跨刷新保留
  const data: Record<string, string> = opts.data ?? {}
  const listeners: Record<string, (e: any) => void> = {}
  const deps = {
    hasNew: () => state.hasNew,
    setHasNew: vi.fn(() => { state.hasNew = true }),
    checkNew: vi.fn(async () => !!opts.serverHasNew),
    hardNavigate: vi.fn(),
    toast: vi.fn(),
    isOnline: () => state.online,
    storage: () => {
      if (opts.storageThrows) throw new Error('denied')
      return {
        getItem: (k: string) => (k in data ? data[k] : null),
        setItem: (k: string, v: string) => { data[k] = v },
        removeItem: (k: string) => { delete data[k] },
      }
    },
    events: { addEventListener: (n: string, cb: any) => { listeners[n] = cb } },
  }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: stub },
      { path: '/a', component: stub },
      { path: '/b', component: stub },
      { path: '/bad', component: () => Promise.reject(new TypeError('Failed to fetch dynamically imported module: /assets/Bad-1.js')) },
      { path: '/boom', component: () => Promise.reject(new Error('Network Error')) },
      { path: '/redir', redirect: '/a' },
      { path: '/slow', component: stub, beforeEnter: () => new Promise<void>((r) => setTimeout(r, 30)) },
    ],
  })
  installNewVersionGuard(router, deps as any)
  return { router, deps, state, data, listeners }
}

// 导航被挂起时 push 永远不 resolve，不能 await
const pushNoWait = (router: any, to: any) => { void router.push(to) }
const tick = () => new Promise((r) => setTimeout(r, 20))

describe('整页跳转守卫', () => {
  it('检测到新版本后换页：调 hardNavigate，导航挂着（不落地）', async () => {
    const t = setup()
    await t.router.push('/a')
    t.state.hasNew = true
    pushNoWait(t.router, '/b?x=1')
    await tick()
    expect(t.deps.hardNavigate).toHaveBeenCalledWith('/b?x=1')
    expect(t.router.currentRoute.value.path).toBe('/a')
  })
  it('只改 query / hash：放行，不跳', async () => {
    const t = setup()
    await t.router.push('/a')
    t.state.hasNew = true
    await t.router.push('/a?q=1')
    await t.router.push('/a?q=1#x')
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.router.currentRoute.value.fullPath).toBe('/a?q=1#x')
  })
  it('首次导航：不跳（路由记录上的 redirect）', async () => {
    const t = setup({ hasNew: true })
    await t.router.push('/redir')
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.router.currentRoute.value.path).toBe('/a')
  })
  it('首次导航被后面的守卫重定向：第二轮 from 仍是起点，不跳', async () => {
    const t = setup({ hasNew: true })
    t.router.beforeEach((to) => (to.path === '/b' ? '/a' : undefined))
    await t.router.push('/b')
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.router.currentRoute.value.path).toBe('/a')
  })
  it('跳转地址保留原始 query 写法（不把 %2F 改成 /），后退时才能和地址栏对上', async () => {
    const t = setup()
    await t.router.push('/a')
    t.state.hasNew = true
    pushNoWait(t.router, '/b?redirect=%2Fops%2Fcdkeys&q=a%20b')
    await tick()
    expect(t.deps.hardNavigate).toHaveBeenCalledWith('/b?redirect=%2Fops%2Fcdkeys&q=a%20b')
  })
  it('断网：照常单页换页，不整页跳转', async () => {
    const t = setup({ online: false })
    await t.router.push('/a')
    t.state.hasNew = true
    await t.router.push('/b')
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.router.currentRoute.value.path).toBe('/b')
  })
  it('没有新版本：放行', async () => {
    const t = setup()
    await t.router.push('/a')
    await t.router.push('/b')
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.router.currentRoute.value.path).toBe('/b')
  })
  it('组件离开守卫返回 false 时不跳（离开守卫先于全局守卫）', async () => {
    const t = setup()
    await t.router.push('/a')
    // 模拟组件的 onBeforeRouteLeave：记录在当前匹配记录上
    t.router.currentRoute.value.matched[0].leaveGuards.add(() => false)
    t.state.hasNew = true
    await t.router.push('/b')
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.router.currentRoute.value.path).toBe('/a')
  })
})

describe('分包加载失败兜底', () => {
  it('router.onError 分包错误：整页跳到目标地址并写标记', async () => {
    const t = setup()
    await t.router.push('/a')
    await t.router.push('/bad').catch(() => {})
    expect(t.deps.hardNavigate).toHaveBeenCalledWith('/bad')
    expect(t.data[RELOAD_MARK_KEY]).toBe('/bad')
  })
  it('刷新后（标记还在）首次导航又失败：只提示，不再刷新', async () => {
    const first = setup()
    await first.router.push('/a')
    await first.router.push('/bad').catch(() => {})
    expect(first.deps.hardNavigate).toHaveBeenCalledTimes(1)
    // 整页刷新后的新页面：sessionStorage 里的标记还在
    const t = setup({ data: first.data })
    await t.router.push('/bad').catch(() => {})
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.deps.toast).toHaveBeenCalledTimes(1)
  })
  it('已发起整页跳转后，同一页面上的后续失败不再提示', async () => {
    const t = setup()
    await t.router.push('/a')
    await t.router.push('/bad').catch(() => {})
    const e = { preventDefault: vi.fn() }
    t.listeners['vite:preloadError'](e)
    await t.router.push('/bad').catch(() => {})
    expect(t.deps.hardNavigate).toHaveBeenCalledTimes(1)
    expect(t.deps.toast).not.toHaveBeenCalled()
    expect(e.preventDefault).toHaveBeenCalled()
  })
  it('换页成功后标记被 afterEach 清掉，下次失败又能刷新', async () => {
    const t = setup()
    await t.router.push('/a')
    await t.router.push('/bad').catch(() => {})
    expect(t.data[RELOAD_MARK_KEY]).toBeDefined()
    await t.router.push('/b')
    expect(t.data[RELOAD_MARK_KEY]).toBeUndefined()
    await t.router.push('/bad').catch(() => {})
    expect(t.deps.hardNavigate).toHaveBeenCalledTimes(2)
  })
  it('非分包错误：不管', async () => {
    const t = setup()
    await t.router.push('/a')
    await t.router.push('/boom').catch(() => {})
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.deps.toast).not.toHaveBeenCalled()
  })
  it('断网：只提示，不刷新、不写标记', async () => {
    const t = setup({ online: false })
    await t.router.push('/a')
    await t.router.push('/bad').catch(() => {})
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.deps.toast).toHaveBeenCalledTimes(1)
    expect(t.data[RELOAD_MARK_KEY]).toBeUndefined()
  })
  it('存储抛错：只提示，不刷新', async () => {
    const t = setup({ storageThrows: true })
    await t.router.push('/a')
    await t.router.push('/bad').catch(() => {})
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.deps.toast).toHaveBeenCalledTimes(1)
  })
})

describe('vite:preloadError', () => {
  const evt = () => ({ preventDefault: vi.fn() })
  it('换页进行中：拦截并整页跳转到目标地址', async () => {
    const t = setup()
    await t.router.push('/a')
    let fired: ReturnType<typeof evt> | null = null
    t.router.beforeEach(() => { fired = evt(); t.listeners['vite:preloadError'](fired) })
    await t.router.push('/b')
    expect(fired!.preventDefault).toHaveBeenCalled()
    expect(t.deps.hardNavigate).toHaveBeenCalledWith('/b')
    expect(t.deps.setHasNew).not.toHaveBeenCalled()
  })
  it('页面内的动态加载（没有换页）：不拦截、不刷新；向服务器核对确有新版本才出提示条', async () => {
    const t = setup({ serverHasNew: true })
    await t.router.push('/a')
    const e = evt()
    t.listeners['vite:preloadError'](e)
    await tick()
    expect(e.preventDefault).not.toHaveBeenCalled()
    expect(t.deps.hardNavigate).not.toHaveBeenCalled()
    expect(t.deps.checkNew).toHaveBeenCalledTimes(1)
    expect(t.deps.setHasNew).toHaveBeenCalledTimes(1)
    expect(t.state.hasNew).toBe(true)
  })
  it('页面内的动态加载失败、服务器没有新版本（被扩展拦截等）：不出提示条', async () => {
    const t = setup({ serverHasNew: false })
    await t.router.push('/a')
    t.listeners['vite:preloadError'](evt())
    await tick()
    expect(t.deps.checkNew).toHaveBeenCalledTimes(1)
    expect(t.deps.setHasNew).not.toHaveBeenCalled()
  })
  it('页面内的动态加载失败、断网：不核对、不出提示条', async () => {
    const t = setup({ online: false, serverHasNew: true })
    await t.router.push('/a')
    t.listeners['vite:preloadError'](evt())
    await tick()
    expect(t.deps.checkNew).not.toHaveBeenCalled()
    expect(t.deps.setHasNew).not.toHaveBeenCalled()
  })
  it('旧导航被新导航取代时不清掉新导航的目标：新导航的预加载失败仍按换页处理', async () => {
    const t = setup()
    await t.router.push('/a')
    // A 挂在异步的路由守卫上，用户又点了 B
    const a = t.router.push('/slow')
    let fired: ReturnType<typeof evt> | null = null
    t.router.beforeResolve((to) => {
      if (to.path !== '/b') return
      return new Promise<void>((r) => setTimeout(() => { fired = evt(); t.listeners['vite:preloadError'](fired); r() }, 60))
    })
    const b = t.router.push('/b')
    await Promise.all([a, b])
    expect(fired!.preventDefault).toHaveBeenCalled()
    expect(t.deps.hardNavigate).toHaveBeenCalledWith('/b')
  })
})

describe('接线顺序', () => {
  it('router/index.ts 在鉴权守卫之前调用 installNewVersionGuard', () => {
    const src = readFileSync(fileURLToPath(new URL('../index.ts', import.meta.url)), 'utf8')
    const install = src.indexOf('installNewVersionGuard(router')
    const auth = src.indexOf('router.beforeEach(')
    expect(install).toBeGreaterThan(0)
    expect(auth).toBeGreaterThan(install)
  })
})
