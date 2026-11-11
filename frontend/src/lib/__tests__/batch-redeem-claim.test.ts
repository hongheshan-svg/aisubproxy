// 批量兑换页「本批已发出兑换请求的卡密」登记（FE2 审查重要 3）：在途守卫之外的结构性保险，同一张卡密一个批次里
// 只放行一次兑换请求。数据都是假的。
import { describe, expect, it } from 'vitest'
import { claimRedeem, runSubmitPool } from '../batch-redeem'

describe('同一张卡密一个批次只放行一次兑换请求（FE2 审查重要 3）', () => {
  it('第一次登记放行，之后同一张一律拒绝', () => {
    const sent = new Set<string>()
    expect(claimRedeem(sent, 'SXC-AAAA-BBBB')).toBe(true)
    expect(claimRedeem(sent, 'SXC-AAAA-BBBB')).toBe(false)
    expect(claimRedeem(sent, 'SXC-AAAA-BBBB')).toBe(false)
  })
  it('按粘贴去重的写法比较：大小写、前后空格不同也算同一张', () => {
    const sent = new Set<string>()
    expect(claimRedeem(sent, 'sxc-aaaa-bbbb')).toBe(true)
    expect(claimRedeem(sent, ' SXC-AAAA-BBBB ')).toBe(false)
  })
  it('不同的卡密互不影响', () => {
    const sent = new Set<string>()
    expect(claimRedeem(sent, 'SXC-AAAA-BBBB')).toBe(true)
    expect(claimRedeem(sent, 'SXC-CCCC-DDDD')).toBe(true)
  })
  it('空卡密不放行', () => {
    expect(claimRedeem(new Set<string>(), '  ')).toBe(false)
  })
  it('换批次换新集合：新批次里同一张卡密可以再提交一次（是否可用由后端按卡密状态判）', () => {
    const old = new Set<string>()
    expect(claimRedeem(old, 'SXC-AAAA-BBBB')).toBe(true)
    expect(claimRedeem(new Set<string>(), 'SXC-AAAA-BBBB')).toBe(true)
  })
  it('守卫被改坏、同一张卡密被排进两个并发循环：兑换请求仍只发一次', async () => {
    const sent = new Set<string>()
    const redeemed: string[] = []
    const submit = async (key: string) => {
      await Promise.resolve() // 预览、核对账号
      if (!claimRedeem(sent, key)) return
      redeemed.push(key)
    }
    const items = ['SXC-0001', 'SXC-0002', 'SXC-0003']
    await Promise.all([
      runSubmitPool(items, 2, () => true, submit),
      runSubmitPool(items.slice(1), 2, () => true, submit),
    ])
    expect(redeemed.sort()).toEqual(items)
  })
})
