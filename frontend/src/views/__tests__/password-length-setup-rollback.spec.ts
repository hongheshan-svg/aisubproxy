import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const read = (p: string) => readFileSync(new URL(p, import.meta.url), 'utf8')

describe('审查修复：口令长度按字符数、安装回滚失败提示', () => {
  it('代理商首次设密码按字符数判断长度', () => {
    const src = read('../agent/AgentLogin.vue')
    expect(src).toContain('Array.from(newPassword.value).length < 12')
    expect(src).not.toContain('newPassword.value.length < 12')
  })

  it('安装页回滚失败时不提示可重试', () => {
    const src = read('../admin/SetupView.vue')
    expect(src).toContain('d.rollback_failed')
    expect(src).toContain('需要人工处理')
  })
})
