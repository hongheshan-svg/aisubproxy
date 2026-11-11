// 功能开关：代理商功能默认关闭，关闭时侧栏不显示代理商分组、直接打开这些页面会被送回总览。
import { describe, expect, it, vi } from 'vitest'

vi.hoisted(() => {
  const data: Record<string, string> = {}
  ;(globalThis as any).localStorage = {
    getItem: (k: string) => (k in data ? data[k] : null),
    setItem: (k: string, v: string) => { data[k] = v },
    removeItem: (k: string) => { delete data[k] },
  }
})

import { routes } from '../../router/routes'
import { buildNavItems } from '../adminNav'
import { AGENT_ERROR_TEXT } from '../agentApi'
import {
  AGENT_DISABLED_TEXT, featureOn, shouldLeaveFeaturePage, visibleNavItems,
} from '../features'

const off = { loaded: true, agent: false }
const on = { loaded: true, agent: true }
const unknown = { loaded: false, agent: false }

describe('featureOn', () => {
  it('没标功能的菜单总是可见', () => {
    expect(featureOn(off, '')).toBe(true)
    expect(featureOn(off, undefined)).toBe(true)
  })
  it('代理商功能按开关', () => {
    expect(featureOn(off, 'agent')).toBe(false)
    expect(featureOn(on, 'agent')).toBe(true)
    expect(featureOn(unknown, 'agent')).toBe(false)
  })
  it('不认识的功能名当作关闭', () => {
    expect(featureOn(on, 'xyz')).toBe(false)
  })
})

describe('侧栏过滤', () => {
  const items = buildNavItems(routes)
  it('代理商菜单带 feature=agent', () => {
    const agent = items.filter((i) => i.group === '代理商')
    expect(agent.map((i) => i.path)).toEqual(['/ops/agents', '/ops/agent-pricing'])
    expect(agent.every((i) => i.feature === 'agent')).toBe(true)
  })
  it('关闭或还没读到开关时，不显示代理商分组；其他菜单不受影响', () => {
    for (const st of [off, unknown]) {
      const v = visibleNavItems(items, st)
      expect(v.some((i) => i.group === '代理商')).toBe(false)
      expect(v.length).toBe(items.length - 2)
    }
  })
  it('打开后显示', () => {
    expect(visibleNavItems(items, on).length).toBe(items.length)
  })
  it('「系统」分组有「功能开关」页', () => {
    const f = items.find((i) => i.path === '/ops/features')
    expect(f?.title).toBe('功能开关')
    expect(f?.group).toBe('系统')
    expect(f?.feature).toBe('')
  })
  it('代理商详情页也受开关控制', () => {
    const root = routes.find((r) => r.path === '/ops')!
    const d = (root.children || []).find((c) => c.path === 'agents/:id')!
    expect((d.meta as any).feature).toBe('agent')
  })
})

describe('shouldLeaveFeaturePage', () => {
  it('确认关闭后，代理商页面要离开', () => {
    expect(shouldLeaveFeaturePage({ feature: 'agent' }, off)).toBe(true)
  })
  it('还没读到开关时不跳（避免读接口失败把人踢走），打开时不跳，普通页面不跳', () => {
    expect(shouldLeaveFeaturePage({ feature: 'agent' }, unknown)).toBe(false)
    expect(shouldLeaveFeaturePage({ feature: 'agent' }, on)).toBe(false)
    expect(shouldLeaveFeaturePage({ title: '总览' }, off)).toBe(false)
  })
})

describe('代理商端：功能未开放', () => {
  it('提示文字', () => {
    expect(AGENT_DISABLED_TEXT).toBe('代理商功能暂未开放')
    expect(AGENT_ERROR_TEXT.agent_disabled).toBe('代理商功能暂未开放')
  })
})
