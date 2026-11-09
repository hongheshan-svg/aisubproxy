// 兑换页（RechargeView）上的分支判断。纯函数：页面没法在测试里点击，分支提到这里单测，页面只接线。
// 流程本身的判断（能不能开通、能不能重提）都在 useRedeemFlow，这里只决定页面上显示什么、点了调哪个方法。
import type { FlowError, Screen } from '../composables/useRedeemFlow'
import { isClaudePlan } from './claude'
import { isGrokPlan } from './grok'
import { progressRows, type ProgressView } from './redeemProgress'
import { isXPremiumPlan } from './x-premium'

type SubmitState = 'idle' | 'submitting' | 'unknown'
export type Product = 'chatgpt' | 'claude' | 'grok' | 'x'

const LONG_WAIT_MS = 10 * 60 * 1000

/** 带码链接里的卡密：?code= 优先，其次 ?cdk=（规格 §2、§6.2）；数组取第一个，去掉首尾空白。 */
export function queryCode(query: Record<string, unknown>): string {
  const first = (v: unknown) => (Array.isArray(v) ? v[0] : v)
  return String(first(query.code) || first(query.cdk) || '').trim()
}

/** 顶部三格：①在第一格，②③在第二格，④⑤在第三格；开通成功才算三格都完成（规格 §6.2）。 */
export function ticksState(screen: Screen, status: string): { current: 1 | 2 | 3; done: boolean } {
  if (screen === 'code') return { current: 1, done: false }
  if (screen === 'credential' || screen === 'confirm') return { current: 2, done: false }
  return { current: 3, done: screen === 'result' && status === 'completed' }
}

/** 码对应的产品，决定成功屏打开哪个网站。判断顺序和第 ② 屏一致：Claude → X → Grok → 其余是 ChatGPT。 */
export function productOf(plan: string): Product {
  if (isClaudePlan(plan)) return 'claude'
  if (isXPremiumPlan(plan)) return 'x'
  if (isGrokPlan(plan)) return 'grok'
  return 'chatgpt'
}

/** 结果卡上点了带码的按钮：和当前卡密相同是「换个账号再试」，不同是换来的新码（裁决 R28）。 */
export function codeAction(emitted: string, current: string): 'retry' | 'useCode' {
  const norm = (s: string) => String(s || '').trim().toLowerCase()
  return norm(emitted) === norm(current) ? 'retry' : 'useCode'
}

/** 第 ③ 屏「重新核对」：页面内存里还有上次提交的登录信息就直接再核对，没有（例如刷新过）就回第 ② 屏重新粘贴（裁决 R19）。 */
export function recheckAction(hasCredential: boolean): 'preflight' | 'changeAccount' {
  return hasCredential ? 'preflight' : 'changeAccount'
}

/** 只有空闲时才能换码：提交中或不确定是否已提交时，流程会拒绝 reset、preview，页面也不给入口。 */
export function canSwitchCode(submitState: SubmitState): boolean {
  return submitState === 'idle'
}

/**
 * 页面自己的「换一张卡密」入口（第 ②③ 屏）：
 * - 不确定是否已提交：锁住，改为一句提示，告诉买家怎么换码；
 * - 提交中：不显示（按钮正在转圈，很快会有结果）；
 * - 第 ①⑤ 屏有输入框或结果卡自带的入口，第 ④ 屏开通中不给。
 */
export function switchCodeEntry(screen: Screen, submitState: SubmitState): 'show' | 'locked' | 'none' {
  if (submitState === 'unknown') return 'locked'
  if (!canSwitchCode(submitState)) return 'none'
  return screen === 'credential' || screen === 'confirm' ? 'show' : 'none'
}

/**
 * 换一张卡密的编排（裁决 R28）：不确定是否已提交时什么都不做；否则清掉页面内存（登录信息等）→ 流程重来 →
 * 输入框换成新码 → 网址跟着改（刷新后还是这张码）→ 有新码就预览。
 */
