import { apiRequest } from './api'
import { homeAPI, type WorkInboxItem, type WorkItemCommand } from './home-api'
import { canRunInlineTaskAction, type InlineTaskAction } from './task-actions-model'

export { canRunInlineTaskAction, taskResourceID, type InlineTaskAction } from './task-actions-model'

function commandBody(command: WorkItemCommand, expectedVersion: string, note: string) {
	const body: Record<string, string | number> = { ...command.fixedValues }
	for (const field of command.fields ?? []) {
		if (field.name === 'expectedVersion' || field.name === 'recordVersion') {
			const version = Number(expectedVersion)
			if (!Number.isSafeInteger(version) || version < 1) throw new Error('任务版本无效，请刷新后重试')
			body[field.name] = version
			continue
		}
		if (['reason', 'comment', 'note', 'completionEvidence'].includes(field.name)) {
			if (field.required && !note) throw new Error('请填写处理说明后再提交')
			body[field.name] = note
			continue
		}
		if (field.required) throw new Error(`任务缺少必填信息：${field.name}`)
	}
	return body
}

/** 依据工作箱返回的受权命令调用来源模块，不在前端复制对象标识或状态机。 */
export async function runInlineTaskAction(item: WorkInboxItem, action: InlineTaskAction, note = '') {
  if (!canRunInlineTaskAction(item, action) || !item.allowedActions.includes(action)) {
    throw new Error('当前任务不允许执行此操作，请刷新后重试')
  }
	const detail = await homeAPI.getWorkItemDetail(item)
	const command = detail.actionContext.commands.find(candidate => candidate.action === action)
	if (!command) throw new Error('来源模块未提供该操作合同，请刷新后重试')
	const path = command.href.replace(/^\/api/, '')
	await apiRequest(path, {
		method: command.method,
		headers: command.idempotencyRequired ? { 'Idempotency-Key': crypto.randomUUID() } : undefined,
		body: command.method === 'GET' ? undefined : JSON.stringify(commandBody(command, detail.actionContext.expectedVersion, note.trim())),
	})
}
