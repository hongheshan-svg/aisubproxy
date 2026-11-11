// 统一确认框的按键（审查修复第 2 轮）：
// - 回车只作用于当前获得焦点的元素：输入框里的回车只交回输入结果，不能冒泡到框上，把紧接着弹出的确认框点成「确定」。
//   后台「付款」页「恢复付款派发」是「先填备注（输入框）→ 再弹红色确认框」，原来在备注里按回车，确认框一闪就恢复了，
//   可能再次动用卡池资金。
// - 确认框里按 Esc 只关确认框，不冒泡到 document 上，把底下的 Element 弹窗（调账、开卡表单等）也关掉。
//
// 本机没有 DOM 环境，没法发真实按键。这里的做法：
// 1. 用 Vue 的模板编译器解析 DialogHost.vue 的模板，取出从目标元素到最外层的每一层 keydown 监听（按键修饰、.stop、.prevent、处理函数名）；
// 2. headless 挂载 DialogHost 和付款页，按浏览器的冒泡顺序依次调用 DialogHost 里真实的处理函数，
//    每调完一层就让微任务跑完（浏览器在两个监听之间会跑微任务，调用方 await 之后的代码就在这时开出下一个框）；
// 3. 看确认框是否还开着、有没有发出恢复请求。
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { parse } from 'vue/compiler-sfc'
import { createMemoryHistory, createRouter } from 'vue-router'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

const posts: string[] = []
vi.mock('../../lib/api', () => ({
  authFetch: async (url: string, init: { method?: string } = {}) => {
    if ((init.method || 'GET').toUpperCase() === 'POST') posts.push(String(url))
    return { ok: false, status: 404, json: async () => ({ error: 'nf' }) }
  },
}))

import DialogHost from '../DialogHost.vue'
import MarsWallexPay from '../../views/admin/MarsWallexPay.vue'
import { dialogState, resolveActive } from '../../lib/dialog'
import { mountHeadless, settle } from '../../views/admin/__tests__/headless'

// ---------- 从模板取出每层的 keydown 监听 ----------
interface El { tag: string; props: any[]; children?: any[] }
interface Listener { tag: string; keys: string[]; stop: boolean; prevent: boolean; handler: string }

const KEY_MODS = ['enter', 'esc', 'tab', 'space', 'up', 'down', 'left', 'right', 'delete']
const template = parse(readFileSync(fileURLToPath(new URL('../DialogHost.vue', import.meta.url)), 'utf8')).descriptor.template!.ast!

/** 从模板根往下找满足条件的元素，返回从它到最外层的链（含它自己）。 */
function chainTo(match: (el: El) => boolean): El[] {
  const walk = (node: any, up: El[]): El[] | null => {
    if (node.type === 1) {
      const path = [node as El, ...up]
      if (match(node)) return path
      for (const c of node.children || []) { const r = walk(c, path); if (r) return r }
    }
    return null
  }
  for (const c of (template as any).children) { const r = walk(c, []); if (r) return r }
  throw new Error('模板里找不到目标元素')
}
const attr = (el: El, name: string) => el.props.find((p) => p.type === 6 && p.name === name)?.value?.content
function keydownListeners(el: El): Listener[] {
  return el.props
    .filter((p) => p.type === 7 && p.name === 'on' && p.arg?.content === 'keydown')
    .map((p) => {
      const mods: string[] = (p.modifiers || []).map((m: any) => (typeof m === 'string' ? m : m.content))
      return { tag: el.tag, keys: mods.filter((m) => KEY_MODS.includes(m)), stop: mods.includes('stop'), prevent: mods.includes('prevent'), handler: String(p.exp?.content || '') }
    })
}

const inputChain = chainTo((el) => el.tag === 'input' && attr(el, 'ref') === 'inputEl')
const cancelChain = chainTo((el) => el.tag === 'button' && attr(el, 'ref') === 'cancelEl')
const panel = inputChain.find((el) => attr(el, 'role') === 'dialog')!

/**
 * 按冒泡顺序把一次按键交给 DialogHost 真实的处理函数；每层之间跑完微任务。
 * 返回是否冒泡到了最外层之外（document，Element 弹窗的 Esc 监听在那里）。
 */
