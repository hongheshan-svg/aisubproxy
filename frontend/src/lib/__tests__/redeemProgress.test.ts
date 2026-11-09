import { describe, it, expect } from 'vitest'
import { progressRows, type ProgressView } from '../redeemProgress'

const rows = (s: string, st = '', prev?: ProgressView['rows']) => progressRows(s, st, prev)
const R1: ProgressView['rows'] = ['current', 'todo', 'todo']
const R2: ProgressView['rows'] = ['done', 'current', 'todo']
const R3: ProgressView['rows'] = ['done', 'done', 'current']

describe('progressRows（规格 §6.5）', () => {
  it('queued：第 1 行进行中', () => {
    expect(rows('queued')).toEqual({ rows: R1, phase: 'running' })
  })
  it('running + queued / credential_check：第 1 行进行中', () => {
    expect(rows('running', 'queued')).toEqual({ rows: R1, phase: 'running' })
    expect(rows('running', 'credential_check')).toEqual({ rows: R1, phase: 'running' })
  })
  it('running + checkout / card_funding / payment：第 2 行进行中', () => {
    for (const st of ['checkout', 'card_funding', 'payment']) {
      expect(rows('running', st)).toEqual({ rows: R2, phase: 'running' })
    }
  })
  it('running + subscription：第 3 行进行中', () => {
    expect(rows('running', 'subscription')).toEqual({ rows: R3, phase: 'running' })
  })
  it('completed：全部完成', () => {
    expect(rows('completed')).toEqual({ rows: ['done', 'done', 'done'], phase: 'done' })
  })
  it('failed_precharge / failed / declined / cancelled：failed，行照 prev', () => {
    for (const s of ['failed_precharge', 'failed', 'declined', 'cancelled']) {
      expect(rows(s, '', R2)).toEqual({ rows: R2, phase: 'failed' })
      expect(rows(s)).toEqual({ rows: R1, phase: 'failed' })
    }
  })
  it('review：review，行照 prev', () => {
    expect(rows('review', '', R3)).toEqual({ rows: R3, phase: 'review' })
    expect(rows('review')).toEqual({ rows: R1, phase: 'review' })
  })
  it('认不出的值：带 prev 等于 prev，不带为默认', () => {
    expect(rows('weird', '', R2)).toEqual({ rows: R2, phase: 'running' })
    expect(rows('weird')).toEqual({ rows: R1, phase: 'running' })
    expect(rows('running', 'weird', R3)).toEqual({ rows: R3, phase: 'running' })
    expect(rows('running', '')).toEqual({ rows: R1, phase: 'running' })
  })
  it('checkout 在 card_funding 之前、之后都是第 2 行进行中，不倒退', () => {
    const seq = ['credential_check', 'checkout', 'card_funding', 'checkout', 'payment']
    let prev: ProgressView['rows'] | undefined
    const got = seq.map((st) => (prev = rows('running', st, prev).rows))
    expect(got[1]).toEqual(R2)
    expect(got[2]).toEqual(R2)
    expect(got[3]).toEqual(R2)
    expect(got[4]).toEqual(R2)
  })
})
