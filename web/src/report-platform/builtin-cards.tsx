/* eslint-disable react-refresh/only-export-components */
import { useEffect, useMemo, useRef, useState } from "react";
import * as echarts from "echarts/core";
import { BarChart, LineChart, PieChart } from "echarts/charts";
import {
  GridComponent,
  LegendComponent,
  TooltipComponent,
} from "echarts/components";
import { CanvasRenderer } from "echarts/renderers";
import type { EChartsCoreOption, EChartsType } from "echarts/core";
import type { CardPlugin, CardRenderProps, SemanticQuery } from "./card-sdk";
import type {
  CardQueryResult,
  ReportCardDefinition,
  ReportValidationIssue,
} from "./types";

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  CanvasRenderer,
]);

export const builtinCardPlugins: CardPlugin[] = [
  plugin("TITLE", "标题卡", "报告标题、说明和筛选变量", TitleCard),
  plugin("CONCLUSION", "结论卡", "由可信查询结果生成业务结论", ConclusionCard, {
    metrics: [1, 1],
  }),
  plugin("CHART", "图形卡", "ECharts 柱状、折线和饼图", ChartCard, {
    metrics: [1, 16],
  }),
  plugin("COMPARISON", "对比卡", "当前值、基线、差值和变化率", ComparisonCard, {
    metrics: [1, 2],
  }),
  plugin("RANKING", "排序卡", "按指标排序的 TopN", RankingCard, {
    metrics: [1, 16],
    dimensions: [1, 16],
    requireSort: true,
  }),
  plugin("TABLE", "表格卡", "维度、指标、分页和汇总", TableCard, {
    fields: [1, 32],
  }),
];

function plugin(
  type: CardPlugin["type"],
  label: string,
  description: string,
  Renderer: CardPlugin["Renderer"],
  constraints: BindingConstraints = {},
): CardPlugin {
  return {
    type,
    label,
    description,
    version: "1.0.0",
    configSchema: { type: "object", additionalProperties: true },
    bindingSchema: {
      type: "object",
      required: ["metrics", "dimensions", "globalFilterBindings", "filters", "sort"],
    },
    Renderer,
    validateBinding: (card) => validateBinding(card, constraints),
    buildQuery: (card) =>
      card.type === "TITLE" ? undefined : buildQuery(card),
    migrate: (config) => (isRecord(config) ? structuredClone(config) : {}),
  };
}

type BindingConstraints = {
  metrics?: [number, number];
  dimensions?: [number, number];
  fields?: [number, number];
  requireSort?: boolean;
};

function validateBinding(
  card: ReportCardDefinition,
  constraints: BindingConstraints,
): ReportValidationIssue[] {
  const issues: ReportValidationIssue[] = [];
  const prefix = `cards.${card.id}.binding`;
  if (card.type !== "TITLE" && !card.binding.semanticModelId)
    issues.push({
      path: `${prefix}.semanticModelId`,
      reason: "请选择语义模型",
    });
  if (
    constraints.metrics &&
    (card.binding.metrics.length < constraints.metrics[0] ||
      card.binding.metrics.length > constraints.metrics[1])
  )
    issues.push({
      path: `${prefix}.metrics`,
      reason: `指标数量应为 ${constraints.metrics[0]}～${constraints.metrics[1]}`,
    });
  if (
    constraints.dimensions &&
    (card.binding.dimensions.length < constraints.dimensions[0] ||
      card.binding.dimensions.length > constraints.dimensions[1])
  )
    issues.push({
      path: `${prefix}.dimensions`,
      reason: `维度数量应为 ${constraints.dimensions[0]}～${constraints.dimensions[1]}`,
    });
  const fields = card.binding.metrics.length + card.binding.dimensions.length;
  if (
    constraints.fields &&
    (fields < constraints.fields[0] || fields > constraints.fields[1])
  )
    issues.push({
      path: prefix,
      reason: `字段数量应为 ${constraints.fields[0]}～${constraints.fields[1]}`,
    });
  if (
    constraints.requireSort &&
    (!card.binding.sort.length || !card.binding.limit)
  )
    issues.push({ path: prefix, reason: "必须配置排序和 TopN" });
  return issues;
}

function buildQuery(card: ReportCardDefinition): SemanticQuery {
  return {
    metricIds: card.binding.metrics.map((item) => item.id),
    dimensionIds: card.binding.dimensions.map((item) => item.id),
    filters: card.binding.filters,
    sort: card.binding.sort,
    limit: card.binding.limit || 100,
  };
}

function TitleCard({ card, filters }: CardRenderProps) {
  return (
    <div className="rpt-title-card">
      <h2>{renderFilterVariables(stringConfig(card, "text") || card.appearance.title, filters)}</h2>
      <p>{renderFilterVariables(stringConfig(card, "subtitle") || card.appearance.subtitle || "", filters)}</p>
    </div>
  );
}

