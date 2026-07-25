import { apiRequest } from './api'

export type BackgroundTaskStatus =
  | 'QUEUED'
  | 'RUNNING'
  | 'SUCCEEDED'
  | 'PARTIAL'
  | 'FAILED'
  | 'CANCELLED'
  | 'SKIPPED'
  | 'STALE'

export type BackgroundTask = {
  id: string
  kind: string
  kindLabel: string
  name: string
  description: string
  status: BackgroundTaskStatus
  sourceStatus: string
  resourceType: 'DATA_SOURCE' | 'DATASET'
  resourceId: string
  processed?: number
  total?: number
  progressPercent?: number
  progressText: string
  attempt: number
  maxAttempts: number
  canCancel: boolean
  cancelDisabledReason?: string
  errorCode?: string
  errorMessage?: string
  createdAt: string
  startedAt?: string
  updatedAt: string
  completedAt?: string
}

export type BackgroundTaskPage = {
  items: BackgroundTask[]
  activeCount: number
  generatedAt: string
}

export type BackgroundTaskView = 'ACTIVE' | 'RECENT' | 'ALL'

export const backgroundTaskAPI = {
  list(view: BackgroundTaskView = 'ACTIVE', limit = 100) {
    const query = new URLSearchParams({ view, limit: String(limit) })
    return apiRequest<BackgroundTaskPage>(`/v1/background-tasks?${query}`)
  },
  cancel(task: Pick<BackgroundTask, 'kind' | 'id'>) {
    return apiRequest<BackgroundTask>(
      `/v1/background-tasks/${encodeURIComponent(task.kind)}/${encodeURIComponent(task.id)}/cancel`,
      { method: 'POST' },
    )
  },
}
