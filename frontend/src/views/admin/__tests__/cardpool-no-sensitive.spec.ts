// 卡池「查看卡号」下线（H2-03）：按钮、弹窗、接口调用和只给它用的工具函数都删掉；查看验证码保留。
import { describe, expect, it } from 'vitest'
import { existsSync, readFileSync } from 'node:fs'
import { resolve } from 'node:path'

const src = readFileSync(resolve(__dirname, '../CardPool.vue'), 'utf8')
const text = readFileSync(resolve(__dirname, '../../../lib/mwcardtext.ts'), 'utf8')

describe('卡池页不再有查看卡号', () => {
  it('没有按钮、弹窗和 /sensitive 接口调用', () => {
    expect(src).not.toContain('查看卡号')
    expect(src).not.toContain('/sensitive')
    expect(src).not.toMatch(/viewSensitive|canViewSensitive|sensitiveOpen|toSensitiveView|sensitiveNote/)
  })
  it('查看验证码保留', () => {
    expect(src).toContain('查看验证码')
    expect(src).toContain('/otp')
  })
  it('只给查看卡号用的工具函数已删除', () => {
    expect(existsSync(resolve(__dirname, '../../../lib/cardpool-sensitive.ts'))).toBe(false)
    expect(text).not.toContain('sensitiveNote')
  })
})
