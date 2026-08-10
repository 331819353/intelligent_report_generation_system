import { RequestError, apiRequest } from '../../lib/api.ts'
import { datasetAPI } from '../../lib/datasets.ts'
import type {
  CreateDataRequestInput,
  DataRequest,
  DataRequestDeliveryType,
  DataRequestFieldOption,
  DataRequestSensitivity,
  DataRequestState,
} from '../datarequest/model.ts'

export type DataRequestTransitionInput = {
  toState: DataRequestState
  note: string
  assigneeUserId?: string
  deliveryType?: DataRequestDeliveryType
  deliveryRef?: string
  recordVersion: number
}

export const dataRequestAPI = {
  list: (limit = 50) => apiRequest<{ items: DataRequest[] }>(`/v1/data-requests?limit=${limit}`, { cache: 'no-store' }),
  get: (requestId: string) => apiRequest<DataRequest>(`/v1/data-requests/${encodeURIComponent(requestId)}`, { cache: 'no-store' }),
  create: (input: CreateDataRequestInput) => apiRequest<DataRequest>('/v1/data-requests', {
    method: 'POST',
    cache: 'no-store',
    body: JSON.stringify(input),
  }),
  submit: (requestId: string, recordVersion: number) => apiRequest<DataRequest>(`/v1/data-requests/${encodeURIComponent(requestId)}/submit`, {
    method: 'POST',
    cache: 'no-store',
    body: JSON.stringify({ recordVersion }),
  }),
  transition: (requestId: string, input: DataRequestTransitionInput) => apiRequest<DataRequest>(`/v1/data-requests/${encodeURIComponent(requestId)}/transition`, {
    method: 'POST',
    cache: 'no-store',
    body: JSON.stringify(input),
  }),
}

const supportedSensitivity = (value: unknown): DataRequestSensitivity =>
  value === 'PUBLIC' || value === 'CONFIDENTIAL' || value === 'RESTRICTED' ? value : 'INTERNAL'

/** 从当前领域可见的已发布数据集构造申请字段目录；不读取任何结果行。 */
export async function loadDataRequestFieldOptions(): Promise<DataRequestFieldOption[]> {
  const page = await datasetAPI.list(200, 0)
  const published = page.items.filter(item => item.currentPublishedVersionId).slice(0, 40)
  const versions = await Promise.all(published.map(async dataset => {
    try {
      return { dataset, version: await datasetAPI.getVersion(dataset.id, dataset.currentPublishedVersionId!) }
    } catch (error) {
      if (error instanceof RequestError && (error.status === 403 || error.status === 404)) return null
      throw error
    }
  }))
  return versions.flatMap(item => {
    if (!item) return []
    const fields = Array.isArray(item.version.dsl.fields) ? item.version.dsl.fields : []
    return fields.flatMap((raw, index) => {
      if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return []
      const field = raw as Record<string, unknown>
      if (field.visible === false) return []
      const fieldId = typeof field.id === 'string' ? field.id : ''
      const fieldCode = typeof field.code === 'string' ? field.code : fieldId
      if (!fieldId) return []
      return [{
        datasetId: item.dataset.id,
        datasetName: item.dataset.name,
        datasetVersionId: item.version.id,
        fieldId,
        fieldCode,
        fieldName: typeof field.name === 'string' && field.name.trim() ? field.name : fieldCode || `字段 ${index + 1}`,
        sensitivityLevel: supportedSensitivity(field.sensitivityLevel),
      }]
    })
  }).sort((left, right) => left.datasetName.localeCompare(right.datasetName, 'zh-CN') || left.fieldName.localeCompare(right.fieldName, 'zh-CN'))
}
