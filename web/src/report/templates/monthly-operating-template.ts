import monthlyOperatingTemplateData from './monthly-operating-template.json' with { type: 'json' }
import { normalizeAnalysisTemplate, type AnalysisTemplate } from './model.ts'

/**
 * Return an isolated editable copy of the bundled JSON template.
 *
 * The JSON file is the source of truth. Normalization only supplies backwards-
 * compatible defaults when older persisted drafts are opened by a newer client.
 */
export function createMonthlyOperatingAnalysisTemplate(): AnalysisTemplate {
  const template = structuredClone(monthlyOperatingTemplateData) as unknown as AnalysisTemplate
  return normalizeAnalysisTemplate(template)
}
