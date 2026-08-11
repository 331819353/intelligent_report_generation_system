import { dataRequestAPI } from '../askdata/api/dataRequest'
import { administrationAPI } from './administration'
import { apiRequest } from './api'
import { dataSourceAPI } from './data-sources'
import { datasetAPI } from './datasets'
import type { WorkInboxItem } from './home-api'
import { canRunInlineTaskAction, taskResourceID, type InlineTaskAction } from './task-actions-model'

export { canRunInlineTaskAction, taskResourceID, type InlineTaskAction } from './task-actions-model'

function numericVersion(item: WorkInboxItem) {
  const value = Number(item.version)
  if (!Number.isSafeInteger(value) || value < 0) throw new Error('任务版本无效，请刷新后重试')
  return value
}

/** 将工作箱动作路由到来源模块已有的权威写接口，不在前端复制状态机。 */
export async function runInlineTaskAction(item: WorkInboxItem, action: InlineTaskAction, note = '') {
  if (!canRunInlineTaskAction(item, action) || !item.allowedActions.includes(action)) {
    throw new Error('当前任务不允许执行此操作，请刷新后重试')
  }
  const decision = action === 'APPROVE' ? 'APPROVED' : 'REJECTED'
  if (item.type === 'DOMAIN_ACCESS_APPROVAL' && action !== 'START') {
    await administrationAPI.reviewDomainApplication(item.objectId, decision, note)
    return
  }
  if (item.type === 'DATA_SOURCE_PUBLICATION' && action !== 'START') {
    const resourceID = taskResourceID(item, 'data-sources')
    if (!resourceID) throw new Error('数据源标识缺失，请打开来源页面处理')
    if (action === 'APPROVE') await dataSourceAPI.approvePublicationRequest(resourceID, item.objectId, numericVersion(item), note)
    else await dataSourceAPI.rejectPublicationRequest(resourceID, item.objectId, numericVersion(item), note)
    return
  }
  if (item.type === 'DATASET_PUBLICATION' && action !== 'START') {
    const resourceID = taskResourceID(item, 'datasets')
    if (!resourceID) throw new Error('数据集标识缺失，请打开来源页面处理')
    if (action === 'APPROVE') await datasetAPI.approvePublication(resourceID, item.objectId, numericVersion(item), note)
    else await datasetAPI.rejectPublication(resourceID, item.objectId, numericVersion(item), note)
    return
  }
  if (item.type === 'DATA_REQUEST') {
    await dataRequestAPI.transition(item.objectId, {
      toState: action === 'START' ? 'IN_PROGRESS' : decision,
      note,
      recordVersion: numericVersion(item),
    })
    return
  }
  if (item.type === 'RUNTIME_CONFIG_APPROVAL' && action === 'APPROVE') {
    await apiRequest(`/v1/runtime-config/versions/${encodeURIComponent(item.objectId)}/approve`, {
      method: 'POST',
      businessDomain: false,
      body: JSON.stringify({ expectedVersion: numericVersion(item) }),
    })
    return
  }
  throw new Error('该来源尚未提供可在任务中心调用的动作合同')
}
