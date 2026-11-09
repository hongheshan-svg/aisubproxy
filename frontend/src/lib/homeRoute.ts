// 首页分流（规格 §6.1）：售后查询结果决定是直接去兑换页，还是在首页显示结果卡。
import type { LookupResult } from './aftersale'

export type HomeRoute = { kind: 'goto'; to: string } | { kind: 'card' }

export function homeRoute(r: LookupResult): HomeRoute {
  const to = `/recharge?code=${encodeURIComponent(r.cdk_code)}`
  // 没用过且不用重提的码直接进兑换；处理中的码也去兑换页，那里会认出已兑换并显示进度
  if (r.status === 'unused' && !r.can_resubmit) return { kind: 'goto', to }
  if (r.status === 'processing') return { kind: 'goto', to }
  return { kind: 'card' }
}
