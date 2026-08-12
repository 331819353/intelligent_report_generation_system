import type { WorkInboxItem } from './home-api.ts'

export type InlineTaskAction = 'APPROVE' | 'REJECT' | 'START' | 'BLOCK' | 'COMPLETE'

const supportedActions: Partial<Record<string, readonly InlineTaskAction[]>> = {
  DOMAIN_ACCESS_APPROVAL: ['APPROVE', 'REJECT'],
  DATA_SOURCE_PUBLICATION: ['APPROVE', 'REJECT'],
  DATASET_PUBLICATION: ['APPROVE', 'REJECT'],
  DATA_REQUEST: ['APPROVE', 'REJECT', 'START'],
	DECISION_APPROVAL: ['APPROVE', 'REJECT'],
	ACTION_ASSIGNED: ['START', 'BLOCK', 'COMPLETE'],
  RUNTIME_CONFIG_APPROVAL: ['APPROVE', 'REJECT'],
}

/** 只有现有来源合同具备完整对象标识和乐观锁版本时，才允许在统一工作箱内直接执行。 */
export function canRunInlineTaskAction(item: WorkInboxItem, action: string): action is InlineTaskAction {
  return supportedActions[item.type]?.includes(action as InlineTaskAction) ?? false
}

export function taskResourceID(item: Pick<WorkInboxItem, 'sourceHref'>, kind: 'data-sources' | 'datasets') {
  const match = item.sourceHref.match(new RegExp(`^/governance/${kind}/([0-9a-f-]+)$`, 'i'))
  return match?.[1]
}
