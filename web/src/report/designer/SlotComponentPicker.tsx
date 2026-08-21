import { ChartLineUp, Check, Lightbulb, MagnifyingGlass, X } from '@phosphor-icons/react'
import { useMemo, useState } from 'react'

import {
  analysisCardCatalog, analysisCardOption, analysisCardVariants,
  type AnalysisCardCatalogItem, type AnalysisCardVariant,
} from '../analysis/catalog.ts'
import type { ComponentManifest } from '../render/manifests.ts'
import type { ComponentOptions } from '../render/schema.ts'

export type SlotPickerTarget = {
  blockId: string
  zoneId: string
  slotId: string
  role: 'CONCLUSION' | 'EVIDENCE'
}

type PickerCandidate = {
  manifest: ComponentManifest
  catalog?: AnalysisCardCatalogItem
}

function cardThumbnail(item: AnalysisCardCatalogItem, variant: AnalysisCardVariant) {
  const extension = item.id === 37 ? 'png' : 'webp'
  return `/analysis-card-gallery/${String(item.id).padStart(2, '0')}-${item.slug}/${variant}.${extension}`
}

function isConclusionManifest(manifest: ComponentManifest) {
  return manifest.type === 'analysis-insight-conclusion' || manifest.type === 'insight-text' || manifest.type === 'rich-text'
}

export function SlotComponentPicker({ target, manifests, busy, error, onClose, onSelect }: {
  target: SlotPickerTarget
  manifests: ComponentManifest[]
  busy?: boolean
  error?: string
  onClose: () => void
  onSelect: (manifest: ComponentManifest, options?: Partial<ComponentOptions>) => void
}) {
  const conclusion = target.role === 'CONCLUSION'
  const [query, setQuery] = useState('')
  const [variant, setVariant] = useState<AnalysisCardVariant>('01')
  const candidates = useMemo<PickerCandidate[]>(() => {
    const catalogByType = new Map(analysisCardCatalog.map(item => [item.type, item]))
    return manifests
      .filter(manifest => conclusion ? isConclusionManifest(manifest) : manifest.category === 'CHART')
      .map(manifest => ({ manifest, catalog: catalogByType.get(manifest.type) }))
      .sort((left, right) => (left.catalog?.id ?? 999) - (right.catalog?.id ?? 999) || left.manifest.displayName.localeCompare(right.manifest.displayName))
  }, [conclusion, manifests])
  const [selectedRef, setSelectedRef] = useState(() => candidates[0] ? `${candidates[0].manifest.type}@${candidates[0].manifest.version}` : '')
  const normalized = query.trim().toLocaleLowerCase()
  const visible = candidates.filter(candidate => !normalized || [
    candidate.manifest.displayName,
    candidate.catalog?.name ?? '',
    candidate.catalog?.question ?? '',
    ...(candidate.catalog?.subtypes ?? []),
  ].some(value => value.toLocaleLowerCase().includes(normalized)))
  const selected = candidates.find(candidate => `${candidate.manifest.type}@${candidate.manifest.version}` === selectedRef) ?? visible[0]
  const Icon = conclusion ? Lightbulb : ChartLineUp

  return <div className="report-modal-backdrop" role="presentation" onMouseDown={onClose}>
    <section className="report-modal report-slot-picker" role="dialog" aria-modal="true" aria-labelledby="slot-picker-title"
      onMouseDown={event => event.stopPropagation()}>
      <header>
        <div className="report-slot-picker-heading"><span><Icon size={19} weight="duotone" /></span><div>
          <small>{conclusion ? '结论槽位' : '论据图表槽位'}</small>
          <h2 id="slot-picker-title">{conclusion ? '选择结论样式' : '选择图表组件'}</h2>
        </div></div>
        <button type="button" aria-label="关闭选择弹窗" onClick={onClose}><X size={18} /></button>
      </header>
      <div className="report-slot-picker-body">
        <section className="report-slot-picker-catalog" aria-label={conclusion ? '结论类型' : '图表类型'}>
          <div className="report-slot-picker-search"><MagnifyingGlass size={16} />
            <input value={query} onChange={event => setQuery(event.target.value)}
              placeholder={conclusion ? '搜索结论类型' : '搜索图表、问题或场景'} aria-label={conclusion ? '搜索结论类型' : '搜索图表类型'} />
          </div>
          <div className="report-slot-picker-grid">
            {visible.map(candidate => {
              const ref = `${candidate.manifest.type}@${candidate.manifest.version}`
              return <button type="button" className={ref === selectedRef ? 'is-selected' : ''} key={ref}
                onClick={() => setSelectedRef(ref)}>
                {candidate.catalog
                  ? <img src={cardThumbnail(candidate.catalog, '01')} alt="" />
                  : <span className="report-slot-picker-fallback"><Icon size={25} weight="duotone" /></span>}
                <span><strong>{candidate.catalog?.name ?? candidate.manifest.displayName}</strong>
                  <small>{candidate.catalog?.question ?? candidate.manifest.category}</small></span>
                {ref === selectedRef && <Check size={15} weight="bold" />}
              </button>
            })}
          </div>
          {visible.length === 0 && <p className="report-slot-picker-empty">没有匹配的可用组件。</p>}
        </section>
        <aside className="report-slot-picker-variants" aria-label="样式选择">
          <header><span>样式预览</span><strong>{selected?.catalog?.name ?? selected?.manifest.displayName ?? '请选择组件'}</strong>
            <small>{selected?.catalog?.question ?? '选择后填入当前空槽位'}</small></header>
          {selected?.catalog ? <div>
            {analysisCardVariants.map(item => <button type="button" className={variant === item.id ? 'is-selected' : ''}
              aria-pressed={variant === item.id} key={item.id} onClick={() => setVariant(item.id)}>
              <img src={cardThumbnail(selected.catalog!, item.id)} alt={`${item.name}预览`} />
              <span><strong>{item.name}</strong><small>{item.description}</small></span>
              {variant === item.id && <Check size={15} weight="bold" />}
            </button>)}
          </div> : <div className="report-slot-picker-generic-preview"><Icon size={34} weight="duotone" /><span>标准样式</span></div>}
        </aside>
      </div>
      {error && <p className="report-slot-picker-error">{error}</p>}
      <footer><button type="button" className="quiet-button" disabled={busy} onClick={onClose}>取消</button>
        <button type="button" className="primary-button" disabled={busy || !selected}
          onClick={() => selected && onSelect(selected.manifest, selected.catalog ? analysisCardOption(variant) : undefined)}>
          {busy ? '正在插入…' : conclusion ? '插入结论' : '插入图表'}
        </button>
      </footer>
    </section>
  </div>
}
