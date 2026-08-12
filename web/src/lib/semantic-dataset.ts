import type { PublishedVersionRecord } from './datasets.ts'

export type SemanticGrainField = { code: string; role: string }

/**
 * Resolve the stable business grain of a published dataset. Aggregated DWS/ADS
 * datasets commonly use several TIME/DIMENSION fields as a compound key, so
 * outputGrain is authoritative and IDENTIFIER is only a legacy fallback.
 */
export function semanticDatasetGrain(
  version: PublishedVersionRecord,
  fields: SemanticGrainField[],
) {
  const fieldCodes = new Set(fields.map(field => field.code))
  const candidate = version.dsl.outputGrain
  const outputGrain = candidate && typeof candidate === 'object' && !Array.isArray(candidate)
    ? candidate as Record<string, unknown>
    : {}
  const publishedKeys = Array.isArray(outputGrain.keyFields)
    ? outputGrain.keyFields.filter((value): value is string => typeof value === 'string' && fieldCodes.has(value))
    : []
  const identifierKeys = fields.filter(field => field.role === 'IDENTIFIER').map(field => field.code)
  const description = typeof outputGrain.description === 'string' ? outputGrain.description.trim() : ''
  const timeFieldCandidate = typeof outputGrain.timeField === 'string' ? outputGrain.timeField.trim() : ''
  return {
    keys: publishedKeys.length ? publishedKeys : identifierKeys,
    description,
    timeField: fieldCodes.has(timeFieldCandidate) ? timeFieldCandidate : '',
  }
}
