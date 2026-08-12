import { useState, useSyncExternalStore } from 'react'
import {
  assetAccessAPI,
  type AssetAccessImpact,
  type AssetResourceType,
  type AssetSharingScope,
} from '../lib/asset-access'
import { currentSubject } from '../lib/auth'
import { currentDomain, currentDomainID, subscribeDomainChange } from '../lib/domain-context'

const labels: Record<AssetSharingScope, string> = {
  PRIVATE: '仅自己',
  DOMAIN: '领域内共享',
}

export function AssetSharingSelect({
  resourceType,
  resourceID,
  value,
  ownerUserID,
  assetDomainID,
  disabled = false,
  onChange,
}: {
  resourceType: AssetResourceType
  resourceID: string
  value: AssetSharingScope
  ownerUserID: string | undefined
  assetDomainID: string | undefined
  disabled?: boolean
  onChange: (value: AssetSharingScope) => void
}) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [pending, setPending] = useState<AssetAccessImpact | null>(null)
  const selectedDomainID = useSyncExternalStore(
    subscribeDomainChange,
    currentDomainID,
    () => '',
  )
  const subject = currentSubject()
  const canManage = (Boolean(ownerUserID) && ownerUserID === subject) ||
    Boolean(currentDomain()?.administrators?.some(item => item.id === subject))
  const isOwner = canManage &&
    Boolean(assetDomainID) &&
    assetDomainID === selectedDomainID

  const apply = async (next: AssetSharingScope) => {
    setBusy(true)
    setError('')
    try {
      const result = await assetAccessAPI.update(resourceType, resourceID, next)
      setPending(null)
      onChange(result.sharingScope)
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '更新共享范围失败')
    } finally {
      setBusy(false)
    }
  }

  const update = async (next: AssetSharingScope) => {
    if (next === value) return
    // Widening never takes access away, so it applies directly. Narrowing is
    // previewed first: the server reports which governed objects depend on
    // this asset and whether the change is allowed at all.
    if (next !== 'PRIVATE') {
      await apply(next)
      return
    }
    setBusy(true)
    setError('')
    try {
      setPending(await assetAccessAPI.impact(resourceType, resourceID, next))
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '共享范围影响预览失败')
    } finally {
      setBusy(false)
    }
  }

  if (!isOwner) {
    return <span
      className={`asset-sharing-select scope-${value.toLowerCase()} is-readonly`}
      title="仅资产持有者或领域管理员可以在资产所属领域修改共享状态"
      onClick={event => event.stopPropagation()}
    >
      <span className="asset-sharing-select-label">{labels[value]}</span>
    </span>
  }

  const dependentLabels: Record<string, string> = {
    DATASET: '数据集', SEMANTIC_MODEL: '语义模型', REPORT: '报告',
  }

  return <><label
    className={`asset-sharing-select scope-${value.toLowerCase()}${error ? ' has-error' : ''}`}
    title={error || '资产只能在所属领域内共享'}
    onClick={event => event.stopPropagation()}
  >
    <span className="asset-sharing-select-label">{busy ? '更新中' : labels[value]}</span>
    <svg className="asset-sharing-select-chevron" aria-hidden="true" viewBox="0 0 12 12">
      <path d="m3 4.75 3 3 3-3" />
    </svg>
    <select
      aria-label="共享范围"
      value={value}
      disabled={disabled || busy}
      onChange={event => void update(event.target.value as AssetSharingScope)}
    >
      <option value="PRIVATE">仅自己</option>
      <option value="DOMAIN">领域内共享</option>
    </select>
  </label>
  {pending && <div className="asset-scope-impact-backdrop" role="presentation" onClick={event => event.stopPropagation()}>
    <section className="asset-scope-impact" role="dialog" aria-modal="true" aria-label="共享范围影响预览">
      <header>
        <strong>收回领域共享前的影响预览</strong>
        <small>改为「仅自己」不会删除任何对象，但会移除它们依赖的读取权限。</small>
      </header>
      <dl>
        <div><dt>领域内将失去访问的成员</dt><dd>{pending.domainMemberCount}</dd></div>
        <div><dt>依赖该资产的受治理对象</dt><dd>{pending.dependents.length}</dd></div>
        <div><dt>其中由他人持有</dt><dd>{pending.blockingDependents}</dd></div>
      </dl>
      {pending.dependents.length > 0 && <ul>
        {pending.dependents.map(item => <li className={item.foreignOwner ? 'is-blocking' : ''} key={`${item.type}:${item.id}`}>
          <span>{dependentLabels[item.type] ?? item.type}</span>
          <strong>{item.name}</strong>
          <em>{item.status}{item.foreignOwner ? ' · 他人持有' : ''}</em>
        </li>)}
      </ul>}
      {!pending.canApply && <p className="asset-scope-impact-blocked">
        {pending.blockedCode === 'ASSET_SCOPE_OWNER_REQUIRED'
          ? '该资产当前没有持有人，改为「仅自己」会使其除管理员外无人可达。请先完成持有人转交。'
          : '仍有由他人持有的受治理对象依赖该资产。请先转交或下线这些对象，再收回共享。'}
      </p>}
      <footer>
        <button className="quiet-button" type="button" disabled={busy} onClick={() => setPending(null)}>取消</button>
        <button
          className="primary-button compact"
          type="button"
          disabled={busy || !pending.canApply}
          onClick={() => void apply('PRIVATE')}
        >{busy ? '正在收回…' : '确认收回共享'}</button>
      </footer>
    </section>
  </div>}
  </>
}
