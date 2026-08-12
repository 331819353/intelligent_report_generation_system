import type {
  QuestionResult,
  QuestionResultColumn,
  QuestionResultDataset,
  QuestionResultView,
} from '../../lib/ask-data-api'

const exactNumber = /^-?(0|[1-9][0-9]*)(\.[0-9]+)?$/
const exactInteger = /^-?(0|[1-9][0-9]*)$/

function columnMap(dataset: QuestionResultDataset): Map<string, QuestionResultColumn> {
  return new Map(dataset.columns.map(column => [column.key, column]))
}

export function resultDataset(result: QuestionResult, datasetId: string): QuestionResultDataset | undefined {
  return result.datasets.find(dataset => dataset.id === datasetId)
}

export function numericResultValue(value: string | null | undefined): number | undefined {
  if (value === null || value === undefined || !exactNumber.test(value)) return undefined
  const numeric = Number(value)
  return Number.isFinite(numeric) && Math.abs(numeric) <= Number.MAX_SAFE_INTEGER ? numeric : undefined
}

function validViewCellShape(view: QuestionResultView, dataset: QuestionResultDataset, requireSafeNumbers: boolean): boolean {
  const columns = columnMap(dataset)
	const dimensionKeys = view.dimensionKeys ?? []
	const measureKeys = view.measureKeys ?? []
	const dimensions = dimensionKeys.map(key => columns.get(key))
	const measures = measureKeys.map(key => columns.get(key))
  if (dimensions.some(column => !column || column.role !== 'DIMENSION')) return false
  if (measures.some(column => !column || column.role !== 'MEASURE' || !['INTEGER', 'DECIMAL'].includes(column.type))) return false
	return !requireSafeNumbers || dataset.rows.every(row => measureKeys.every(key => row[key] === null || numericResultValue(row[key]) !== undefined))
}

export function resultViewEligible(result: QuestionResult, view: QuestionResultView): boolean {
  const dataset = resultDataset(result, view.datasetId)
  if (!dataset || dataset.totalRows < dataset.rows.length) return false
  if (!validViewCellShape(view, dataset, view.type !== 'TABLE')) return false
	const dimensionKeys = view.dimensionKeys ?? []
	const measureKeys = view.measureKeys ?? []
  if (view.type === 'LINE') {
		const dimension = dataset.columns.find(column => column.key === dimensionKeys[0])
		return dataset.rows.length >= 2 && dimensionKeys.length === 1 && measureKeys.length === 1 &&
      (dimension?.type === 'DATE' || dimension?.type === 'DATETIME')
  }
  if (view.type === 'BAR') {
		return dataset.rows.length >= 2 && dataset.rows.length <= 20 && dimensionKeys.length === 1 && measureKeys.length === 1
  }
  if (view.type === 'KPI') {
		return dataset.rows.length === 1 && dimensionKeys.length === 0 && measureKeys.length === 1
  }
  return view.type === 'TABLE'
}

export function eligibleResultViews(result: QuestionResult): QuestionResultView[] {
  return result.views.filter(view => resultViewEligible(result, view))
}

export function initialResultView(result: QuestionResult): QuestionResultView | undefined {
  const eligible = eligibleResultViews(result)
  const recommended = eligible.find(view => view.id === result.recommendedViewId)
  if (recommended) return recommended
  const fallback = eligible.find(view => view.id === result.defaultViewId)
  if (fallback) return fallback
  return ['LINE', 'BAR', 'TABLE', 'KPI']
    .map(type => eligible.find(view => view.type === type))
    .find((view): view is QuestionResultView => Boolean(view))
}

export function questionResultReady(result: QuestionResult | undefined): result is QuestionResult {
  if (!result || result.schemaVersion !== 'question-result-v1' || !result.title.trim()) return false
  if (!exactNumber.test(result.summary.value) || !result.summary.formattedValue.trim() || !result.summary.metricLabel.trim()) return false
  const evidence = result.evidence
  if (!evidence || result.evidenceIds.length === 0 || !evidence.definition.trim()) return false
  if (!evidence.owner.id.trim() || !evidence.owner.displayName.trim() || !evidence.semanticVersion.trim()) return false
  if (!evidence.time.label.trim() || !evidence.time.start.trim() || !evidence.time.end.trim() || !evidence.time.timezone.trim()) return false
  if (!evidence.quality.dataAsOf.trim() || evidence.quality.rulesPassed < 0 || evidence.quality.rulesTotal < evidence.quality.rulesPassed) return false
  return initialResultView(result) !== undefined
}

export function formatExactNumber(value: string | null, fallback = '—'): string {
  if (value === null || !exactNumber.test(value)) return fallback
  const sign = value.startsWith('-') ? '-' : ''
  const unsigned = sign ? value.slice(1) : value
  const [integer, fraction] = unsigned.split('.')
  const grouped = integer.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return `${sign}${grouped}${fraction ? `.${fraction}` : ''}`
}

export function formatResultCell(value: string | null, column: QuestionResultColumn): string {
  if (value === null) return '—'
  if (column.type === 'INTEGER' && !exactInteger.test(value)) return '—'
  if (column.type === 'INTEGER' || column.type === 'DECIMAL') return formatExactNumber(value)
  if (column.type === 'DATE') return value.replaceAll('-', '/')
  if (column.type === 'DATETIME') return value.replace('T', ' ').replace(/([+-]\d{2}:\d{2}|Z)$/, '')
  return value
}

export function resultPageCount(dataset: QuestionResultDataset): number {
  return Math.max(1, Math.ceil(dataset.totalRows / dataset.pageSize))
}
