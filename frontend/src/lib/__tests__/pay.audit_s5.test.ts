import { describe, expect, it } from 'vitest'
import { RESOLVE_FAILED_HINT, resolveConfirmText } from '../pay'

describe('人工定论确认文案（D-3）', () => {
  it('定论失败时带上「禁用这张码后重新发码」的提示', () => {
    expect(RESOLVE_FAILED_HINT).toContain('没有扣款')
    expect(RESOLVE_FAILED_HINT).toContain('禁用这张码后重新发码给用户')
    expect(resolveConfirmText('failed')).toContain(RESOLVE_FAILED_HINT)
  })
  it('定论成功不带这条提示', () => {
    expect(resolveConfirmText('succeeded')).not.toContain('禁用')
  })
})
