// 本机没有 DOM 环境，但 onMounted 里的请求也要验证：用 Vue 的自定义渲染器把组件挂到一棵内存里的树上，
// 这样 setup 和生命周期钩子会真正执行。vitest 以 SSR 模式编译 .vue，组件只有 ssrRender，
// 所以这里补一个空的 render：只验证钩子里发出的请求，页面文字由 SSR 渲染的用例检查。
import { createRenderer, ssrContextKey, type App, type Component } from 'vue'

interface N { tag: string; text?: string; children: N[]; parent: N | null; props: Record<string, unknown> }
const node = (tag: string, text?: string): N => ({ tag, text, children: [], parent: null, props: {} })

const { createApp } = createRenderer<N, N>({
  createElement: (tag) => node(tag),
  createText: (t) => node('#text', t),
  createComment: (t) => node('#comment', t),
  setText: (n, t) => { n.text = t },
  setElementText: (n, t) => { n.children = []; n.text = t },
  patchProp: (el, key, _o, v) => { el.props[key] = v },
  insert: (el, parent, anchor) => {
    if (el.parent) el.parent.children = el.parent.children.filter((c) => c !== el)
    el.parent = parent
    const i = anchor ? parent.children.indexOf(anchor) : -1
    if (i < 0) parent.children.push(el)
    else parent.children.splice(i, 0, el)
  },
  remove: (el) => { if (el.parent) el.parent.children = el.parent.children.filter((c) => c !== el); el.parent = null },
  parentNode: (el) => el.parent,
  nextSibling: (el) => { const s = el.parent?.children; return s ? s[s.indexOf(el) + 1] ?? null : null },
})

/** 挂载并执行生命周期；返回卸载函数。 */
export function mountHeadless(root: Component, setup: (app: App) => void = () => {}): () => void {
  const comp = root as { render?: () => null }
  if (!comp.render) comp.render = () => null
  const app = createApp(root)
  // 组件 setup 里会读 useSSRContext()
  app.provide(ssrContextKey, {})
  setup(app)
  app.mount(node('root'))
  return () => app.unmount()
}

/** 等异步钩子跑完。 */
export const settle = async () => { for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0)) }
