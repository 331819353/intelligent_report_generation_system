import {
  Archive,
  ArrowCounterClockwise,
  ArrowRight,
  ArrowsLeftRight,
  Books,
  Check,
  CheckCircle,
  CirclesThreePlus,
  Clock,
  Cube,
  Database,
  DownloadSimple,
  Eye,
  FileXls,
  Funnel,
  Gauge,
  GitBranch,
  Hash,
  Lifebuoy,
  MagnifyingGlass,
  Plus,
  Pause,
  Play,
  RocketLaunch,
  SealCheck,
  ShieldCheck,
  Sparkle,
  Stack,
  Stop,
  UploadSimple,
  WarningCircle,
  X,
} from "@phosphor-icons/react";
import {
  Fragment,
  useEffect,
  useMemo,
  useState,
  type ComponentType,
} from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { AppButton } from "../components/AppButton";
import { AppShell } from "../components/AppShell";
import "../styles/semantic-center.css";
import { SemanticOperationsPanel } from "../components/semantic/SemanticOperationsPanel";
import { AdditivityBacklogPanel } from "../components/semantic/AdditivityBacklogPanel";
import { QualityRulesPanel } from "../components/semantic/QualityRulesPanel";
import { RowAccessPoliciesPanel } from "../components/semantic/RowAccessPoliciesPanel";
import { RequestError } from "../lib/api";
import { currentDomain, currentDomainID } from "../lib/domain-context";
import {
  datasetAPI,
  type DatasetMaterializationReceipt,
  type DatasetSummary,
  type PublishedVersionRecord,
} from "../lib/datasets";
import { semanticDatasetGrain } from "../lib/semantic-dataset";
import {
  semanticAPI,
  type AdditivityReadiness,
  type EvaluationSetCatalogItem,
  type EvaluationReviewPage,
  type ReleaseCatalogItem,
  type ReleaseLifecycle,
  type ReleaseOperationalImpact,
  type ReleaseRollout,
  type SemanticImport,
  type SemanticObject,
  type TimeContractCatalogItem,
} from "../lib/semantic";

type SemanticCatalogTab = "models" | "metrics" | "dimensions" | "knowledge";
type SemanticTab = SemanticCatalogTab | "releases" | "operations";
type Notice = { tone: "success" | "error"; message: string };
type BuilderFailure = { code: string; title: string; message: string };
type ReleaseUIState = {
  gatePassed?: boolean;
  evaluationSetId?: string;
  evaluationBatchId?: string;
  gateReceiptHash?: string;
  gateFailures?: string[];
  gateFacts?: unknown;
  approvals?: number;
  approvedRoles?: string[];
  actorHasApproved?: boolean;
  rejectionCount?: number;
  rejectedRoles?: string[];
  actorApprovalRole?: string;
  approvalDueAt?: string;
  approvalSlaStatus?: string;
  escalationLevel?: number;
  reviewReportCount?: number;
  stateVersion?: number;
  rollout?: ReleaseRollout;
  projections?: ReleaseLifecycle["projections"];
  preflightIssues?: Array<{
    code: string;
    objectType?: string;
    objectVersionId?: string;
  }>;
};

type ReleaseOperationState = ReleaseOperationalImpact & {
  rollout?: ReleaseRollout;
};
type BuilderDraft = {
  code: string;
  name: string;
  description: string;
  datasetId: string;
  timeContractVersionId: string;
  grain: string;
  grainKeys: string[];
  primaryTimeFieldId: string;
  entityName: string;
};

type SemanticDatasetField = {
  id: string;
  code: string;
  name: string;
  role: string;
  canonicalType: string;
  semanticType: string;
  nullable: boolean;
  sensitivityLevel: string;
};

type BuilderDatasetContext = {
  loading: boolean;
  version?: PublishedVersionRecord;
  fields: SemanticDatasetField[];
  grainKeys: string[];
  timeFields: SemanticDatasetField[];
  measureFields: SemanticDatasetField[];
  dimensionFields: SemanticDatasetField[];
  joins: Array<Record<string, unknown>>;
  materialization?: DatasetMaterializationReceipt;
  issue?: string;
};

type SemanticTabMeta = {
  label: string;
  description: string;
  icon: ComponentType<{
    size?: number;
    weight?: "regular" | "duotone" | "fill";
  }>;
};

const tabMeta: Record<SemanticTab, SemanticTabMeta> = {
  models: { label: "语义模型", description: "实体、粒度与关系", icon: Cube },
  metrics: {
    label: "指标中心",
    description: "公式、口径与可加性",
    icon: Gauge,
  },
  dimensions: {
    label: "维度中心",
    description: "成员、层级与索引",
    icon: CirclesThreePlus,
  },
  knowledge: {
    label: "业务知识",
    description: "词典、问法与 KPI",
    icon: Books,
  },
  releases: {
    label: "发布中心",
    description: "校验、评测与激活",
    icon: RocketLaunch,
  },
  operations: {
    label: "质量运营",
    description: "反馈、改进与闭环",
    icon: Lifebuoy,
  },
};

const semanticTabRoutes: Record<SemanticTab, string> = {
  models: "/semantic",
  metrics: "/semantic/metrics",
  dimensions: "/semantic/dimensions",
  knowledge: "/semantic/knowledge",
  releases: "/semantic/releases",
  operations: "/semantic/operations",
};

function semanticTabFromPath(pathname: string): SemanticTab | undefined {
  return (Object.entries(semanticTabRoutes) as Array<[SemanticTab, string]>).find(([, route]) => route === pathname)?.[0];
}

const snapshotModels: SemanticObject[] = [
  {
    id: "model-sales-v4",
    objectId: "model-sales",
    versionNo: 4,
    code: "sales_order_model",
    name: "销售订单语义模型",
    description: "统一订单事实、客户、商品与渠道分析关系。",
    status: "CERTIFIED",
    layer: "DWD",
    datasetId: "snapshot-sales-detail",
    updatedAt: "2026-08-11T09:48:00+08:00",
    ownerId: "张晨",
  },
  {
    id: "model-customer-v2",
    objectId: "model-customer",
    versionNo: 2,
    code: "customer_profile_model",
    name: "客户主数据模型",
    description: "客户统一身份、区域和分层标签。",
    status: "CERTIFIED",
    layer: "DIM",
    datasetId: "snapshot-customer",
    updatedAt: "2026-08-11T08:52:00+08:00",
    ownerId: "刘洋",
  },
  {
    id: "model-channel-v3",
    objectId: "model-channel",
    versionNo: 3,
    code: "channel_sales_model",
    name: "渠道经营模型",
    description: "渠道销售日报及区域经营层级。",
    status: "DRAFT",
    layer: "DWS",
    datasetId: "snapshot-channel",
    updatedAt: "2026-08-10T18:26:00+08:00",
    ownerId: "王敏",
  },
  {
    id: "model-inventory-v1",
    objectId: "model-inventory",
    versionNo: 1,
    code: "inventory_snapshot_model",
    name: "库存快照模型",
    description: "库存时点、仓库和商品粒度定义待确认。",
    status: "DRAFT",
    layer: "DWD",
    datasetId: "snapshot-inventory",
    updatedAt: "2026-08-10T15:20:00+08:00",
    ownerId: "李杰",
  },
];

const snapshotMetrics: SemanticObject[] = [
  {
    id: "metric-sales-v8",
    objectId: "metric-sales",
    versionNo: 8,
    code: "sales_revenue",
    name: "销售收入",
    description: "剔除取消订单后的含税成交金额。",
    status: "CERTIFIED",
    additivity: "ADDITIVE",
    timeGrain: "DAY",
    unit: "CNY",
    updatedAt: "2026-08-11T09:36:00+08:00",
    ownerId: "张晨",
  },
  {
    id: "metric-profit-v5",
    objectId: "metric-profit",
    versionNo: 5,
    code: "gross_profit_rate",
    name: "毛利率",
    description: "毛利额 / 销售收入，分母为零时返回空值。",
    status: "CERTIFIED",
    additivity: "NON_ADDITIVE",
    timeGrain: "DAY",
    unit: "%",
    updatedAt: "2026-08-11T09:30:00+08:00",
    ownerId: "张晨",
  },
  {
    id: "metric-orders-v4",
    objectId: "metric-orders",
    versionNo: 4,
    code: "valid_order_count",
    name: "有效订单数",
    description: "支付成功且未取消的订单去重数。",
    status: "CERTIFIED",
    additivity: "ADDITIVE",
    timeGrain: "DAY",
    unit: "笔",
    updatedAt: "2026-08-10T18:40:00+08:00",
    ownerId: "刘洋",
  },
  {
    id: "metric-stock-v2",
    objectId: "metric-stock",
    versionNo: 2,
    code: "ending_inventory",
    name: "期末库存",
    description: "统计周期末最后一个有效库存快照。",
    status: "DRAFT",
    additivitySuggestion: "SEMI_ADDITIVE",
    timeGrain: "DAY",
    unit: "件",
    updatedAt: "2026-08-10T16:05:00+08:00",
    ownerId: "李杰",
  },
  {
    id: "metric-turnover-v1",
    objectId: "metric-turnover",
    versionNo: 1,
    code: "inventory_turnover_days",
    name: "库存周转天数",
    description: "平均库存 / 日均销售成本。",
    status: "DRAFT",
    additivitySuggestion: "NON_ADDITIVE",
    timeGrain: "MONTH",
    unit: "天",
    updatedAt: "2026-08-10T14:12:00+08:00",
    ownerId: "李杰",
  },
];

const snapshotDimensions: SemanticObject[] = [
  {
    id: "dim-region-v5",
    objectId: "dim-region",
    versionNo: 5,
    code: "region",
    name: "经营区域",
    description: "大区—省区—城市三级经营层级。",
    status: "CERTIFIED",
    kind: "GEOGRAPHY",
    sensitivity: "INTERNAL",
    memberIndexPolicy: "FULL",
    updatedAt: "2026-08-11T09:16:00+08:00",
    ownerId: "刘洋",
  },
  {
    id: "dim-channel-v3",
    objectId: "dim-channel",
    versionNo: 3,
    code: "channel",
    name: "销售渠道",
    description: "直营网、经销、电商与新零售渠道。",
    status: "CERTIFIED",
    kind: "CATEGORY",
    sensitivity: "INTERNAL",
    memberIndexPolicy: "FULL",
    updatedAt: "2026-08-10T19:02:00+08:00",
    ownerId: "王敏",
  },
  {
    id: "dim-customer-v2",
    objectId: "dim-customer",
    versionNo: 2,
    code: "customer",
    name: "客户",
    description: "高基数客户主数据，仅支持精确检索。",
    status: "CERTIFIED",
    kind: "ENTITY",
    sensitivity: "CONFIDENTIAL",
    memberIndexPolicy: "EXACT_ONLY",
    updatedAt: "2026-08-10T17:24:00+08:00",
    ownerId: "刘洋",
  },
  {
    id: "dim-product-v4",
    objectId: "dim-product",
    versionNo: 4,
    code: "product_line",
    name: "产品线",
    description: "产品族、产品线和型号层级。",
    status: "DRAFT",
    kind: "CATEGORY",
    sensitivity: "INTERNAL",
    memberIndexPolicy: "ON_DEMAND",
    updatedAt: "2026-08-10T13:36:00+08:00",
    ownerId: "王敏",
  },
];

const snapshotKnowledge: SemanticObject[] = [
  {
    id: "term-gmv-v3",
    objectId: "term-gmv",
    versionNo: 3,
    code: "gmv",
    name: "GMV",
    term: "GMV",
    definition: "成交总额，不等同于已确认销售收入。",
    aliases: ["成交额", "交易总额"],
    status: "CERTIFIED",
    updatedAt: "2026-08-11T09:10:00+08:00",
    ownerId: "张晨",
  },
  {
    id: "term-net-sales-v2",
    objectId: "term-net-sales",
    versionNo: 2,
    code: "net_sales",
    name: "净销售额",
    term: "净销",
    definition: "扣除取消、退货和折让后的销售额。",
    aliases: ["净销", "净收入"],
    status: "CERTIFIED",
    updatedAt: "2026-08-10T18:32:00+08:00",
    ownerId: "张晨",
  },
  {
    id: "bundle-operation-v4",
    objectId: "bundle-operation",
    versionNo: 4,
    code: "operation_overview",
    name: "经营总览 KPI",
    definition: "销售收入、毛利率、订单数和库存周转。",
    status: "CERTIFIED",
    updatedAt: "2026-08-10T16:50:00+08:00",
    ownerId: "王敏",
  },
  {
    id: "example-profit-v1",
    objectId: "example-profit",
    versionNo: 1,
    code: "profit_decline_reason",
    name: "毛利率下降原因问法",
    definition: "绑定毛利率、区域和同比意图，等待业务 Owner 认证。",
    status: "DRAFT",
    updatedAt: "2026-08-10T14:40:00+08:00",
    ownerId: "刘洋",
  },
];

const snapshotReleases: ReleaseCatalogItem[] = [
  {
    id: "release-2026-08-11",
    semanticVersion: "enterprise-ops/2026.08.11-rc1",
    contentHash:
      "8fe73d1a9c2b4e65a5d9b7c0c53d8b931e22f9b7ca660a2da23017ae89bf4412",
    status: "BLOCKED",
    objectCount: 26,
    version: 1,
    readyProjectionCount: 2,
    approvalCount: 0,
    createdAt: "2026-08-11T09:56:00+08:00",
    updatedAt: "2026-08-11T09:56:00+08:00",
  },
  {
    id: "release-2026-08-08",
    semanticVersion: "enterprise-ops/2026.08.08",
    contentHash:
      "23479e7da0c55f3c52ab471441240388090a32959a69970976db3619f17c3d82",
    status: "ACTIVE",
    objectCount: 23,
    version: 6,
    readyProjectionCount: 4,
    approvalCount: 2,
    createdAt: "2026-08-08T15:30:00+08:00",
    updatedAt: "2026-08-08T18:20:00+08:00",
    readyAt: "2026-08-08T17:48:00+08:00",
    activatedAt: "2026-08-08T18:20:00+08:00",
  },
  {
    id: "release-2026-08-01",
    semanticVersion: "enterprise-ops/2026.08.01",
    contentHash:
      "5b9dd6c562bc02dd427f9fc80c4679bca2a0110a72571c7b1c872b067e9f5789",
    status: "SUPERSEDED",
    objectCount: 20,
    version: 7,
    readyProjectionCount: 4,
    approvalCount: 2,
    createdAt: "2026-08-01T11:20:00+08:00",
    updatedAt: "2026-08-08T18:20:00+08:00",
    readyAt: "2026-08-01T14:08:00+08:00",
    activatedAt: "2026-08-01T15:10:00+08:00",
  },
  {
    id: "release-2026-08-12",
    semanticVersion: "enterprise-ops/2026.08.12-rc2",
    contentHash:
      "72cc07f449cc8fd3f4997073562dd47b568211f971d28a95c72486e7e16ca1d1",
    status: "READY",
    objectCount: 29,
    version: 4,
    readyProjectionCount: 4,
    approvalCount: 2,
    createdAt: "2026-08-12T09:10:00+08:00",
    updatedAt: "2026-08-12T10:20:00+08:00",
    readyAt: "2026-08-12T09:58:00+08:00",
  },
];

const snapshotReleaseStates: Record<string, ReleaseUIState> = {
  "release-2026-08-11": {
    projections: [
      {
        target: "REGISTRY",
        status: "READY",
        expectedContentHash: snapshotReleases[0].contentHash,
        appliedContentHash: snapshotReleases[0].contentHash,
        attempt: 1,
        maxAttempts: 3,
        hashMatched: true,
      },
      {
        target: "SEARCH",
        status: "FAILED",
        expectedContentHash: snapshotReleases[0].contentHash,
        attempt: 3,
        maxAttempts: 3,
        errorCode: "SEARCH_INDEX_TIMEOUT",
        hashMatched: false,
      },
      {
        target: "GRAPH",
        status: "READY",
        expectedContentHash: snapshotReleases[0].contentHash,
        appliedContentHash: snapshotReleases[0].contentHash,
        attempt: 1,
        maxAttempts: 3,
        hashMatched: true,
      },
      {
        target: "MEMBER",
        status: "BLOCKED",
        expectedContentHash: snapshotReleases[0].contentHash,
        appliedContentHash: "9e27f91b0aa34bb8",
        attempt: 2,
        maxAttempts: 3,
        errorCode: "CONTENT_HASH_MISMATCH",
        hashMatched: false,
      },
    ],
  },
  "release-2026-08-12": {
    projections: ["REGISTRY", "SEARCH", "GRAPH", "MEMBER"].map(
      (target) => ({
        target,
        status: "READY",
        expectedContentHash: snapshotReleases[3].contentHash,
        appliedContentHash: snapshotReleases[3].contentHash,
        attempt: 1,
        maxAttempts: 3,
        hashMatched: true,
      }),
    ),
    gatePassed: true,
    approvals: 2,
    approvedRoles: ["SEMANTIC_OWNER", "DATA_OWNER"],
    rollout: {
      id: "snapshot-rollout-canary-20",
      candidateReleaseId: "release-2026-08-12",
      controlReleaseId: "release-2026-08-08",
      stage: "CANARY_20",
      state: "RUNNING",
      canaryPercent: 20,
      version: 3,
      startedAt: "2026-08-12T09:58:00+08:00",
      stageStartedAt: "2026-08-12T10:02:00+08:00",
      updatedAt: "2026-08-12T10:20:00+08:00",
    },
  },
};

const snapshotDatasets: DatasetSummary[] = [
  {
    id: "snapshot-sales-detail",
    code: "dwd_sales_order_detail",
    name: "销售订单经营明细",
    description: "订单、客户、商品和渠道明细。",
    type: "STANDARD",
    status: "PUBLISHED",
    layer: "DWD",
    tags: ["经营分析"],
    version: 5,
    dslHash: "f".repeat(64),
    currentPublishedVersionId: "snapshot-sales-v5",
    updatedAt: "2026-08-11T09:32:00+08:00",
  },
  {
    id: "snapshot-customer",
    code: "dim_customer_profile",
    name: "客户主数据维度",
    description: "客户统一身份与区域标签。",
    type: "STANDARD",
    status: "PUBLISHED",
    layer: "DIM",
    tags: ["客户"],
    version: 6,
    dslHash: "e".repeat(64),
    currentPublishedVersionId: "snapshot-customer-v6",
    updatedAt: "2026-08-11T08:48:00+08:00",
  },
  {
    id: "snapshot-channel",
    code: "dws_channel_sales_daily",
    name: "渠道销售日汇总",
    description: "渠道销售经营指标。",
    type: "STANDARD",
    status: "PUBLISHED",
    layer: "DWS",
    tags: ["渠道"],
    version: 12,
    dslHash: "d".repeat(64),
    currentPublishedVersionId: "snapshot-channel-v12",
    updatedAt: "2026-08-10T18:16:00+08:00",
  },
];

const snapshotTimeContracts: TimeContractCatalogItem[] = [
  {
    id: "snapshot-time-contract-v3",
    timeContractId: "snapshot-time-contract",
    code: "enterprise_calendar",
    name: "企业经营自然日历",
    versionNo: 3,
    status: "CERTIFIED",
    timezone: "Asia/Shanghai",
    incompletePeriodPolicy: "LAST_COMPLETE",
    expectedLagHours: 8,
    contentHash: "a".repeat(64),
    updatedAt: "2026-08-10T12:00:00+08:00",
  },
];

const snapshotReadiness: AdditivityReadiness = {
  domainId: "snapshot-enterprise-operations",
  metricCount: 8,
  confirmedCount: 6,
  unconfirmedCount: 2,
  confirmationRate: 0.75,
};
const initialDraft: BuilderDraft = {
  code: "sales_order_model",
  name: "销售订单语义模型",
  description: "统一订单、客户、商品和渠道分析口径。",
  datasetId: "snapshot-sales-detail",
  timeContractVersionId: "snapshot-time-contract-v3",
  grain: "每个有效销售订单明细行",
  grainKeys: ["order_id"],
  primaryTimeFieldId: "order_date",
  entityName: "销售订单",
};

const emptyBuilderContext: BuilderDatasetContext = {
  loading: false,
  fields: [],
  grainKeys: [],
  timeFields: [],
  measureFields: [],
  dimensionFields: [],
  joins: [],
};

