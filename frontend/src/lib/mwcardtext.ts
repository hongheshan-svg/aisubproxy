// 卡池页的文案与显示规则（纯函数）：查看卡号、释放、用后注销时间、导入清零提醒、冻结按钮。
import { fmtBeijing } from './mwwebhook'
import { money } from './cardpool'

/** 只用到策略里这两个数值。 */
export interface UsePolicy { max_uses_per_card: number; cancel_after_days: number }

/** 注销时间说明：0 天写「立即注销」。 */
export function cancelWhen(days: number): string {
  return days > 0 ? `${days} 天后注销` : '立即注销'
}

/** 接在「清零、冻结，」后面：并在 N 天后注销 / 并立即注销。 */
const andCancel = (days: number) => (days > 0 ? `并在 ${cancelWhen(days)}` : `并${cancelWhen(days)}`)

interface LeaseLike { use_count: number; lease_sensitive?: boolean }

/** 释放后这张卡会不会用满：取过卡号则 use_count + 1 与上限比，没取过则用 use_count。策略没加载时返回 null。 */
export function releaseWillBeUsedUp(c: LeaseLike, p: UsePolicy | null | undefined): boolean | null {
  if (!p) return null
  return c.use_count + (c.lease_sensitive ? 1 : 0) >= p.max_uses_per_card
}

/** 释放确认框的文字。 */
export function releaseConfirmText(c: LeaseLike & { card_id: string; last4: string }, p: UsePolicy | null | undefined): string {
  const taken = c.lease_sensitive ? '本次租约已取过卡号' : '本次租约没有取过卡号'
  const used = releaseWillBeUsedUp(c, p)
  let fate: string
  if (used === null) fate = '卡的去向取决于使用次数是否达到上限（策略未加载，无法预判）'
  else if (used) fate = `已用满，将清零、冻结，${andCancel(p!.cancel_after_days)}`
  else fate = '余额清零后回到可用'
  return `确认释放卡 ${c.card_id}（后四位 ${c.last4}）？${taken}，释放后：${fate}。`
}

/** 释放成功后的提示：卡的去向。 */
export function releaseDoneText(c: LeaseLike, p: UsePolicy | null | undefined): string {
  const used = releaseWillBeUsedUp(c, p)
  if (used === null) return '已释放'
  return used ? `已释放，卡已用满，将清零、冻结，${andCancel(p!.cancel_after_days)}` : '已释放，卡会清零后回到可用'
}

/** 「查看卡号」确认框里的说明。租用中的卡写明用满后的后果。 */
export function sensitiveNote(poolState: string, p: UsePolicy | null | undefined): string {
  if (poolState !== 'leased') return '仅供排查，不改变卡的状态。'
  const base = '本次租用将计为已取卡号（使用次数 +1）。'
  if (!p) return base
  return `${base}用满 ${p.max_uses_per_card} 次后，释放时这张卡会被清零、冻结，${cancelWhen(p.cancel_after_days)}。`
}

/** 卡列表状态列标签下面的小字：已用且有注销时间显示北京时间 MM-DD HH:mm，已用但还没冻结显示等待。其他状态为空。 */
export function usedStateNote(c: { pool_state: string; cancel_after?: number | null }): string {
  if (c.pool_state !== 'used') return ''
  if (c.cancel_after && c.cancel_after > 0) return `已冻结，${fmtBeijing(c.cancel_after).slice(5, 16)} 注销`
  return '等待清零、冻结'
}

/** 导入卡时卡上有余额的清零提醒；没有余额返回空串。 */
export function importSweepWarning(availableCents: number): string {
  if (!(availableCents > 0)) return ''
  return `导入后，卡上 ${money(availableCents)} USD 会自动转回主钱包（清零），然后卡才变成可用。`
}

/** 冻结、解冻按钮只按平台状态显示。 */
export const canFreeze = (platformStatus: string) => platformStatus === 'ACTIVE'
export const canUnfreeze = (platformStatus: string) => platformStatus === 'REVOKED'