async function press(chain: El[], key: 'Enter' | 'Escape', host: any): Promise<{ reachedDocument: boolean; prevented: boolean }> {
  const mod = key === 'Enter' ? 'enter' : 'esc'
  let stopped = false
  let prevented = false
  const target = { tagName: chain[0].tag.toUpperCase() }
  const ev = { key, target, shiftKey: false, preventDefault: () => { prevented = true }, stopPropagation: () => { stopped = true } }
  for (const el of chain) {
    for (const l of keydownListeners(el)) {
      if (l.keys.length && !l.keys.includes(mod)) continue
      if (l.prevent) ev.preventDefault()
      const fn = host[l.handler]
      expect(typeof fn, `DialogHost 里找不到处理函数 ${l.handler}`).toBe('function')
      fn(ev)
      if (l.stop) ev.stopPropagation()
      await settle()
    }
    if (stopped) break
  }
  return { reachedDocument: !stopped, prevented }
}

async function mountBoth() {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:p(.*)*', component: { template: '<div />' } }] })
  router.push('/ops/marswallex-pay')
  await router.isReady()
  let host: any = null
  let pay: any = null
  const pick = (want: string, set: (st: any) => void) => ({ created() { const st = (this as any).$.setupState; if (st && want in st) set(st) } })
  const u1 = mountHeadless(DialogHost, (app) => { app.use(router); app.mixin(pick('onCancel', (st) => { host = st })) })
  const u2 = mountHeadless(MarsWallexPay, (app) => { app.use(router); app.mixin(pick('resumeBreaker', (st) => { pay = st })) })
  await settle()
  return { host, pay, unmount: () => { u2(); u1() } }
}

beforeEach(() => {
  posts.length = 0
  while (dialogState.active) resolveActive(null)
})

describe('确认框面板上不绑回车（审查修复第 2 轮）', () => {
  it('输入框到最外层，只有输入框自己处理回车，并且 .stop、.prevent', () => {
    const enter = inputChain.flatMap((el) => keydownListeners(el).filter((l) => !l.keys.length || l.keys.includes('enter')))
    expect(enter.map((l) => `${l.tag}:${l.handler}`)).toEqual(['input:onEnter'])
    expect(enter[0]).toMatchObject({ stop: true, prevent: true })
  })
  it('面板（role=dialog）和外层都没有回车处理；按钮上的回车交给按钮自己', () => {
    for (const el of inputChain.slice(1)) {
      expect(keydownListeners(el).filter((l) => !l.keys.length || l.keys.includes('enter')), el.tag).toEqual([])
    }
    expect(keydownListeners(panel).map((l) => l.keys.join('+'))).toEqual(['esc', 'tab'])
  })
  it('Esc 在面板上处理后不再冒泡（.stop）', () => {
    expect(keydownListeners(panel).find((l) => l.keys.includes('esc'))).toMatchObject({ handler: 'onCancel', stop: true })
  })
})

describe('恢复付款派发：备注里按回车不会把紧接着的确认框点成「确定」（审查修复第 2 轮）', () => {
  it('备注框回车 → 弹出红色确认框并停住，不发恢复请求；点「取消」后也不发', async () => {
    const { host, pay, unmount } = await mountBoth()
    expect(host, 'DialogHost 的 setupState').not.toBeNull()
    pay.breaker = { paused: true, reason: 'submit_failures', reason_text: '', threshold: 3, submitted_failure_streak: 3, fill_failure_streak: 0, tripped_at: 1, order_nos: ['PO1'], last_resume_at: null, last_resume_by: '' }
    const run = pay.resumeBreaker()
    await settle()
    expect(dialogState.active).toMatchObject({ kind: 'prompt', title: '恢复付款派发' })
    dialogState.active!.value = '已核对 PO1，代理出口 IP 被拦，已更换'

    const r = await press(inputChain, 'Enter', host)
    expect(r.prevented).toBe(true) // 浏览器不会再对随后获得焦点的按钮补一次点击
    expect(dialogState.active).toMatchObject({ kind: 'confirm', title: '恢复付款派发', okText: '确认恢复', danger: true })
    expect(posts.filter((u) => u.includes('/breaker/resume'))).toEqual([])

    resolveActive(false) // 点「取消」
    await run
    expect(posts.filter((u) => u.includes('/breaker/resume'))).toEqual([])
    unmount()
  })
})

describe('确认框里按 Esc 只关确认框（审查修复第 2 轮）', () => {
  it('焦点在「取消」上按 Esc：确认框取消，按键不冒泡到 document（底下的 Element 弹窗不跟着关）', async () => {
    const { host, unmount } = await mountBoth()
    let answer: unknown = 'pending'
    const { dialog } = await import('../../lib/dialog')
    dialog.confirm('确定调账？', { danger: true }).then((v) => { answer = v })
    await settle()
    const r = await press(cancelChain, 'Escape', host)
    expect(answer).toBe(false)
    expect(r.reachedDocument).toBe(false)
    unmount()
  })
})
