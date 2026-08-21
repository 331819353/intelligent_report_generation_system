import type { Block, ReportComponent, Section } from '../render/schema.ts'
import { frameSlotRole, smartInsightPendingText } from './operations.ts'

function configuredDataComponent(component: ReportComponent | undefined) {
  const binding = component?.dataBinding
  if (!binding) return false
  if (binding.bindingMode === 'SEMANTIC_IR') return Boolean(binding.semanticQueryRef)
  const measures = binding.measures ?? []
  const dimensions = binding.dimensions ?? []
  return binding.bindingMode === 'DATASET_FIELD' && measures.length > 0 &&
    measures.every(item => Boolean(item.field && item.role)) &&
    dimensions.every(item => Boolean(item.field && item.role))
}

export function subsectionChartsReady(block: Block, components: ReportComponent[]) {
  const byId = new Map(components.map(component => [component.id, component]))
  const chartSlots = block.zones.flatMap(zone => zone.slots).filter(slot => frameSlotRole(slot.cardKind) === 'EVIDENCE')
  return chartSlots.length > 0 && chartSlots.every(slot => {
    const componentId = slot.componentId
    return componentId ? configuredDataComponent(byId.get(componentId)) : false
  })
}

export function sectionChartsReady(section: Section, components: ReportComponent[]) {
  const subsections = section.blocks.filter(block => block.cardKind?.startsWith('LAYOUT_SUBSECTION_'))
  return subsections.length > 0 && subsections.every(block => subsectionChartsReady(block, components))
}

export function smartInsightIsPending(component: ReportComponent | undefined) {
  return String(component?.options.richText ?? '').trim() === smartInsightPendingText
}