export function makeStartOver(d: {
  canSwitch: () => boolean
  clearLocal: () => void
  reset: () => void
  setInput: (code: string) => void
  replaceQuery: (query: Record<string, string>) => void
  preview: (code: string) => void
}): (next?: string) => void {
  return (next = '') => {
    if (!d.canSwitch()) return
    const c = next.trim()
    d.clearLocal()
    d.reset()
    d.setInput(c)
    d.replaceQuery(c ? { code: c } : {})
    if (c) d.preview(c)
  }
}

/**
 * 「恢复订阅」：先弹确认框，买家确认后才调恢复（裁决 R19）。确认框开着或恢复请求在途时再点不动作，
 * 不会叠出第二个确认框、也不会发第二次。返回恢复有没有真的发了请求（被流程守卫挡掉为 false）。
 */
export function makeRecoverAction(confirm: () => Promise<boolean>, recover: () => Promise<boolean>): () => Promise<boolean> {
  let running = false
  return async () => {
    if (running) return false
    running = true
    try {
      if (!(await confirm())) return false
      return await recover()
    } finally {
      running = false
    }
  }
}

type TFn = (key: string) => string
/** 恢复订阅确认框的参数，全部用买家口吻的 redeemUi.grace.*（裁决 R31）。 */
export function graceConfirmArgs(t: TFn): [string, { title: string; okText: string; cancelText: string; danger: true }] {
  return [t('redeemUi.grace.confirm'), {
    title: t('redeemUi.grace.title'), okText: t('redeemUi.grace.confirmAction'), cancelText: t('redeemUi.grace.dismiss'), danger: true,
  }]
}

/** 点过「恢复订阅」后仍要先恢复、又没出错（服务器说还在处理）：第 ③ 屏显示 grace.pending（裁决 R19）。 */
export function showGracePending(i: { tried: boolean; screen: Screen; confirmView: string; error: FlowError | null }): boolean {
  return i.tried && i.screen === 'confirm' && i.confirmView === 'recover' && !i.error
}

/** 开通中的三行：第 ④⑤ 屏每次带上一次的行去算，认不出的状态保持当前行、不倒退；离开这两屏回到初始（规格 §6.5）。 */
export function nextProgress(screen: Screen, status: string, stage: string, prev: ProgressView['rows']): ProgressView {
  return screen === 'progress' || screen === 'result' ? progressRows(status, stage, prev) : progressRows('', '')
}

/** 进入第 ④ 屏满 10 分钟（规格 §6.5）。 */
export function waitedLong(startedAt: number | null, now: number): boolean {
  return startedAt !== null && now - startedAt >= LONG_WAIT_MS
}

/**
 * 「查一下结果」按钮调哪个方法（裁决 R30）。第 ④⑤ 屏已经建单，不能用 checkResult：它查不到单会回第 ③ 屏并把
 * 提交状态清成空闲，买家就能再点「确认开通」。checkResult 只留给第 ③ 屏「不确定是否已提交」。
 */
export function checkActionFor(screen: Screen, submitState: SubmitState): 'checkResult' | 'refreshProgress' | 'refreshLookup' | null {
  if (screen === 'progress') return 'refreshProgress'
  if (screen === 'result') return 'refreshLookup'
  if (screen === 'confirm' && submitState === 'unknown') return 'checkResult'
  return null
}

/**
 * 页面上的「查一下结果」（不会重新提交）：
 * - 第 ④ 屏查进度出错时（规格 §8：网络断了要有「再试一次」）；
 * - 第 ⑤ 屏没开通成功、又没拿到售后结果时。
 * 第 ③ 屏「不确定是否已提交」的同名按钮在 ConfirmStep 里。
 */
export function showCheckButton(i: { screen: Screen; error: FlowError | null; status: string; hasLookup: boolean }): boolean {
  if (i.screen === 'progress') return !!i.error
  if (i.screen === 'result') return i.status !== 'completed' && !i.hasLookup
  return false
}