function ConclusionCard({ card, result }: CardRenderProps) {
  const value = firstMetricValue(result);
  const template = stringConfig(card, "template") || "本期指标为 {value}。";
  return (
    <div className="rpt-conclusion-card">
      <span>AI 结论模板</span>
      <p>
        {value === undefined
          ? emptyText(result)
          : template
              .replace("{value}", formatValue(value))
              .replace("{metric}", card.appearance.title)
              .replace("{change}", "")}
      </p>
    </div>
  );
}

function ComparisonCard({ result }: CardRenderProps) {
  const values = metricValues(result);
  const current = values[0];
  const baseline = values[1];
  const change =
    typeof current === "number" &&
    typeof baseline === "number" &&
    baseline !== 0
      ? (current - baseline) / Math.abs(baseline)
      : undefined;
  return (
    <div className="rpt-comparison-card">
      <strong>{current === undefined ? "—" : formatValue(current)}</strong>
      <span>
        {baseline === undefined
          ? emptyText(result)
          : `基线 ${formatValue(baseline)}`}
      </span>
      {change !== undefined && (
        <em className={change >= 0 ? "positive" : "negative"}>
          {change >= 0 ? "+" : ""}
          {(change * 100).toFixed(1)}%
        </em>
      )}
    </div>
  );
}

function ChartCard({ card, result, onInteraction }: CardRenderProps) {
  const option = useMemo(() => chartOption(card, result), [card, result]);
  return (
    <EChart
      option={option}
      onClick={(params) =>
        onInteraction?.({
          cardId: card.id,
          event: "data.click",
          value: params.name ?? params.value,
          datum: isRecord(params.data) ? params.data : undefined,
        })
      }
    />
  );
}

function RankingCard({ card, result, onInteraction }: CardRenderProps) {
  const dimensionIndex = firstColumnIndex(result, "DIMENSION");
  const metricIndex = firstColumnIndex(result, "METRIC");
  if (!result || result.status === "ERROR" || !result.rows.length)
    return <CardEmpty result={result} />;
  return (
    <ol className="rpt-ranking-card">
      {result.rows.slice(0, card.binding.limit || 10).map((row, index) => (
        <li
          key={`${String(row[dimensionIndex])}-${index}`}
          onClick={() =>
            onInteraction?.({
              cardId: card.id,
              event: "data.click",
              value: row[dimensionIndex],
            })
          }
        >
          <span>{index + 1}</span>
          <b>{String(row[dimensionIndex] ?? "—")}</b>
          <strong>{formatValue(row[metricIndex])}</strong>
        </li>
      ))}
    </ol>
  );
}

function TableCard({ card, result, onInteraction }: CardRenderProps) {
  const pageSize = Math.max(5, Math.min(100, numberConfig(card, "pageSize") || 10));
  const [page, setPage] = useState(0);
  if (!result || result.status === "ERROR" || !result.rows.length) return <CardEmpty result={result} />;
  const dimensionIndex = firstColumnIndex(result, "DIMENSION");
  const pageCount = Math.max(1, Math.ceil(result.rows.length / pageSize));
  const safePage = Math.min(page, pageCount - 1);
  const rows = result.rows.slice(safePage * pageSize, (safePage + 1) * pageSize);
  const showSummary = card.config?.showSummary !== false;
  return (
    <div className="rpt-table-card">
      <div className="rpt-table-scroll"><table className="rpt-data-table">
        <thead>
          <tr>
            {result.columns.map((column) => (
              <th key={column.code}>{column.name || column.code}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, rowIndex) => (
              <tr
                key={rowIndex}
                onClick={() =>
                  onInteraction?.({
                    cardId: card.id,
                    event: "table.row.click",
                    value: row[dimensionIndex],
                    datum: Object.fromEntries(
                      result.columns.map((column, index) => [
                        column.fieldId || column.code,
                        row[index],
                      ]),
                    ),
                  })
                }
              >
                {row.map((value, index) => (
                  <td key={`${rowIndex}-${index}`}>{formatValue(value)}</td>
                ))}
              </tr>
            ))}
        </tbody>
        {showSummary && <tfoot><tr>{result.columns.map((column, index) => <td key={column.code}>{index === 0 ? "合计" : column.role === "METRIC" ? formatValue(result.rows.reduce((sum, row) => sum + numberValue(row[index]), 0)) : ""}</td>)}</tr></tfoot>}
      </table></div>
      {pageCount > 1 && <nav className="rpt-table-pagination" aria-label="表格分页"><button type="button" disabled={safePage === 0} onClick={() => setPage(value => Math.max(0, value - 1))}>上一页</button><span>{safePage + 1} / {pageCount}</span><button type="button" disabled={safePage + 1 >= pageCount} onClick={() => setPage(value => Math.min(pageCount - 1, value + 1))}>下一页</button></nav>}
    </div>
  );
}

