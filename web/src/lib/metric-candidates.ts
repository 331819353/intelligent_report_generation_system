import { apiRequest } from './api'

export type MetricIdentificationResult = {
  eligibleDatasetCount: number
  enqueuedJobCount: number
  historicalMetricCount: number
  existingCandidateCount: number
  dimensionDatasetCount: number
  dimensionAssetCount: number
  datasets: MetricIdentificationDatasetIndex[]
}

export type MetricIdentificationMetric = {
  code: string
  name: string
  status: string
  source: 'METRIC_VERSION' | 'CANDIDATE'
  allowedFieldIds: string[]
  vectorStatus: 'PENDING' | 'RUNNING' | 'SUCCEEDED' | 'FAILED'
}

export type MetricIdentificationDimension = {
  fieldId: string
  code: string
  name: string
  memberIndexPolicy: 'FULL' | 'EXACT_ONLY' | 'NONE'
  memberValues: string[]
  memberValueCount: number
  vectorizedMemberCount: number
  valuesTruncated: boolean
  sensitive: boolean
}

export type MetricIdentificationDatasetIndex = {
  datasetId: string
  datasetVersionId: string
  code: string
  name: string
  layer: 'DWS'
  domain: string
  metrics: MetricIdentificationMetric[]
  dimensions: MetricIdentificationDimension[]
  indexDocument: {
    domain: string
    datasetVersionId: string
    metrics: MetricIdentificationMetric[]
    dimensions: MetricIdentificationDimension[]
    retrieval: string[]
  }
}

export const metricCandidateAPI = {
  identify: () => apiRequest<MetricIdentificationResult>('/v1/metric-candidates/identify', {
    method: 'POST', cache: 'no-store',
  }),
}