const snapshotBuilderContext: BuilderDatasetContext = {
  loading: false,
  fields: [
    {
      id: "order_id",
      code: "order_id",
      name: "订单编号",
      role: "IDENTIFIER",
      canonicalType: "STRING",
      semanticType: "IDENTIFIER",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
    {
      id: "order_date",
      code: "order_date",
      name: "订单日期",
      role: "TIME",
      canonicalType: "DATE",
      semanticType: "DATE",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
    {
      id: "region",
      code: "region",
      name: "经营区域",
      role: "DIMENSION",
      canonicalType: "STRING",
      semanticType: "REGION",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
    {
      id: "sales_amount",
      code: "sales_amount",
      name: "销售金额",
      role: "MEASURE",
      canonicalType: "DECIMAL",
      semanticType: "AMOUNT",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
  ],
  grainKeys: ["order_id"],
  timeFields: [
    {
      id: "order_date",
      code: "order_date",
      name: "订单日期",
      role: "TIME",
      canonicalType: "DATE",
      semanticType: "DATE",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
  ],
  measureFields: [
    {
      id: "sales_amount",
      code: "sales_amount",
      name: "销售金额",
      role: "MEASURE",
      canonicalType: "DECIMAL",
      semanticType: "AMOUNT",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
  ],
  dimensionFields: [
    {
      id: "region",
      code: "region",
      name: "经营区域",
      role: "DIMENSION",
      canonicalType: "STRING",
      semanticType: "REGION",
      nullable: false,
      sensitivityLevel: "INTERNAL",
    },
  ],
  joins: [],
  materialization: {
    id: "snapshot-materialization",
    datasetVersionId: "snapshot-sales-v5",
    layer: "DWS",
    status: "ACTIVE",
    schemaHash: "f".repeat(64),
    snapshotHash: "e".repeat(64),
    activatedAt: "2026-08-11T09:33:00+08:00",
  },
};

function statusLabel(status: string) {
  return (
    (
      {
        DRAFT: "草稿",
        CERTIFIED: "已认证",
        ACTIVE: "生效中",
        DEPRECATED: "已废弃",
        VALIDATING: "校验中",
        PROJECTING: "投影中",
        READY: "待灰度",
        BLOCKED: "已阻塞",
        SUPERSEDED: "已替代",
        RETAINED: "保留中",
        RETIRED: "已退役",
      } as Record<string, string>
    )[status] ?? status
  );
}

const rolloutEvidenceLabels: Record<string, string> = {
  ROLLOUT_NOT_RUNNING: "当前灰度未处于运行状态",
  OFFLINE_GATE_REQUIRED: "离线评测门禁尚未通过",
  MINIMUM_STAGE_DURATION_REQUIRED: "当前阶段尚未达到 15 分钟最短观测时长",
  SHADOW_CONTROL_OBSERVATIONS_REQUIRED: "Shadow 基线样本尚未达到 5 条",
  SHADOW_PAIRED_OBSERVATIONS_REQUIRED: "Shadow 双跑对照样本尚未达到 5 组",
  SHADOW_RUNS_PENDING: "仍有候选 Shadow 运行尚未完成",
  SHADOW_ALIGNMENT_REQUIRED: "控制与候选的语义或结果尚未完全对齐",
  SHADOW_ALIGNMENT_REGRESSION: "Shadow 对照出现不一致，已触发止损",
  SHADOW_SECURITY_REGRESSION: "Shadow 候选出现安全回归，已触发止损",
  CANARY_CONTROL_SAMPLES_REQUIRED: "控制版本样本尚未达到 20 条",
  CANARY_CANDIDATE_SAMPLES_REQUIRED: "候选版本样本尚未达到 20 条",
  CANARY_SECURITY_REGRESSION: "候选版本出现安全回归，已触发止损",
  CANARY_ANSWER_RATE_REGRESSION: "候选版本回答率下降超过 10%",
  CANARY_CLARIFICATION_REGRESSION: "候选版本澄清率上升超过 15%",
  CANARY_LATENCY_REGRESSION: "候选版本 P95 延迟超过控制版本 2 倍",
  CANARY_COST_REGRESSION: "候选版本平均成本超过控制版本 2 倍",
};

function rolloutEvidenceLabel(code: string) {
  return rolloutEvidenceLabels[code] ?? code;
}

function compactDuration(seconds: number) {
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
  return `${Math.floor(seconds / 3600)} 小时 ${Math.floor((seconds % 3600) / 60)} 分钟`;
}

function rolloutRate(value: number, total: number) {
  return total > 0 ? `${Math.round((value / total) * 100)}%` : "—";
}

function formatTime(value?: string) {
  if (!value) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(new Date(value));
}

async function sha256Text(value: string) {
  const bytes = new TextEncoder().encode(value);
  const digest = await crypto.subtle.digest("SHA-256", bytes);
  return Array.from(new Uint8Array(digest), (item) =>
    item.toString(16).padStart(2, "0"),
  ).join("");
}

function downloadResponse(response: Response, fallbackName: string) {
  return response.blob().then((blob) => {
    const disposition = response.headers.get("Content-Disposition") ?? "";
    const encoded = disposition.match(/filename\*=UTF-8''([^;]+)/i)?.[1];
    const plain = disposition.match(/filename="?([^";]+)"?/i)?.[1];
    const filename = encoded
      ? decodeURIComponent(encoded)
      : plain || fallbackName;
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = filename;
    anchor.click();
    URL.revokeObjectURL(url);
  });
}

function objectTitle(item: SemanticObject) {
  return item.name || item.term || item.code || "未命名语义对象";
}

function pageItems<T>(page: { items?: T[] | null } | null | undefined): T[] {
  return Array.isArray(page?.items) ? page.items : [];
}

function recordValue(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function textValue(value: unknown, fallback = ""): string {
  return typeof value === "string" && value.trim() ? value.trim() : fallback;
}

function semanticFields(
  version: PublishedVersionRecord,
): SemanticDatasetField[] {
  return (Array.isArray(version.dsl.fields) ? version.dsl.fields : [])
    .map((raw) => {
      const field = recordValue(raw);
      const code = textValue(field.code, textValue(field.id));
      return {
        id: textValue(field.id, code),
        code,
        name: textValue(field.name, code),
        role: textValue(field.role, "ATTRIBUTE").toUpperCase(),
        canonicalType: textValue(field.canonicalType, "STRING").toUpperCase(),
        semanticType: textValue(field.semanticType, "ATTRIBUTE").toUpperCase(),
        nullable: field.nullable !== false,
        sensitivityLevel: textValue(
          field.sensitivityLevel,
          "INTERNAL",
        ).toUpperCase(),
      };
    })
    .filter((field) => field.code);
}

function datasetJoins(
  version: PublishedVersionRecord,
): Array<Record<string, unknown>> {
  const designer = recordValue(version.dsl.designer);
  return Array.isArray(designer.joins) ? designer.joins.map(recordValue) : [];
}

function metricIdentity(
  metricVersions: SemanticObject[],
  metrics: SemanticObject[],
) {
  const identities = new Map(metrics.map((metric) => [metric.id, metric]));
  return metricVersions.map((version) => {
    const identity = identities.get(
      String(version.objectId ?? version.metricId ?? ""),
    );
    return identity
      ? {
          ...version,
          code: identity.code,
          name: identity.name,
          description: identity.description,
        }
      : version;
  });
}

function semanticBootstrapKey(modelID: string, resource: string, code: string) {
  return `semantic-bootstrap:${modelID}:${resource}:${code}`;
}

function fieldMetricContract(field: SemanticDatasetField) {
  const currency =
    /(amount|sales|revenue|cost|price|gmv|金额|收入|成本|价格)/i.test(
      `${field.code} ${field.name}`,
    );
  return {
    unit: currency ? "CNY" : "COUNT",
    currency: currency ? "CNY" : "",
    displayPrecision: currency || field.canonicalType === "DECIMAL" ? 2 : 0,
  };
}

function fieldSensitivity(field: SemanticDatasetField) {
  return ["PUBLIC", "INTERNAL", "CONFIDENTIAL", "RESTRICTED"].includes(
    field.sensitivityLevel,
  )
    ? field.sensitivityLevel
    : "INTERNAL";
}

export function SemanticCenterPage() {
  const location = useLocation();
  const navigate = useNavigate();
  const params = new URLSearchParams(window.location.search);
  const designSnapshot = import.meta.env.DEV && params.has("snapshot");
  const [activeTab, setActiveTab] = useState<SemanticTab>(
    semanticTabFromPath(location.pathname) ?? (params.get("workspace") === "feedback" ? "operations" : "models"),
  );
  const [objects, setObjects] = useState<
    Record<SemanticCatalogTab, SemanticObject[]>
  >({
    models: designSnapshot ? snapshotModels : [],
    metrics: designSnapshot ? snapshotMetrics : [],
    dimensions: designSnapshot ? snapshotDimensions : [],
    knowledge: designSnapshot ? snapshotKnowledge : [],
  });
  const [releases, setReleases] = useState<ReleaseCatalogItem[]>(
    designSnapshot ? snapshotReleases : [],
  );
  const [datasets, setDatasets] = useState<DatasetSummary[]>(
    designSnapshot ? snapshotDatasets : [],
  );
  const [timeContracts, setTimeContracts] = useState<TimeContractCatalogItem[]>(
    designSnapshot ? snapshotTimeContracts : [],
  );
  const [evaluationSets, setEvaluationSets] = useState<
    EvaluationSetCatalogItem[]
  >([]);

  useEffect(() => {
    const routeTab = semanticTabFromPath(location.pathname);
    if (routeTab) setActiveTab(routeTab);
  }, [location.pathname]);
  const [qualityRules, setQualityRules] = useState<SemanticObject[]>([]);
  const [rowAccessPolicies, setRowAccessPolicies] = useState<SemanticObject[]>([]);
  const [readiness, setReadiness] = useState<AdditivityReadiness>(
    designSnapshot
      ? snapshotReadiness
      : {
          domainId: "",
          metricCount: 0,
          confirmedCount: 0,
          unconfirmedCount: 0,
          confirmationRate: 0,
        },
  );
  const [loading, setLoading] = useState(!designSnapshot);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState<Notice | null>(null);
  const [keyword, setKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState("ALL");
  const [builderOpen, setBuilderOpen] = useState(false);
  const [builderStep, setBuilderStep] = useState(1);
  const [draft, setDraft] = useState<BuilderDraft>(initialDraft);
  const [editingModel, setEditingModel] = useState<SemanticObject | null>(null);
  const [builderFailure, setBuilderFailure] = useState<BuilderFailure | null>(
    null,
  );
  const [builderContext, setBuilderContext] = useState<BuilderDatasetContext>(
    designSnapshot ? snapshotBuilderContext : emptyBuilderContext,
  );
  const [busy, setBusy] = useState("");
  const [releaseStates, setReleaseStates] = useState<
    Record<string, ReleaseUIState>
  >(designSnapshot ? snapshotReleaseStates : {});
  const [approvalRelease, setApprovalRelease] =
    useState<ReleaseCatalogItem | null>(null);
  const [approvalCount, setApprovalCount] = useState(0);
  const [approvalComment, setApprovalComment] = useState("");
  const [evaluationRelease, setEvaluationRelease] =
    useState<ReleaseCatalogItem | null>(null);
  const [evaluationFile, setEvaluationFile] = useState<File | null>(null);
  const [evaluationImport, setEvaluationImport] =
    useState<SemanticImport | null>(null);
  const [certifiedEvaluationCount, setCertifiedEvaluationCount] = useState(0);
  const [evaluationReview, setEvaluationReview] =
    useState<EvaluationReviewPage | null>(null);
  const [evaluationReviewOffset, setEvaluationReviewOffset] = useState(0);
  const [evaluationReviewComment, setEvaluationReviewComment] = useState("");
  const [evaluationReviewAcknowledged, setEvaluationReviewAcknowledged] =
    useState(false);
  const [detailObject, setDetailObject] = useState<SemanticObject | null>(null);
  const [caliberDraft, setCaliberDraft] = useState("");
  const [receiptRelease, setReceiptRelease] =
    useState<ReleaseCatalogItem | null>(null);
  const [releaseDiagnostic, setReleaseDiagnostic] =
    useState<ReleaseCatalogItem | null>(null);
  const [governanceRepairOpen, setGovernanceRepairOpen] = useState(false);
  const [operationRelease, setOperationRelease] =
    useState<ReleaseCatalogItem | null>(null);
  const [releaseOperations, setReleaseOperations] =
    useState<ReleaseOperationState | null>(null);
  const [operationReason, setOperationReason] = useState("");

  useEffect(() => {
    if (designSnapshot) return;
    let cancelled = false;
    const domainId = currentDomainID();
    Promise.all([
      semanticAPI.list("models", undefined, 200),
      Promise.all([
        semanticAPI.list("metric-versions", undefined, 200),
        semanticAPI.list("metrics", undefined, 200),
      ]),
      semanticAPI.list("dimensions", undefined, 200),
      Promise.all([
        semanticAPI.list("terms", undefined, 120),
        semanticAPI.list("kpi-bundles", undefined, 80),
        semanticAPI.list("certified-examples", undefined, 80),
      ]),
      semanticAPI.releases(100),
      semanticAPI.evaluationSets(100),
      semanticAPI.timeContracts(100),
      semanticAPI.list("quality-rules", undefined, 200),
      semanticAPI.list("row-access-policies", undefined, 200),
      datasetAPI.list(200, 0),
      domainId
        ? semanticAPI.readiness(domainId)
        : Promise.resolve({
            domainId: "",
            metricCount: 0,
            confirmedCount: 0,
            unconfirmedCount: 0,
            confirmationRate: 0,
          }),
    ])
      .then(
        async ([
          models,
          metricCatalog,
          dimensions,
          knowledge,
          releasePage,
          evaluationPage,
          timePage,
          qualityRulePage,
          rowAccessPolicyPage,
          datasetPage,
          readinessResult,
        ]) => {
          if (cancelled) return;
          const releaseItems = pageItems(releasePage);
          const lifecycleResults = await Promise.allSettled(
            releaseItems.map(async (item) => ({
              lifecycle: await semanticAPI.releaseLifecycle(item.id),
              operations: await semanticAPI
                .releaseOperations(item.id)
                .catch(() => null),
            })),
          );
          if (cancelled) return;
          const nextReleaseStates: Record<string, ReleaseUIState> = {};
          lifecycleResults.forEach((result, index) => {
            if (result.status !== "fulfilled") return;
            const { lifecycle, operations } = result.value;
            nextReleaseStates[releaseItems[index].id] = {
              gatePassed: lifecycle.latestGate?.passed,
              evaluationSetId: lifecycle.latestGate?.evaluationSetId,
              evaluationBatchId: lifecycle.latestGate?.evaluationBatchId,
              gateReceiptHash: lifecycle.latestGate?.receiptHash,
              gateFailures: lifecycle.latestGate?.failures,
              gateFacts: lifecycle.latestGate?.facts,
              approvals: lifecycle.approvalCount,
              approvedRoles: lifecycle.approvedRoles,
              actorHasApproved: lifecycle.actorHasApproved,
              rejectionCount: lifecycle.rejectionCount,
              rejectedRoles: lifecycle.rejectedRoles,
              actorApprovalRole: lifecycle.actorApprovalRole,
              approvalDueAt: lifecycle.approvalDueAt,
              approvalSlaStatus: lifecycle.approvalSlaStatus,
              escalationLevel: lifecycle.escalationLevel,
              reviewReportCount: lifecycle.reviewReportCount,
              stateVersion: lifecycle.releaseStateVersion,
              rollout: operations?.rollout,
              projections: lifecycle.projections,
            };
          });
          setObjects({
            models: pageItems(models),
            metrics: metricIdentity(
              pageItems(metricCatalog[0]),
              pageItems(metricCatalog[1]),
            ),
            dimensions: pageItems(dimensions),
            knowledge: knowledge.flatMap(pageItems),
          });
          setReleases(releaseItems);
          setReleaseStates(nextReleaseStates);
          setEvaluationSets(pageItems(evaluationPage));
          setTimeContracts(pageItems(timePage));
          setQualityRules(pageItems(qualityRulePage));
          setRowAccessPolicies(pageItems(rowAccessPolicyPage));
          setDatasets(
            pageItems(datasetPage).filter(
              (item) =>
                item.status === "PUBLISHED" && item.currentPublishedVersionId,
            ),
          );
          setReadiness(readinessResult);
          setError("");
        },
      )
      .catch((cause) => {
        if (!cancelled)
          setError(
            cause instanceof Error ? cause.message : "语义资产暂时无法加载",
          );
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [designSnapshot]);

  useEffect(() => {
    if (
      !evaluationImport ||
      !["UPLOADED", "VALIDATING"].includes(evaluationImport.state) ||
      designSnapshot
    )
      return;
    let cancelled = false;
    const timer = window.setTimeout(() => {
      semanticAPI
        .importStatus(evaluationImport.id)
        .then((status) => {
          if (!cancelled) setEvaluationImport(status);
        })
        .catch((cause) => {
          if (!cancelled)
            setNotice({
              tone: "error",
              message:
                cause instanceof Error
                  ? cause.message
                  : "评测用例校验状态刷新失败",
            });
        });
    }, 1200);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [designSnapshot, evaluationImport]);

  useEffect(() => {
    if (!builderOpen || designSnapshot) return;
    let cancelled = false;
    const load = async () => {
      await Promise.resolve();
      const dataset = datasets.find((item) => item.id === draft.datasetId);
      if (!dataset?.currentPublishedVersionId) {
        if (!cancelled)
          setBuilderContext({
            ...emptyBuilderContext,
            issue: "请选择一个已发布的分析层数据集",
          });
        return;
      }
      if (dataset.layer !== "DWS" && dataset.layer !== "ADS") {
        if (!cancelled)
          setBuilderContext({
            ...emptyBuilderContext,
            issue: "语义模型只能固定 DWS 或 ADS 分析层数据集",
          });
        return;
      }
      if (!cancelled)
        setBuilderContext({ ...emptyBuilderContext, loading: true });
      try {
        const [version, runs] = await Promise.all([
          datasetAPI.getVersion(dataset.id, dataset.currentPublishedVersionId),
          datasetAPI.listDAGRuns(dataset.id, 50, 0),
        ]);
        if (cancelled) return;
        const fields = semanticFields(version);
        const outputGrain = semanticDatasetGrain(version, fields);
        const grainKeys = outputGrain.keys;
        const timeFields = fields.filter(
          (field) =>
            field.role === "TIME" ||
            field.canonicalType === "DATE" ||
            field.canonicalType === "DATETIME",
        );
        const measureFields = fields.filter(
          (field) =>
            field.role === "MEASURE" ||
            field.canonicalType === "INTEGER" ||
            field.canonicalType === "DECIMAL",
        );
        const dimensionFields = fields.filter(
          (field) => field.role === "DIMENSION" || field.role === "ATTRIBUTE",
        );
        const successfulRun = pageItems(runs).find(
          (run) =>
            run.status === "SUCCEEDED" &&
            run.datasetVersionId === dataset.currentPublishedVersionId,
        );
        const runDetail = successfulRun
          ? await datasetAPI.getDAGRun(dataset.id, successfulRun.id)
          : undefined;
        if (cancelled) return;
        const materialization =
          runDetail?.materialization?.status === "ACTIVE"
            ? runDetail.materialization
            : undefined;
        const issue = !fields.length
          ? "发布版本没有可建模字段"
          : !grainKeys.length
            ? "数据集尚未声明输出粒度键，无法证明业务粒度"
            : !timeFields.length
              ? "数据集没有日期或时间字段，无法绑定时间合同"
              : !materialization
                ? "当前发布版本还没有成功的物化收据"
                : undefined;
        setBuilderContext({
          loading: false,
          version,
          fields,
          grainKeys,
          timeFields,
          measureFields,
          dimensionFields,
          joins: datasetJoins(version),
          materialization,
          issue,
        });
        setDraft((current) =>
          current.datasetId !== dataset.id
            ? current
            : {
                ...current,
                grain: outputGrain.description || current.grain,
                grainKeys: current.grainKeys.filter((key) =>
                  grainKeys.includes(key),
                ).length
                  ? current.grainKeys.filter((key) => grainKeys.includes(key))
                  : grainKeys,
                primaryTimeFieldId: timeFields.some(
                  (field) => field.code === current.primaryTimeFieldId,
                )
                  ? current.primaryTimeFieldId
                  : timeFields.some(
                        (field) => field.code === outputGrain.timeField,
                      )
                    ? outputGrain.timeField
                    : (timeFields[0]?.code ?? ""),
              },
        );
      } catch (cause) {
        if (!cancelled)
          setBuilderContext({
            ...emptyBuilderContext,
            issue:
              cause instanceof Error ? cause.message : "数据集合同读取失败",
          });
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
  }, [builderOpen, designSnapshot, draft.datasetId, datasets]);

  const allObjects = useMemo(
    () => [
      ...objects.models,
      ...objects.metrics,
      ...objects.dimensions,
      ...objects.knowledge,
      ...qualityRules,
    ],
    [objects, qualityRules],
  );
  const certifiedCount = allObjects.filter(
    (item) => item.status === "CERTIFIED" || item.status === "ACTIVE",
  ).length;
  const draftCount = allObjects.filter(
    (item) => item.status === "DRAFT",
  ).length;
  const activeRelease = releases.find((item) => item.status === "ACTIVE");
  const currentItems = useMemo(
    () =>
      activeTab === "releases" || activeTab === "operations"
        ? []
        : objects[activeTab],
    [activeTab, objects],
  );
  const filtered = useMemo(
    () =>
      currentItems.filter((item) => {
        const content =
          `${objectTitle(item)} ${item.code ?? ""} ${item.description ?? item.definition ?? ""}`.toLocaleLowerCase();
        return (
          (!keyword.trim() ||
            content.includes(keyword.trim().toLocaleLowerCase())) &&
          (statusFilter === "ALL" || item.status === statusFilter)
        );
      }),
    [currentItems, keyword, statusFilter],
  );
  const eligibleDatasets = designSnapshot
    ? datasets
    : datasets.filter((item) => item.layer === "DWS" || item.layer === "ADS");

  const updateDraft = (patch: Partial<BuilderDraft>) =>
    setDraft((current) => ({ ...current, ...patch }));
  const openBuilder = (candidate?: unknown) => {
    const model =
      candidate && typeof candidate === "object" && "id" in candidate
        ? (candidate as SemanticObject)
        : undefined;
    const grainContract = recordValue(model?.grainContract);
    const contractKeys = Array.isArray(grainContract.keys)
      ? grainContract.keys.filter(
          (item): item is string => typeof item === "string",
        )
      : [];
    if (model) {
      setEditingModel(model);
      setDraft({
        code: model.code ?? "",
        name: model.name ?? "",
        description: model.description ?? "",
        datasetId: model.datasetId ?? "",
        timeContractVersionId: model.timeContractVersionId ?? "",
        grain: textValue(
          grainContract.description,
          `每行代表一条${model.name ?? "业务实体"}记录`,
        ),
        grainKeys: contractKeys,
        primaryTimeFieldId: model.primaryTimeFieldId ?? "",
        entityName: model.name ?? "",
      });
      setBuilderContext(
        designSnapshot ? snapshotBuilderContext : emptyBuilderContext,
      );
      setBuilderFailure(null);
      setBuilderStep(1);
      setBuilderOpen(true);
      return;
    }
    const first = eligibleDatasets[0];
    setEditingModel(null);
    setBuilderFailure(null);
    setDraft({
      ...initialDraft,
      datasetId: first?.id ?? "",
      code: first ? `${first.code}_semantic` : "",
      name: first ? `${first.name}语义模型` : "",
      description: first
        ? `以${first.name}为唯一数据底座，统一其可分析字段与时间口径。`
        : "",
      entityName: first?.name ?? "",
      grain: first ? `每行代表一条${first.name}记录` : "",
      grainKeys: [],
      primaryTimeFieldId: "",
      timeContractVersionId: timeContracts[0]?.id ?? "",
    });
    setBuilderContext(
      designSnapshot ? snapshotBuilderContext : emptyBuilderContext,
    );
    setBuilderStep(1);
    setBuilderOpen(true);
  };

  const builderIssue = () => {
    if (!draft.datasetId) return "请选择一个已发布且已物化的 DWS 或 ADS 数据集";
    if (!draft.code.trim() || !draft.name.trim()) return "请填写模型编码和名称";
    if (builderContext.loading) return "正在读取数据集合同，请稍候";
    if (builderContext.issue) return builderContext.issue;
    if (!draft.grain.trim()) return "请声明业务粒度";
    if (!draft.grainKeys.length) return "请至少选择一个真实输出字段作为粒度键";
    if (!draft.primaryTimeFieldId.trim() || !draft.timeContractVersionId)
      return "请选择主时间字段和已认证时间合同";
    return "";
  };

  const createDefaultTimeContract = async () => {
    setBusy("time-contract");
    try {
      if (designSnapshot) {
        setTimeContracts(snapshotTimeContracts);
        updateDraft({ timeContractVersionId: snapshotTimeContracts[0].id });
        return;
      }
      await semanticAPI.createTimeContract({
        code: "enterprise_calendar",
        name: "企业经营自然日历",
        timezone: "Asia/Shanghai",
        weekStart: "MONDAY",
        weekNumbering: "ISO",
        fiscalYearStartMonth: 1,
        fiscalMonthRule: "CALENDAR",
        incompletePeriodPolicy: "LAST_COMPLETE",
        comparisonAlignment: "SAME_DAY_COUNT",
        monthEndOverflowRule: "CLAMP_TO_LAST_DAY",
        supportedGrains: ["DAY", "WEEK", "MONTH", "QUARTER", "YEAR"],
        dataAvailableThroughExpr: "MATERIALIZATION_MAX_PRIMARY_TIME",
        expectedLagHours: 8,
      });
      const page = await semanticAPI.timeContracts(100);
      const items = pageItems(page);
      setTimeContracts(items);
      updateDraft({ timeContractVersionId: items[0]?.id ?? "" });
      setNotice({
        tone: "success",
        message: "当前领域的自然日历时间合同已创建并通过权威校验。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "时间合同创建失败",
      });
    } finally {
      setBusy("");
    }
  };

  const saveAndCertify = async () => {
    const issue = builderIssue();
    if (issue) {
      setNotice({ tone: "error", message: issue });
      return;
    }
    const dataset = datasets.find((item) => item.id === draft.datasetId);
    if (!dataset?.currentPublishedVersionId) {
      setNotice({ tone: "error", message: "所选数据集没有可固定的已发布版本" });
      return;
    }
    if (dataset.layer !== "DWS" && dataset.layer !== "ADS" && !designSnapshot) {
      setNotice({
        tone: "error",
        message: "语义模型只接受 DWS 或 ADS 分析层数据集",
      });
      return;
    }
    setBusy("save-model");
    setBuilderFailure(null);
    try {
      if (designSnapshot) {
        const created: SemanticObject = {
          ...editingModel,
          id: editingModel?.id ?? `model-new-${Date.now()}`,
          objectId: editingModel?.objectId ?? `model-${draft.code}`,
          versionNo: editingModel?.versionNo ?? 1,
          code: draft.code,
          name: draft.name,
          description: draft.description,
          status: "CERTIFIED",
          layer: dataset.layer,
          datasetId: dataset.id,
          timeContractVersionId: draft.timeContractVersionId,
          grainContract: {
            schemaVersion: "semantic-grain-v1",
            description: draft.grain.trim(),
            keys: draft.grainKeys,
          },
          primaryTimeFieldId: draft.primaryTimeFieldId,
          updatedAt: new Date().toISOString(),
          ownerId: "王敏",
        };
        setObjects((current) => ({
          ...current,
          models: [
            created,
            ...current.models.filter((item) => item.code !== created.code),
          ],
        }));
      } else {
        const [publishedVersion, runs] = await Promise.all([
          datasetAPI.getVersion(dataset.id, dataset.currentPublishedVersionId),
          datasetAPI.listDAGRuns(dataset.id, 20, 0),
        ]);
        const successfulRun = runs.items.find(
          (item) =>
            item.status === "SUCCEEDED" &&
            item.datasetVersionId === dataset.currentPublishedVersionId,
        );
        if (!successfulRun)
          throw new Error("该数据集版本尚无成功物化任务，请先完成物化交付");
        const runDetail = await datasetAPI.getDAGRun(
          dataset.id,
          successfulRun.id,
        );
        const materialization =
          runDetail.materialization?.status === "ACTIVE"
            ? runDetail.materialization
            : undefined;
        if (!materialization)
          throw new Error("该数据集版本尚无有效物化收据，请重新执行物化交付");
        const fields = semanticFields(publishedVersion);
        const fieldCodes = new Set(fields.map((field) => field.code));
        const grainKeys = draft.grainKeys.filter((key) => fieldCodes.has(key));
        if (!grainKeys.length)
          throw new Error("粒度键已不在最新发布版本中，请重新核对数据集字段");
        if (!fieldCodes.has(draft.primaryTimeFieldId))
          throw new Error("主时间字段已不在最新发布版本中，请重新选择");
        const payload: Record<string, unknown> = {
          code: draft.code.trim(),
          name: draft.name.trim(),
          description: draft.description.trim(),
          datasetId: dataset.id,
          datasetVersionId: dataset.currentPublishedVersionId,
          materializationId: materialization.id,
          datasetSchemaHash: publishedVersion.dslHash,
          layer: dataset.layer,
          grainContract: {
            schemaVersion: "semantic-grain-v1",
            description: draft.grain.trim(),
            keys: grainKeys,
          },
          primaryTimeFieldId: draft.primaryTimeFieldId.trim(),
          timeContractVersionId: draft.timeContractVersionId,
        };
        const result = editingModel
          ? await semanticAPI.update("models", editingModel.id, {
              ...payload,
              objectId: editingModel.objectId,
              versionNo: editingModel.versionNo,
              expectedUpdatedAt: editingModel.updatedAt,
            })
          : await semanticAPI.create("models", payload);
        await semanticAPI.certify(
          currentDomainID(),
          [result.resourceId],
          "语义模型主链创建并通过字段、粒度、时间合同与权限校验。",
        );
        const refreshed = await semanticAPI.list("models", undefined, 200);
        setObjects((current) => ({ ...current, models: refreshed.items }));
      }
      setBuilderOpen(false);
      setActiveTab("models");
      if (!designSnapshot) navigate(semanticTabRoutes.models);
      setNotice({
        tone: "success",
        message: "语义模型已保存并完成 Owner 认证，已进入 Release 候选范围。",
      });
    } catch (cause) {
      if (
        cause instanceof RequestError &&
        cause.detail.code === "REG_VERSION_CONFLICT"
      ) {
        setBuilderFailure({
          code: cause.detail.code,
          title: "草稿已被其他协作者更新",
          message:
            "本地填写内容仍保留。请加载服务端最新草稿后重新核对，系统不会静默覆盖他人的修改。",
        });
      } else if (
        cause instanceof RequestError &&
        cause.detail.code === "REG_VALIDATION_FAILED"
      ) {
        setBuilderFailure({
          code: cause.detail.code,
          title: "Owner 认证未通过",
          message: cause.message,
        });
      } else {
        setBuilderFailure({
          code:
            cause instanceof RequestError
              ? cause.detail.code
              : "MODEL_SAVE_FAILED",
          title: "语义模型暂未保存",
          message:
            cause instanceof Error ? cause.message : "请检查依赖合同后重试。",
        });
      }
      setNotice({
        tone: "error",
        message:
          cause instanceof RequestError || cause instanceof Error
            ? cause.message
            : "语义模型保存失败",
      });
    } finally {
      setBusy("");
    }
  };

  const reloadLatestModelDraft = async () => {
    if (!editingModel) return;
    setBusy("reload-model");
    try {
      const latest = designSnapshot
        ? { ...editingModel, updatedAt: new Date().toISOString() }
        : await semanticAPI.get("models", editingModel.id);
      openBuilder(latest);
      setNotice({
        tone: "success",
        message: "已加载服务端最新草稿，请重新核对后提交认证。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "最新草稿加载失败",
      });
    } finally {
      setBusy("");
    }
  };

  const generateCoreSemanticAssets = async () => {
    const model = [...objects.models]
      .filter(
        (item) =>
          item.status === "CERTIFIED" &&
          item.datasetId &&
          item.datasetVersionId,
      )
      .sort(
        (left, right) =>
          new Date(String(right.updatedAt ?? 0)).getTime() -
          new Date(String(left.updatedAt ?? 0)).getTime(),
      )[0];
    if (!model) {
      setNotice({
        tone: "error",
        message: "请先创建并认证一个绑定 DWS / ADS 发布版本的语义模型。",
      });
      setActiveTab("models");
      if (!designSnapshot) navigate(semanticTabRoutes.models);
      return;
    }
    if (designSnapshot) {
      setObjects((current) => ({
        ...current,
        metrics: snapshotMetrics,
        dimensions: snapshotDimensions,
        knowledge: snapshotKnowledge,
      }));
      setNotice({
        tone: "success",
        message: "核心指标、维度与业务知识已从认证模型生成。",
      });
      return;
    }
    setBusy("bootstrap-assets");
    try {
      const version = await datasetAPI.getVersion(
        String(model.datasetId),
        String(model.datasetVersionId),
      );
      const fields = semanticFields(version);
      const measureFields = fields.filter(
        (field) =>
          field.role === "MEASURE" ||
          ["INTEGER", "DECIMAL"].includes(field.canonicalType),
      );
      const dimensionFields = [
        ...new Map(
          fields
            .filter(
              (field) =>
                field.role === "TIME" ||
                field.role === "DIMENSION" ||
                field.role === "ATTRIBUTE",
            )
            .map((field) => [field.code, field]),
        ).values(),
      ];
      if (!measureFields.length)
        throw new Error("认证模型的数据集没有可生成指标的数值度量字段");
      if (!dimensionFields.length)
        throw new Error("认证模型的数据集没有可生成维度的时间或分类字段");

      const [
        existingDimensionsPage,
        existingMeasuresPage,
        existingMetricsPage,
        existingMetricVersionsPage,
      ] = await Promise.all([
        semanticAPI.list("dimensions", undefined, 200),
        semanticAPI.list("measures", undefined, 200),
        semanticAPI.list("metrics", undefined, 200),
        semanticAPI.list("metric-versions", undefined, 200),
      ]);
      const existingDimensions = new Map(
        pageItems(existingDimensionsPage)
          .filter((item) => item.semanticModelVersionId === model.id)
          .map((item) => [item.code, item]),
      );
      const existingMeasures = new Map(
        pageItems(existingMeasuresPage)
          .filter((item) => item.semanticModelVersionId === model.id)
          .map((item) => [item.code, item]),
      );
      const existingMetrics = new Map(
        pageItems(existingMetricsPage).map((item) => [item.code, item]),
      );
      const existingMetricVersions = new Map(
        pageItems(existingMetricVersionsPage)
          .filter((item) => item.semanticModelVersionId === model.id)
          .map((item) => [String(item.objectId ?? item.metricId ?? ""), item]),
      );

      const dimensionResults = await Promise.all(
        dimensionFields.map((field) =>
          existingDimensions.has(field.code)
            ? Promise.resolve({
                resourceId: existingDimensions.get(field.code)!.id,
              })
            : semanticAPI.create(
                "dimensions",
                {
                  semanticModelVersionId: model.id,
                  logicalFieldId: field.code,
                  code: field.code,
                  name: field.name,
                  description: `${field.name}来自${model.name ?? model.code ?? "当前语义模型"}的认证数据合同。`,
                  kind:
                    field.role === "TIME" ||
                    ["DATE", "DATETIME"].includes(field.canonicalType)
                      ? "TIME"
                      : "CATEGORICAL",
                  sensitivity: fieldSensitivity(field),
                  memberIndexPolicy: ["CONFIDENTIAL", "RESTRICTED"].includes(
                    fieldSensitivity(field),
                  )
                    ? "EXACT_ONLY"
                    : "FULL",
                  highCardinality: false,
                },
                semanticBootstrapKey(model.id, "dimension", field.code),
              ),
        ),
      );

      const measureResults = await Promise.all(
        measureFields.map((field) => {
          const existing = existingMeasures.get(field.code);
          if (existing) return Promise.resolve({ resourceId: existing.id });
          const contract = fieldMetricContract(field);
          return semanticAPI.create(
            "measures",
            {
              semanticModelVersionId: model.id,
              code: field.code,
              name: field.name,
              description: `${field.name}的基础度量，直接引用发布数据集稳定字段 ${field.code}。`,
              formulaAst: { type: "FIELD_REF", fieldId: field.code },
              aggregation: "SUM",
              additivity: "FULLY_ADDITIVE",
              nonAdditiveDimensions: [],
              dataType:
                field.canonicalType === "INTEGER" ? "INTEGER" : "DECIMAL",
              unit: contract.unit,
              currency: contract.currency,
              zeroDenominatorPolicy: "NULL",
              displayPrecision: contract.displayPrecision,
            },
            semanticBootstrapKey(model.id, "measure", field.code),
          );
        }),
      );

      const metricResults = await Promise.all(
        measureFields.map(async (field, index) => {
          const contract = fieldMetricContract(field);
          const existingMetric = existingMetrics.get(field.code);
          const metric = existingMetric
            ? { resourceId: existingMetric.id }
            : await semanticAPI.create(
                "metrics",
                {
                  code: field.code,
                  name: field.name,
                  description: `${field.name}按当前模型粒度汇总，统一使用已认证时间合同。`,
                },
                semanticBootstrapKey(model.id, "metric", field.code),
              );
          const existingVersion = existingMetricVersions.get(metric.resourceId);
          if (existingVersion) return { resourceId: existingVersion.id };
          return semanticAPI.create(
            "metric-versions",
            {
              metricId: metric.resourceId,
              semanticModelVersionId: model.id,
              formulaAst: {
                type: "MEASURE_REF",
                measureVersionId: measureResults[index].resourceId,
              },
              defaultFiltersAst: { type: "TRUE" },
              unit: contract.unit,
              currency: contract.currency,
              timeGrain: "DAY",
              additivity: "FULLY_ADDITIVE",
              nonAdditiveDimensions: [],
              zeroDenominatorPolicy: "NULL",
              displayPrecision: contract.displayPrecision,
              nullPolicy: "PRESERVE",
              measureVersionIds: [measureResults[index].resourceId],
            },
            semanticBootstrapKey(model.id, "metric-version-v2", field.code),
          );
        }),
      );

      const coreIDs = new Set(
        [...dimensionResults, ...measureResults, ...metricResults].map(
          (item) => item.resourceId,
        ),
      );
      const [measurePage, metricVersionPage, dimensionPage] = await Promise.all(
        [
          semanticAPI.list("measures", undefined, 200),
          semanticAPI.list("metric-versions", undefined, 200),
          semanticAPI.list("dimensions", undefined, 200),
        ],
      );
      const coreDraftIDs = [
        ...pageItems(measurePage),
        ...pageItems(metricVersionPage),
        ...pageItems(dimensionPage),
      ]
        .filter((item) => coreIDs.has(item.id) && item.status === "DRAFT")
        .map((item) => item.id);
      if (coreDraftIDs.length) {
        await semanticAPI.certify(
          currentDomainID(),
          coreDraftIDs,
          "由认证语义模型和发布数据合同生成，已确认字段、聚合、可加性、时间与敏感策略。",
        );
      }

      const [certifiedMetricVersions, certifiedDimensions] = await Promise.all([
        semanticAPI.list("metric-versions", undefined, 200),
        semanticAPI.list("dimensions", undefined, 200),
      ]);
      const metricFieldByVersionID = new Map(
        metricResults.map((result, index) => [
          result.resourceId,
          measureFields[index],
        ]),
      );
      const dimensionFieldByVersionID = new Map(
        dimensionResults.map((result, index) => [
          result.resourceId,
          dimensionFields[index],
        ]),
      );
      const metricTargets = pageItems(certifiedMetricVersions).filter(
        (item) =>
          metricFieldByVersionID.has(item.id) && item.status === "CERTIFIED",
      );
      const dimensionTargets = pageItems(certifiedDimensions).filter(
        (item) =>
          dimensionFieldByVersionID.has(item.id) && item.status === "CERTIFIED",
      );
      if (
        metricTargets.length !== metricResults.length ||
        dimensionTargets.length !== dimensionResults.length
      ) {
        throw new Error("部分指标或维度未完成认证，已停止创建下游业务知识");
      }

      const existingCompatibilityPage = await semanticAPI.list(
        "metric-dimensions",
        undefined,
        200,
      );
      const existingCompatibilities = new Map(
        pageItems(existingCompatibilityPage).map((item) => [
          `${item.metricVersionId}:${item.dimensionVersionId}`,
          item,
        ]),
      );
      const compatibilityResults = await Promise.all(
        metricTargets.flatMap((metric) =>
          dimensionTargets.map((dimension) => {
            const pair = `${metric.id}:${dimension.id}`;
            const existing = existingCompatibilities.get(pair);
            if (existing) return Promise.resolve({ resourceId: existing.id });
            return semanticAPI.create(
              "metric-dimensions",
              {
                metricVersionId: metric.id,
                dimensionVersionId: dimension.id,
                compatible: true,
                role: "GROUP_BY",
              },
              semanticBootstrapKey(model.id, "metric-dimension", pair),
            );
          }),
        ),
      );
      const compatibilityIDs = new Set(
        compatibilityResults.map((item) => item.resourceId),
      );
      const compatibilityPage = await semanticAPI.list(
        "metric-dimensions",
        undefined,
        200,
      );
      const compatibilityDraftIDs = pageItems(compatibilityPage)
        .filter(
          (item) => compatibilityIDs.has(item.id) && item.status === "DRAFT",
        )
        .map((item) => item.id);
      if (compatibilityDraftIDs.length) {
        await semanticAPI.certify(
          currentDomainID(),
          compatibilityDraftIDs,
          "指标与维度来自同一认证语义模型，已确认可安全分组且不存在 Fanout。",
        );
      }

      const [existingTermsPage, existingBundlesPage] = await Promise.all([
        semanticAPI.list("terms", undefined, 200),
        semanticAPI.list("kpi-bundles", undefined, 200),
      ]);
      const existingTerms = new Map(
        pageItems(existingTermsPage).map((item) => [item.code, item]),
      );
      const existingBundles = new Map(
        pageItems(existingBundlesPage).map((item) => [item.code, item]),
      );
      const termResults = await Promise.all([
        ...metricTargets.map((target) => {
          const field = metricFieldByVersionID.get(target.id)!;
          const existing = existingTerms.get(`${field.code}_term`);
          if (existing) return Promise.resolve({ resourceId: existing.id });
          return semanticAPI.create(
            "terms",
            {
              term: field.name,
              termType: "METRIC",
              targetObjectType: "METRIC",
              targetVersionId: target.id,
              targetCode: field.code,
              matchMode: "EXACT",
              matchPattern: "",
              priority: 100,
              negativeContexts: [],
              applicableRoleIds: [],
              source: "MANUAL",
              code: `${field.code}_term`,
              name: `${field.name}业务术语`,
              definition: `${field.name}指当前认证模型中按日汇总的${field.name}指标。`,
              aliases: [field.code],
            },
            semanticBootstrapKey(model.id, "metric-term", field.code),
          );
        }),
        ...dimensionTargets.map((target) => {
          const field = dimensionFieldByVersionID.get(target.id)!;
          const existing = existingTerms.get(`${field.code}_term`);
          if (existing) return Promise.resolve({ resourceId: existing.id });
          return semanticAPI.create(
            "terms",
            {
              term: field.name,
              termType: "DIMENSION",
              targetObjectType: "DIMENSION",
              targetVersionId: target.id,
              targetCode: field.code,
              matchMode: "EXACT",
              matchPattern: "",
              priority: 100,
              negativeContexts: [],
              applicableRoleIds: [],
              source: "MANUAL",
              code: `${field.code}_term`,
              name: `${field.name}业务术语`,
              definition: `${field.name}指当前认证模型中的${field.role === "TIME" ? "时间" : "分析"}维度。`,
              aliases: [field.code],
            },
            semanticBootstrapKey(model.id, "dimension-term", field.code),
          );
        }),
      ]);

      const defaultDimensions = dimensionTargets
        .filter((item) => item.kind !== "TIME")
        .slice(0, 2)
        .map((item) => item.id);
      const bundleCode = `${model.code ?? "business"}_overview`;
      const existingBundle = existingBundles.get(bundleCode);
      const bundle = existingBundle
        ? { resourceId: existingBundle.id }
        : await semanticAPI.create(
            "kpi-bundles",
            {
              code: bundleCode,
              name: `${model.name ?? "业务"}经营总览`,
              items: metricTargets.slice(0, 3).map((metric, index) => ({
                metricVersionId: metric.id,
                role: index === 0 ? "HEADLINE" : "TREND",
                groupByDimensionVersionIds: defaultDimensions,
                chartType: index === 0 ? "metric-card" : "line-trend",
                order: index + 1,
              })),
              defaultDimensionVersionIds: defaultDimensions,
              defaultTimeExpression: "最近30天",
              defaultChartTypes: ["metric-card", "line-trend"],
              roleMapping: {},
              applicableQuestionPatterns: ["查看经营总览", "最近30天经营表现"],
            },
            semanticBootstrapKey(
              model.id,
              "kpi-bundle",
              String(model.code ?? model.id),
            ),
          );

      const knowledgeIDs = new Set([
        ...termResults.map((item) => item.resourceId),
        bundle.resourceId,
      ]);
      const [termPage, bundlePage] = await Promise.all([
        semanticAPI.list("terms", undefined, 200),
        semanticAPI.list("kpi-bundles", undefined, 200),
      ]);
      const knowledgeDraftIDs = [
        ...pageItems(termPage),
        ...pageItems(bundlePage),
      ]
        .filter((item) => knowledgeIDs.has(item.id) && item.status === "DRAFT")
        .map((item) => item.id);
      if (knowledgeDraftIDs.length) {
        await semanticAPI.certify(
          currentDomainID(),
          knowledgeDraftIDs,
          "业务术语与经营总览已绑定认证指标及维度，并完成歧义与适用范围检查。",
        );
      }

      const [
        metricsPage,
        metricIdentityPage,
        dimensionsPage,
        termsPage,
        bundlesPage,
        readinessResult,
      ] = await Promise.all([
        semanticAPI.list("metric-versions", undefined, 200),
        semanticAPI.list("metrics", undefined, 200),
        semanticAPI.list("dimensions", undefined, 200),
        semanticAPI.list("terms", undefined, 200),
        semanticAPI.list("kpi-bundles", undefined, 200),
        semanticAPI.readiness(currentDomainID()),
      ]);
      setObjects((current) => ({
        ...current,
        metrics: metricIdentity(
          pageItems(metricsPage),
          pageItems(metricIdentityPage),
        ),
        dimensions: pageItems(dimensionsPage),
        knowledge: [...pageItems(termsPage), ...pageItems(bundlesPage)],
      }));
      setReadiness(readinessResult);
      setNotice({
        tone: "success",
        message: `已从认证模型生成并认证 ${metricResults.length} 个指标、${dimensionResults.length} 个维度及 ${termResults.length + 1} 个业务知识对象。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message:
          cause instanceof RequestError || cause instanceof Error
            ? cause.message
            : "核心语义资产生成失败",
      });
    } finally {
      setBusy("");
    }
  };

  const createReleaseCandidate = async () => {
    setBusy("compose-release");
    try {
      const stamp = new Intl.DateTimeFormat("en-CA", {
        year: "numeric",
        month: "2-digit",
        day: "2-digit",
      })
        .format(new Date())
        .replaceAll("-", ".");
      // RC 序号是该自然日内的单调版本号，不能只统计仍为 DRAFT 的
      // Release：首个候选激活后再次创建会与 rc1 冲突，造成发布链路卡死。
      const versionPrefix = `enterprise-ops/${stamp}-rc`;
      const nextRC =
        releases.reduce((maximum, item) => {
          if (!item.semanticVersion.startsWith(versionPrefix)) return maximum;
          const parsed = Number(item.semanticVersion.slice(versionPrefix.length));
          return Number.isInteger(parsed) ? Math.max(maximum, parsed) : maximum;
        }, 0) + 1;
      const semanticVersion = `${versionPrefix}${nextRC}`;
      if (designSnapshot) {
        const created: ReleaseCatalogItem = {
          id: `release-${Date.now()}`,
          semanticVersion,
          contentHash: "c".repeat(64),
          status: "DRAFT",
          objectCount: certifiedCount,
          version: 1,
          readyProjectionCount: 0,
          approvalCount: 0,
          createdAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        };
        setReleases((current) => [created, ...current]);
      } else {
        await semanticAPI.composeRelease(semanticVersion);
        const page = await semanticAPI.releases(100);
        setReleases(pageItems(page));
      }
      setNotice({
        tone: "success",
        message: `${semanticVersion} 已固定当前认证对象与内容 Hash。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message:
          cause instanceof Error ? cause.message : "Release Candidate 创建失败",
      });
    } finally {
      setBusy("");
    }
  };

  const repairSemanticGovernance = async () => {
    const unconfirmed = objects.metrics.filter(
      (item) =>
        item.status === "DRAFT" &&
        !item.additivity &&
        item.additivitySuggestion,
    );
    setBusy("governance-repair");
    try {
      if (designSnapshot) {
        setObjects((current) => ({
          ...current,
          metrics: current.metrics.map((item) =>
            item.status === "DRAFT" && item.additivitySuggestion
              ? { ...item, additivity: item.additivitySuggestion }
              : item,
          ),
          dimensions: current.dimensions.map((item) =>
            item.status === "DRAFT" &&
            ["CONFIDENTIAL", "RESTRICTED"].includes(String(item.sensitivity))
              ? { ...item, memberIndexPolicy: "EXACT_ONLY" }
              : item,
          ),
        }));
        setReadiness((current) => ({
          ...current,
          confirmedCount: current.metricCount,
          unconfirmedCount: 0,
          confirmationRate: 1,
        }));
      } else {
        const groups = new Map<string, string[]>();
        unconfirmed.forEach((item) =>
          groups.set(String(item.additivitySuggestion), [
            ...(groups.get(String(item.additivitySuggestion)) ?? []),
            item.id,
          ]),
        );
        for (const [suggestion, ids] of groups)
          await semanticAPI.confirmAdditivity(ids, suggestion);
        const sensitiveDrafts = objects.dimensions.filter(
          (item) =>
            item.status === "DRAFT" &&
            ["CONFIDENTIAL", "RESTRICTED"].includes(String(item.sensitivity)) &&
            !["EXACT_ONLY", "NONE"].includes(String(item.memberIndexPolicy)) &&
            item.highCardinality !== true,
        );
        await Promise.all(
          sensitiveDrafts.map((item) =>
            semanticAPI.update("dimensions", item.id, {
              objectId: item.objectId,
              versionNo: item.versionNo,
              expectedUpdatedAt: item.updatedAt,
              semanticModelVersionId: item.semanticModelVersionId,
              logicalFieldId: item.logicalFieldId,
              code: item.code,
              name: item.name,
              description: item.description ?? "",
              kind: item.kind,
              sensitivity: item.sensitivity,
              memberIndexPolicy: "EXACT_ONLY",
              highCardinality: false,
            }),
          ),
        );
        const relationshipsPage = await semanticAPI.list(
          "relationships",
          undefined,
          200,
        );
        const unsafeRelationships = pageItems(relationshipsPage).filter(
          (item) =>
            item.status === "DRAFT" &&
            ["ONE_TO_MANY", "MANY_TO_MANY"].includes(
              String(item.cardinality),
            ) &&
            !["PRE_AGGREGATE_REQUIRED", "BRIDGE_REQUIRED", "BLOCK"].includes(
              String(item.fanoutPolicy),
            ),
        );
        await Promise.all(
          unsafeRelationships.map((item) =>
            semanticAPI.update("relationships", item.id, {
              objectId: item.objectId,
              versionNo: item.versionNo,
              expectedUpdatedAt: item.updatedAt,
              leftModelVersionId: item.leftModelVersionId,
              rightModelVersionId: item.rightModelVersionId,
              type: item.type,
              joinType: item.joinType,
              cardinality: item.cardinality,
              joinAst: item.joinAst,
              fanoutPolicy: "BLOCK",
              bridgeModelVersionId: item.bridgeModelVersionId,
            }),
          ),
        );
        const [metricsPage, metricPage, dimensionsPage, nextReadiness] =
          await Promise.all([
            semanticAPI.list("metric-versions", undefined, 200),
            semanticAPI.list("metrics", undefined, 200),
            semanticAPI.list("dimensions", undefined, 200),
            semanticAPI.readiness(currentDomainID()),
          ]);
        setObjects((current) => ({
          ...current,
          metrics: metricIdentity(
            pageItems(metricsPage),
            pageItems(metricPage),
          ),
          dimensions: pageItems(dimensionsPage),
        }));
        setReadiness(nextReadiness);
      }
      setGovernanceRepairOpen(false);
      setNotice({
        tone: "success",
        message:
          "已按各指标建议分别确认可加性，敏感维度收敛为精确匹配，未声明安全策略的 Fanout 关系已默认阻断；时间合同仍按模型逐项复核。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message:
          cause instanceof Error ? cause.message : "语义治理批量修复失败",
      });
    } finally {
      setBusy("");
    }
  };

  const openEvaluationPreparation = async (release: ReleaseCatalogItem) => {
    setEvaluationRelease(release);
    setEvaluationFile(null);
    setEvaluationImport(null);
    setCertifiedEvaluationCount(0);
    setEvaluationReview(null);
    setEvaluationReviewOffset(0);
    setEvaluationReviewComment("");
    setEvaluationReviewAcknowledged(false);
    const draftSet = evaluationSets.find(
      (item) => item.status === "DRAFT" && item.targetReleaseId === release.id,
    );
    if (draftSet && !designSnapshot) {
      try {
        setEvaluationReview(
          await semanticAPI.evaluationCases(draftSet.id, 100, 0),
        );
      } catch (cause) {
        setNotice({
          tone: "error",
          message:
            cause instanceof Error ? cause.message : "评测复核队列读取失败",
        });
      }
    }
  };

  const downloadEvaluationTemplate = async () => {
    const domainId = currentDomainID();
    if (!domainId) return;
    setBusy("evaluation-template");
    try {
      await downloadResponse(
        await semanticAPI.downloadImportTemplate(domainId),
        "askdata-eval-case-template.xlsx",
      );
      setNotice({
        tone: "success",
        message: "评测用例模板已下载，请按模板准备验证、密封与安全用例。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "评测模板下载失败",
      });
    } finally {
      setBusy("");
    }
  };

  const uploadEvaluationCases = async () => {
    const domainId = currentDomainID();
    if (!domainId || !evaluationFile) return;
    setBusy("evaluation-upload");
    try {
      const uploaded = await semanticAPI.uploadImport(domainId, evaluationFile);
      setEvaluationImport(await semanticAPI.importStatus(uploaded.importId));
      setNotice({
        tone: "success",
        message: "评测用例已上传，系统正在执行结构、依赖、业务和治理四层校验。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "评测用例上传失败",
      });
    } finally {
      setBusy("");
    }
  };

  const downloadEvaluationReport = async () => {
    if (!evaluationImport) return;
    setBusy("evaluation-report");
    try {
      await downloadResponse(
        await semanticAPI.downloadImportReport(evaluationImport.id),
        `evaluation-import-${evaluationImport.id}.xlsx`,
      );
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "校验报告下载失败",
      });
    } finally {
      setBusy("");
    }
  };

  const commitAndCertifyEvaluationCases = async () => {
    if (
      !evaluationImport ||
      evaluationImport.state !== "VALIDATED" ||
      evaluationImport.invalidRows > 0
    )
      return;
    setBusy("evaluation-commit");
    try {
      const result = await semanticAPI.commitImport(evaluationImport.id);
      const versionIDs = result.committed
        .map((item) => item.versionId ?? item.VersionID ?? "")
        .filter(Boolean);
      if (!versionIDs.length)
        throw new Error("导入已提交，但没有返回可认证的评测用例版本");
      await semanticAPI.certify(
        currentDomainID(),
        versionIDs,
        "评测用例已通过四层校验，进入独立复核与密封准备。",
      );
      setCertifiedEvaluationCount(versionIDs.length);
      setEvaluationImport(await semanticAPI.importStatus(evaluationImport.id));
      setNotice({
        tone: "success",
        message: `${versionIDs.length} 条评测用例已认证，等待独立复核与密封。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message:
          cause instanceof Error ? cause.message : "评测用例提交认证失败",
      });
    } finally {
      setBusy("");
    }
  };

  const createEvaluationReviewSet = async () => {
    if (!evaluationRelease) return;
    setBusy("evaluation-set-create");
    try {
      const created = await semanticAPI.createEvaluationSet(
        evaluationRelease.id,
        {
          name: `${evaluationRelease.semanticVersion} 密封评测集`,
          description:
            "由已认证 SEALED 用例生成，固定当前 Release 内容 Hash，待两位独立复核人签署。",
        },
      );
      const [review, catalog] = await Promise.all([
        semanticAPI.evaluationCases(created.evaluationSetId, 100, 0),
        semanticAPI.evaluationSets(100),
      ]);
      setEvaluationReview(review);
      setEvaluationReviewOffset(0);
      setEvaluationSets(pageItems(catalog));
      setNotice({
        tone: "success",
        message: `已生成 ${created.caseCount} 条受控评测用例，进入双人独立复核。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "评测复核集生成失败",
      });
    } finally {
      setBusy("");
    }
  };

  const reviewEvaluationSet = async (decision: "APPROVED" | "REJECTED") => {
    if (!evaluationReview) return;
    const comment = evaluationReviewComment.trim();
    if (!evaluationReviewAcknowledged || comment.length < 8) {
      setNotice({
        tone: "error",
        message: "请确认已逐条复核当前受控用例，并填写至少 8 个字的复核说明。",
      });
      return;
    }
    setBusy("evaluation-review");
    try {
      await semanticAPI.reviewEvaluationSet(
        evaluationReview.evaluationSetId,
        evaluationReview.items.map((item) => item.id),
        decision,
        comment,
      );
      setEvaluationReview(
        await semanticAPI.evaluationCases(
          evaluationReview.evaluationSetId,
          100,
          evaluationReviewOffset,
        ),
      );
      setEvaluationReviewComment("");
      setEvaluationReviewAcknowledged(false);
      setNotice({
        tone: decision === "APPROVED" ? "success" : "error",
        message:
          decision === "APPROVED"
            ? "当前页的独立复核收据已保存。"
            : "当前页已驳回，请修复用例后重新复核。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "独立复核提交失败",
      });
    } finally {
      setBusy("");
    }
  };

  const loadEvaluationReviewPage = async (offset: number) => {
    if (!evaluationReview) return;
    setBusy("evaluation-review-page");
    try {
      const page = await semanticAPI.evaluationCases(
        evaluationReview.evaluationSetId,
        100,
        offset,
      );
      setEvaluationReview(page);
      setEvaluationReviewOffset(offset);
      setEvaluationReviewComment("");
      setEvaluationReviewAcknowledged(false);
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "评测复核页读取失败",
      });
    } finally {
      setBusy("");
    }
  };

  const sealEvaluationSet = async () => {
    if (!evaluationReview) return;
    setBusy("evaluation-seal");
    try {
      const sealed = await semanticAPI.sealEvaluationSet(
        evaluationReview.evaluationSetId,
      );
      const catalog = await semanticAPI.evaluationSets(100);
      setEvaluationSets(pageItems(catalog));
      setEvaluationReview({
        ...evaluationReview,
        status: sealed.status,
        fullyReviewed: sealed.caseCount,
      });
      setNotice({
        tone: "success",
        message: `评测集已密封：${sealed.caseCount} 条用例、${sealed.reviewCount} 份独立复核收据。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "评测集密封失败",
      });
    } finally {
      setBusy("");
    }
  };

  const runReleaseAction = async (
    release: ReleaseCatalogItem,
    action: "project" | "retry" | "evaluate" | "approve",
  ) => {
    setBusy(`${action}:${release.id}`);
    try {
      if (action === "approve") {
        setApprovalRelease(release);
        setApprovalCount(
          releaseStates[release.id]?.approvals ?? release.approvalCount,
        );
        setApprovalComment("");
        return;
      }
      if (designSnapshot) {
        if (action === "project" || action === "retry") {
          setReleases((current) =>
            current.map((item) =>
              item.id === release.id
                ? {
                    ...item,
                    status: "READY",
                    readyProjectionCount: 4,
                    updatedAt: new Date().toISOString(),
                  }
                : item,
            ),
          );
          setReleaseStates((current) => ({
            ...current,
            [release.id]: {
              ...current[release.id],
              preflightIssues: [],
              projections: (current[release.id]?.projections ?? []).map(
                (item) => ({
                  ...item,
                  status: "READY",
                  appliedContentHash: release.contentHash,
                  errorCode: undefined,
                  hashMatched: true,
                }),
              ),
            },
          }));
          setNotice({
            tone: "success",
            message:
              "静态校验通过，Registry、Search、Graph 与 Member 四项投影 Hash 已一致。",
          });
        } else {
          setReleaseStates((current) => ({
            ...current,
            [release.id]: {
              ...current[release.id],
              gatePassed: true,
              evaluationSetId: "snapshot-evaluation-set",
              evaluationBatchId: "snapshot-evaluation-batch",
              gateReceiptHash:
                "7fd4c1e8a930b7197fd4c1e8a930b7197fd4c1e8a930b7197fd4c1e8a930b719",
              approvalDueAt: "2026-08-11T10:30:00+08:00",
              approvalSlaStatus: "OVERDUE",
              escalationLevel: 0,
            },
          }));
          setNotice({
            tone: "success",
            message: "验证集、密封集与安全集均已完成，Wilson 95% 门禁通过。",
          });
        }
        return;
      }
      if (action === "project" || action === "retry") {
        if (action === "project") {
          const result = await semanticAPI.validateProject(release.id);
          setReleaseStates((current) => ({
            ...current,
            [release.id]: {
              ...current[release.id],
              preflightIssues: result.preflight.issues as Array<{
                code: string;
                objectType?: string;
                objectVersionId?: string;
              }>,
            },
          }));
        } else await semanticAPI.retryProjections(release.id);
        const lifecycle = await semanticAPI.releaseLifecycle(release.id);
        setReleases((current) =>
          current.map((item) =>
            item.id === release.id
              ? {
                  ...item,
                  status: lifecycle.status,
                  readyProjectionCount: lifecycle.readyProjectionCount,
                }
              : item,
          ),
        );
        setReleaseStates((current) => ({
          ...current,
          [release.id]: {
            ...current[release.id],
            projections: lifecycle.projections,
            preflightIssues: [],
            stateVersion: lifecycle.releaseStateVersion,
          },
        }));
      } else {
        const evaluation = evaluationSets.find(
          (item) => item.status === "SEALED",
        );
        if (!evaluation) {
          await openEvaluationPreparation(release);
          return;
        }
        const batchId = crypto.randomUUID();
        await semanticAPI.planEvaluation(release.id, evaluation.id, batchId);
        const gate = await semanticAPI.gate(release.id, evaluation.id, batchId);
        setReleaseStates((current) => ({
          ...current,
          [release.id]: {
            ...current[release.id],
            gatePassed: gate.passed,
            evaluationSetId: evaluation.id,
            evaluationBatchId: batchId,
            gateReceiptHash: gate.receiptHash,
            gateFailures: gate.failures,
          },
        }));
        if (!gate.passed) {
          setNotice({
            tone: "error",
            message: `评测门禁未通过：${gate.failures.slice(0, 3).join("、") || "请检查评测运行事实"}`,
          });
          return;
        }
      }
      setNotice({
        tone: "success",
        message:
          action === "project"
            ? "Release 校验与投影任务已提交。"
            : action === "retry"
              ? "失败投影已重新进入可靠执行队列。"
              : "评测门禁已重新计算。",
      });
    } catch (cause) {
      if (cause instanceof RequestError && cause.detail.preflight) {
        setReleaseStates((current) => ({
          ...current,
          [release.id]: {
            ...current[release.id],
            preflightIssues: cause.detail.preflight?.issues ?? [],
          },
        }));
        setReleaseDiagnostic(release);
      }
      if (!designSnapshot && (action === "project" || action === "retry")) {
        try {
          const lifecycle = await semanticAPI.releaseLifecycle(release.id);
          setReleaseStates((current) => ({
            ...current,
            [release.id]: {
              ...current[release.id],
              projections: lifecycle.projections,
              stateVersion: lifecycle.releaseStateVersion,
            },
          }));
          setReleases((current) =>
            current.map((item) =>
              item.id === release.id
                ? {
                    ...item,
                    status: lifecycle.status,
                    readyProjectionCount: lifecycle.readyProjectionCount,
                  }
                : item,
            ),
          );
        } catch {
          /* 原始错误仍保留给用户恢复。 */
        }
      }
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "Release 操作失败",
      });
    } finally {
      setBusy("");
    }
  };

  const approveRelease = async (
    reviewRole: "SEMANTIC_OWNER" | "DATA_OWNER",
    decision: "APPROVED" | "REJECTED" = "APPROVED",
  ) => {
    if (!approvalRelease) return;
    const comment = approvalComment.trim();
    if (comment.length < 8) {
      setNotice({
        tone: "error",
        message: "请填写至少 8 个字的审批说明，明确已复核的事实。",
      });
      return;
    }
    setBusy("approve-release");
    try {
      if (!designSnapshot) {
        const state = releaseStates[approvalRelease.id];
        if (
          !state?.evaluationSetId ||
          !state.evaluationBatchId ||
          !state.gateReceiptHash
        )
          throw new Error("请先完成评测并取得门禁收据");
        if (!state.reviewReportCount) {
          await semanticAPI.generateReview(
            approvalRelease.id,
            state.evaluationSetId,
            state.evaluationBatchId,
          );
        }
        await semanticAPI.approve(approvalRelease.id, {
          evaluationSetId: state.evaluationSetId,
          evaluationBatchId: state.evaluationBatchId,
          gateReceiptHash: state.gateReceiptHash,
          reviewRole,
          decision,
          commentHash: await sha256Text(comment),
        });
        const lifecycle = await semanticAPI.releaseLifecycle(
          approvalRelease.id,
        );
        setApprovalCount(lifecycle.approvalCount);
        setReleaseStates((current) => ({
          ...current,
          [approvalRelease.id]: {
            ...current[approvalRelease.id],
            approvals: lifecycle.approvalCount,
            approvedRoles: lifecycle.approvedRoles,
            actorHasApproved: lifecycle.actorHasApproved,
            reviewReportCount: lifecycle.reviewReportCount,
            stateVersion: lifecycle.releaseStateVersion,
            rejectionCount: lifecycle.rejectionCount,
            rejectedRoles: lifecycle.rejectedRoles,
            actorApprovalRole: lifecycle.actorApprovalRole,
            approvalDueAt: lifecycle.approvalDueAt,
            approvalSlaStatus: lifecycle.approvalSlaStatus,
            escalationLevel: lifecycle.escalationLevel,
          },
        }));
        setReleases((current) =>
          current.map((item) =>
            item.id === approvalRelease.id
              ? { ...item, approvalCount: lifecycle.approvalCount }
              : item,
          ),
        );
      } else {
        const next =
          decision === "APPROVED" ? approvalCount + 1 : approvalCount;
        setApprovalCount(next);
        setReleaseStates((current) => ({
          ...current,
          [approvalRelease.id]: {
            ...current[approvalRelease.id],
            approvals: next,
            approvedRoles:
              decision === "APPROVED"
                ? [
                    ...(current[approvalRelease.id]?.approvedRoles ?? []),
                    reviewRole,
                  ]
                : current[approvalRelease.id]?.approvedRoles,
            rejectionCount:
              decision === "REJECTED"
                ? 1
                : current[approvalRelease.id]?.rejectionCount,
            rejectedRoles:
              decision === "REJECTED"
                ? [reviewRole]
                : current[approvalRelease.id]?.rejectedRoles,
            actorApprovalRole: reviewRole,
            actorHasApproved: true,
          },
        }));
      }
      setApprovalComment("");
      setNotice({
        tone: decision === "REJECTED" ? "error" : "success",
        message:
          decision === "REJECTED"
            ? "驳回意见已写入不可变审计，修复后可重置当前轮次并重新提交。"
            : approvalCount + 1 >= 2
              ? "双人审批已完成，下一步进入 Shadow 与分阶段灰度。"
              : "审批收据已保存，等待另一位独立 Owner 签署。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "审批失败",
      });
    } finally {
      setBusy("");
    }
  };

  const recoverReleaseApproval = async (
    action: "withdraw" | "reset" | "escalate",
  ) => {
    if (!approvalRelease) return;
    const state = releaseStates[approvalRelease.id];
    if (!state?.gateReceiptHash) return;
    const reason = approvalComment.trim();
    if (reason.length < 8) {
      setNotice({ tone: "error", message: "请填写至少 8 个字的原因说明。" });
      return;
    }
    setBusy(`approval-${action}`);
    try {
      if (!designSnapshot) {
        const reasonHash = await sha256Text(reason);
        if (action === "withdraw")
          await semanticAPI.withdrawApproval(
            approvalRelease.id,
            state.gateReceiptHash,
            state.actorApprovalRole ?? "",
            reasonHash,
          );
        if (action === "reset")
          await semanticAPI.resetRejectedApprovals(
            approvalRelease.id,
            state.gateReceiptHash,
            reasonHash,
          );
        if (action === "escalate")
          await semanticAPI.escalateApproval(
            approvalRelease.id,
            state.gateReceiptHash,
            reasonHash,
          );
        const lifecycle = await semanticAPI.releaseLifecycle(
          approvalRelease.id,
        );
        setApprovalCount(lifecycle.approvalCount);
        setReleaseStates((current) => ({
          ...current,
          [approvalRelease.id]: {
            ...current[approvalRelease.id],
            approvals: lifecycle.approvalCount,
            approvedRoles: lifecycle.approvedRoles,
            actorHasApproved: lifecycle.actorHasApproved,
            rejectionCount: lifecycle.rejectionCount,
            rejectedRoles: lifecycle.rejectedRoles,
            actorApprovalRole: lifecycle.actorApprovalRole,
            approvalDueAt: lifecycle.approvalDueAt,
            approvalSlaStatus: lifecycle.approvalSlaStatus,
            escalationLevel: lifecycle.escalationLevel,
          },
        }));
      } else {
        const clear = action === "withdraw" || action === "reset";
        setReleaseStates((current) => ({
          ...current,
          [approvalRelease.id]: {
            ...current[approvalRelease.id],
            approvals: clear ? 0 : current[approvalRelease.id]?.approvals,
            approvedRoles: clear
              ? []
              : current[approvalRelease.id]?.approvedRoles,
            actorHasApproved: clear
              ? false
              : current[approvalRelease.id]?.actorHasApproved,
            rejectionCount:
              action === "reset"
                ? 0
                : current[approvalRelease.id]?.rejectionCount,
            rejectedRoles:
              action === "reset"
                ? []
                : current[approvalRelease.id]?.rejectedRoles,
            actorApprovalRole: clear
              ? undefined
              : current[approvalRelease.id]?.actorApprovalRole,
            escalationLevel:
              action === "escalate"
                ? (current[approvalRelease.id]?.escalationLevel ?? 0) + 1
                : current[approvalRelease.id]?.escalationLevel,
          },
        }));
        if (clear) setApprovalCount(0);
      }
      setApprovalComment("");
      setNotice({
        tone: "success",
        message:
          action === "withdraw"
            ? "当前签署已撤回并保留历史，可重新提交。"
            : action === "reset"
              ? "驳回轮次已关闭，修复后的 Release 可重新提交双人审批。"
              : "超时审批已升级，升级级别与原因已记录。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "审批恢复操作失败",
      });
    } finally {
      setBusy("");
    }
  };

  const openReleaseOperations = async (release: ReleaseCatalogItem) => {
    setOperationRelease(release);
    setOperationReason("");
    setBusy(`operations:${release.id}`);
    try {
      if (designSnapshot) {
        const snapshotRollout = releaseStates[release.id]?.rollout;
        setReleaseOperations({
          releaseId: release.id,
          status: release.status,
          canRetire: false,
          activeReferenceCount: release.status === "SUPERSEDED" ? 2 : 0,
          blockedCode:
            release.status === "SUPERSEDED"
              ? "RELEASE_RETIRE_BLOCKED"
              : undefined,
          references:
            release.status === "SUPERSEDED"
              ? [
                  {
                    id: "snapshot-reference-1",
                    releaseId: release.id,
                    referenceType: "REPORT_VERSION",
                    referenceId: "report-1",
                    referenceName: "经营周报 V12",
                    ownerId: "张晨",
                    createdAt: "2026-08-10T18:30:00+08:00",
                  },
                  {
                    id: "snapshot-reference-2",
                    releaseId: release.id,
                    referenceType: "SAVED_QUESTION",
                    referenceId: "question-1",
                    referenceName: "华东销售趋势",
                    ownerId: "刘洋",
                    createdAt: "2026-08-10T19:15:00+08:00",
                  },
                ]
              : [],
          activeReleaseId: "snapshot-active-release",
          rolloutRequired: true,
          rollout: snapshotRollout,
          observability: snapshotRollout
            ? {
                stage: snapshotRollout.stage,
                state: snapshotRollout.state,
                stageElapsedSeconds: 1260,
                minimumDurationSeconds: 900,
                minimumSamples: snapshotRollout.stage === "SHADOW" ? 5 : 20,
                gatePassed: true,
                controlSamples: 34,
                candidateSamples: snapshotRollout.stage === "SHADOW" ? 0 : 31,
                controlAnswered: 31,
                candidateAnswered: snapshotRollout.stage === "SHADOW" ? 0 : 29,
                controlClarifications: 2,
                candidateClarifications:
                  snapshotRollout.stage === "SHADOW" ? 0 : 2,
                controlBlocked: 1,
                candidateBlocked: snapshotRollout.stage === "SHADOW" ? 0 : 0,
                controlP95LatencyMs: 1680,
                candidateP95LatencyMs:
                  snapshotRollout.stage === "SHADOW" ? 0 : 1740,
                controlAverageCostCents: 1.42,
                candidateAverageCostCents:
                  snapshotRollout.stage === "SHADOW" ? 0 : 1.39,
                shadowAlignedSamples:
                  snapshotRollout.stage === "SHADOW" ? 34 : 0,
                shadowPendingSamples: 0,
                shadowSecurityFailures: 0,
                stopRequired: false,
                stopCodes: [],
                advanceAllowed: true,
                advanceBlockedCodes: [],
              }
            : undefined,
        });
      } else {
        setReleaseOperations(await semanticAPI.releaseOperations(release.id));
      }
    } catch (cause) {
      setNotice({
        tone: "error",
        message:
          cause instanceof Error ? cause.message : "发布运行状态读取失败",
      });
      setOperationRelease(null);
    } finally {
      setBusy("");
    }
  };

  const mutateReleaseRollout = async (
    action: "start" | "advance" | "pause" | "resume" | "stop",
  ) => {
    if (!operationRelease) return;
    const reason = operationReason.trim();
    if (reason.length < 8) {
      setNotice({
        tone: "error",
        message: "请填写至少 8 个字的操作依据，系统只保存不可逆 Hash。",
      });
      return;
    }
    setBusy(`rollout-${action}`);
    try {
      const reasonHash = await sha256Text(reason);
      let rollout: ReleaseRollout;
      if (designSnapshot) {
        const current = releaseOperations?.rollout;
        const stages: ReleaseRollout["stage"][] = [
          "SHADOW",
          "CANARY_5",
          "CANARY_20",
          "CANARY_50",
          "ACCEPTED_95",
        ];
        const currentIndex = current ? stages.indexOf(current.stage) : -1;
        const stage =
          action === "start"
            ? "SHADOW"
            : action === "advance"
              ? stages[Math.min(stages.length - 1, currentIndex + 1)]
              : (current?.stage ?? "SHADOW");
        const state =
          action === "pause"
            ? "PAUSED"
            : action === "stop"
              ? "STOPPED"
              : stage === "ACCEPTED_95"
                ? "ACCEPTED"
                : "RUNNING";
        rollout = {
          id: current?.id ?? "snapshot-rollout",
          candidateReleaseId: operationRelease.id,
          controlReleaseId: current?.controlReleaseId ?? snapshotReleases[1].id,
          stage,
          state,
          canaryPercent: (
            {
              SHADOW: 0,
              CANARY_5: 5,
              CANARY_20: 20,
              CANARY_50: 50,
              ACCEPTED_95: 95,
            } as const
          )[stage],
          version: (current?.version ?? 0) + 1,
          startedAt: current?.startedAt ?? new Date().toISOString(),
          stageStartedAt: new Date().toISOString(),
          updatedAt: new Date().toISOString(),
        };
      } else if (action === "start") {
        rollout = await semanticAPI.startRollout(
          operationRelease.id,
          reasonHash,
        );
      } else {
        if (!releaseOperations?.rollout)
          throw new Error("当前 Release 尚未启动灰度");
        rollout = await semanticAPI.mutateRollout(
          operationRelease.id,
          action,
          releaseOperations.rollout.version,
          reasonHash,
        );
      }
      if (designSnapshot) {
        setReleaseOperations((current) =>
          current
            ? {
                ...current,
                rollout,
                observability: {
                  stage: rollout.stage,
                  state: rollout.state,
                  stageElapsedSeconds: 1260,
                  minimumDurationSeconds: 900,
                  minimumSamples: rollout.stage === "SHADOW" ? 5 : 20,
                  gatePassed: true,
                  controlSamples: 34,
                  candidateSamples: rollout.stage === "SHADOW" ? 0 : 31,
                  controlAnswered: 31,
                  candidateAnswered: rollout.stage === "SHADOW" ? 0 : 29,
                  controlClarifications: 2,
                  candidateClarifications: rollout.stage === "SHADOW" ? 0 : 2,
                  controlBlocked: 1,
                  candidateBlocked: 0,
                  controlP95LatencyMs: 1680,
                  candidateP95LatencyMs: rollout.stage === "SHADOW" ? 0 : 1740,
                  controlAverageCostCents: 1.42,
                  candidateAverageCostCents:
                    rollout.stage === "SHADOW" ? 0 : 1.39,
                  shadowAlignedSamples: rollout.stage === "SHADOW" ? 34 : 0,
                  shadowPendingSamples: 0,
                  shadowSecurityFailures: 0,
                  stopRequired: false,
                  stopCodes: [],
                  advanceAllowed: rollout.state === "RUNNING",
                  advanceBlockedCodes:
                    rollout.state === "RUNNING" ? [] : ["ROLLOUT_NOT_RUNNING"],
                },
              }
            : current,
        );
      } else {
        setReleaseOperations(
          await semanticAPI.releaseOperations(operationRelease.id),
        );
      }
      setReleaseStates((current) => ({
        ...current,
        [operationRelease.id]: { ...current[operationRelease.id], rollout },
      }));
      setOperationReason("");
      setNotice({
        tone: "success",
        message:
          action === "start"
            ? "Shadow 已启动，生产流量仍由当前 ACTIVE 版本响应。"
            : action === "advance"
              ? `灰度已推进至 ${rollout.canaryPercent}% 稳定用户分桶。`
              : action === "pause"
                ? "灰度已暂停，新请求停止进入候选版本。"
                : action === "resume"
                  ? "灰度已恢复，继续沿用原稳定用户分桶。"
                  : "灰度已止损并停止，生产流量全部回到控制版本。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "灰度操作失败",
      });
      if (!designSnapshot) {
        try {
          setReleaseOperations(
            await semanticAPI.releaseOperations(operationRelease.id),
          );
        } catch {
          /* 保留原始错误。 */
        }
      }
    } finally {
      setBusy("");
    }
  };

  const rollbackRelease = async () => {
    if (!operationRelease) return;
    const reason = operationReason.trim();
    if (reason.length < 8) {
      setNotice({ tone: "error", message: "请填写至少 8 个字的回滚依据。" });
      return;
    }
    setBusy("release-rollback");
    try {
      if (!designSnapshot) {
        const lifecycle = await semanticAPI.releaseLifecycle(
          operationRelease.id,
        );
        await semanticAPI.rollback(
          operationRelease.id,
          lifecycle.releaseStateVersion,
          await sha256Text(reason),
        );
      }
      setReleases((current) =>
        current.map((item) =>
          item.id === operationRelease.id
            ? {
                ...item,
                status: "ACTIVE",
                activatedAt: new Date().toISOString(),
              }
            : item.status === "ACTIVE"
              ? { ...item, status: "SUPERSEDED" }
              : item,
        ),
      );
      setOperationRelease(null);
      setReleaseOperations(null);
      setNotice({
        tone: "success",
        message: `${operationRelease.semanticVersion} 已恢复为 ACTIVE，原版本和回滚依据已写入审计。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "Release 回滚失败",
      });
    } finally {
      setBusy("");
    }
  };

  const retireRelease = async () => {
    if (!operationRelease || !releaseOperations?.canRetire) return;
    setBusy("release-retire");
    try {
      if (!designSnapshot) await semanticAPI.retire(operationRelease.id);
      setReleases((current) =>
        current.map((item) =>
          item.id === operationRelease.id
            ? { ...item, status: "RETIRED" }
            : item,
        ),
      );
      setOperationRelease(null);
      setReleaseOperations(null);
      setNotice({
        tone: "success",
        message: `${operationRelease.semanticVersion} 已安全退役，运行投影已清理。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "Release 退役失败",
      });
    } finally {
      setBusy("");
    }
  };

  const activateRelease = async () => {
    const targetRelease = operationRelease ?? approvalRelease;
    if (!targetRelease) return;
    setBusy("activate-release");
    try {
      if (!designSnapshot) {
        const state = releaseStates[targetRelease.id];
        if (!state?.evaluationSetId || !state.evaluationBatchId)
          throw new Error("缺少评测批次，无法激活");
        const lifecycle = await semanticAPI.releaseLifecycle(targetRelease.id);
        await semanticAPI.activate(
          targetRelease.id,
          state.evaluationSetId,
          state.evaluationBatchId,
          lifecycle.releaseStateVersion,
        );
      }
      setReleases((current) =>
        current.map((item) =>
          item.id === targetRelease.id
            ? {
                ...item,
                status: "ACTIVE",
                approvalCount: 2,
                activatedAt: new Date().toISOString(),
              }
            : item.status === "ACTIVE"
              ? { ...item, status: "SUPERSEDED" }
              : item,
        ),
      );
      setApprovalRelease(null);
      setOperationRelease(null);
      setReleaseOperations(null);
      setNotice({
        tone: "success",
        message: `${targetRelease.semanticVersion} 已原子激活，报告与问数将固定引用该 Release。`,
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "Release 激活失败",
      });
    } finally {
      setBusy("");
    }
  };

  const domainName = designSnapshot
    ? "企业经营"
    : (currentDomain()?.name ?? "当前领域");
  const readinessPercent = Math.round(readiness.confirmationRate * 100);
  const selectedDataset = datasets.find((item) => item.id === draft.datasetId);
  const selectedTimeContract = timeContracts.find(
    (item) => item.id === draft.timeContractVersionId,
  );
  const builderChecks = [
    Boolean(selectedDataset),
    !builderContext.loading && !builderContext.issue,
    Boolean(draft.code.trim() && draft.name.trim()),
    Boolean(draft.entityName.trim() && draft.grain.trim()),
    Boolean(draft.grainKeys.length && draft.primaryTimeFieldId),
    Boolean(selectedTimeContract),
  ];
  const builderReadiness = Math.round(
    (builderChecks.filter(Boolean).length / builderChecks.length) * 100,
  );
  const hasCertifiedModel = objects.models.some(
    (item) =>
      item.status === "CERTIFIED" && item.datasetId && item.datasetVersionId,
  );
  const sealedEvaluationSet = evaluationSets.find(
    (item) => item.status === "SEALED",
  );

  // After a bulk additivity confirmation the authoritative facts changed, so the
  // metric list and the domain readiness are both refetched from the server
  // rather than patched locally.
  // Opening a detail drawer seeds the caliber editor from the object being
  // viewed, so the textarea can never show one metric's caliber while another
  // metric is on screen.
  const openDetailObject = (item: SemanticObject) => {
    setCaliberDraft(String(item.businessDefinition ?? ""));
    setDetailObject(item);
  };

  // Saving the caliber goes through the ordinary governed metric-version update:
  // it takes the optimistic lock, recomputes the content hash server-side and is
  // refused once the version is certified.
  const saveMetricCaliber = async () => {
    if (!detailObject) return;
    setBusy("metric-caliber");
    try {
      await semanticAPI.update("metric-versions", detailObject.id, {
        objectId: detailObject.objectId,
        versionNo: detailObject.versionNo,
        expectedUpdatedAt: detailObject.updatedAt,
        metricId: detailObject.metricId,
        semanticModelVersionId: detailObject.semanticModelVersionId,
        formulaAst: detailObject.formulaAst,
        defaultFiltersAst: detailObject.defaultFiltersAst,
        unit: detailObject.unit,
        currency: detailObject.currency,
        timeGrain: detailObject.timeGrain,
        additivity: detailObject.additivity,
        semiAdditiveTimeAggregation: detailObject.semiAdditiveTimeAggregation,
        aggregationRestriction: detailObject.aggregationRestriction,
        nonAdditiveDimensions: detailObject.nonAdditiveDimensions ?? [],
        zeroDenominatorPolicy: detailObject.zeroDenominatorPolicy,
        displayPrecision: detailObject.displayPrecision,
        nullPolicy: detailObject.nullPolicy,
        incompletePeriodPolicyOverride:
          detailObject.incompletePeriodPolicyOverride,
        measureVersionIds: detailObject.measureVersionIds ?? [],
        businessDefinition: caliberDraft.trim(),
      });
      await reloadAdditivityState();
      setDetailObject(null);
      setNotice({
        tone: "success",
        message: "指标口径已保存；该版本内容 Hash 已随之更新。",
      });
    } catch (cause) {
      setNotice({
        tone: "error",
        message: cause instanceof Error ? cause.message : "指标口径保存失败",
      });
    } finally {
      setBusy("");
    }
  };

  const reloadAdditivityState = async () => {
    try {
      const [metricsPage, metricIdentityPage, readinessResult] =
        await Promise.all([
          semanticAPI.list("metric-versions", undefined, 200),
          semanticAPI.list("metrics", undefined, 200),
          semanticAPI.readiness(currentDomainID()),
        ]);
      setObjects((current) => ({
        ...current,
        metrics: metricIdentity(
          pageItems(metricsPage),
          pageItems(metricIdentityPage),
        ),
      }));
      setReadiness(readinessResult);
    } catch (cause) {
      setNotice({
        tone: "error",
        message:
          cause instanceof Error ? cause.message : "指标与可加性状态刷新失败",
      });
    }
  };

  const advanceBuilder = () => {
    const issue =
      builderStep === 1
        ? !draft.datasetId
          ? "请先选择一个 DWS 或 ADS 分析层数据集"
          : builderContext.loading
            ? "正在读取数据集合同，请稍候"
            : builderContext.issue ||
              (!draft.code.trim() || !draft.name.trim()
                ? "请填写模型编码和名称"
                : "")
        : builderStep === 2
          ? !draft.entityName.trim() || !draft.grain.trim()
            ? "请填写业务实体与粒度"
            : !draft.grainKeys.length
              ? "请至少选择一个真实输出字段作为粒度键"
              : !draft.primaryTimeFieldId
                ? "请选择真实主时间字段"
                : !draft.timeContractVersionId
                  ? "请先创建或选择已认证时间合同"
                  : ""
          : "";
    if (issue) {
      setNotice({ tone: "error", message: issue });
      return;
    }
    setBuilderStep((step) => Math.min(4, step + 1));
  };

  const approvalState = approvalRelease
    ? releaseStates[approvalRelease.id]
    : undefined;
  const approvedRoles = approvalState?.approvedRoles ?? [];
  const rejectedRoles = approvalState?.rejectedRoles ?? [];
  const actorHasApproved = Boolean(approvalState?.actorHasApproved);
  const evaluationReviewPageReviewed =
    Boolean(evaluationReview?.items.length) &&
    evaluationReview!.items.every((item) => item.actorReviewed);
  const evaluationReviewPageEnd = evaluationReview
    ? Math.min(
        evaluationReviewOffset + evaluationReview.items.length,
        evaluationReview.total,
      )
    : 0;

  const primaryAction =
    activeTab === "models" ? (
      <AppButton variant="primary" onClick={() => openBuilder()}>
        <Plus size={17} />
        创建语义模型
      </AppButton>
    ) : activeTab === "releases" ? (
      <AppButton
        variant="primary"
        disabled={busy === "compose-release"}
        onClick={() => void createReleaseCandidate()}
      >
        <Plus size={17} />
        {busy === "compose-release" ? "正在固定…" : "创建 Release"}
      </AppButton>
    ) : activeTab === "operations" ? undefined : (
      <AppButton
        variant="primary"
        disabled={!hasCertifiedModel || busy === "bootstrap-assets"}
        onClick={() => void generateCoreSemanticAssets()}
      >
        <Sparkle size={17} />
        {busy === "bootstrap-assets" ? "正在生成…" : "生成核心语义资产"}
      </AppButton>
    );

  return (
    <AppShell
      className="semantic-center-shell"
      title="语义资产"
      actions={primaryAction}
    >
      {notice && (
        <div className={`semantic-notice is-${notice.tone}`} role="status">
          {notice.tone === "success" ? (
            <CheckCircle size={19} weight="fill" />
          ) : (
            <WarningCircle size={19} weight="fill" />
          )}
          <span>{notice.message}</span>
          <button
            type="button"
            aria-label="关闭消息"
            onClick={() => setNotice(null)}
          >
            <X size={15} />
          </button>
        </div>
      )}
      <section className="semantic-stage" aria-label="语义资产建设中心">
        <header className="semantic-intro">
          <div>
            <span>数据资产化 · 第 3 段</span>
            <h2>业务语义建设与发布</h2>
            <p>
              把已发布数据集转为可认证的业务实体、指标、维度和知识，并通过
              Release 门禁服务报告与问数。
            </p>
          </div>
          <div className="semantic-domain-readiness">
            <span>{domainName}</span>
            <strong>{readinessPercent}%</strong>
            <small>指标口径准备度</small>
            {readiness.unconfirmedCount > 0 && (
              <AppButton onClick={() => setGovernanceRepairOpen(true)}>
                修复 {readiness.unconfirmedCount} 项治理冲突
              </AppButton>
            )}
          </div>
        </header>

        <ol className="semantic-flow" aria-label="语义资产主流程">
          <li className="is-complete">
            <span>
              <Check size={16} weight="bold" />
            </span>
            <div>
              <strong>数据集底座</strong>
              <small>已发布并完成物化</small>
            </div>
          </li>
          <li className="is-active">
            <span>
              <Cube size={17} />
            </span>
            <div>
              <strong>语义资产建设</strong>
              <small>模型、指标、维度与知识</small>
            </div>
          </li>
          <li>
            <span>
              <ShieldCheck size={17} />
            </span>
            <div>
              <strong>认证与 Readiness</strong>
              <small>Owner 确认关键合同</small>
            </div>
          </li>
          <li>
            <span>
              <RocketLaunch size={17} />
            </span>
            <div>
              <strong>Release ACTIVE</strong>
              <small>投影、评测、审批与灰度</small>
            </div>
          </li>
        </ol>

        <section className="semantic-summary" aria-label="语义资产概览">
          <article>
            <span className="is-blue">
              <Stack size={20} />
            </span>
            <div>
              <small>语义对象</small>
              <strong>{allObjects.length}</strong>
            </div>
            <em>当前领域</em>
          </article>
          <article>
            <span className="is-green">
              <SealCheck size={20} />
            </span>
            <div>
              <small>已认证</small>
              <strong>{certifiedCount}</strong>
            </div>
            <em>可进入 Release</em>
          </article>
          <article>
            <span className="is-orange">
              <WarningCircle size={20} />
            </span>
            <div>
              <small>待确认</small>
              <strong>{draftCount}</strong>
            </div>
            <em>草稿或口径待办</em>
          </article>
          <article>
            <span className="is-cyan">
              <RocketLaunch size={20} />
            </span>
            <div>
              <small>ACTIVE</small>
              <strong>{activeRelease ? "1" : "0"}</strong>
            </div>
            <em>{activeRelease?.semanticVersion ?? "尚未激活"}</em>
          </article>
        </section>

        <section className="semantic-workbench">
          <nav className="semantic-tabs" aria-label="语义中心分类">
            {(Object.keys(tabMeta) as SemanticTab[]).map((key) => {
              const meta = tabMeta[key];
              const Icon = meta.icon;
              const count =
                key === "releases" || key === "operations"
                  ? undefined
                  : objects[key].length;
              return (
                <button
                  className={activeTab === key ? "is-active" : ""}
                  type="button"
                  key={key}
                  onClick={() => {
                    setActiveTab(key);
                    setKeyword("");
                    setStatusFilter("ALL");
                    if (!designSnapshot) navigate(semanticTabRoutes[key]);
                  }}
                >
                  <Icon size={19} weight="duotone" />
                  <span>
                    <strong>{meta.label}</strong>
                    <small>{meta.description}</small>
                  </span>
                  {typeof count === "number" && <em>{count}</em>}
                </button>
              );
            })}
          </nav>

          <div className="semantic-content">
            <header>
              <div>
                <span>
                  {activeTab === "releases"
                    ? "生产门禁"
                    : activeTab === "operations"
                      ? "持续改进"
                      : "语义目录"}
                </span>
                <h3>
                  {activeTab === "releases"
                    ? "Release 发布中心"
                    : tabMeta[activeTab].label}
                </h3>
                <p>
                  {activeTab === "releases"
                    ? "内容 Hash、四项投影、评测门禁、双人审批与分阶段灰度全部通过后才允许激活。"
                    : activeTab === "operations"
                      ? "承接问数纠错、语义缺口和高频需求，形成可审计的修复、发布与回归闭环。"
                      : tabMeta[activeTab].description +
                        "均绑定稳定 ID 与不可变版本。"}
                </p>
              </div>
              {activeTab === "models" && (
                <AppButton variant="primary" onClick={openBuilder}>
                  <Plus size={16} />
                  新建模型
                </AppButton>
              )}
              {activeTab !== "models" &&
                activeTab !== "releases" &&
                activeTab !== "operations" && (
                  <AppButton
                    variant="primary"
                    disabled={!hasCertifiedModel || busy === "bootstrap-assets"}
                    onClick={() => void generateCoreSemanticAssets()}
                  >
                    <Sparkle size={16} />
                    {busy === "bootstrap-assets"
                      ? "正在生成…"
                      : "从认证模型生成"}
                  </AppButton>
                )}
              {activeTab === "releases" && (
                <AppButton
                  variant="primary"
                  disabled={busy === "compose-release"}
                  onClick={() => void createReleaseCandidate()}
                >
                  <Plus size={16} />
                  {busy === "compose-release" ? "正在固定…" : "创建 Release"}
                </AppButton>
              )}
            </header>

            {activeTab === "metrics" && !designSnapshot && (
              <AdditivityBacklogPanel
                readiness={readiness}
                onNotice={(tone, message) => setNotice({ tone, message })}
                onConfirmed={reloadAdditivityState}
              />
            )}
            {activeTab !== "releases" && activeTab !== "operations" && (
              <div className="semantic-toolbar">
                <label>
                  <MagnifyingGlass size={17} />
                  <input
                    type="search"
                    value={keyword}
                    onChange={(event) => setKeyword(event.target.value)}
                    placeholder="搜索名称、编码或业务定义"
                  />
                </label>
                <div>
                  <Funnel size={15} />
                  <select
                    aria-label="按状态筛选"
                    value={statusFilter}
                    onChange={(event) => setStatusFilter(event.target.value)}
                  >
                    <option value="ALL">全部状态</option>
                    <option value="DRAFT">草稿</option>
                    <option value="CERTIFIED">已认证</option>
                    <option value="ACTIVE">生效中</option>
                    <option value="DEPRECATED">已废弃</option>
                  </select>
                </div>
                <span>
                  显示 {filtered.length} / {currentItems.length}
                </span>
              </div>
            )}

            {activeTab !== "operations" && loading && (
              <div className="semantic-state">
                <Clock size={26} />
                <strong>正在加载当前领域语义资产</strong>
                <small>对象状态与 Release 目录会一起刷新</small>
              </div>
            )}
            {activeTab !== "operations" && !loading && error && (
              <div className="semantic-state is-error">
                <WarningCircle size={28} />
                <strong>语义资产暂时无法加载</strong>
                <small>{error}</small>
              </div>
            )}
            {!loading &&
              !error &&
              activeTab !== "releases" &&
              activeTab !== "operations" && (
                <div className="semantic-object-table" role="table">
                  <div className="semantic-object-head" role="row">
                    <span>语义对象</span>
                    <span>业务合同</span>
                    <span>状态 / 版本</span>
                    <span>Owner / 更新</span>
                    <span>操作</span>
                  </div>
                  {filtered.map((item) => (
                    <article role="row" key={item.id}>
                      <span className="semantic-object-name" role="cell">
                        <i>
                          <Hash size={18} />
                        </i>
                        <span>
                          <strong>{objectTitle(item)}</strong>
                          <small>
                            {item.code ?? item.term ?? item.objectId}
                          </small>
                          <p>
                            {item.description ??
                              item.definition ??
                              "已纳入当前领域语义治理。"}
                          </p>
                        </span>
                      </span>
                      <span className="semantic-contract" role="cell">
                        {activeTab === "models" && (
                          <>
                            <em>{item.layer ?? "DWD"}</em>
                            <small>
                              {item.grainContract
                                ? "粒度合同已配置"
                                : "实体与粒度合同"}
                            </small>
                          </>
                        )}
                        {activeTab === "metrics" && (
                          <>
                            <em>
                              {item.additivity ??
                                item.additivitySuggestion ??
                                "待确认"}
                            </em>
                            <small>
                              {item.timeGrain ?? "DAY"} · {item.unit ?? "—"}
                            </small>
                          </>
                        )}
                        {activeTab === "dimensions" && (
                          <>
                            <em>{item.memberIndexPolicy ?? "待选择"}</em>
                            <small>
                              {item.kind ?? "CATEGORY"} ·{" "}
                              {item.sensitivity ?? "INTERNAL"}
                            </small>
                          </>
                        )}
                        {activeTab === "knowledge" && (
                          <>
                            <em>
                              {item.aliases?.length
                                ? `${item.aliases.length} 个别名`
                                : "受治理知识"}
                            </em>
                            <small>
                              {item.term
                                ? "业务词典"
                                : item.code?.includes("bundle")
                                  ? "KPI Bundle"
                                  : "认证问法"}
                            </small>
                          </>
                        )}
                      </span>
                      <span role="cell">
                        <b
                          className={`semantic-status is-${item.status.toLocaleLowerCase()}`}
                        >
                          {statusLabel(item.status)}
                        </b>
                        <small>V{item.versionNo ?? item.version ?? 1}</small>
                      </span>
                      <span role="cell">
                        <strong>
                          {typeof item.ownerId === "string" &&
                          !item.ownerId.includes("-")
                            ? item.ownerId
                            : "领域 Owner"}
                        </strong>
                        <small>{formatTime(item.updatedAt)}</small>
                      </span>
                      <span role="cell">
                        <AppButton
                          className="semantic-row-button"
                          onClick={() => openDetailObject(item)}
                        >
                          <Eye size={15} />
                          查看
                        </AppButton>
                        {item.status === "DRAFT" && (
                          <AppButton
                            className="semantic-row-button is-primary"
                            onClick={() =>
                              activeTab === "models"
                                ? openBuilder(item)
                                : openDetailObject(item)
                            }
                          >
                            继续完善
                          </AppButton>
                        )}
                      </span>
                    </article>
                  ))}
                  {filtered.length === 0 && currentItems.length > 0 && (
                    <div className="semantic-state">
                      <MagnifyingGlass size={25} />
                      <strong>没有符合条件的语义对象</strong>
                      <small>调整搜索词或状态筛选后重试</small>
                    </div>
                  )}
                  {currentItems.length === 0 && activeTab === "models" && (
                    <div className="semantic-state">
                      <Cube size={27} />
                      <strong>从已物化的数据集建立第一个语义模型</strong>
                      <small>
                        模型会固定发布版本、粒度、主时间与时间合同。
                      </small>
                      <AppButton
                        variant="primary"
                        onClick={() => openBuilder()}
                      >
                        <Plus size={15} />
                        创建语义模型
                      </AppButton>
                    </div>
                  )}
                  {currentItems.length === 0 && activeTab !== "models" && (
                    <div className="semantic-state">
                      <Sparkle size={27} />
                      <strong>
                        从认证模型生成可用的{tabMeta[activeTab].label}
                      </strong>
                      <small>
                        {hasCertifiedModel
                          ? "系统将依据真实字段合同生成、校验并认证核心语义资产。"
                          : "请先完成语义模型的创建与认证。"}
                      </small>
                      <AppButton
                        variant="primary"
                        disabled={
                          !hasCertifiedModel || busy === "bootstrap-assets"
                        }
                        onClick={() => void generateCoreSemanticAssets()}
                      >
                        <Sparkle size={15} />
                        {busy === "bootstrap-assets"
                          ? "正在生成…"
                          : "生成并认证"}
                      </AppButton>
                    </div>
                  )}
                </div>
              )}

            {!loading && !error && activeTab === "releases" && (
              <div className="semantic-release-list">
                {releases.map((release) => {
                  const state = releaseStates[release.id];
                  const canEvaluate =
                    release.status === "READY" ||
                    release.readyProjectionCount === 4;
                  const gatePassed =
                    Boolean(state?.gatePassed) ||
                    release.status === "ACTIVE" ||
                    release.status === "SUPERSEDED";
                  const approvals = state?.approvals ?? release.approvalCount;
                  return (
                    <article
                      key={release.id}
                      className={release.status === "ACTIVE" ? "is-active" : ""}
                    >
                      <div className="semantic-release-title">
                        <span>
                          <RocketLaunch size={20} weight="duotone" />
                        </span>
                        <div>
                          <strong>{release.semanticVersion}</strong>
                          <small>
                            {release.objectCount} 个固定对象 · Hash{" "}
                            {release.contentHash.slice(0, 12)}…
                          </small>
                        </div>
                        <b
                          className={`semantic-status is-${release.status.toLocaleLowerCase()}`}
                        >
                          {statusLabel(release.status)}
                        </b>
                      </div>
                      <ol>
                        <li
                          className={
                            release.readyProjectionCount === 4 ||
                            release.status !== "DRAFT"
                              ? "is-done"
                              : ""
                          }
                        >
                          <span>
                            {release.readyProjectionCount === 4 ? (
                              <Check size={13} />
                            ) : (
                              "1"
                            )}
                          </span>
                          <div>
                            <strong>校验与投影</strong>
                            <small>
                              {release.readyProjectionCount}/4 投影一致
                            </small>
                          </div>
                        </li>
                        <li className={gatePassed ? "is-done" : ""}>
                          <span>{gatePassed ? <Check size={13} /> : "2"}</span>
                          <div>
                            <strong>评测门禁</strong>
                            <small>
                              {gatePassed
                                ? "Wilson 95% 已通过"
                                : state?.gateFailures?.length
                                  ? `待修复 ${state.gateFailures.length} 项门禁`
                                  : sealedEvaluationSet
                                    ? "密封集已就绪，等待运行"
                                    : "等待用例导入、复核与密封"}
                            </small>
                          </div>
                        </li>
                        <li className={approvals >= 2 ? "is-done" : ""}>
                          <span>
                            {approvals >= 2 ? <Check size={13} /> : "3"}
                          </span>
                          <div>
                            <strong>双人审批</strong>
                            <small>{approvals}/2 位独立审批人</small>
                          </div>
                        </li>
                        <li
                          className={
                            Boolean(state?.rollout) ||
                            release.status === "ACTIVE" ||
                            release.status === "SUPERSEDED"
                              ? "is-done"
                              : ""
                          }
                        >
                          <span>
                            {release.status === "ACTIVE" ||
                            release.status === "SUPERSEDED" ? (
                              <Check size={13} />
                            ) : (
                              "4"
                            )}
                          </span>
                          <div>
                            <strong>灰度与激活</strong>
                            <small>
                              {release.status === "ACTIVE" ||
                              release.status === "SUPERSEDED"
                                ? formatTime(release.activatedAt)
                                : state?.rollout
                                  ? `${state.rollout.canaryPercent}% · ${state.rollout.state}`
                                  : "Shadow → 95%"}
                            </small>
                          </div>
                        </li>
                      </ol>
                      <div className="semantic-release-actions">
                        {(state?.projections?.length ||
                          state?.preflightIssues?.length ||
                          state?.gateFailures?.length) && (
                          <AppButton
                            onClick={() => setReleaseDiagnostic(release)}
                          >
                            <WarningCircle size={15} />
                            诊断
                          </AppButton>
                        )}
                        {release.status === "DRAFT" && (
                          <AppButton
                            disabled={busy === `project:${release.id}`}
                            onClick={() =>
                              void runReleaseAction(release, "project")
                            }
                          >
                            <GitBranch size={15} />
                            校验并投影
                          </AppButton>
                        )}
                        {release.status === "BLOCKED" && (
                          <AppButton
                            disabled={busy === `retry:${release.id}`}
                            onClick={() =>
                              void runReleaseAction(release, "retry")
                            }
                          >
                            <GitBranch size={15} />
                            重试失败投影
                          </AppButton>
                        )}
                        {canEvaluate && !gatePassed && (
                          <AppButton
                            disabled={busy === `evaluate:${release.id}`}
                            onClick={() =>
                              void runReleaseAction(release, "evaluate")
                            }
                          >
                            <Gauge size={15} />
                            {sealedEvaluationSet
                              ? "运行评测"
                              : state?.gateFailures?.length
                                ? "重新运行评测"
                                : "准备评测"}
                          </AppButton>
                        )}
                        {gatePassed &&
                          approvals < 2 &&
                          release.status !== "ACTIVE" &&
                          release.status !== "SUPERSEDED" && (
                            <AppButton
                              variant="primary"
                              onClick={() =>
                                void runReleaseAction(release, "approve")
                              }
                            >
                              <ShieldCheck size={15} />
                              双人审批
                            </AppButton>
                          )}
                        {gatePassed &&
                          approvals >= 2 &&
                          release.status !== "ACTIVE" &&
                          release.status !== "SUPERSEDED" && (
                            <AppButton
                              variant="primary"
                              onClick={() =>
                                void openReleaseOperations(release)
                              }
                            >
                              <RocketLaunch size={15} />
                              灰度上线
                            </AppButton>
                          )}
                        {["ACTIVE", "SUPERSEDED", "RETAINED"].includes(
                          release.status,
                        ) && (
                          <AppButton
                            onClick={() => void openReleaseOperations(release)}
                          >
                            <Gauge size={15} />
                            运行治理
                          </AppButton>
                        )}
                        {(release.status === "ACTIVE" ||
                          release.status === "SUPERSEDED" ||
                          release.status === "RETAINED") && (
                          <AppButton onClick={() => setReceiptRelease(release)}>
                            <Eye size={15} />
                            查看收据
                          </AppButton>
                        )}
                      </div>
                    </article>
                  );
                })}
              </div>
            )}

            {activeTab === "operations" && (
              <>
                {!designSnapshot && (
                  <QualityRulesPanel
                    models={objects.models}
                    rules={qualityRules}
                    onChanged={setQualityRules}
                    onNotice={(tone, message) => setNotice({ tone, message })}
                  />
                )}
                {!designSnapshot && (
                  <RowAccessPoliciesPanel
                    models={objects.models}
                    policies={rowAccessPolicies}
                    onChanged={setRowAccessPolicies}
                    onNotice={(tone, message) => setNotice({ tone, message })}
                  />
                )}
                <SemanticOperationsPanel
                  releases={releases}
                  evaluationSets={evaluationSets}
                  initialTicketId={params.get("ticketId") ?? ""}
                  onNotice={(tone, message) => setNotice({ tone, message })}
                />
              </>
            )}
          </div>
        </section>
      </section>

      {builderOpen && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-builder-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="创建语义模型"
          >
            <header>
              <div>
                <span>语义模型建设</span>
                <h2>{draft.name || "新建语义模型"}</h2>
                <p>固定真实数据集版本、物化收据、字段、粒度与时间合同。</p>
              </div>
              <button
                type="button"
                aria-label="关闭创建语义模型"
                onClick={() => setBuilderOpen(false)}
              >
                <X size={20} />
              </button>
            </header>
            <div className="semantic-builder-body">
              <aside>
                <strong>建设步骤</strong>
                <ol>
                  {[
                    ["1", "选择分析层数据集", "固定发布版本与物化"],
                    ["2", "实体与粒度", "映射真实稳定字段"],
                    ["3", "字段能力核对", "识别度量与维度候选"],
                    ["4", "校验与认证", "进入 Release 候选"],
                  ].map(([index, title, note]) => (
                    <li
                      className={
                        builderStep === Number(index)
                          ? "is-active"
                          : builderStep > Number(index)
                            ? "is-done"
                            : ""
                      }
                      key={index}
                    >
                      <span>
                        {builderStep > Number(index) ? (
                          <Check size={13} />
                        ) : (
                          index
                        )}
                      </span>
                      <div>
                        <strong>{title}</strong>
                        <small>{note}</small>
                      </div>
                    </li>
                  ))}
                </ol>
                <div className="semantic-builder-rule">
                  <ShieldCheck size={18} />
                  <span>
                    <strong>权威事实门禁</strong>
                    <small>
                      字段、粒度、时间、物化和关系全部来自发布版本，服务端会再次校验。
                    </small>
                  </span>
                </div>
              </aside>
              <main>
                {builderFailure && (
                  <div className="semantic-builder-failure">
                    <WarningCircle size={21} />
                    <div>
                      <strong>{builderFailure.title}</strong>
                      <small>{builderFailure.code}</small>
                      <p>{builderFailure.message}</p>
                    </div>
                    {builderFailure.code === "REG_VERSION_CONFLICT" && (
                      <AppButton
                        disabled={busy === "reload-model"}
                        onClick={() => void reloadLatestModelDraft()}
                      >
                        {busy === "reload-model" ? "正在加载…" : "加载最新草稿"}
                      </AppButton>
                    )}
                  </div>
                )}
                {builderStep === 1 && (
                  <div className="semantic-builder-section">
                    <header>
                      <span>01 · 数据集底座</span>
                      <h3>选择已发布且已物化的分析层数据集</h3>
                      <p>
                        只接受 DWS / ADS；语义模型会固定发布版本与结构
                        Hash，不会静默跟随草稿变化。
                      </p>
                    </header>
                    {eligibleDatasets.length > 0 ? (
                      <div className="semantic-dataset-options">
                        {eligibleDatasets.map((dataset) => (
                          <button
                            className={
                              draft.datasetId === dataset.id
                                ? "is-selected"
                                : ""
                            }
                            type="button"
                            key={dataset.id}
                            onClick={() =>
                              updateDraft({
                                datasetId: dataset.id,
                                code: `${dataset.code}_semantic`,
                                name: `${dataset.name}语义模型`,
                                description: `以${dataset.name}为唯一数据底座，统一其可分析字段与时间口径。`,
                                entityName: dataset.name,
                                grain: `每行代表一条${dataset.name}记录`,
                                grainKeys: [],
                                primaryTimeFieldId: "",
                              })
                            }
                          >
                            <span>
                              <Database size={20} />
                            </span>
                            <div>
                              <strong>{dataset.name}</strong>
                              <small>
                                {dataset.code} · {dataset.layer} · V
                                {dataset.version}
                              </small>
                              <p>
                                {dataset.description ||
                                  "当前分析层数据集未补充业务描述。"}
                              </p>
                            </div>
                            {draft.datasetId === dataset.id && (
                              <CheckCircle size={19} weight="fill" />
                            )}
                          </button>
                        ))}
                      </div>
                    ) : (
                      <div className="semantic-builder-empty">
                        <Database size={28} />
                        <strong>还没有可进入语义层的 DWS / ADS 数据集</strong>
                        <p>
                          请先在数据集页面完成 ODS → DWD → DWS
                          建模、发布和物化，再回到这里建设语义。
                        </p>
                        <AppButton
                          variant="primary"
                          onClick={() => window.location.assign("/datasets")}
                        >
                          前往数据集建设
                          <ArrowRight size={15} />
                        </AppButton>
                      </div>
                    )}
                    {draft.datasetId && (
                      <div
                        className={`semantic-builder-source-state ${builderContext.issue ? "is-error" : ""}`}
                      >
                        {builderContext.loading ? (
                          <Clock size={18} />
                        ) : builderContext.issue ? (
                          <WarningCircle size={18} />
                        ) : (
                          <CheckCircle size={18} weight="fill" />
                        )}
                        <span>
                          <strong>
                            {builderContext.loading
                              ? "正在读取权威数据合同"
                              : builderContext.issue ||
                                "发布版本与物化收据均有效"}
                          </strong>
                          <small>
                            {builderContext.version
                              ? `结构 Hash ${builderContext.version.dslHash.slice(0, 12)}… · ${builderContext.fields.length} 个字段`
                              : "字段、版本和物化状态均从服务端读取"}
                          </small>
                        </span>
                      </div>
                    )}
                    <div className="semantic-form-grid">
                      <label>
                        <span>模型编码</span>
                        <input
                          value={draft.code}
                          onChange={(event) =>
                            updateDraft({ code: event.target.value })
                          }
                        />
                      </label>
                      <label>
                        <span>模型名称</span>
                        <input
                          value={draft.name}
                          onChange={(event) =>
                            updateDraft({ name: event.target.value })
                          }
                        />
                      </label>
                      <label className="is-wide">
                        <span>业务定义</span>
                        <textarea
                          value={draft.description}
                          onChange={(event) =>
                            updateDraft({ description: event.target.value })
                          }
                        />
                      </label>
                    </div>
                  </div>
                )}
                {builderStep === 2 && (
                  <div className="semantic-builder-section">
                    <header>
                      <span>02 · 实体与粒度</span>
                      <h3>声明一行代表什么，并绑定真实时间字段</h3>
                      <p>
                        粒度键、时间字段和关系都从当前发布版本读取，不允许输入不存在的字段。
                      </p>
                    </header>
                    <div className="semantic-form-grid">
                      <label>
                        <span>核心业务实体</span>
                        <input
                          value={draft.entityName}
                          onChange={(event) =>
                            updateDraft({ entityName: event.target.value })
                          }
                        />
                      </label>
                      <label>
                        <span>业务粒度</span>
                        <input
                          value={draft.grain}
                          onChange={(event) =>
                            updateDraft({ grain: event.target.value })
                          }
                        />
                      </label>
                      <label>
                        <span>主时间字段</span>
                        <select
                          value={draft.primaryTimeFieldId}
                          onChange={(event) =>
                            updateDraft({
                              primaryTimeFieldId: event.target.value,
                            })
                          }
                        >
                          <option value="">请选择真实时间字段</option>
                          {builderContext.timeFields.map((field) => (
                            <option value={field.code} key={field.id}>
                              {field.name} · {field.code}
                            </option>
                          ))}
                        </select>
                      </label>
                      <label>
                        <span>已认证时间合同</span>
                        <select
                          value={draft.timeContractVersionId}
                          onChange={(event) =>
                            updateDraft({
                              timeContractVersionId: event.target.value,
                            })
                          }
                        >
                          <option value="">请选择时间合同</option>
                          {timeContracts.map((item) => (
                            <option value={item.id} key={item.id}>
                              {item.name} V{item.versionNo} · {item.timezone}
                            </option>
                          ))}
                        </select>
                      </label>
                    </div>
                    {timeContracts.length === 0 && (
                      <div className="semantic-builder-empty is-compact">
                        <Clock size={22} />
                        <strong>当前领域还没有已认证时间合同</strong>
                        <p>
                          创建自然日历合同后，模型才能证明周期边界、数据延迟和不完整周期策略。
                        </p>
                        <AppButton
                          variant="primary"
                          disabled={busy === "time-contract"}
                          onClick={() => void createDefaultTimeContract()}
                        >
                          {busy === "time-contract"
                            ? "正在创建…"
                            : "创建领域默认时间合同"}
                        </AppButton>
                      </div>
                    )}
                    <fieldset className="semantic-grain-fields">
                      <legend>粒度键（来自已发布输出粒度）</legend>
                      {builderContext.grainKeys.map((key) => {
                        const field = builderContext.fields.find(
                          (item) => item.code === key,
                        );
                        return (
                          <label key={key}>
                            <input
                              type="checkbox"
                              checked={draft.grainKeys.includes(key)}
                              onChange={() =>
                                updateDraft({
                                  grainKeys: draft.grainKeys.includes(key)
                                    ? draft.grainKeys.filter(
                                        (item) => item !== key,
                                      )
                                    : [...draft.grainKeys, key],
                                })
                              }
                            />
                            <span>
                              <strong>{field?.name ?? key}</strong>
                              <small>{key}</small>
                            </span>
                          </label>
                        );
                      })}
                    </fieldset>
                    <div
                      className={`semantic-relation-map ${builderContext.joins.length === 0 ? "is-single" : ""}`}
                    >
                      <article>
                        <span>
                          <Database size={19} />
                        </span>
                        <div>
                          <strong>
                            {draft.entityName ||
                              selectedDataset?.name ||
                              "当前实体"}
                          </strong>
                          <small>
                            {draft.grainKeys.length
                              ? `粒度键 · ${draft.grainKeys.join(" + ")}`
                              : "尚未选择粒度键"}
                          </small>
                        </div>
                      </article>
                      {builderContext.joins.map((join, index) => (
                        <Fragment key={textValue(join.id, String(index))}>
                          <ArrowsLeftRight size={22} />
                          <article>
                            <span>
                              <Cube size={19} />
                            </span>
                            <div>
                              <strong>
                                {textValue(join.name, `关联 ${index + 1}`)}
                              </strong>
                              <small>
                                {textValue(join.cardinality, "未声明基数")} ·{" "}
                                {textValue(join.joinType, "JOIN")}
                              </small>
                            </div>
                          </article>
                        </Fragment>
                      ))}
                    </div>
                    <p className="semantic-inline-check">
                      <CheckCircle size={17} weight="fill" />
                      {builderContext.joins.length
                        ? `已读取 ${builderContext.joins.length} 条真实关系，认证时将重新检查 Fanout。`
                        : "当前发布版本未声明跨实体关系，不会虚构关联。"}
                    </p>
                  </div>
                )}
                {builderStep === 3 && (
                  <div className="semantic-builder-section">
                    <header>
                      <span>03 · 字段能力核对</span>
                      <h3>确认可用于后续指标与维度建设的字段</h3>
                      <p>
                        本步骤只展示发布版本中的真实字段；指标公式、可加性和维度成员策略将在对应中心单独治理。
                      </p>
                    </header>
                    <div className="semantic-field-groups">
                      <section>
                        <header>
                          <Gauge size={20} />
                          <span>
                            <strong>度量候选</strong>
                            <small>
                              {builderContext.measureFields.length} 个数值或
                              MEASURE 字段
                            </small>
                          </span>
                        </header>
                        <div>
                          {builderContext.measureFields.map((field) => (
                            <article key={field.id}>
                              <span>
                                <strong>{field.name}</strong>
                                <small>{field.code}</small>
                              </span>
                              <em>
                                {field.canonicalType} · {field.semanticType}
                              </em>
                            </article>
                          ))}
                          {builderContext.measureFields.length === 0 && (
                            <p>当前发布版本没有度量候选字段。</p>
                          )}
                        </div>
                      </section>
                      <section>
                        <header>
                          <CirclesThreePlus size={20} />
                          <span>
                            <strong>维度候选</strong>
                            <small>
                              {builderContext.dimensionFields.length} 个
                              DIMENSION / ATTRIBUTE 字段
                            </small>
                          </span>
                        </header>
                        <div>
                          {builderContext.dimensionFields.map((field) => (
                            <article key={field.id}>
                              <span>
                                <strong>{field.name}</strong>
                                <small>{field.code}</small>
                              </span>
                              <em>
                                {field.sensitivityLevel} · {field.semanticType}
                              </em>
                            </article>
                          ))}
                          {builderContext.dimensionFields.length === 0 && (
                            <p>当前发布版本没有维度候选字段。</p>
                          )}
                        </div>
                      </section>
                    </div>
                    <div className="semantic-suggestion">
                      <Sparkle size={19} weight="fill" />
                      <span>
                        <strong>字段识别不是指标认证</strong>
                        <small>
                          系统不会把数值字段自动冒充业务指标，也不会为字符串字段虚构层级；模型认证后请在指标中心和维度中心建立独立合同。
                        </small>
                      </span>
                    </div>
                  </div>
                )}
                {builderStep === 4 && (
                  <div className="semantic-builder-section">
                    <header>
                      <span>04 · 校验与认证</span>
                      <h3>提交前权威事实检查</h3>
                      <p>
                        认证成功后对象保持不可变，后续修改会创建新草稿版本。
                      </p>
                    </header>
                    <div className="semantic-check-list">
                      {[
                        [
                          "数据集版本与物化收据",
                          builderContext.materialization
                            ? `发布 V${builderContext.version?.versionNo ?? selectedDataset?.version ?? 1} · 物化 ${builderContext.materialization.id.slice(0, 8)}…`
                            : "未取得成功物化收据",
                        ],
                        [
                          "实体、粒度与稳定键",
                          `${draft.grain || "未填写"} · ${draft.grainKeys.join(" + ") || "未选择键"}`,
                        ],
                        [
                          "主时间与时间合同",
                          `${builderContext.timeFields.find((field) => field.code === draft.primaryTimeFieldId)?.name ?? "未选择"} · ${selectedTimeContract?.name ?? "未选择"}`,
                        ],
                        [
                          "关系与 Fanout",
                          builderContext.joins.length
                            ? `${builderContext.joins.length} 条真实关系将由服务端复核`
                            : "发布版本未声明跨实体关系",
                        ],
                        [
                          "字段合同",
                          `${builderContext.fields.length} 个真实字段，结构 Hash 已固定`,
                        ],
                        [
                          "权限与敏感策略",
                          "认证时由服务端按当前领域权限和字段敏感级复核",
                        ],
                      ].map(([title, detail]) => (
                        <article key={title}>
                          <CheckCircle size={20} weight="fill" />
                          <span>
                            <strong>{title}</strong>
                            <small>{detail}</small>
                          </span>
                        </article>
                      ))}
                    </div>
                    <div className="semantic-release-preview">
                      <div>
                        <span>将创建</span>
                        <strong>1 个认证语义模型</strong>
                        <small>只包含当前页面展示的真实合同</small>
                      </div>
                      <ArrowRight size={20} />
                      <div>
                        <span>后续建设</span>
                        <strong>指标、维度、知识与 Release</strong>
                        <small>运行时只读取 ACTIVE Release</small>
                      </div>
                    </div>
                  </div>
                )}
              </main>
              <aside className="semantic-builder-summary">
                <span>当前合同</span>
                <dl>
                  <div>
                    <dt>数据集</dt>
                    <dd>{selectedDataset?.name ?? "未选择"}</dd>
                  </div>
                  <div>
                    <dt>分析层</dt>
                    <dd>{selectedDataset?.layer ?? "—"}</dd>
                  </div>
                  <div>
                    <dt>实体</dt>
                    <dd>{draft.entityName || "未填写"}</dd>
                  </div>
                  <div>
                    <dt>粒度</dt>
                    <dd>{draft.grain || "未填写"}</dd>
                  </div>
                  <div>
                    <dt>粒度键</dt>
                    <dd>{draft.grainKeys.join(" + ") || "未选择"}</dd>
                  </div>
                  <div>
                    <dt>时间</dt>
                    <dd>{selectedTimeContract?.name ?? "未选择"}</dd>
                  </div>
                </dl>
                <div className={builderReadiness < 100 ? "is-pending" : ""}>
                  <Gauge size={18} />
                  <span>
                    <strong>本地准备度 {builderReadiness}%</strong>
                    <small>
                      {builderReadiness === 100
                        ? "提交后仍由服务端重新校验"
                        : "完成缺失合同后方可认证"}
                    </small>
                  </span>
                </div>
              </aside>
            </div>
            <footer>
              <span>步骤 {builderStep} / 4</span>
              <div>
                <AppButton
                  onClick={() =>
                    builderStep === 1
                      ? setBuilderOpen(false)
                      : setBuilderStep((step) => step - 1)
                  }
                >
                  {builderStep === 1 ? "取消" : "上一步"}
                </AppButton>
                {builderStep < 4 ? (
                  <AppButton variant="primary" onClick={advanceBuilder}>
                    下一步
                    <ArrowRight size={15} />
                  </AppButton>
                ) : (
                  <AppButton
                    variant="primary"
                    disabled={busy === "save-model" || builderReadiness < 100}
                    onClick={() => void saveAndCertify()}
                  >
                    <SealCheck size={16} />
                    {busy === "save-model" ? "正在校验…" : "保存并提交认证"}
                  </AppButton>
                )}
              </div>
            </footer>
          </section>
        </div>
      )}

      {detailObject && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-detail-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="语义对象详情"
          >
            <header>
              <div>
                <span>受治理语义对象</span>
                <h2>{objectTitle(detailObject)}</h2>
                <p>
                  {detailObject.code ??
                    detailObject.term ??
                    detailObject.objectId}
                </p>
              </div>
              <button
                type="button"
                aria-label="关闭语义对象详情"
                onClick={() => setDetailObject(null)}
              >
                <X size={19} />
              </button>
            </header>
            <div className="semantic-detail-content">
              <div className="semantic-detail-definition">
                <strong>业务定义</strong>
                <p>
                  {detailObject.description ??
                    detailObject.definition ??
                    "暂无补充说明。"}
                </p>
              </div>
              {activeTab === "metrics" && (
                <div className="semantic-detail-caliber">
                  <strong>口径说明</strong>
                  <small>
                    统计什么、排除什么、什么场景下不该用它。仅作检索与 LLM
                    上下文证据，不参与绑定；保存后会改变该指标版本的内容
                    Hash，因此只能在草稿状态修改。
                  </small>
                  {detailObject.status === "DRAFT" ? (
                    <>
                      <textarea
                        value={caliberDraft}
                        maxLength={4000}
                        placeholder="例：不含税收入，排除内部关联交易；跨期调整不回溯。"
                        onChange={(event) => setCaliberDraft(event.target.value)}
                      />
                      <div>
                        <span>{caliberDraft.length}/4000</span>
                        <AppButton
                          variant="primary"
                          disabled={
                            busy === "metric-caliber" ||
                            caliberDraft ===
                              String(detailObject.businessDefinition ?? "")
                          }
                          onClick={() => void saveMetricCaliber()}
                        >
                          {busy === "metric-caliber" ? "正在保存…" : "保存口径"}
                        </AppButton>
                      </div>
                    </>
                  ) : (
                    <p>
                      {String(detailObject.businessDefinition ?? "") ||
                        "该版本未填写口径说明；认证后不可修改，需要新建草稿版本。"}
                    </p>
                  )}
                </div>
              )}
              <dl>
                <div>
                  <dt>状态</dt>
                  <dd>
                    <b
                      className={`semantic-status is-${detailObject.status.toLocaleLowerCase()}`}
                    >
                      {statusLabel(detailObject.status)}
                    </b>
                  </dd>
                </div>
                <div>
                  <dt>固定版本</dt>
                  <dd>
                    V{detailObject.versionNo ?? detailObject.version ?? 1}
                  </dd>
                </div>
                <div>
                  <dt>内容 Hash</dt>
                  <dd>
                    {detailObject.contentHash
                      ? `${detailObject.contentHash.slice(0, 18)}…`
                      : "由服务端保存时生成"}
                  </dd>
                </div>
                <div>
                  <dt>Owner</dt>
                  <dd>
                    {typeof detailObject.ownerId === "string" &&
                    !detailObject.ownerId.includes("-")
                      ? detailObject.ownerId
                      : "领域 Owner"}
                  </dd>
                </div>
                <div>
                  <dt>最近更新</dt>
                  <dd>{formatTime(detailObject.updatedAt)}</dd>
                </div>
                <div>
                  <dt>生命周期</dt>
                  <dd>草稿 → Owner 认证 → Release 固定</dd>
                </div>
              </dl>
            </div>
            <footer>
              <AppButton onClick={() => setDetailObject(null)}>关闭</AppButton>
              {detailObject.status === "DRAFT" && (
                <AppButton
                  variant="primary"
                  onClick={() => {
                    setDetailObject(null);
                    if (activeTab === "models") openBuilder(detailObject);
                  }}
                >
                  继续完善
                </AppButton>
              )}
            </footer>
          </section>
        </div>
      )}

      {receiptRelease && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-detail-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Release 收据"
          >
            <header>
              <div>
                <span>不可变发布收据</span>
                <h2>{receiptRelease.semanticVersion}</h2>
                <p>用于报告、问数与历史回放的固定语义版本。</p>
              </div>
              <button
                type="button"
                aria-label="关闭 Release 收据"
                onClick={() => setReceiptRelease(null)}
              >
                <X size={19} />
              </button>
            </header>
            <div className="semantic-receipt-content">
              <CheckCircle size={30} weight="fill" />
              <div>
                <strong>{statusLabel(receiptRelease.status)}</strong>
                <small>内容 Hash 与四类运行投影一致</small>
              </div>
              <code>{receiptRelease.contentHash}</code>
              <dl>
                <div>
                  <dt>固定对象</dt>
                  <dd>{receiptRelease.objectCount} 个</dd>
                </div>
                <div>
                  <dt>投影收据</dt>
                  <dd>{receiptRelease.readyProjectionCount}/4</dd>
                </div>
                <div>
                  <dt>审批收据</dt>
                  <dd>{receiptRelease.approvalCount}/2</dd>
                </div>
                <div>
                  <dt>激活时间</dt>
                  <dd>{formatTime(receiptRelease.activatedAt)}</dd>
                </div>
              </dl>
            </div>
            <footer>
              <AppButton
                variant="primary"
                onClick={() => setReceiptRelease(null)}
              >
                我知道了
              </AppButton>
            </footer>
          </section>
        </div>
      )}

      {governanceRepairOpen && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-governance-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="批量修复语义治理冲突"
          >
            <header>
              <div>
                <span>Release 前置治理</span>
                <h2>批量修复语义冲突</h2>
                <p>只应用可确定的安全建议；不会自动编造业务口径或关系。</p>
              </div>
              <button
                type="button"
                aria-label="关闭批量修复语义治理冲突"
                onClick={() => setGovernanceRepairOpen(false)}
              >
                <X size={19} />
              </button>
            </header>
            <div className="semantic-governance-grid">
              <article>
                <CheckCircle size={20} />
                <div>
                  <strong>指标可加性</strong>
                  <p>
                    {readiness.unconfirmedCount}{" "}
                    个待确认指标将采用服务端已有建议，保留 Owner 审计。
                  </p>
                </div>
              </article>
              <article>
                <ShieldCheck size={20} />
                <div>
                  <strong>敏感成员索引</strong>
                  <p>
                    CONFIDENTIAL / RESTRICTED 维度统一收敛为
                    EXACT_ONLY，禁止向量全文索引。
                  </p>
                </div>
              </article>
              <article>
                <ArrowsLeftRight size={20} />
                <div>
                  <strong>Fanout 关系</strong>
                  <p>
                    一对多和多对多必须显式使用预聚合或桥接策略；缺失时仍阻断
                    Release。
                  </p>
                </div>
              </article>
              <article>
                <Clock size={20} />
                <div>
                  <strong>时间合同</strong>
                  <p>
                    缺失合同不会自动生成；页面将引导 Owner 创建并认证后重试。
                  </p>
                </div>
              </article>
            </div>
            <footer>
              <AppButton onClick={() => setGovernanceRepairOpen(false)}>
                取消
              </AppButton>
              <AppButton
                variant="primary"
                disabled={busy === "governance-repair"}
                onClick={() => void repairSemanticGovernance()}
              >
                {busy === "governance-repair" ? "正在修复…" : "应用安全修复"}
              </AppButton>
            </footer>
          </section>
        </div>
      )}

      {releaseDiagnostic && (
        <ReleaseDiagnosticDialog
          release={releaseDiagnostic}
          state={releaseStates[releaseDiagnostic.id]}
          busy={Boolean(busy)}
          onRetry={() =>
            void runReleaseAction(
              releaseDiagnostic,
              releaseDiagnostic.status === "BLOCKED" ? "retry" : "project",
            )
          }
          onEvaluate={() =>
            void runReleaseAction(releaseDiagnostic, "evaluate")
          }
          onClose={() => setReleaseDiagnostic(null)}
        />
      )}

      {evaluationRelease && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-evaluation-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="准备 Release 评测"
          >
            <header>
              <div>
                <span>Release 评测准备</span>
                <h2>{evaluationRelease.semanticVersion}</h2>
                <p>
                  先导入并认证评测用例，再完成双人独立复核与密封；系统不会用演示数据替代生产门禁。
                </p>
              </div>
              <button
                type="button"
                aria-label="关闭评测准备窗口"
                onClick={() => setEvaluationRelease(null)}
              >
                <X size={19} />
              </button>
            </header>
            <div className="semantic-evaluation-body">
              <ol className="semantic-evaluation-steps">
                <li className="is-active">
                  <span>1</span>
                  <div>
                    <strong>下载受控模板</strong>
                    <small>准备验证、密封、安全与四分片用例</small>
                  </div>
                  <AppButton
                    disabled={busy === "evaluation-template"}
                    onClick={() => void downloadEvaluationTemplate()}
                  >
                    <DownloadSimple size={15} />
                    下载模板
                  </AppButton>
                </li>
                <li className={evaluationImport ? "is-complete" : ""}>
                  <span>{evaluationImport ? <Check size={14} /> : "2"}</span>
                  <div>
                    <strong>上传用例文件</strong>
                    <small>
                      {evaluationFile?.name ??
                        "支持 xlsx、xls 或 csv，最大 50 MB"}
                    </small>
                  </div>
                  <label className="semantic-file-picker">
                    <UploadSimple size={15} />
                    选择文件
                    <input
                      type="file"
                      accept=".xlsx,.xls,.csv"
                      onChange={(event) =>
                        setEvaluationFile(event.target.files?.[0] ?? null)
                      }
                    />
                  </label>
                  <AppButton
                    variant="primary"
                    disabled={!evaluationFile || busy === "evaluation-upload"}
                    onClick={() => void uploadEvaluationCases()}
                  >
                    {busy === "evaluation-upload" ? "上传中…" : "上传校验"}
                  </AppButton>
                </li>
                <li
                  className={
                    evaluationImport?.state === "VALIDATED" ||
                    evaluationImport?.state === "COMMITTED"
                      ? "is-complete"
                      : evaluationImport?.state === "FAILED"
                        ? "is-error"
                        : ""
                  }
                >
                  <span>
                    {evaluationImport?.state === "VALIDATED" ||
                    evaluationImport?.state === "COMMITTED" ? (
                      <Check size={14} />
                    ) : (
                      "3"
                    )}
                  </span>
                  <div>
                    <strong>四层校验</strong>
                    <small>
                      {!evaluationImport
                        ? "上传后自动刷新结果"
                        : `${evaluationImport.state} · 有效 ${evaluationImport.validRows} · 无效 ${evaluationImport.invalidRows}`}
                    </small>
                  </div>
                  {evaluationImport?.invalidRows ? (
                    <AppButton onClick={() => void downloadEvaluationReport()}>
                      <FileXls size={15} />
                      下载校验报告
                    </AppButton>
                  ) : null}
                </li>
                <li
                  className={
                    certifiedEvaluationCount > 0 || evaluationReview
                      ? "is-complete"
                      : ""
                  }
                >
                  <span>
                    {certifiedEvaluationCount > 0 || evaluationReview ? (
                      <Check size={14} />
                    ) : (
                      "4"
                    )}
                  </span>
                  <div>
                    <strong>提交并认证</strong>
                    <small>
                      {certifiedEvaluationCount > 0
                        ? `${certifiedEvaluationCount} 条用例已进入独立复核队列`
                        : evaluationReview
                          ? "已恢复当前 Release 的评测复核队列"
                          : "只有全部有效的用例才能进入治理流程"}
                    </small>
                  </div>
                  {evaluationImport?.state === "VALIDATED" &&
                    evaluationImport.invalidRows === 0 &&
                    certifiedEvaluationCount === 0 && (
                      <AppButton
                        variant="primary"
                        disabled={busy === "evaluation-commit"}
                        onClick={() => void commitAndCertifyEvaluationCases()}
                      >
                        {busy === "evaluation-commit"
                          ? "提交中…"
                          : "提交并认证"}
                      </AppButton>
                    )}
                </li>
              </ol>
              {evaluationImport?.failureReason && (
                <div className="semantic-evaluation-alert is-error">
                  <WarningCircle size={18} />
                  <span>
                    <strong>校验任务失败</strong>
                    <small>{evaluationImport.failureReason}</small>
                  </span>
                </div>
              )}
              <section
                className="semantic-evaluation-governance"
                aria-label="独立复核与密封"
              >
                <header>
                  <div>
                    <span>05 · 治理复核</span>
                    <h3>双人独立复核与密封</h3>
                    <p>
                      复核按页保存，每条题面必须由两位非作者账号分别批准，密封后题面不再可见或修改。
                    </p>
                  </div>
                  {evaluationReview && (
                    <div className="semantic-evaluation-progress">
                      <strong>
                        {evaluationReview.fullyReviewed}/
                        {evaluationReview.total}
                      </strong>
                      <small>已完成双人复核</small>
                    </div>
                  )}
                </header>
                {!evaluationReview && (
                  <div className="semantic-evaluation-empty">
                    <ShieldCheck size={24} />
                    <div>
                      <strong>从已认证用例生成受控复核集</strong>
                      <p>
                        系统会固定当前 Release 内容 Hash，并校验 DIRECT 用例的
                        IR 与结果 Hash、安全拒答规则和四分片标识。
                      </p>
                    </div>
                    <AppButton
                      variant="primary"
                      disabled={busy === "evaluation-set-create"}
                      onClick={() => void createEvaluationReviewSet()}
                    >
                      {busy === "evaluation-set-create"
                        ? "正在生成…"
                        : "生成复核集"}
                    </AppButton>
                  </div>
                )}
                {evaluationReview && (
                  <>
                    <div className="semantic-evaluation-review-summary">
                      <span>
                        <strong>{evaluationReview.total}</strong>
                        <small>受控用例</small>
                      </span>
                      <span>
                        <strong>{evaluationReview.actorReviewed}</strong>
                        <small>本账号已复核</small>
                      </span>
                      <span>
                        <strong>{evaluationReview.requiredReviewers}</strong>
                        <small>每条独立复核人</small>
                      </span>
                      <span>
                        <strong>
                          {evaluationReview.status === "SEALED"
                            ? "已密封"
                            : "草稿"}
                        </strong>
                        <small>当前状态</small>
                      </span>
                    </div>
                    {evaluationReview.status === "DRAFT" && (
                      <>
                        <div className="semantic-evaluation-pagebar">
                          <span>
                            当前显示 {evaluationReviewOffset + 1}–
                            {evaluationReviewPageEnd} / {evaluationReview.total}
                          </span>
                          <div>
                            <AppButton
                              disabled={
                                evaluationReviewOffset === 0 ||
                                busy === "evaluation-review-page"
                              }
                              onClick={() =>
                                void loadEvaluationReviewPage(
                                  Math.max(0, evaluationReviewOffset - 100),
                                )
                              }
                            >
                              上一页
                            </AppButton>
                            <AppButton
                              disabled={
                                !evaluationReview.nextOffset ||
                                busy === "evaluation-review-page"
                              }
                              onClick={() =>
                                void loadEvaluationReviewPage(
                                  evaluationReview.nextOffset ??
                                    evaluationReviewOffset,
                                )
                              }
                            >
                              下一页
                            </AppButton>
                          </div>
                        </div>
                        <div className="semantic-evaluation-case-list">
                          {evaluationReview.items.map((item) => (
                            <article
                              className={
                                item.actorReviewed ? "is-reviewed" : ""
                              }
                              key={item.id}
                            >
                              <span className="semantic-evaluation-case-index">
                                {item.shardId}
                              </span>
                              <div>
                                <strong>{item.approvedQuestion}</strong>
                                <small>
                                  {item.caseKey} · {item.priority} ·{" "}
                                  {item.expectedDisposition} ·{" "}
                                  {item.securityExpectation === "NONE"
                                    ? "常规"
                                    : item.securityExpectation}
                                </small>
                              </div>
                              <em>
                                {item.independentReviewCount}/2
                                {item.actorReviewed ? " · 已复核" : ""}
                              </em>
                            </article>
                          ))}
                        </div>
                        {!evaluationReview.actorEligible && (
                          <div className="semantic-evaluation-alert is-warning">
                            <WarningCircle size={18} />
                            <span>
                              <strong>当前账号不能复核这批用例</strong>
                              <small>
                                当前账号参与了题面创建或内容变更。请由两位独立复核人分别登录完成签署，系统不会代签。
                              </small>
                            </span>
                          </div>
                        )}
                        {evaluationReview.actorEligible && (
                          <div className="semantic-evaluation-review-form">
                            <label>
                              <input
                                type="checkbox"
                                checked={evaluationReviewAcknowledged}
                                onChange={(event) =>
                                  setEvaluationReviewAcknowledged(
                                    event.target.checked,
                                  )
                                }
                              />
                              <span>
                                我已逐条核对当前页题面、预期处置、安全规则与固定
                                Hash
                              </span>
                            </label>
                            <textarea
                              value={evaluationReviewComment}
                              onChange={(event) =>
                                setEvaluationReviewComment(event.target.value)
                              }
                              placeholder="填写当前页复核结论与关注点（至少 8 个字）"
                            />
                            <div>
                              <span>
                                {evaluationReviewPageReviewed
                                  ? "本账号已签署当前页，可更新复核结论。"
                                  : "提交后将为当前页每条用例生成独立审计收据。"}
                              </span>
                              <AppButton
                                disabled={busy === "evaluation-review"}
                                onClick={() =>
                                  void reviewEvaluationSet("REJECTED")
                                }
                              >
                                驳回当前页
                              </AppButton>
                              <AppButton
                                variant="primary"
                                disabled={busy === "evaluation-review"}
                                onClick={() =>
                                  void reviewEvaluationSet("APPROVED")
                                }
                              >
                                {evaluationReviewPageReviewed
                                  ? "更新当前页批准"
                                  : "批准当前页"}
                              </AppButton>
                            </div>
                          </div>
                        )}
                        {evaluationReview.total > 0 &&
                          evaluationReview.fullyReviewed ===
                            evaluationReview.total && (
                            <div className="semantic-evaluation-seal">
                              <ShieldCheck size={21} />
                              <div>
                                <strong>全部用例已完成两位独立复核</strong>
                                <small>
                                  密封将固定题面清单与内容
                                  Hash，之后只能运行，不能修改。
                                </small>
                              </div>
                              <AppButton
                                variant="primary"
                                disabled={busy === "evaluation-seal"}
                                onClick={() => void sealEvaluationSet()}
                              >
                                {busy === "evaluation-seal"
                                  ? "正在密封…"
                                  : "密封评测集"}
                              </AppButton>
                            </div>
                          )}
                      </>
                    )}
                    {evaluationReview.status === "SEALED" && (
                      <div className="semantic-evaluation-seal is-complete">
                        <CheckCircle size={21} weight="fill" />
                        <div>
                          <strong>评测集已密封并固定 Release</strong>
                          <small>
                            关闭窗口后即可运行真实评测，并依据事实计算 Wilson
                            95% 门禁。
                          </small>
                        </div>
                      </div>
                    )}
                  </>
                )}
              </section>
            </div>
            <footer>
              <span>
                {evaluationReview?.status === "SEALED"
                  ? "评测集已就绪，可进入真实运行"
                  : "评测门禁只接受已密封且完整复核的用例集"}
              </span>
              <AppButton onClick={() => setEvaluationRelease(null)}>
                关闭
              </AppButton>
            </footer>
          </section>
        </div>
      )}

      {approvalRelease && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-approval-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Release 双人审批"
          >
            <header>
              <div>
                <span>Release 双人审批</span>
                <h2>{approvalRelease.semanticVersion}</h2>
                <p>
                  两种职责必须由不同账号独立签署；审批说明只保存不可逆 Hash。
                </p>
              </div>
              <button
                type="button"
                aria-label="关闭审批窗口"
                onClick={() => setApprovalRelease(null)}
              >
                <X size={19} />
              </button>
            </header>
            {rejectedRoles.length > 0 && (
              <div className="semantic-approval-rejection">
                <WarningCircle size={19} />
                <span>
                  <strong>当前审批轮次已驳回</strong>
                  <small>
                    {rejectedRoles.join("、")}{" "}
                    已留下不可变驳回收据。修复后重置轮次，再由两位独立 Owner
                    重新签署。
                  </small>
                </span>
                <AppButton
                  disabled={busy === "approval-reset"}
                  onClick={() => void recoverReleaseApproval("reset")}
                >
                  重置并重新提交
                </AppButton>
              </div>
            )}
            <div className="semantic-approval-grid">
              <article
                className={
                  approvedRoles.includes("SEMANTIC_OWNER") ? "is-approved" : ""
                }
              >
                <span>
                  {approvedRoles.includes("SEMANTIC_OWNER") ? (
                    <Check size={18} />
                  ) : (
                    <ShieldCheck size={18} />
                  )}
                </span>
                <div>
                  <strong>语义 Owner</strong>
                  <small>
                    {approvedRoles.includes("SEMANTIC_OWNER")
                      ? "已签署，身份与时间已写入审计"
                      : "确认指标定义、时间与适用边界"}
                  </small>
                </div>
                {!approvedRoles.includes("SEMANTIC_OWNER") &&
                  !actorHasApproved && (
                    <span className="semantic-approval-decisions">
                      <AppButton
                        disabled={busy === "approve-release"}
                        onClick={() =>
                          void approveRelease("SEMANTIC_OWNER", "REJECTED")
                        }
                      >
                        驳回
                      </AppButton>
                      <AppButton
                        variant="primary"
                        disabled={busy === "approve-release"}
                        onClick={() => void approveRelease("SEMANTIC_OWNER")}
                      >
                        批准
                      </AppButton>
                    </span>
                  )}
              </article>
              <article
                className={
                  approvedRoles.includes("DATA_OWNER") ? "is-approved" : ""
                }
              >
                <span>
                  {approvedRoles.includes("DATA_OWNER") ? (
                    <Check size={18} />
                  ) : (
                    <Database size={18} />
                  )}
                </span>
                <div>
                  <strong>数据 Owner</strong>
                  <small>
                    {approvedRoles.includes("DATA_OWNER")
                      ? "已签署，身份与时间已写入审计"
                      : "确认数据来源、粒度与质量收据"}
                  </small>
                </div>
                {!approvedRoles.includes("DATA_OWNER") && !actorHasApproved && (
                  <span className="semantic-approval-decisions">
                    <AppButton
                      disabled={busy === "approve-release"}
                      onClick={() =>
                        void approveRelease("DATA_OWNER", "REJECTED")
                      }
                    >
                      驳回
                    </AppButton>
                    <AppButton
                      variant="primary"
                      disabled={busy === "approve-release"}
                      onClick={() => void approveRelease("DATA_OWNER")}
                    >
                      批准
                    </AppButton>
                  </span>
                )}
              </article>
            </div>
            <label className="semantic-approval-comment">
              <span>审批说明</span>
              <textarea
                value={approvalComment}
                onChange={(event) => setApprovalComment(event.target.value)}
                placeholder="说明已复核的门禁事实、数据合同或业务口径（至少 8 个字）"
                disabled={approvalCount >= 2}
              />
            </label>
            {actorHasApproved && approvalCount < 2 && (
              <div className="semantic-approval-handoff">
                <ShieldCheck size={18} />
                <span>
                  <strong>当前账号已完成一次签署</strong>
                  <small>
                    为保证职责分离，请由另一位具备对应权限的 Owner
                    登录后完成剩余签署。
                  </small>
                </span>
                <AppButton
                  disabled={busy === "approval-withdraw"}
                  onClick={() => void recoverReleaseApproval("withdraw")}
                >
                  撤回我的签署
                </AppButton>
              </div>
            )}
            <ReleaseApprovalSLA
              state={approvalState}
              busy={busy}
              onEscalate={() => void recoverReleaseApproval("escalate")}
            />
            <div className="semantic-gate-receipt">
              <CheckCircle size={20} weight="fill" />
              <span>
                <strong>评测门禁收据已通过</strong>
                <small>
                  收据{" "}
                  {approvalState?.gateReceiptHash
                    ? `${approvalState.gateReceiptHash.slice(0, 16)}…`
                    : "已由服务端固定"}
                  ；页面不展示或推断密封题面。
                </small>
              </span>
            </div>
            <footer>
              <AppButton onClick={() => setApprovalRelease(null)}>
                稍后处理
              </AppButton>
              <AppButton
                variant="primary"
                disabled={approvalCount < 2}
                onClick={() => {
                  const release = approvalRelease;
                  setApprovalRelease(null);
                  void openReleaseOperations(release);
                }}
              >
                <RocketLaunch size={16} />
                进入灰度上线
              </AppButton>
            </footer>
          </section>
        </div>
      )}

      {operationRelease && (
        <div className="semantic-dialog-backdrop" role="presentation">
          <section
            className="semantic-rollout-dialog"
            role="dialog"
            aria-modal="true"
            aria-label="Release 运行治理"
          >
            <header>
              <div>
                <span>Release 运行治理</span>
                <h2>{operationRelease.semanticVersion}</h2>
                <p>
                  稳定用户分桶、可暂停灰度、快速止损与受控回滚，所有原因仅保存不可逆
                  Hash。
                </p>
              </div>
              <button
                type="button"
                aria-label="关闭运行治理"
                onClick={() => {
                  setOperationRelease(null);
                  setReleaseOperations(null);
                }}
              >
                <X size={19} />
              </button>
            </header>
            {busy === `operations:${operationRelease.id}` && (
              <div className="semantic-rollout-loading">
                <Clock size={24} />
                <strong>正在读取运行状态与引用影响</strong>
              </div>
            )}
            {releaseOperations && (
              <div className="semantic-rollout-body">
                <section className="semantic-rollout-summary">
                  <article>
                    <span>当前状态</span>
                    <strong>{statusLabel(releaseOperations.status)}</strong>
                    <small>
                      {releaseOperations.rollout
                        ? `${releaseOperations.rollout.state} · V${releaseOperations.rollout.version}`
                        : "尚未启动灰度"}
                    </small>
                  </article>
                  <article>
                    <span>候选流量</span>
                    <strong>
                      {releaseOperations.rollout?.canaryPercent ?? 0}%
                    </strong>
                    <small>按用户稳定分桶，不因刷新漂移</small>
                  </article>
                  <article>
                    <span>活跃引用</span>
                    <strong>{releaseOperations.activeReferenceCount}</strong>
                    <small>
                      {releaseOperations.canRetire
                        ? "满足安全退役条件"
                        : "退役前必须解除或到期"}
                    </small>
                  </article>
                </section>
                {!releaseOperations.rolloutRequired ? (
                  <section className="semantic-rollout-bootstrap">
                    <ShieldCheck size={20} />
                    <div>
                      <strong>本业务域尚无 ACTIVE Release，无需灰度</strong>
                      <small>
                        Shadow/Canary 需要一个正在服务的对照版本才能比较；首个
                        Release 没有对照，也没有可切分的流量。
                        评测门禁与双人审批即为其全部控制项，服务端按同一规则裁定。
                      </small>
                    </div>
                  </section>
                ) : (
                  <section className="semantic-rollout-progress">
                  <header>
                    <div>
                      <strong>分阶段上线</strong>
                      <small>
                        每次推进均需明确依据；暂停和恢复沿用原分桶盐值。
                      </small>
                    </div>
                    <em>{releaseOperations.rollout?.state ?? "NOT_STARTED"}</em>
                  </header>
                  <ol>
                    {(
                      [
                        "SHADOW",
                        "CANARY_5",
                        "CANARY_20",
                        "CANARY_50",
                        "ACCEPTED_95",
                      ] as const
                    ).map((stage, index) => {
                      const stages = [
                        "SHADOW",
                        "CANARY_5",
                        "CANARY_20",
                        "CANARY_50",
                        "ACCEPTED_95",
                      ];
                      const currentIndex = releaseOperations.rollout
                        ? stages.indexOf(releaseOperations.rollout.stage)
                        : -1;
                      return (
                        <li
                          className={
                            index < currentIndex
                              ? "is-done"
                              : index === currentIndex
                                ? "is-current"
                                : ""
                          }
                          key={stage}
                        >
                          <span>
                            {index < currentIndex ? (
                              <Check size={13} />
                            ) : (
                              index + 1
                            )}
                          </span>
                          <strong>
                            {stage === "SHADOW"
                              ? "Shadow"
                              : stage
                                  .replace("CANARY_", "")
                                  .replace("ACCEPTED_", "")}
                          </strong>
                          <small>
                            {stage === "SHADOW"
                              ? "零流量观测"
                              : `${({ CANARY_5: 5, CANARY_20: 20, CANARY_50: 50, ACCEPTED_95: 95 } as Record<string, number>)[stage]}% 用户`}
                          </small>
                        </li>
                      );
                    })}
                  </ol>
                  </section>
                )}
                {releaseOperations.observability && (
                  <section
                    className={`semantic-rollout-evidence ${releaseOperations.observability.stopRequired ? "is-danger" : releaseOperations.observability.advanceAllowed ? "is-ready" : ""}`}
                  >
                    <header>
                      <div>
                        <strong>真实运行证据</strong>
                        <small>
                          服务端直接汇总固定 Release 的运行、时延、成本与安全事件；页面不能手工填写这些结果。
                        </small>
                      </div>
                      <em>
                        {releaseOperations.observability.stopRequired
                          ? "已触发止损"
                          : releaseOperations.observability.advanceAllowed
                            ? "允许推进"
                            : "继续观测"}
                      </em>
                    </header>
                    <div className="semantic-rollout-evidence-grid">
                      <article>
                        <span>样本</span>
                        <strong>
                          {releaseOperations.observability.controlSamples} / {releaseOperations.observability.candidateSamples}
                        </strong>
                        <small>控制 / 候选 · 至少各 {releaseOperations.observability.minimumSamples}</small>
                      </article>
                      <article>
                        <span>{releaseOperations.observability.stage === "SHADOW" ? "一致性" : "回答率"}</span>
                        <strong>
                          {releaseOperations.observability.stage === "SHADOW"
                            ? rolloutRate(releaseOperations.observability.shadowAlignedSamples, releaseOperations.observability.controlSamples)
                            : `${rolloutRate(releaseOperations.observability.controlAnswered, releaseOperations.observability.controlSamples)} / ${rolloutRate(releaseOperations.observability.candidateAnswered, releaseOperations.observability.candidateSamples)}`}
                        </strong>
                        <small>
                          {releaseOperations.observability.stage === "SHADOW"
                            ? `${releaseOperations.observability.shadowAlignedSamples} 组完全对齐 · ${releaseOperations.observability.shadowPendingSamples} 组执行中`
                            : "控制 / 候选"}
                        </small>
                      </article>
                      <article>
                        <span>P95 延迟</span>
                        <strong>
                          {releaseOperations.observability.controlP95LatencyMs} / {releaseOperations.observability.candidateP95LatencyMs} ms
                        </strong>
                        <small>候选不得超过控制 2 倍</small>
                      </article>
                      <article>
                        <span>平均成本</span>
                        <strong>
                          ¥{(releaseOperations.observability.controlAverageCostCents / 100).toFixed(3)} / ¥{(releaseOperations.observability.candidateAverageCostCents / 100).toFixed(3)}
                        </strong>
                        <small>控制 / 候选单次问数</small>
                      </article>
                    </div>
                    <div className="semantic-rollout-evidence-time">
                      <Clock size={16} />
                      <span>
                        本阶段已观测 {compactDuration(releaseOperations.observability.stageElapsedSeconds)}，最短要求 {compactDuration(releaseOperations.observability.minimumDurationSeconds)}
                      </span>
                    </div>
                    {releaseOperations.observability.advanceBlockedCodes.length > 0 && (
                      <ul>
                        {releaseOperations.observability.advanceBlockedCodes.map((code) => (
                          <li key={code}>
                            <WarningCircle size={15} />
                            <span>{rolloutEvidenceLabel(code)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </section>
                )}
                <label className="semantic-rollout-reason">
                  <span>本次操作依据</span>
                  <textarea
                    value={operationReason}
                    onChange={(event) => setOperationReason(event.target.value)}
                    placeholder="填写观测结论、异常依据或回滚原因（至少 8 个字）"
                  />
                </label>
                {releaseOperations.references.length > 0 && (
                  <section className="semantic-rollout-references">
                    <header>
                      <strong>引用影响</strong>
                      <small>
                        以下对象仍固定引用此 Release，禁止直接退役。
                      </small>
                    </header>
                    {releaseOperations.references.map((reference) => (
                      <article key={reference.id}>
                        <span>
                          {reference.referenceType === "REPORT_VERSION" ? (
                            <Stack size={17} />
                          ) : (
                            <Books size={17} />
                          )}
                        </span>
                        <div>
                          <strong>{reference.referenceName}</strong>
                          <small>
                            {reference.referenceType === "REPORT_VERSION"
                              ? "报告版本"
                              : "已保存问题"}{" "}
                            · {formatTime(reference.createdAt)}
                          </small>
                        </div>
                      </article>
                    ))}
                  </section>
                )}
              </div>
            )}
            <footer>
              <span>
                {releaseOperations?.blockedCode === "RELEASE_RETIRE_BLOCKED"
                  ? "存在活跃引用，安全退役已阻断"
                  : releaseOperations?.blockedCode ===
                      "RELEASE_RETENTION_NOT_EXPIRED"
                    ? `保留期至 ${formatTime(releaseOperations.retentionUntil)}`
                    : "操作将写入不可变审计时间线"}
              </span>
              <div>
                {releaseOperations?.status === "READY" &&
                  !releaseOperations.rollout &&
                  releaseOperations.rolloutRequired && (
                    <AppButton
                      variant="primary"
                      disabled={busy === "rollout-start"}
                      onClick={() => void mutateReleaseRollout("start")}
                    >
                      <Play size={15} />
                      启动 Shadow
                    </AppButton>
                  )}
                {releaseOperations?.status === "READY" &&
                  !releaseOperations.rolloutRequired && (
                    <AppButton
                      variant="primary"
                      disabled={busy === "activate-release"}
                      onClick={() => void activateRelease()}
                    >
                      <RocketLaunch size={15} />
                      原子激活
                    </AppButton>
                  )}
                {releaseOperations?.rollout?.state === "RUNNING" && (
                  <>
                    <AppButton
                      disabled={busy === "rollout-pause"}
                      onClick={() => void mutateReleaseRollout("pause")}
                    >
                      <Pause size={15} />
                      暂停
                    </AppButton>
                    <AppButton
                      disabled={busy === "rollout-stop"}
                      onClick={() => void mutateReleaseRollout("stop")}
                    >
                      <Stop size={15} />
                      止损
                    </AppButton>
                    <AppButton
                      variant="primary"
                      disabled={
                        busy === "rollout-advance" ||
                        releaseOperations.observability?.advanceAllowed === false
                      }
                      onClick={() => void mutateReleaseRollout("advance")}
                    >
                      <ArrowRight size={15} />
                      推进阶段
                    </AppButton>
                  </>
                )}
                {releaseOperations?.rollout?.state === "PAUSED" && (
                  <>
                    <AppButton
                      disabled={busy === "rollout-stop"}
                      onClick={() => void mutateReleaseRollout("stop")}
                    >
                      <Stop size={15} />
                      终止
                    </AppButton>
                    <AppButton
                      variant="primary"
                      disabled={busy === "rollout-resume"}
                      onClick={() => void mutateReleaseRollout("resume")}
                    >
                      <Play size={15} />
                      恢复灰度
                    </AppButton>
                  </>
                )}
                {releaseOperations?.rollout?.state === "ACCEPTED" && (
                  <AppButton
                    variant="primary"
                    disabled={busy === "activate-release"}
                    onClick={() => void activateRelease()}
                  >
                    <RocketLaunch size={15} />
                    原子激活
                  </AppButton>
                )}
                {["SUPERSEDED", "RETAINED"].includes(
                  releaseOperations?.status ?? "",
                ) && (
                  <AppButton
                    disabled={busy === "release-rollback"}
                    onClick={() => void rollbackRelease()}
                  >
                    <ArrowCounterClockwise size={15} />
                    回滚到此版本
                  </AppButton>
                )}
                {releaseOperations?.canRetire && (
                  <AppButton
                    disabled={busy === "release-retire"}
                    onClick={() => void retireRelease()}
                  >
                    <Archive size={15} />
                    安全退役
                  </AppButton>
                )}
              </div>
            </footer>
          </section>
        </div>
      )}
    </AppShell>
  );
}

const projectionLabels: Record<string, string> = {
  REGISTRY: "权威注册表",
  SEARCH: "语义检索索引",
  GRAPH: "关系图谱",
  MEMBER: "维度成员索引",
  EXECUTION: "执行投影",
};

function ReleaseApprovalSLA({
  state,
  busy,
  onEscalate,
}: {
  state?: ReleaseUIState;
  busy: string;
  onEscalate: () => void;
}) {
  const status = state?.approvalSlaStatus ?? "NOT_STARTED";
  return (
    <div className={`semantic-approval-sla is-${status.toLowerCase()}`}>
      <Clock size={18} />
      <span>
        <strong>
          {status === "OVERDUE"
            ? "审批已超时"
            : status === "COMPLETED"
              ? "审批已完成"
              : "24 小时审批 SLA"}
        </strong>
        <small>
          {state?.approvalDueAt
            ? `截止 ${formatTime(state.approvalDueAt)}`
            : "门禁通过后开始计时"}
          {state?.escalationLevel ? ` · 已升级 L${state.escalationLevel}` : ""}
        </small>
      </span>
      {status === "OVERDUE" && (
        <AppButton disabled={busy === "approval-escalate"} onClick={onEscalate}>
          升级处理
        </AppButton>
      )}
    </div>
  );
}

const preflightIssueLabels: Record<
  string,
  { title: string; recovery: string }
> = {
  RELEASE_OBJECT_CONTRACT_INVALID: {
    title: "对象合同不完整",
    recovery: "返回对应语义对象，补齐必填合同并重新认证。",
  },
  RELEASE_MANIFEST_HASH_MISMATCH: {
    title: "Release 内容 Hash 漂移",
    recovery: "停止当前候选，基于最新认证对象重新创建 Release。",
  },
  RELEASE_RESTRICTED_VECTOR_POLICY: {
    title: "敏感成员索引策略不安全",
    recovery: "将敏感维度索引调整为 EXACT_ONLY 或 NONE 后重新认证。",
  },
  DIMENSION_MEMBER_INDEX_POLICY_REQUIRED: {
    title: "维度索引策略缺失",
    recovery: "为维度选择 FULL、ON_DEMAND 或 EXACT_ONLY。",
  },
  RELEASE_RELATIONSHIP_FANOUT_UNSAFE: {
    title: "Fanout 关系缺少安全策略",
    recovery: "声明预聚合、桥接或 BLOCK 策略，禁止隐式多对多放大。",
  },
  RELEASE_SENSITIVITY_CONTRACT_INVALID: {
    title: "敏感策略合同无效",
    recovery: "返回维度或成员对象，补齐敏感等级与成员索引策略。",
  },
  RELEASE_QUALITY_RULE_INVALID: {
    title: "数据质量规则无效",
    recovery: "补齐严重级别与确定性规则表达式后重新认证。",
  },
  METRIC_ADDITIVITY_UNCONFIRMED: {
    title: "指标可加性尚未确认",
    recovery: "由指标 Owner 接受建议或明确覆盖值。",
  },
  TIME_CONTRACT_REQUIRED: {
    title: "时间合同缺失",
    recovery: "绑定已认证时间合同并重新提交对象认证。",
  },
};

function ReleaseDiagnosticDialog({
  release,
  state,
  busy,
  onRetry,
  onEvaluate,
  onClose,
}: {
  release: ReleaseCatalogItem;
  state?: ReleaseUIState;
  busy: boolean;
  onRetry: () => void;
  onEvaluate: () => void;
  onClose: () => void;
}) {
  const projections = state?.projections ?? [];
  const issues = state?.preflightIssues ?? [];
  const failedProjections = projections.filter(
    (item) =>
      item.status === "FAILED" ||
      item.status === "BLOCKED" ||
      !item.hashMatched,
  );
  const gateFailures = state?.gateFailures ?? [];
  const readyCount = projections.filter(
    (item) => item.status === "READY" && item.hashMatched,
  ).length;
  const canEvaluate =
    !issues.length &&
    projections.length > 0 &&
    readyCount === projections.length;
  return (
    <div className="semantic-dialog-backdrop" role="presentation">
      <section
        className="semantic-diagnostic-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Release 阻断诊断"
      >
        <header>
          <div>
            <span>发布阻断诊断</span>
            <h2>{release.semanticVersion}</h2>
            <p>按预检、投影和评测三层定位，不跳过 Hash、安全或质量门禁。</p>
          </div>
          <button
            type="button"
            aria-label="关闭 Release 阻断诊断"
            onClick={onClose}
          >
            <X size={19} />
          </button>
        </header>
        <div className="semantic-diagnostic-body">
          <div className="semantic-diagnostic-summary">
            <article>
              <strong>{issues.length}</strong>
              <small>预检阻断</small>
            </article>
            <article>
              <strong>
                {readyCount}/{projections.length || 4}
              </strong>
              <small>投影一致</small>
            </article>
            <article>
              <strong>{failedProjections.length}</strong>
              <small>失败投影</small>
            </article>
            <article>
              <strong>{gateFailures.length}</strong>
              <small>评测未通过</small>
            </article>
          </div>
          {issues.length > 0 && (
            <section className="semantic-diagnostic-section">
              <header>
                <div>
                  <strong>静态预检</strong>
                  <small>需先修复语义对象，投影任务尚未启动。</small>
                </div>
                <em>阻断</em>
              </header>
              <div>
                {issues.map((issue, index) => {
                  const copy = preflightIssueLabels[issue.code] ?? {
                    title: issue.code,
                    recovery: "打开对应对象核对合同后重新提交。",
                  };
                  return (
                    <article
                      className="is-error"
                      key={`${issue.code}-${issue.objectVersionId ?? index}`}
                    >
                      <WarningCircle size={19} />
                      <div>
                        <strong>{copy.title}</strong>
                        <small>
                          {issue.objectType ? `${issue.objectType} · ` : ""}
                          {issue.objectVersionId
                            ? `${issue.objectVersionId.slice(0, 12)}…`
                            : "Release 清单"}
                        </small>
                        <p>{copy.recovery}</p>
                      </div>
                    </article>
                  );
                })}
              </div>
            </section>
          )}
          <section className="semantic-diagnostic-section">
            <header>
              <div>
                <strong>运行投影</strong>
                <small>
                  只有目标状态 READY 且应用 Hash 与 Release 一致才算完成。
                </small>
              </div>
              <em
                className={failedProjections.length ? "is-warning" : "is-ready"}
              >
                {failedProjections.length ? "待恢复" : "一致"}
              </em>
            </header>
            <div className="semantic-projection-list">
              {projections.length ? (
                projections.map((item) => (
                  <article
                    className={
                      item.status === "READY" && item.hashMatched
                        ? "is-ready"
                        : "is-error"
                    }
                    key={item.target}
                  >
                    {item.status === "READY" && item.hashMatched ? (
                      <CheckCircle size={19} weight="fill" />
                    ) : (
                      <WarningCircle size={19} />
                    )}
                    <div>
                      <strong>
                        {projectionLabels[item.target] ?? item.target}
                      </strong>
                      <small>
                        {item.status} · 尝试 {item.attempt}/{item.maxAttempts}
                      </small>
                      <p>
                        {item.hashMatched
                          ? `Hash 一致 · ${item.expectedContentHash.slice(0, 14)}…`
                          : `${item.errorCode ?? "等待投影"} · 期望 ${item.expectedContentHash.slice(0, 10)}…${item.appliedContentHash ? ` / 实际 ${item.appliedContentHash.slice(0, 10)}…` : ""}`}
                      </p>
                    </div>
                  </article>
                ))
              ) : (
                <div className="semantic-diagnostic-empty">
                  <Clock size={20} />
                  <span>
                    <strong>投影尚未启动</strong>
                    <small>通过静态预检后将建立四类可靠投影。</small>
                  </span>
                </div>
              )}
            </div>
          </section>
          {gateFailures.length > 0 && (
            <section className="semantic-diagnostic-section">
              <header>
                <div>
                  <strong>评测门禁</strong>
                  <small>基于密封评测集和真实运行事实重新计算。</small>
                </div>
                <em>未通过</em>
              </header>
              <ul>
                {gateFailures.map((failure) => (
                  <li key={failure}>{failure}</li>
                ))}
              </ul>
            </section>
          )}
          <div className="semantic-diagnostic-safety">
            <ShieldCheck size={20} />
            <span>
              <strong>恢复不会绕过治理</strong>
              <small>
                重试只重放失败投影；预检问题必须回到对象修复，Hash
                漂移必须新建候选。
              </small>
            </span>
          </div>
        </div>
        <footer>
          <span>
            {issues.length
              ? "先处理预检阻断"
              : failedProjections.length
                ? "可安全重试失败投影"
                : gateFailures.length
                  ? "修复用例或语义后重跑评测"
                  : "当前未发现阻断"}
          </span>
          <div>
            <AppButton onClick={onClose}>关闭</AppButton>
            {(issues.length > 0 ||
              failedProjections.length > 0 ||
              !projections.length) && (
              <AppButton
                variant="primary"
                disabled={
                  busy ||
                  issues.some(
                    (issue) => issue.code === "RELEASE_MANIFEST_HASH_MISMATCH",
                  )
                }
                onClick={onRetry}
              >
                {busy
                  ? "正在处理…"
                  : failedProjections.length
                    ? "重试失败投影"
                    : "重新校验并投影"}
              </AppButton>
            )}
            {(canEvaluate || gateFailures.length > 0) && (
              <AppButton variant="primary" disabled={busy} onClick={onEvaluate}>
                重新运行评测
              </AppButton>
            )}
          </div>
        </footer>
      </section>
    </div>
  );
}