function EChart({
  option,
  onClick,
}: {
  option: EChartsCoreOption;
  onClick?: (params: {
    name?: string;
    value?: unknown;
    data?: unknown;
  }) => void;
}) {
  const elementRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<EChartsType | undefined>(undefined);
  useEffect(() => {
    if (!elementRef.current) return;
    const chart = echarts.init(elementRef.current, undefined, {
      renderer: "canvas",
    });
    chartRef.current = chart;
    if (onClick) chart.on("click", onClick);
    const observer = new ResizeObserver(() => chart.resize());
    observer.observe(elementRef.current);
    return () => {
      observer.disconnect();
      chart.dispose();
      chartRef.current = undefined;
    };
  }, [onClick]);
  useEffect(() => {
    chartRef.current?.setOption(option, { notMerge: true });
  }, [option]);
  return (
    <div
      className="rpt-echart"
      ref={elementRef}
      role="img"
      aria-label="数据图表"
    />
  );
}

function chartOption(
  card: ReportCardDefinition,
  result?: CardQueryResult,
): EChartsCoreOption {
  if (!result || result.status === "ERROR" || !result.rows.length)
    return {
      graphic: {
        type: "text",
        left: "center",
        top: "middle",
        style: { text: emptyText(result), fill: "#64748b", fontSize: 13 },
      },
    };
  const dimensionIndex = firstColumnIndex(result, "DIMENSION");
  const metricIndexes = result.columns
    .map((column, index) => (column.role === "METRIC" ? index : -1))
    .filter((index) => index >= 0);
  const chartType = stringConfig(card, "chartType") || "bar";
  const categories = result.rows.map((row) =>
    String(row[dimensionIndex] ?? "总计"),
  );
  if (chartType === "pie") {
    const metricIndex =
      metricIndexes[0] ?? Math.min(1, result.columns.length - 1);
    return {
      tooltip: { trigger: "item" },
      legend: { bottom: 0 },
      series: [
        {
          type: "pie",
          radius: ["42%", "70%"],
          data: result.rows.map((row, index) => ({
            name: categories[index],
            value: numberValue(row[metricIndex]),
          })),
        },
      ],
    };
  }
  return {
    tooltip: { trigger: "axis" },
    legend: { bottom: 0 },
    grid: { left: 42, right: 18, top: 20, bottom: 48, containLabel: true },
    xAxis: {
      type: "category",
      data: categories,
      axisLabel: { color: "#64748b" },
    },
    yAxis: {
      type: "value",
      axisLabel: { color: "#64748b" },
      splitLine: { lineStyle: { color: "#e8edf4" } },
    },
    series: (metricIndexes.length
      ? metricIndexes
      : [Math.min(1, result.columns.length - 1)]
    ).map((index) => ({
      name: result.columns[index]?.name,
      type: chartType === "line" ? "line" : "bar",
      smooth: chartType === "line",
      data: result.rows.map((row) => numberValue(row[index])),
      itemStyle: {
        color: "#2864dc",
        borderRadius: chartType === "bar" ? [5, 5, 0, 0] : 0,
      },
    })),
  };
}

function CardEmpty({ result }: { result?: CardQueryResult }) {
  return <div className="rpt-card-empty">{emptyText(result)}</div>;
}
function emptyText(result?: CardQueryResult) {
  return result?.status === "ERROR"
    ? result.errorMessage || "查询失败"
    : result
      ? "暂无数据"
      : "尚未执行查询";
}
function stringConfig(card: ReportCardDefinition, key: string) {
  const value = card.config?.[key];
  return typeof value === "string" ? value : "";
}
function numberConfig(card: ReportCardDefinition, key: string) { const value = card.config?.[key]; return typeof value === "number" && Number.isFinite(value) ? value : 0; }
function firstMetricValue(result?: CardQueryResult) {
  return metricValues(result)[0];
}
function metricValues(result?: CardQueryResult): unknown[] {
  if (!result?.rows.length) return [];
  const indexes = result.columns
    .map((column, index) => (column.role === "METRIC" ? index : -1))
    .filter((index) => index >= 0);
  return (indexes.length ? indexes : [result.columns.length - 1]).map(
    (index) => result.rows[0][index],
  );
}
function firstColumnIndex(
  result: CardQueryResult | undefined,
  role: "DIMENSION" | "METRIC",
) {
  const index =
    result?.columns.findIndex((column) => column.role === role) ?? -1;
  return index >= 0
    ? index
    : role === "DIMENSION"
      ? 0
      : Math.max(0, (result?.columns.length ?? 1) - 1);
}
function numberValue(value: unknown) {
  const number = typeof value === "number" ? value : Number(value);
  return Number.isFinite(number) ? number : 0;
}
function formatValue(value: unknown) {
  if (typeof value === "number")
    return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 2 }).format(
      value,
    );
  return value === null || value === undefined ? "—" : String(value);
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}
function renderFilterVariables(template: string, filters: Record<string, unknown>) {
  return template.replace(/\{\{filter:([a-zA-Z0-9_-]+)\}\}/g, (_match, id: string) => formatValue(filters[id]));
}
