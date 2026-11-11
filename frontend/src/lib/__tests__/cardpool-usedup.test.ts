// 调低「单卡最大使用次数」后，后端返回将不再可用的卡数（C1-11），选卡规则页保存后提示管理员手动处理。
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { splitSelectionSaved, usedUpNotice } from '../cardpool'

describe('usedUpNotice', () => {
  it('写明张数、不会自动注销、去哪里处理', () => {
    const t = usedUpNotice(3)
    expect(t).toContain('有 3 张卡将不再可用')
    expect(t).toContain('不会自动')
    expect(t).toContain('已用次数')
    expect(t).toContain('卡池')
  })
})

describe('splitSelectionSaved', () => {
  it('拆出不可用卡数，其余作为选卡字段，不改原对象', () => {
    const raw = { plan_products: { plus: ['p1'] }, max_uses_per_card: 1, health_window_hours: 24, product_fail_threshold: 3, card_fail_threshold: 2, auto_suspend: true, unusable_cards: 2 }
    const { selection, unusable } = splitSelectionSaved(raw)
    expect(unusable).toBe(2)
    expect(selection).toEqual({ plan_products: { plus: ['p1'] }, max_uses_per_card: 1, health_window_hours: 24, product_fail_threshold: 3, card_fail_threshold: 2, auto_suspend: true })
    expect(raw.unusable_cards).toBe(2)
  })
  it('没带这一项为 0；plan_products 为空补成空对象', () => {
    const { selection, unusable } = splitSelectionSaved({ plan_products: null as any, max_uses_per_card: 3, health_window_hours: 24, product_fail_threshold: 3, card_fail_threshold: 2, auto_suspend: false })
    expect(unusable).toBe(0)
    expect(selection.plan_products).toEqual({})
  })
})

describe('选卡规则页保存后提示', () => {
  const src = readFileSync(resolve(__dirname, '../../views/admin/MarsWallexSelection.vue'), 'utf8')
  it('保存成功后按返回的不可用卡数弹提示，提示文字来自 usedUpNotice', () => {
    expect(src).toMatch(/splitSelectionSaved\(r\.data\)/)
    expect(src).toMatch(/if \(unusable > 0\)[^\n]*dialog\.alert\(usedUpNotice\(unusable\)/)
  })
})
