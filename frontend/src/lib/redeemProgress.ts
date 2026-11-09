// 把结果接口的 status / stage 对应到开通中页的三行进度（规格 §6.5）。纯函数。
export type RowState = 'done' | 'current' | 'todo'

export type ProgressView = {
  rows: [RowState, RowState, RowState]
  phase: 'running' | 'done' | 'failed' | 'review'
}

const DEFAULT_ROWS: ProgressView['rows'] = ['current', 'todo', 'todo']

const STAGE_ROW1 = ['queued', 'credential_check']
const STAGE_ROW2 = ['checkout', 'card_funding', 'payment']
const FAILED = ['failed_precharge', 'failed', 'declined', 'cancelled']

export function progressRows(status: string, stage: string, prev?: ProgressView['rows']): ProgressView {
  const keep = prev ?? DEFAULT_ROWS
  if (status === 'completed') return { rows: ['done', 'done', 'done'], phase: 'done' }
  if (FAILED.includes(status)) return { rows: keep, phase: 'failed' }
  if (status === 'review') return { rows: keep, phase: 'review' }
  if (status === 'queued') return { rows: ['current', 'todo', 'todo'], phase: 'running' }
  if (status === 'running') {
    if (STAGE_ROW1.includes(stage)) return { rows: ['current', 'todo', 'todo'], phase: 'running' }
    if (STAGE_ROW2.includes(stage)) return { rows: ['done', 'current', 'todo'], phase: 'running' }
    if (stage === 'subscription') return { rows: ['done', 'done', 'current'], phase: 'running' }
  }
  return { rows: keep, phase: 'running' }
}
