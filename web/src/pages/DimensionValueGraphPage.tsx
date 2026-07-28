import { useCallback, useEffect, useMemo, useState } from 'react'
import { AppShell } from '../components/AppShell'
import { AssetManagementTabs } from '../components/AssetManagementTabs'
import { RequestError } from '../lib/api'
import {
  semanticGovernanceAPI,
  type DimensionWhereDecision,
  type DimensionWhereDecisionGroup,
} from '../lib/semantic-governance'

type GraphNotice = { tone: 'error' | 'info'; message: string }

type DimensionDecisionPage = {
  items: DimensionWhereDecision[]
  total: number
  loading: boolean
}

const decisionPageSize = 50

const formatCount = (value: number) =>
  new Intl.NumberFormat('zh-CN').format(value)

const errorMessage = (cause: unknown) => {
  if (cause instanceof RequestError && cause.status === 403) {
    return '当前账号没有维度值读取权限。'
  }
  return cause instanceof Error
    ? cause.message
    : '维度值决策图暂不可用，请稍后重试。'
}

const targetTable = (decision: DimensionWhereDecision) =>
  `${decision.tableSchema}.${decision.tableName}`

const buildStatusLabel: Record<
  DimensionWhereDecisionGroup['buildStatus'],
  string
> = {
  READY: '已完成',
  BUILDING: '向量处理中',
  RUNNING: 'LLM处理中',
  PENDING: '等待处理',
  FAILED: '处理失败',
  EXACT_ONLY: '精确匹配',
  EMPTY: '无非空值',
}

const groupDecisionTotal = (groups: DimensionWhereDecisionGroup[]) =>
  groups.reduce((total, group) => total + group.decisionCount, 0)

const groupMemberTotal = (groups: DimensionWhereDecisionGroup[]) =>
  groups.reduce((total, group) => total + group.memberCount, 0)

const groupPendingVectorTotal = (groups: DimensionWhereDecisionGroup[]) =>
  groups.reduce((total, group) => total + group.pendingVectorCount, 0)

