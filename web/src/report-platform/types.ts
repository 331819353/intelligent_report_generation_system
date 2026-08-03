export type ReportBreakpoint = "lg" | "md" | "sm";

export type ReportGrid = { x: number; y: number; w: number; h: number };

export type ReportDefinition = {
  $schema: "https://schemas.intelligent-report.local/report-1.0.schema.json";
  schemaVersion: "1.0.0";
  report: {
    id?: string;
    code: string;
    name?: string;
    title: string;
    description?: string;
    type: "DASHBOARD" | "REPORT";
    status: "DRAFT" | "PUBLISHED" | "ARCHIVED";
    themeId: string;
    language: string;
    timezone: string;
    visibility: "PRIVATE" | "TENANT" | "PUBLIC";
    onlineEnabled?: boolean;
    pdfArchiveEnabled?: boolean;
    defaultRefreshPolicy?: "REALTIME" | "CACHE" | "MATERIALIZED" | "SNAPSHOT";
  };
  layout: {
    columns: 12;
    rowHeight: number;
    margin: number;
    breakpoints: Record<ReportBreakpoint, number>;
  };
  globalFilters: GlobalFilterDefinition[];
  cards: ReportCardDefinition[];
  extensions?: Record<string, unknown>;
};

export type GlobalFilterDefinition = {
  id: string;
  label: string;
  type:
    "DATE_RANGE" | "DATE" | "SELECT" | "MULTI_SELECT" | "NUMBER_RANGE" | "TEXT";
  source: { semanticModelId: string; dimensionId: string };
  operator: FilterOperator;
  defaultValue?: unknown;
  required: boolean;
  multiValue?: boolean;
  options?: Array<{ label: string; value: unknown }>;
  appearance?: Record<string, unknown>;
};

export type FilterOperator =
  "equals" | "notEquals" | "in" | "notIn" | "between" | "gte" | "lt";
export type CardType =
  "TITLE" | "CONCLUSION" | "CHART" | "COMPARISON" | "RANKING" | "TABLE";
export type MetricRole =
  "value" | "baseline" | "target" | "numerator" | "denominator" | "series";
export type DimensionRole = "category" | "series" | "group" | "time" | "column";

export type ReportCardDefinition = {
  id: string;
  type: CardType;
  cardVersion: string;
  layout: Record<ReportBreakpoint, ReportGrid>;
  appearance: {
    title: string;
    subtitle?: string;
    description?: string;
    showHeader?: boolean;
    heightMode?: "FIXED" | "CONTENT";
  };
  config?: Record<string, unknown>;
  binding: {
    semanticModelId?: string;
    metrics: Array<{
      id: string;
      version?: number;
      versionId?: string;
      role: MetricRole;
      alias?: string;
    }>;
    dimensions: Array<{ id: string; role: DimensionRole; alias?: string }>;
    globalFilterBindings: Array<{
      filterId: string;
      targetDimensionId: string;
      enabled?: boolean;
    }>;
    filters: Array<{
      dimensionId: string;
      operator: FilterOperator;
      value: unknown;
    }>;
    sort: Array<{ field: string; direction: "asc" | "desc" }>;
    limit?: number;
  };
  interactions: Array<{
    id: string;
    event: "data.click" | "card.click" | "table.row.click";
    action: {
      type: "drillDown" | "crossFilter" | "navigate" | "openModal" | "openUrl";
      pathId?: string;
      toDimension?: string;
      targetReportId?: string;
      targetCardId?: string;
      url?: string;
      parameterMap?: Record<string, string>;
    };
  }>;
  permissions?: {
    requiredPermission?: string;
    allowedRoleCodes?: string[];
    denyDownload?: boolean;
  };
  extensions?: Record<string, unknown>;
};

export type ReportValidationIssue = { path: string; reason: string };
export type ReportValidationResult = {
  definition?: ReportDefinition;
  errors: ReportValidationIssue[];
};

export type QueryColumn = {
  code: string;
  name: string;
  fieldId?: string;
  role?: "DIMENSION" | "METRIC";
  canonicalType?: string;
};

export type CardQueryResult = {
  cardId: string;
  status: "SUCCESS" | "ERROR";
  columns: QueryColumn[];
  rows: unknown[][];
  rowCount: number;
  durationMs: number;
  cacheHit: boolean;
  errorCode?: string;
  errorMessage?: string;
  warnings?: Array<{ code: string; message: string }>;
};

export type ReportQueryBatchRequest = {
  cardIds: string[];
  filters: Record<string, unknown>;
  interactionContext?: Record<string, ReportInteractionContext>;
};

export type ReportInteractionContext = {
  sourceCardId: string;
  interactionId: string;
  value: unknown;
};

export type ReportQueryBatchResponse = {
  requestId: string;
  results: CardQueryResult[];
};

export type CardInteractionEvent = {
  cardId: string;
  event: "data.click" | "card.click" | "table.row.click";
  datum?: Record<string, unknown>;
  value?: unknown;
};
