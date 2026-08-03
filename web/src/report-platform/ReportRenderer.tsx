/* eslint-disable react-refresh/only-export-components */
import {
  Component,
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ErrorInfo,
  type ReactNode,
} from "react";
import { GridStackCanvas } from "./GridStackCanvas";
import { reportCardRegistry } from "./registry";
import type {
  CardInteractionEvent,
  CardQueryResult,
  GlobalFilterDefinition,
  ReportBreakpoint,
  ReportCardDefinition,
  ReportDefinition,
  ReportGrid,
} from "./types";

export type ReportRendererProps = {
  definition: ReportDefinition;
  mode: "designer" | "runtime";
  breakpoint: ReportBreakpoint;
  results?: Record<string, CardQueryResult>;
  filters?: Record<string, unknown>;
  selectedCardId?: string;
  onFilterChange?: (filterId: string, value: unknown) => void;
  onInteraction?: (event: CardInteractionEvent) => void;
  onSelectCard?: (cardId: string) => void;
  onLayoutChange?: (cardId: string, grid: ReportGrid) => void;
  onAddCard?: Parameters<typeof GridStackCanvas>[0]["onAddCard"];
};

export function ReportRenderer(props: ReportRendererProps) {
  const {
    definition,
    mode,
    breakpoint,
    results = {},
    filters = {},
    selectedCardId,
    onFilterChange,
    onInteraction,
    onSelectCard,
    onLayoutChange,
    onAddCard,
  } = props;
  const themeClass = `rpt-theme-${definition.report.themeId.replace(/[^a-z0-9_-]/gi, "")}`;
  return (
    <section className={`rpt-runtime ${themeClass}`}>
      <ReportFilterBar
        filters={definition.globalFilters}
        values={filters}
        disabled={!onFilterChange}
        onChange={onFilterChange}
      />
      <GridStackCanvas
        definition={definition}
        breakpoint={breakpoint}
        editable={mode === "designer"}
        selectedCardId={selectedCardId}
        onSelect={onSelectCard}
        onLayoutChange={onLayoutChange}
        onAddCard={onAddCard}
        renderCard={(card) => (
          <CardBoundary cardId={card.id}>
            <ReportCard
              card={card}
              definition={definition}
              result={results[card.id]}
              mode={mode}
              filters={filters}
              onInteraction={onInteraction}
            />
          </CardBoundary>
        )}
      />
    </section>
  );
}

function ReportCard({
  card,
  definition,
  result,
  mode,
  filters,
  onInteraction,
}: {
  card: ReportCardDefinition;
  definition: ReportDefinition;
  result?: CardQueryResult;
  mode: "designer" | "runtime";
  filters: Record<string, unknown>;
  onInteraction?: (event: CardInteractionEvent) => void;
}) {
  const plugin = reportCardRegistry.get(card.type);
  const bindingIssues = useMemo(
    () => plugin?.validateBinding(card, definition) ?? [],
    [card, definition, plugin],
  );
  if (!plugin)
    return <div className="rpt-card-unknown">未知卡片类型 {card.type}</div>;
  const Renderer = plugin.Renderer;
  return (
    <article className={`rpt-card rpt-card--${card.type.toLowerCase()}`}>
      <header
        className={
          card.appearance.showHeader === false ? "rpt-card-header--hidden" : ""
        }
      >
        <div>
          <h3>{card.appearance.title}</h3>
          {card.appearance.subtitle && <p>{card.appearance.subtitle}</p>}
        </div>
        {result && (
          <span className={result.cacheHit ? "cache-hit" : "cache-miss"}>
            {result.cacheHit ? "缓存" : `${result.durationMs}ms`}
          </span>
        )}
      </header>
      {mode === "designer" && bindingIssues.length > 0 && (
        <div className="rpt-binding-warning">{bindingIssues[0].reason}</div>
      )}
      <div className="rpt-card-body">
        <Renderer
          card={card}
          definition={definition}
          result={result}
          mode={mode}
          filters={filters}
          onInteraction={onInteraction}
        />
      </div>
    </article>
  );
}

