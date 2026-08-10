import { apiRequest } from '../../lib/api'

export type RuntimeDefinition = {
  metadata: { id: string; code: string; name: string; description?: string; reportType: 'REPORT' | 'DASHBOARD' }
  pages: Array<{ id: string; name: string }>
  components: Array<{ id: string; templateRef: { type: string; version: string }; options: { title?: string } }>
}
export type LoadedReport = {
  reportId: string
  versionId: string
  versionNo: number
  definitionHash: string
  definition: RuntimeDefinition
}

export const reportRuntimeAPI = {
  load(reportId: string, versionNo?: number) {
    const suffix = versionNo ? `?versionNo=${versionNo}` : ''
    return apiRequest<LoadedReport>(`/v1/reports/${encodeURIComponent(reportId)}/runtime${suffix}`)
  },
}
