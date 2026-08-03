import {
  ArrowRightIcon,
  BookOpenTextIcon,
  CheckCircleIcon,
  FunctionIcon,
  GraphIcon,
  PathIcon,
  ShieldCheckIcon,
  SlidersHorizontalIcon,
  TableIcon,
  WarningCircleIcon,
} from '@phosphor-icons/react'
import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import {
  semanticAssetAPI,
  type SemanticCatalogView,
  type SemanticReleaseState,
} from '../lib/semantic-assets'

const percent = (ready: number, total: number) =>
  total > 0 ? Math.round(ready / total * 100) : 0

const formatCount = (value: number) => new Intl.NumberFormat('zh-CN').format(value)

export function AssetOverviewPage() {
  const [data, setData] = useState<SemanticCatalogView>()
  const [releaseState, setReleaseState] = useState<SemanticReleaseState>()
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [reloadToken, setReloadToken] = useState(0)

  const reload = () => {
    setLoading(true)
    setError('')
    setReloadToken(value => value + 1)
  }

  useEffect(() => {
    let active = true
    Promise.all([
      semanticAssetAPI.catalog(),
      semanticAssetAPI.activeRelease(),
    ]).then(([catalog, release]) => {
      if (active) {
        setData(catalog)
        setReleaseState(release)
      }
    }).catch(cause => {
      if (active) setError(cause instanceof Error ? cause.message : '资产治理概览加载失败')
    }).finally(() => {
      if (active) setLoading(false)
    })
    return () => { active = false }
  }, [reloadToken])

  const readiness = data?.readiness
  const counts = readiness?.counts
  const checks = readiness?.checks ?? []
  const passedChecks = checks.filter(item => item.status === 'PASS').length

  return (
    <AppShell title="资产管理中心" eyebrow="语义优先 · 可执行合同">
      <AssetManagementTabs />
      <section className="asset-overview-page">
        <header className="asset-overview-hero">
          <div>
            <span className="eyebrow">Governed semantic assets</span>
            <h2>从业务口径到问数证据的一套资产控制面</h2>
            <p>统一查看指标、维度、业务词汇、维度值和解析规则的发布状态；只有通过版本、成员索引、关系和语义图检查的资产才进入智能问答。</p>
          </div>
          <div className="asset-overview-release-state">
            <span>{releaseState?.activeReleaseId ? '活动语义版本' : '语义发布状态'}</span>
            <strong>{releaseState?.semanticVersion || '尚未原子激活'}</strong>
            <small>{releaseState?.contentHash ? `内容 ${releaseState.contentHash.slice(0, 12)}… · 状态 V${releaseState.version}` : '执行层、Registry、检索和 NebulaGraph 必须同版本就绪'}</small>
            <button className="quiet-button" type="button" disabled={loading} onClick={reload}>
              {loading ? '正在核对…' : '重新核对'}
            </button>
          </div>
        </header>

        {error && <div className="asset-overview-error" role="alert"><span>{error}</span><button type="button" onClick={reload}>重试</button></div>}

        <section className="asset-overview-stats" aria-label="资产治理统计">
          <article><FunctionIcon size={20} weight="duotone" /><span>认证指标</span><strong>{formatCount(counts?.metrics.ready ?? 0)}</strong><small>{percent(counts?.metrics.ready ?? 0, counts?.metrics.total ?? 0)}% 可执行覆盖</small></article>
          <article><TableIcon size={20} weight="duotone" /><span>发布维度</span><strong>{formatCount(counts?.dimensions.total ?? 0)}</strong><small>{formatCount(counts?.dimensions.ready ?? 0)} 个成员策略就绪</small></article>
          <article><BookOpenTextIcon size={20} weight="duotone" /><span>生效业务词汇</span><strong>{formatCount(counts?.terms.total ?? 0)}</strong><small>{percent(counts?.terms.ready ?? 0, counts?.terms.total ?? 0)}% 检索投影就绪</small></article>
          <article><PathIcon size={20} weight="duotone" /><span>维值决策</span><strong>{formatCount(counts?.decisionEntries ?? 0)}</strong><small>{formatCount(counts?.decisionGraph.total ?? 0)} 个维度关系组</small></article>
        </section>

        <section className="asset-overview-grid">
          <article className="asset-overview-readiness">
            <header><div><span className="eyebrow">发布门禁</span><h3>问数就绪检查</h3></div><span className={readiness?.questionEnabled ? 'ready' : 'attention'}>{passedChecks}/{checks.length} 通过</span></header>
            <ol>
              {checks.map(item => <li className={item.status === 'PASS' ? 'ready' : 'attention'} key={item.code}>
                {item.status === 'PASS' ? <CheckCircleIcon size={18} weight="fill" /> : <WarningCircleIcon size={18} weight="fill" />}
                <span><strong>{item.label}</strong><small>{item.detail}</small></span>
              </li>)}
            </ol>
            <p><ShieldCheckIcon size={16} />这里展示的是资产和投影就绪度，不等同于端到端正确率；正式发布仍需黄金问题集和执行结果回归。</p>
          </article>

          <article className="asset-overview-flow">
            <header><span className="eyebrow">问数资产链</span><h3>权威合同如何进入运行时</h3></header>
            <ol>
              <li><span>01</span><div><strong>指标与维度合同</strong><small>固定公式、粒度、允许维度、数据集与发布版本</small></div></li>
              <li><span>02</span><div><strong>词汇和维值投影</strong><small>处理同义表达、成员归属、敏感值与高基数策略</small></div></li>
              <li><span>03</span><div><strong>解析与关系门禁</strong><small>只保留确定性规则和经过验证的指标—维度路径</small></div></li>
              <li><span>04</span><div><strong>智能问答执行</strong><small>依据语义图创建 QueryPlan，执行前重新校验版本和权限</small></div></li>
            </ol>
          </article>
        </section>

        <section className="asset-overview-readiness" aria-label="统一语义资产目录">
          <header><div><span className="eyebrow">Unified catalog</span><h3>统一治理对象</h3></div><span className={readiness?.questionEnabled ? 'ready' : 'attention'}>{formatCount(data?.total ?? 0)} 个对象</span></header>
          <ol>
            {(data?.items ?? []).slice(0, 8).map(item => <li className={item.executionEligible ? 'ready' : 'attention'} key={`${item.objectType}-${item.id}`}>
              {item.executionEligible ? <CheckCircleIcon size={18} weight="fill" /> : <WarningCircleIcon size={18} weight="fill" />}
              <span><strong>{item.name}</strong><small>{item.objectType} · {item.certification} · V{item.version} · {item.readinessCode}</small></span>
            </li>)}
          </ol>
          <p><ShieldCheckIcon size={16} />目录对象只在认证状态、版本和投影门禁同时通过时向 Question Orchestrator 开放。</p>
        </section>

        <section className="asset-overview-modules" aria-label="资产治理模块">
          <Link to="/assets/metrics"><FunctionIcon size={22} /><span><strong>指标与维度</strong><small>{counts?.metrics.total ?? 0} 个指标 · {counts?.dimensions.total ?? 0} 个发布维度</small></span><ArrowRightIcon /></Link>
          <Link to="/assets/semantics"><BookOpenTextIcon size={22} /><span><strong>业务词汇</strong><small>{counts?.terms.total ?? 0} 条生效映射 · {counts?.terms.ready ?? 0} 条已投影</small></span><ArrowRightIcon /></Link>
          <Link to="/assets/dimension-values"><GraphIcon size={22} /><span><strong>维值决策图</strong><small>{counts?.decisionEntries ?? 0} 条安全 WHERE 决策</small></span><ArrowRightIcon /></Link>
          <Link to="/assets/parsing-rules"><SlidersHorizontalIcon size={22} /><span><strong>解析规则</strong><small>{counts?.parsingRules.ready ?? 0} / {counts?.parsingRules.total ?? 0} 条规则生效</small></span><ArrowRightIcon /></Link>
        </section>
      </section>
    </AppShell>
  )
}