function ReportFilterBar({
  filters,
  values,
  disabled,
  onChange,
}: {
  filters: GlobalFilterDefinition[];
  values: Record<string, unknown>;
  disabled: boolean;
  onChange?: (id: string, value: unknown) => void;
}) {
  if (!filters.length) return null;
  return (
    <div className="rpt-filter-bar">
      {filters.map((filter) => (
        <label key={filter.id}>
          <span>
            {filter.label}
            {filter.required ? " *" : ""}
          </span>
          <FilterControl
            filter={filter}
            value={values[filter.id] ?? filter.defaultValue ?? ""}
            disabled={disabled}
            onChange={(value) => onChange?.(filter.id, value)}
          />
        </label>
      ))}
    </div>
  );
}

function FilterControl({
  filter,
  value,
  disabled,
  onChange,
}: {
  filter: GlobalFilterDefinition;
  value: unknown;
  disabled: boolean;
  onChange: (value: unknown) => void;
}) {
  if (filter.type === "MULTI_SELECT")
    return (
      <select
        multiple
        disabled={disabled}
        value={Array.isArray(value) ? value.map(String) : []}
        onChange={(event) =>
          onChange(
            [...event.currentTarget.selectedOptions].map(
              (option) => option.value,
            ),
          )
        }
      >
        {filter.options?.map((option) => (
          <option key={String(option.value)} value={String(option.value)}>
            {option.label}
          </option>
        ))}
      </select>
    );
  if (filter.type === "SELECT")
    return (
      <select
        disabled={disabled}
        value={typeof value === "string" ? value : ""}
        onChange={(event) => onChange(event.target.value)}
      >
        <option value="">全部</option>
        {filter.options?.map((option) => (
          <option key={String(option.value)} value={String(option.value)}>
            {option.label}
          </option>
        ))}
      </select>
    );
  if (filter.type === "DATE_RANGE") {
    const range = Array.isArray(value) ? value : [];
    return (
      <span className="rpt-date-range">
        <input
          disabled={disabled}
          type="date"
          value={typeof range[0] === "string" ? range[0] : ""}
          onChange={(event) => onChange([event.target.value, range[1] ?? ""])}
        />
        <i>至</i>
        <input
          disabled={disabled}
          type="date"
          value={typeof range[1] === "string" ? range[1] : ""}
          onChange={(event) => onChange([range[0] ?? "", event.target.value])}
        />
      </span>
    );
  }
  if (filter.type === "DATE")
    return (
      <input
        disabled={disabled}
        type="date"
        value={typeof value === "string" ? value : ""}
        onChange={(event) => onChange(event.target.value)}
      />
    );
  return (
    <input
      disabled={disabled}
      value={
        typeof value === "string" || typeof value === "number"
          ? String(value)
          : ""
      }
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

class CardBoundary extends Component<
  { cardId: string; children: ReactNode },
  { error?: Error }
> {
  state: { error?: Error } = {};
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(
      "report card render failed",
      this.props.cardId,
      error,
      info.componentStack,
    );
  }
  render() {
    return this.state.error ? (
      <div className="rpt-card-error">
        <strong>卡片渲染失败</strong>
        <span>{this.state.error.message}</span>
      </div>
    ) : (
      this.props.children
    );
  }
}

export function useReportBreakpoint(
  definition: ReportDefinition,
): ReportBreakpoint {
  const resolve = useCallback(
    () =>
      window.innerWidth >= definition.layout.breakpoints.lg
        ? "lg"
        : window.innerWidth >= definition.layout.breakpoints.md
          ? "md"
          : "sm",
    [definition.layout.breakpoints.lg, definition.layout.breakpoints.md],
  );
  const [breakpoint, setBreakpoint] = useState<ReportBreakpoint>(resolve);
  useEffect(() => {
    const listener = () => setBreakpoint(resolve());
    window.addEventListener("resize", listener);
    return () => window.removeEventListener("resize", listener);
  }, [resolve]);
  return breakpoint;
}

export function ReportRuntimeModal({
  reportId,
  onClose,
}: {
  reportId: string;
  onClose: () => void;
}) {
  useEffect(() => {
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [onClose]);
  return (
    <div
      className="rpt-report-modal"
      role="dialog"
      aria-modal="true"
      aria-label="关联报告"
    >
      <button
        type="button"
        className="rpt-report-modal-backdrop"
        aria-label="关闭弹窗"
        onClick={onClose}
      />
      <div className="rpt-report-modal-panel">
        <header>
          <strong>关联报告</strong>
          <button type="button" onClick={onClose} aria-label="关闭">
            ×
          </button>
        </header>
        <iframe
          title="关联报告"
          src={`/reports/${encodeURIComponent(reportId)}`}
        />
      </div>
    </div>
  );
}
