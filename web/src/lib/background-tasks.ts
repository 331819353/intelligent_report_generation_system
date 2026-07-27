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
  canRetry: boolean
  retryDisabledReason?: string
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
export type BackgroundTaskFocus = 'DIM_MODELING' | 'DWD_MODELING' | 'DWS_MODELING'

const backgroundTaskFocusKey = 'intelligent-report-background-task-focus'

export function rememberBackgroundTaskFocus(focus: BackgroundTaskFocus) {
  try {
    sessionStorage.setItem(backgroundTaskFocusKey, focus)
  } catch {
    // 受限浏览器环境不支持 sessionStorage 时，任务中心仍可使用全局视图。
  }
}

export function takeBackgroundTaskFocus(): BackgroundTaskFocus | null {
  try {
    const value = sessionStorage.getItem(backgroundTaskFocusKey)
    sessionStorage.removeItem(backgroundTaskFocusKey)
    return value === 'DIM_MODELING' || value === 'DWD_MODELING' ||
      value === 'DWS_MODELING' ? value : null
  } catch {
    return null
  }
}

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
  retry(task: Pick<BackgroundTask, 'kind' | 'id'>) {
    return apiRequest<BackgroundTask>(
      `/v1/background-tasks/${encodeURIComponent(task.kind)}/${encodeURIComponent(task.id)}/retry`,
      { method: 'POST' },
    )
  },
}
