import { configureStore, createSlice, type PayloadAction } from '@reduxjs/toolkit'
import { createDraftChange, type ReportChangeTarget, type ReportDraftChange, type ReportOperationType } from './api'
import type { ReportBreakpoint, ReportDefinition } from './types'

type HistoryEntry = { definition: ReportDefinition; operationId: string; target?: ReportChangeTarget }

export type ReportEditorState = {
  definition: ReportDefinition
  selectedCardId?: string
  breakpoint: ReportBreakpoint
  past: HistoryEntry[]
  future: HistoryEntry[]
  pendingChanges: ReportDraftChange[]
}

const slice = createSlice({
  name: 'reportEditor',
  initialState: undefined as unknown as ReportEditorState,
  reducers: {
    commit(state, action: PayloadAction<{ previous: ReportDefinition; definition: ReportDefinition; change: ReportDraftChange }>) {
      state.past.push({ definition: action.payload.previous, operationId: action.payload.change.clientOperationId, target: action.payload.change.target })
      if (state.past.length > 100) state.past.shift()
      state.definition = action.payload.definition
      state.future = []
      state.pendingChanges.push(action.payload.change)
    },
    coalesce(state, action: PayloadAction<{ definition: ReportDefinition; change: ReportDraftChange }>) {
      state.definition = action.payload.definition
      if (state.pendingChanges.length) state.pendingChanges[state.pendingChanges.length - 1] = action.payload.change
      if (state.past.length) {
        state.past[state.past.length - 1].operationId = action.payload.change.clientOperationId
        state.past[state.past.length - 1].target = action.payload.change.target
      }
    },
    undo(state, action: PayloadAction<{ current: ReportDefinition; definition: ReportDefinition; change: ReportDraftChange; source: HistoryEntry }>) {
      state.future.unshift({ definition: action.payload.current, operationId: action.payload.source.operationId, target: action.payload.source.target })
      state.past.pop()
      state.definition = action.payload.definition
      state.pendingChanges.push(action.payload.change)
    },
    redo(state, action: PayloadAction<{ current: ReportDefinition; definition: ReportDefinition; change: ReportDraftChange; source: HistoryEntry }>) {
      state.past.push({ definition: action.payload.current, operationId: action.payload.source.operationId, target: action.payload.source.target })
      state.future.shift()
      state.definition = action.payload.definition
      state.pendingChanges.push(action.payload.change)
    },
    selectCard(state, action: PayloadAction<string | undefined>) { state.selectedCardId = action.payload },
    selectBreakpoint(state, action: PayloadAction<ReportBreakpoint>) { state.breakpoint = action.payload },
    acknowledge(state, action: PayloadAction<string[]>) {
      const ids = new Set(action.payload)
      state.pendingChanges = state.pendingChanges.filter(change => !ids.has(change.clientOperationId))
    },
    replaceFromServer(state, action: PayloadAction<ReportDefinition>) {
      state.definition = action.payload
      state.past = []
      state.future = []
      state.pendingChanges = []
      if (state.selectedCardId && !state.definition.cards.some(card => card.id === state.selectedCardId)) state.selectedCardId = undefined
    },
  },
})

export function createReportEditorStore(definition: ReportDefinition, pendingChanges: ReportDraftChange[] = []) {
  return configureStore({ reducer: slice.reducer, preloadedState: { definition: cloneDefinition(definition), breakpoint: 'lg', past: [], future: [], pendingChanges } })
}

// Report DSL 必须是纯 JSON；JSON 快照既保持合同边界，也能安全脱离 Immer Proxy。
function cloneDefinition(value: ReportDefinition): ReportDefinition {
  return JSON.parse(JSON.stringify(value)) as ReportDefinition
}

export type ReportEditorStore = ReturnType<typeof createReportEditorStore>
export type ReportEditorDispatch = ReportEditorStore['dispatch']
export const reportEditorActions = slice.actions

export function commitReportEdit(store: ReportEditorStore, definition: ReportDefinition, operationType: ReportOperationType, target?: ReportChangeTarget) {
  const state = store.getState()
  const before = state.definition
  if (JSON.stringify(before) === JSON.stringify(definition)) return
  const previous = state.pendingChanges.at(-1)
  const canCoalesce = previous?.operationType === operationType
    && JSON.stringify(previous.target ?? {}) === JSON.stringify(target ?? {})
    && ['REPORT_SETTINGS_UPDATE', 'FILTER_UPDATE', 'CARD_LAYOUT_UPDATE', 'CARD_CONFIG_UPDATE'].includes(operationType)
    && state.past.length > 0
  if (canCoalesce) {
    const base = state.past.at(-1)!.definition
    store.dispatch(reportEditorActions.coalesce({ definition, change: createDraftChange(base, definition, operationType, target) }))
    return
  }
  store.dispatch(reportEditorActions.commit({ previous: cloneDefinition(before), definition: cloneDefinition(definition), change: createDraftChange(before, definition, operationType, target) }))
}

export function undoReportEdit(store: ReportEditorStore) {
  const state = store.getState()
  const source = state.past.at(-1)
  if (!source) return
  const change = createDraftChange(state.definition, source.definition, 'UNDO', { ...source.target, referencedOperationId: source.operationId })
  store.dispatch(reportEditorActions.undo({ current: cloneDefinition(state.definition), definition: cloneDefinition(source.definition), change, source }))
}

export function redoReportEdit(store: ReportEditorStore) {
  const state = store.getState()
  const source = state.future[0]
  if (!source) return
  const change = createDraftChange(state.definition, source.definition, 'REDO', { ...source.target, referencedOperationId: source.operationId })
  store.dispatch(reportEditorActions.redo({ current: cloneDefinition(state.definition), definition: cloneDefinition(source.definition), change, source }))
}
