import { useEffect, useState, type ReactNode } from "react";
import {
  metricAPI,
  type MetricRecord,
  type MetricSummary,
} from "../lib/metrics";
import type {
  GlobalFilterDefinition,
  MetricRole,
  ReportCardDefinition,
  ReportDefinition,
} from "./types";

type PropertyPanelProps = {
  definition: ReportDefinition;
  selectedCardId?: string;
  onReportChange: (report: ReportDefinition["report"]) => void;
  onCardChange: (card: ReportCardDefinition) => void;
  onFiltersChange: (filters: GlobalFilterDefinition[]) => void;
  onDeleteCard: (cardId: string) => void;
};

export function PropertyPanel(props: PropertyPanelProps) {
  const card = props.definition.cards.find(
    (item) => item.id === props.selectedCardId,
  );
  if (!card) return <ReportProperties {...props} />;
  return (
    <CardProperties
      card={card}
      definition={props.definition}
      onChange={props.onCardChange}
      onDelete={() => props.onDeleteCard(card.id)}
    />
  );
}

function ReportProperties({
  definition,
  onReportChange,
  onFiltersChange,
}: PropertyPanelProps) {
  function addFilter() {
    onFiltersChange([
      ...definition.globalFilters,
      {
        id: `filter-${crypto.randomUUID().slice(0, 8)}`,
        label: "新筛选",
        type: "SELECT",
        source: { semanticModelId: "", dimensionId: "" },
        operator: "equals",
        required: false,
        options: [],
      },
    ]);
  }
  function updateFilter(index: number, filter: GlobalFilterDefinition) {
    onFiltersChange(
      definition.globalFilters.map((item, itemIndex) =>
        itemIndex === index ? filter : item,
      ),
    );
  }
  return (
    <div className="rpt-property-panel">
      <PanelTitle title="报表属性" subtitle="定义和运行时共享的全局配置" />
      <Field label="标题">
        <input
          value={definition.report.title}
          onChange={(event) =>
            onReportChange({
              ...definition.report,
              title: event.target.value,
              name: event.target.value,
            })
          }
        />
      </Field>
      <Field label="说明">
        <textarea
          value={definition.report.description || ""}
          onChange={(event) =>
            onReportChange({
              ...definition.report,
              description: event.target.value,
            })
          }
        />
      </Field>
      <Field label="主题">
        <select
          value={definition.report.themeId}
          onChange={(event) =>
            onReportChange({
              ...definition.report,
              themeId: event.target.value,
            })
          }
        >
          <option value="business-light">商务浅色</option>
          <option value="executive-dark">高管深色</option>
        </select>
      </Field>
      <div className="rpt-property-section">
        <div className="rpt-property-section-title">
          <strong>全局筛选</strong>
          <button type="button" onClick={addFilter}>
            新增
          </button>
        </div>
        {definition.globalFilters.map((filter, index) => {
          const used = definition.cards.some((card) =>
            card.binding.globalFilterBindings.some(
              (binding) => binding.filterId === filter.id,
            ),
          );
          return (
            <div className="rpt-filter-editor" key={filter.id}>
              <input
                aria-label="筛选名称"
                value={filter.label}
                onChange={(event) =>
                  updateFilter(index, { ...filter, label: event.target.value })
                }
              />
              <select
                aria-label="筛选类型"
                value={filter.type}
                onChange={(event) =>
                  updateFilter(index, {
                    ...filter,
                    type: event.target.value as GlobalFilterDefinition["type"],
                    operator:
                      event.target.value === "DATE_RANGE"
                        ? "between"
                        : event.target.value === "MULTI_SELECT"
                          ? "in"
                          : "equals",
                  })
                }
              >
                <option value="SELECT">单选</option>
                <option value="MULTI_SELECT">多选</option>
                <option value="DATE">日期</option>
                <option value="DATE_RANGE">日期范围</option>
                <option value="TEXT">文本</option>
              </select>
              <input
                aria-label="语义模型"
                placeholder="语义模型 ID"
                value={filter.source.semanticModelId}
                onChange={(event) =>
                  updateFilter(index, {
                    ...filter,
                    source: {
                      ...filter.source,
                      semanticModelId: event.target.value,
                    },
                  })
                }
              />
              <input
                aria-label="维度"
                placeholder="维度 ID"
                value={filter.source.dimensionId}
                onChange={(event) =>
                  updateFilter(index, {
                    ...filter,
                    source: {
                      ...filter.source,
                      dimensionId: event.target.value,
                    },
                  })
                }
              />
              <label className="rpt-check-row">
                <input
                  type="checkbox"
                  checked={filter.required}
                  onChange={(event) =>
                    updateFilter(index, {
                      ...filter,
                      required: event.target.checked,
                    })
                  }
                />
                必填
              </label>
              <button
                type="button"
                className="danger-link"
                disabled={used}
                title={used ? "先解除卡片筛选映射" : undefined}
                onClick={() =>
                  onFiltersChange(
                    definition.globalFilters.filter(
                      (_, itemIndex) => itemIndex !== index,
                    ),
                  )
                }
              >
                删除
              </button>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function CardProperties({
  card,
  definition,
  onChange,
  onDelete,
}: {
  card: ReportCardDefinition;
  definition: ReportDefinition;
  onChange: (card: ReportCardDefinition) => void;
  onDelete: () => void;
}) {
  const [metrics, setMetrics] = useState<MetricSummary[]>([]);
  const [metricRecord, setMetricRecord] = useState<MetricRecord>();
  const primaryMetricID = card.binding.metrics[0]?.id;
  useEffect(() => {
    let active = true;
    void metricAPI
      .list(200, 0)
      .then((page) => {
        if (active)
          setMetrics(page.items.filter((item) => item.status === "PUBLISHED"));
      })
      .catch(() => {
        if (active) setMetrics([]);
      });
    return () => {
      active = false;
    };
  }, []);
  useEffect(() => {
    if (!primaryMetricID) return;
    let active = true;
    void metricAPI
      .get(primaryMetricID)
      .then((record) => {
        if (active) setMetricRecord(record);
      })
      .catch(() => {
        if (active) setMetricRecord(undefined);
      });
    return () => {
      active = false;
    };
  }, [primaryMetricID]);

  function chooseMetric(index: number, id: string) {
    const selected = metrics.find((item) => item.id === id);
    const bindings = [...card.binding.metrics];
    if (!selected) bindings.splice(index, 1);
    else
      bindings[index] = {
        id: selected.id,
        versionId: selected.currentPublishedVersionId,
        role: metricRole(index, card.type),
      };
    const compact = bindings.filter(Boolean);
    setMetricRecord(undefined);
    onChange({
      ...card,
      binding: {
        ...card.binding,
        semanticModelId:
          metrics.find((item) => item.id === compact[0]?.id)?.datasetId || "",
        metrics: compact,
        dimensions: index === 0 ? [] : card.binding.dimensions,
        globalFilterBindings:
          index === 0 ? [] : card.binding.globalFilterBindings,
        sort: normalizeSort(card, compact),
      },
    });
  }
  function chooseDimension(id: string) {
    const dimensions = id ? [{ id, role: "category" as const }] : [];
    onChange({
      ...card,
      binding: {
        ...card.binding,
        dimensions,
        sort: normalizeSort(
          { ...card, binding: { ...card.binding, dimensions } },
          card.binding.metrics,
        ),
      },
    });
  }
  function setInteraction(type: string) {
    if (!type) {
      onChange({ ...card, interactions: [] });
      return;
    }
    const actionType =
      type as ReportCardDefinition["interactions"][number]["action"]["type"];
    const targetCard = definition.cards.find((item) => item.id !== card.id);
    const drillDimension =
      dimensionOptions.find(
        (item) => item.fieldId !== card.binding.dimensions[0]?.id,
      )?.fieldId || "";
    onChange({
      ...card,
      interactions: [
        {
          id: `interaction-${card.id}`,
          event: card.type === "TABLE" ? "table.row.click" : "data.click",
          action: {
            type: actionType,
            ...(actionType === "drillDown"
              ? { pathId: `${card.id}-drill`, toDimension: drillDimension }
              : actionType === "crossFilter"
                ? {
                    targetCardId: targetCard?.id || "",
                    toDimension:
                      targetCard?.binding.dimensions[0]?.id ||
                      card.binding.dimensions[0]?.id ||
                      "",
                  }
                : actionType === "navigate" || actionType === "openModal"
                  ? { targetReportId: "" }
                  : { url: "/" }),
          },
        },
      ],
    });
  }

  const dimensionOptions = metricRecord?.definition.allowedDimensions ?? [];
  const maxMetrics =
    card.type === "COMPARISON"
      ? 2
      : card.type === "CHART" || card.type === "TABLE"
        ? 4
        : 1;
  const interaction = card.interactions[0];
  return (
    <div className="rpt-property-panel">
      <PanelTitle
        title={card.appearance.title}
        subtitle={`${card.type} · Card SDK ${card.cardVersion}`}
      />
      <Field label="卡片标题">
        <input
          value={card.appearance.title}
          onChange={(event) =>
            onChange({
              ...card,
              appearance: { ...card.appearance, title: event.target.value },
            })
          }
        />
      </Field>
      {card.type === "TITLE" ? (
        <>
          <Field label="主标题">
            <input
              value={stringConfig(card, "text")}
              onChange={(event) =>
                onChange({
                  ...card,
                  config: { ...card.config, text: event.target.value },
                })
              }
            />
          </Field>
          <Field label="副标题">
            <input
              value={stringConfig(card, "subtitle")}
              onChange={(event) =>
                onChange({
                  ...card,
                  config: { ...card.config, subtitle: event.target.value },
                })
              }
            />
          </Field>
        </>
      ) : (
        <>
          <div className="rpt-property-section">
            <strong>指标绑定</strong>
            {Array.from(
              {
                length: Math.min(
                  maxMetrics,
                  Math.max(1, card.binding.metrics.length + 1),
                ),
              },
              (_, index) => (
                <Field
                  label={index === 0 ? "主指标" : `附加指标 ${index}`}
                  key={index}
                >
                  <select
                    value={card.binding.metrics[index]?.id || ""}
                    onChange={(event) =>
                      chooseMetric(index, event.target.value)
                    }
                  >
                    <option value="">请选择已发布指标</option>
                    {metrics
                      .filter(
                        (metric) =>
                          !card.binding.semanticModelId ||
                          metric.datasetId === card.binding.semanticModelId ||
                          metric.id === card.binding.metrics[index]?.id,
                      )
                      .map((metric) => (
                        <option key={metric.id} value={metric.id}>
                          {metric.name} · {metric.code}
                        </option>
                      ))}
                  </select>
                </Field>
              ),
            )}
            <Field label="分析维度">
              <select
                value={card.binding.dimensions[0]?.id || ""}
                disabled={!primaryMetricID}
                onChange={(event) => chooseDimension(event.target.value)}
              >
                <option value="">无维度（汇总）</option>
                {dimensionOptions.map((dimension) => (
                  <option key={dimension.fieldId} value={dimension.fieldId}>
                    {dimension.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="返回行数">
              <input
                type="number"
                min={1}
                max={1000}
                value={card.binding.limit || 100}
                onChange={(event) =>
                  onChange({
                    ...card,
                    binding: {
                      ...card.binding,
                      limit: Number(event.target.value),
                    },
                  })
                }
              />
            </Field>
          </div>
          {card.type === "CHART" && (
            <Field label="图形类型">
              <select
                value={stringConfig(card, "chartType") || "bar"}
                onChange={(event) =>
                  onChange({
                    ...card,
                    config: { ...card.config, chartType: event.target.value },
                  })
                }
              >
                <option value="bar">柱状图</option>
                <option value="line">折线图</option>
                <option value="pie">环形图</option>
              </select>
            </Field>
          )}
          {card.type === "TABLE" && <><Field label="每页行数"><select value={String(card.config?.pageSize || 10)} onChange={(event) => onChange({ ...card, config: { ...card.config, pageSize: Number(event.target.value) } })}><option value="10">10 行</option><option value="20">20 行</option><option value="50">50 行</option><option value="100">100 行</option></select></Field><label className="rpt-check-row"><input type="checkbox" checked={card.config?.showSummary !== false} onChange={(event) => onChange({ ...card, config: { ...card.config, showSummary: event.target.checked } })} />显示汇总行</label></>}
          <div className="rpt-property-section">
            <strong>筛选映射</strong>
            {definition.globalFilters.map((filter) => {
              const enabled = card.binding.globalFilterBindings.some(
                (binding) => binding.filterId === filter.id,
              );
              const targetDimensionId = dimensionOptions.some(
                (dimension) => dimension.fieldId === filter.source.dimensionId,
              )
                ? filter.source.dimensionId
                : "";
              return (
                <label className="rpt-check-row" key={filter.id}>
                  <input
                    type="checkbox"
                    checked={enabled}
                    disabled={!primaryMetricID || !targetDimensionId}
                    onChange={(event) =>
                      onChange({
                        ...card,
                        binding: {
                          ...card.binding,
                          globalFilterBindings: event.target.checked
                            ? [
                                ...card.binding.globalFilterBindings,
                                { filterId: filter.id, targetDimensionId },
                              ]
                            : card.binding.globalFilterBindings.filter(
                                (binding) => binding.filterId !== filter.id,
                              ),
                        },
                      })
                    }
                  />
                  {filter.label}
                  {primaryMetricID && !targetDimensionId
                    ? "（指标不支持该维度）"
                    : ""}
                </label>
              );
            })}
            {!definition.globalFilters.length && (
              <small>先在报表属性中创建全局筛选。</small>
            )}
          </div>
          <div className="rpt-property-section">
            <strong>交互</strong>
            <Field label="点击动作">
              <select
                value={interaction?.action.type || ""}
                onChange={(event) => setInteraction(event.target.value)}
              >
                <option value="">无</option>
                <option value="drillDown">下钻</option>
                <option value="crossFilter">跨卡筛选</option>
                <option value="navigate">报表跳转</option>
                <option value="openModal">弹窗报表</option>
                <option value="openUrl">站内链接</option>
              </select>
            </Field>
            {interaction?.action.type === "drillDown" && (
              <Field label="下钻到维度">
                <select
                  value={interaction.action.toDimension || ""}
                  onChange={(event) =>
                    onChange({
                      ...card,
                      interactions: [
                        {
                          ...interaction,
                          action: {
                            ...interaction.action,
                            toDimension: event.target.value,
                          },
                        },
                      ],
                    })
                  }
                >
                  <option value="">请选择下钻维度</option>
                  {dimensionOptions
                    .filter(
                      (item) => item.fieldId !== card.binding.dimensions[0]?.id,
                    )
                    .map((item) => (
                      <option key={item.fieldId} value={item.fieldId}>
                        {item.name}
                      </option>
                    ))}
                </select>
              </Field>
            )}
            {interaction?.action.type === "crossFilter" && (
              <>
                <Field label="目标卡片">
                  <select
                    value={interaction.action.targetCardId || ""}
                    onChange={(event) => {
                      const target = definition.cards.find(
                        (item) => item.id === event.target.value,
                      );
                      onChange({
                        ...card,
                        interactions: [
                          {
                            ...interaction,
                            action: {
                              ...interaction.action,
                              targetCardId: event.target.value,
                              toDimension:
                                target?.binding.dimensions[0]?.id || "",
                            },
                          },
                        ],
                      });
                    }}
                  >
                    {definition.cards
                      .filter((item) => item.id !== card.id)
                      .map((item) => (
                        <option key={item.id} value={item.id}>
                          {item.appearance.title}
                        </option>
                      ))}
                  </select>
                </Field>
                <Field label="目标筛选维度">
                  <input
                    placeholder="目标指标支持的维度 ID"
                    value={interaction.action.toDimension || ""}
                    onChange={(event) =>
                      onChange({
                        ...card,
                        interactions: [
                          {
                            ...interaction,
                            action: {
                              ...interaction.action,
                              toDimension: event.target.value,
                            },
                          },
                        ],
                      })
                    }
                  />
                </Field>
              </>
            )}
            {(interaction?.action.type === "navigate" ||
              interaction?.action.type === "openModal") && (
              <Field label="目标报表 ID">
                <input
                  value={interaction.action.targetReportId || ""}
                  onChange={(event) =>
                    onChange({
                      ...card,
                      interactions: [
                        {
                          ...interaction,
                          action: {
                            ...interaction.action,
                            targetReportId: event.target.value,
                          },
                        },
                      ],
                    })
                  }
                />
              </Field>
            )}
            {interaction?.action.type === "openUrl" && (
              <Field label="站内路径">
                <input
                  value={interaction.action.url || "/"}
                  onChange={(event) =>
                    onChange({
                      ...card,
                      interactions: [
                        {
                          ...interaction,
                          action: {
                            ...interaction.action,
                            url: event.target.value,
                          },
                        },
                      ],
                    })
                  }
                />
              </Field>
            )}
          </div>
        </>
      )}
      <button
        type="button"
        className="danger-button rpt-delete-card"
        disabled={definition.cards.length <= 1}
        onClick={onDelete}
      >
        删除卡片
      </button>
    </div>
  );
}

function normalizeSort(
  card: ReportCardDefinition,
  metrics: ReportCardDefinition["binding"]["metrics"],
) {
  if (card.type !== "RANKING") return card.binding.sort;
  return metrics[0]
    ? [{ field: metrics[0].id, direction: "desc" as const }]
    : [];
}
function metricRole(
  index: number,
  type: ReportCardDefinition["type"],
): MetricRole {
  return index === 0 ? "value" : type === "COMPARISON" ? "baseline" : "series";
}
function PanelTitle({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <header className="rpt-property-title">
      <h2>{title}</h2>
      <p>{subtitle}</p>
    </header>
  );
}
function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label className="rpt-property-field">
      <span>{label}</span>
      {children}
    </label>
  );
}
function stringConfig(card: ReportCardDefinition, key: string) {
  const value = card.config?.[key];
  return typeof value === "string" ? value : "";
}
