import type { ComponentType } from 'react'
import type { CardInteractionEvent, CardQueryResult, CardType, ReportCardDefinition, ReportDefinition, ReportValidationIssue } from './types'

export type CardRenderMode = 'designer' | 'runtime'

export type CardRenderProps = {
  card: ReportCardDefinition
  definition: ReportDefinition
  result?: CardQueryResult
  mode: CardRenderMode
  filters: Record<string, unknown>
  onInteraction?: (event: CardInteractionEvent) => void
}

export type CardPropertyProps = {
  card: ReportCardDefinition
  definition: ReportDefinition
  onChange: (card: ReportCardDefinition) => void
}

export type SemanticQuery = {
  metricIds: string[]
  dimensionIds: string[]
  filters: ReportCardDefinition['binding']['filters']
  sort: ReportCardDefinition['binding']['sort']
  limit: number
}

export interface CardPlugin {
  type: CardType
  version: string
  label: string
  description: string
  configSchema: object
  bindingSchema: object
  validateBinding(card: ReportCardDefinition, definition: ReportDefinition): ReportValidationIssue[]
  buildQuery(card: ReportCardDefinition): SemanticQuery | undefined
  migrate(config: unknown, fromVersion: string): Record<string, unknown>
  Renderer: ComponentType<CardRenderProps>
  PropertyPanel?: ComponentType<CardPropertyProps>
}

export class CardRegistry {
  private readonly plugins = new Map<CardType, CardPlugin>()

  register(plugin: CardPlugin) {
    if (this.plugins.has(plugin.type)) throw new Error(`Card plugin ${plugin.type} 已注册`)
    this.plugins.set(plugin.type, plugin)
    return this
  }

  get(type: CardType): CardPlugin | undefined { return this.plugins.get(type) }
  list(): CardPlugin[] { return [...this.plugins.values()] }
}
