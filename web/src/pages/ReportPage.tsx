import { useSearchParams } from 'react-router-dom'
import { ReportEditorPage } from './ReportEditorPage'
import { ReportRuntimePage } from './ReportRuntimePage'

/** Query mode selects the operation surface while the report identity stays on one stable route. */
export function ReportPage() {
  const [query] = useSearchParams()
  return query.get('mode') === 'edit' ? <ReportEditorPage /> : <ReportRuntimePage />
}
