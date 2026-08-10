import type { QuestionScopeVerdict } from '../../lib/ask-data-api.ts'

export function detailDataRequestAction(verdict?: QuestionScopeVerdict) {
  if (!verdict || verdict.outcome !== 'OUT_OF_SCOPE' || verdict.reason !== 'SCOPE_DETAIL_LIST') return undefined
  return verdict.nextActions.find(action => action.kind === 'DATA_REQUEST' && action.payload.target === 'DATA_REQUEST_DIALOG')
}