/** 按维度按需读取已持久化的全量 DWS 值决策，避免一次渲染数万行。 */
export function DimensionValueGraphPage() {
  const [permissionsReady, setPermissionsReady] = useState(false)
  const [canRead, setCanRead] = useState(false)
  const [groups, setGroups] = useState<DimensionWhereDecisionGroup[]>([])
  const [decisionPages, setDecisionPages] = useState<
    Record<string, DimensionDecisionPage>
  >({})
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [loading, setLoading] = useState(true)
  const [notice, setNotice] = useState<GraphNotice | null>(null)

  const loadDimension = useCallback(async (
    dimensionId: string,
    offset = 0,
  ) => {
    setDecisionPages(current => ({
      ...current,
      [dimensionId]: {
        items: offset ? current[dimensionId]?.items ?? [] : [],
        total: current[dimensionId]?.total ?? 0,
        loading: true,
      },
    }))
    try {
      const page = await semanticGovernanceAPI.listWhereDecisions(
        '', '', dimensionId, decisionPageSize, offset,
      )
      setDecisionPages(current => ({
        ...current,
        [dimensionId]: {
          items: offset
            ? [...(current[dimensionId]?.items ?? []), ...page.items]
            : page.items,
          total: page.total,
          loading: false,
        },
      }))
    } catch (cause) {
      setDecisionPages(current => ({
        ...current,
        [dimensionId]: {
          items: current[dimensionId]?.items ?? [],
          total: current[dimensionId]?.total ?? 0,
          loading: false,
        },
      }))
      setNotice({ tone: 'error', message: errorMessage(cause) })
    }
  }, [])

  const loadGraph = useCallback(async () => {
    setLoading(true)
    try {
      const response = await semanticGovernanceAPI.listWhereDecisionGroups()
      setGroups(response.items)
      setDecisionPages({})
      const firstReady = response.items.find(group => group.decisionCount > 0)
      if (firstReady) {
        setExpanded({ [firstReady.dimensionId]: true })
        await loadDimension(firstReady.dimensionId)
      } else {
        setExpanded({})
      }
      setNotice(null)
    } catch (cause) {
      setNotice({ tone: 'error', message: errorMessage(cause) })
    } finally {
      setLoading(false)
    }
  }, [loadDimension])

  useEffect(() => {
    let active = true
    semanticGovernanceAPI.evaluatePermission('READ').then(result => {
      if (!active) return
      setCanRead(result.allowed)
      if (!result.allowed) setLoading(false)
    }).catch(cause => {
      if (active) setNotice({ tone: 'error', message: errorMessage(cause) })
    }).finally(() => {
      if (active) setPermissionsReady(true)
    })
    return () => {
      active = false
    }
  }, [])

  useEffect(() => {
    if (!permissionsReady || !canRead) return
    const timer = window.setTimeout(() => { void loadGraph() }, 0)
    return () => window.clearTimeout(timer)
  }, [canRead, loadGraph, permissionsReady])

  const summary = useMemo(() => ({
    dimensions: groups.length,
    members: groupMemberTotal(groups),
    decisions: groupDecisionTotal(groups),
    pendingVectors: groupPendingVectorTotal(groups),
  }), [groups])

  const toggleDimension = (group: DimensionWhereDecisionGroup) => {
    const nextExpanded = !expanded[group.dimensionId]
    setExpanded(current => ({
      ...current,
      [group.dimensionId]: nextExpanded,
    }))
    if (
      nextExpanded &&
      group.decisionCount > 0 &&
      !decisionPages[group.dimensionId]
    ) {
      void loadDimension(group.dimensionId)
    }
  }

  return (
    <AppShell title="资产管理中心" eyebrow="维度值 · 决策图">
      <AssetManagementTabs />
      <main className="dimension-value-page">
        {notice && (
          <div
            className={`dimension-value-notice ${notice.tone}`}
            role={notice.tone === 'error' ? 'alert' : 'status'}
          >
            <span>{notice.message}</span>
            <button
              type="button"
              aria-label="关闭提示"
              onClick={() => setNotice(null)}
            >
              ×
            </button>
          </div>
        )}

        <section className="dimension-value-hero">
          <div>
            <span className="eyebrow">Dimension Value Decision Graph</span>
            <h2>维度值决策图</h2>
            <p>
              自动加载当前已发布 DWS 的全部非空治理维度值，
              逐维度完成 LLM WHERE 策略、描述和值向量化及持久化；
              同义表达合并到规范成员，高基数字段保留精确匹配策略。
            </p>
          </div>
          <button
            type="button"
            className="quiet-button"
            disabled={loading || !canRead}
            onClick={() => void loadGraph()}
          >
            {loading ? '加载中…' : '重新加载'}
          </button>
        </section>

        {!permissionsReady ? (
          <section className="dimension-value-state">
            正在检查维度值读取权限…
          </section>
        ) : !canRead ? (
          <section className="dimension-value-state denied">
            当前账号没有维度值读取权限。
          </section>
        ) : (
          <>
            <section
              className="dimension-value-summary"
              aria-label="维度值决策图概览"
            >
              <article>
                <span>DWS 正式维度</span>
                <strong>{formatCount(summary.dimensions)}</strong>
                <small>包含全量构建与精确匹配维度</small>
              </article>
              <article>
                <span>已加载非空维度值</span>
                <strong>{formatCount(summary.members)}</strong>
                <small>空值及建模占位值不入图</small>
              </article>
              <article>
                <span>已持久化 WHERE 决策</span>
                <strong>{formatCount(summary.decisions)}</strong>
                <small>预计算关系与问答验证证据合并</small>
              </article>
              <article>
                <span>待向量化</span>
                <strong>{formatCount(summary.pendingVectors)}</strong>
                <small>后台按32条批次持续处理</small>
              </article>
            </section>

            <section
              className="where-decision-directory"
              aria-label="按维度展示的维度值与指标 WHERE 决策关系"
            >
              <header>
                <div>
                  <span>Persisted Decision Graph</span>
                  <h3>按维度查看“维度字段：维度值 → 指标字段 → WHERE”</h3>
                </div>
                <small>
                  共 {formatCount(summary.dimensions)} 个维度 ·
                  {' '}{formatCount(summary.members)} 个非空治理值 ·
                  按需展开
                </small>
              </header>

              {groups.length ? groups.map(group => {
                const page = decisionPages[group.dimensionId]
                const isExpanded = expanded[group.dimensionId] ?? false
                const hasMore = Boolean(
                  page && page.items.length < page.total,
                )
                return (
                  <article
                    className="where-decision-dimension"
                    key={group.dimensionId}
                  >
                    <header>
                      <button
                        type="button"
                        className="where-decision-dimension-toggle"
                        aria-expanded={isExpanded}
                        onClick={() => toggleDimension(group)}
                      >
                        <span aria-hidden="true">
                          {isExpanded ? '−' : '+'}
                        </span>
                        <strong>{group.dimensionName}</strong>
                        <code>{group.dimensionFieldName}</code>
                      </button>
                      <p>{group.dimensionDescription}</p>
                      <small>
                        {formatCount(group.memberCount)} 个非空值 ·
                        {' '}{formatCount(group.decisionCount)} 条决策 ·
                        {' '}
                        <b data-status={group.buildStatus}>
                          {buildStatusLabel[group.buildStatus]}
                        </b>
                      </small>
                    </header>

                    {isExpanded && (
                      <>
                        {group.decisionCount > 0 && (
                          <div
                            className="where-decision-grid heading"
                            aria-hidden="true"
                          >
                            <span>向量键（描述:规范值）</span>
                            <span>维度字段：维度值</span>
                            <span>指标字段 / 执行表</span>
                            <span>WHERE 决策</span>
                          </div>
                        )}

                        {page?.items.map(decision => (
                          <div
                            className="where-decision-grid"
                            key={decision.id}
                          >
                            <div>
                              <code>{decision.vectorKey}</code>
                              <small>{decision.embeddingModel}</small>
                            </div>
                            <div>
                              <strong>
                                {decision.dimensionFieldName}：
                                {decision.canonicalValue}
                              </strong>
                              <small>
                                {decision.aliases.length
                                  ? `同义表达：${decision.aliases.join('、')}`
                                  : `${decision.selectedMemberCount} 个治理值`}
                              </small>
                            </div>
                            <div>
                              <strong>{decision.metricFieldId}</strong>
                              <small>
                                {decision.metricName} · {decision.metricCode}
                              </small>
                              <code className="target-table">
                                {targetTable(decision)}
                              </code>
                            </div>
                            <div>
                              <code>{decision.whereCondition}</code>
                              <small>
                                {decision.predicateOperator} ·
                                {' '}{decision.llmModel} ·
                                {' '}
                                {decision.sourceType === 'DWS_PRECOMPUTED'
                                  ? 'DWS全量预计算'
                                  : '问答验证'}
                              </small>
                            </div>
                          </div>
                        ))}

                        {page?.loading && (
                          <div className="dimension-value-state compact">
                            正在加载该维度的决策…
                          </div>
                        )}
                        {!page?.loading && group.decisionCount === 0 && (
                          <div className="dimension-value-state compact">
                            {group.buildStatus === 'EXACT_ONLY'
                              ? '该高基数维度使用精确匹配策略，不进入语义向量批次。'
                              : group.buildStatus === 'EMPTY'
                                ? 'DWS 中没有可记录的非空业务值。'
                                : '该维度正在等待 LLM 策略或向量构建。'}
                          </div>
                        )}
                        {hasMore && (
                          <div className="where-decision-more">
                            <button
                              type="button"
                              className="quiet-button"
                              disabled={page?.loading}
                              onClick={() => void loadDimension(
                                group.dimensionId,
                                page?.items.length ?? 0,
                              )}
                            >
                              加载更多（已显示
                              {formatCount(page?.items.length ?? 0)} /
                              {formatCount(page?.total ?? 0)}）
                            </button>
                          </div>
                        )}
                      </>
                    )}
                  </article>
                )
              }) : (
                <div className="dimension-value-state compact">
                  暂无已发布 DWS 维度资产。
                </div>
              )}
            </section>
          </>
        )}
      </main>
    </AppShell>
  )
}
