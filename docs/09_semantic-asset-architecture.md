# Semantic Assets — Four-Section Architecture

> Document status: design baseline for the Semantic Assets module rebuild
> Version: V1.1 — updated after the first implementation pass (see §14)
> Base date: 2026-08-14
> Companion documents: [技术设计](./02_技术设计终稿.md)、[建模会话架构](./08_modeling-session-architecture.md)

This document redesigns the Semantic Assets module around four sections — **Model
Assets**, **Metric Center**, **Dimension Center**, **Business Knowledge** — as one
semantic knowledge layer with a single object graph, one JSON import contract, one
vectorization pipeline and one hybrid retrieval facade.

It is a design baseline, not a description of shipped code. §1 states what exists
today; §11 sequences the work.

---

## 1. What exists today

### 1.1 The registry is already versioned and governed

`internal/askdata/registry` owns immutable, content-hashed object versions
(`VersionIdentity` in [model.go](../internal/askdata/registry/model.go)): `Entity`,
`SemanticModel`, `Measure`, `Metric`/`MetricVersion`, `Dimension`, `Hierarchy`,
`DimensionMember`, `MetricDimension`, `Relationship`, `CertifiedExample`,
`BusinessTerm`, `RowAccessPolicy`, `QualityRule`, plus `KPIBundle`, `TimeContract`
and evaluation objects. Releases compose these into an ACTIVE catalog with gates,
approvals, rollouts and projections.

**This is the right foundation and the redesign keeps it.** The four sections are
not new storage — they are curated views over this graph.

### 1.2 Vectorization already meets most of the stated requirements

`askdata.search_documents` (migration 000215) already carries `input_hash`,
`embedding_status`, `embedding_model`, `embedding_version` and a unique key of
`(tenant, object_type, object_version_id, view_type)`; `askdata.embedding_outbox`
is a leased, retrying work queue keyed on `(search_document_id, input_hash)`.

That means **re-vectorize-only-on-content-change** and **index-state tracking**
are already structurally solved. `internal/askdata/search` already implements
hybrid retrieval — `Exact` + `Lexical` + `Vector` fused by RRF
([retriever.go](../internal/askdata/search/retriever.go), `rank.go`), with a
reranker and recall audit.

The gaps are information architecture, import, lineage and consolidation — not
the embedding machinery.

### 1.3 Real gaps

| Gap | Evidence |
|---|---|
| **No JSON import.** Import accepts only CSV/XLS/XLSX. | `importFileContract` in [upload.go](../internal/askdata/registry/import/upload.go) |
| **Import is per-asset-type, 12 formats.** One file carries exactly one `AssetType`. | `AssetType` in [store.go](../internal/askdata/registry/import/store.go) |
| **No first-class lineage.** Impact is a flat report, not a traversable graph. | [impact.go](../internal/askdata/registry/impact.go) returns four ref lists |
| **Physical vs semantic lineage is not modelled.** Physical dependency lives in `internal/asset` (`Dependency`, `Diff`); semantic dependency lives in the registry. Nothing joins them. | [asset/model.go](../internal/asset/model.go) |
| **Two parallel vector stacks.** `internal/assetembedding` (tables/columns) and `internal/askdata/search` (registry objects) each have their own document builder, outbox, worker and hybrid retriever. | both packages |
| **The four UI tabs are four independent list pages.** No cross-navigation, no shared graph panel. | [SemanticCenterPage.tsx](../web/src/pages/SemanticCenterPage.tsx), 6410 lines |

### 1.4 Duplicated and dead concepts

| Concept | Verdict |
|---|---|
| `platform.metrics`, `platform.metric_versions`, `platform.dimensions`, `platform.dimension_members`, `platform.metric_dimensions`, `platform.metric_dependencies`, `platform.metric_semantic_documents`, `platform.dimension_semantic_documents`, `platform.metric_candidates`, `platform.dimension_where_decisions`, `platform.dimension_survey_*`, `platform.dimension_profile_jobs` | **Dead.** Superseded by the `askdata.*` registry at migration 000214. Zero Go references — verified by grep across `internal/` and `cmd/`. Drop. |
| `Measure` vs `Metric` | **Merge.** Two metric-shaped objects; the search layer already canonicalizes `MEASURE` → `METRIC` and keeps `ObjectMeasureLegacy` only for rolling upgrade. Collapse into one `Metric` with `kind = BASE \| DERIVED \| RATIO`. |
| `Entity` | **Fold into Model Assets.** `Entity` is code/name/description/key-contract — that is model identity, not a separate asset family. |
| `Hierarchy` + `hierarchy_levels` + `DimensionMember` + `MetricDimension` | **Demote.** Sub-structures of Dimension Center, not top-level asset types with their own import format and page. |
| `BusinessTerm` + `metric_versions.business_definition` + `dimension_members.definition` | **Unify at the read boundary.** Migration 000340 deliberately put caliber prose on the object it describes; that stays. Business Knowledge indexes them as authoritative definitions rather than re-storing them. |
| `KPIBundle` | **Move to Metric Center** as a metric collection ("metric view"), not a standalone asset type. |
| `CertifiedExample` | **Move to Business Knowledge** as a `USAGE_EXAMPLE` entry kind. |
| `EvalCase` | **Not a semantic asset.** Stays in the evaluation module; leaves the import contract. |
| `RowAccessPolicy`, `QualityRule` | **Stay, but not as sections.** They are governance attachments to Model Assets, surfaced in the model detail panel and in Operations. |

Net: **12 importable asset types → 4 sections**, and the `platform.*` semantic
family disappears.

---

## 2. Target architecture

### 2.1 One graph, four windows

```text
                      ┌───────────────────────────────┐
                      │   Semantic Asset Graph        │
                      │   (askdata.* registry)        │
                      └───────────────────────────────┘
                          ▲        ▲        ▲       ▲
          ┌───────────────┘        │        │       └───────────────┐
          │                        │        │                       │
   ┌──────┴──────┐        ┌────────┴───┐  ┌─┴──────────┐   ┌────────┴────────┐
   │ Model Assets│        │Metric Center│  │Dimension   │   │Business         │
   │             │        │             │  │Center      │   │Knowledge        │
   │ models      │        │ metrics     │  │ dimensions │   │ terms           │
   │ relationships│       │ metric views│  │ hierarchies│   │ rules           │
   │ lineage     │        │ dependencies│  │ members    │   │ examples        │
   └─────────────┘        └─────────────┘  └────────────┘   └─────────────────┘
          │                        │                │                │
          └────────────────┬───────┴────────────────┴────────────────┘
                           ▼
              ┌────────────────────────────┐
              │ Import  →  Normalize  →    │
              │ Resolve →  Validate   →    │
              │ Persist →  Vectorize  →    │
              │ Index   →  Ready           │
              └────────────────────────────┘
                           ▼
              ┌────────────────────────────┐
              │ Hybrid Retrieval Facade    │
              │ filter → exact → lexical → │
              │ vector → graph expand →    │
              │ rank                       │
              └────────────────────────────┘
                           ▼
   modeling · intent-driven table retrieval · Intelligent Q&A ·
   Intelligent Reports · metric/dimension discovery · impact analysis
```

### 2.2 Invariants

1. **One object graph.** The four sections are read/write projections over the
   same versioned registry. A section never owns private storage.
2. **Structured fields are execution truth; embeddings are discovery only.**
   Nothing that binds, compiles or aggregates may read an embedding. (Consistent
   with the existing `business_definition` rule from migration 000340.)
3. **Physical lineage ≠ semantic dependency.** They are separate edge families
   with separate colours, separate traversal rules and separate impact semantics.
4. **Every asset carries a stable external key.** `(tenant, domain, section, code)`
   is the identity used by import; UUIDs are internal.
5. **Retrieval readiness is a state, not an assumption.** An asset is discoverable
   only when its search documents reach `SUCCEEDED`.

### 2.3 The shared asset envelope

Every asset in all four sections carries one envelope, which is what makes the
unified import contract and unified retrieval possible.

```go
// internal/askdata/registry/asset.go
type AssetSection string

const (
    SectionModel     AssetSection = "MODEL"
    SectionMetric    AssetSection = "METRIC"
    SectionDimension AssetSection = "DIMENSION"
    SectionKnowledge AssetSection = "KNOWLEDGE"
)

type AssetEnvelope struct {
    VersionIdentity                     // id, tenant, domain, objectId, versionNo, status, contentHash, owner
    Section        AssetSection         `json:"section"`
    Kind           string               `json:"kind"`        // section-specific subtype
    Code           string               `json:"code"`        // stable external key, unique per (tenant,domain,section)
    Name           string               `json:"name"`
    Aliases        []string             `json:"aliases"`
    Description    string               `json:"description"` // business meaning, prose
    Tags           []string             `json:"tags"`
    Sensitivity    Sensitivity          `json:"sensitivity"`
    Lifecycle      VersionStatus        `json:"lifecycle"`   // DRAFT | CERTIFIED | DEPRECATED
    SourceOrigin   Origin               `json:"sourceOrigin"`// MANUAL | IMPORT | AI_SUGGESTED | DERIVED
    ImportBatchID  string               `json:"importBatchId,omitempty"`
    SemanticHash   askdata.ContentHash  `json:"semanticHash"` // hash of vectorizable content only
    IndexState     IndexState           `json:"indexState"`
}
```

`SemanticHash` is deliberately separate from `ContentHash`: `ContentHash` covers
the whole governed definition (changing a filter AST changes it), while
`SemanticHash` covers only name + aliases + description + business context. Editing
a formula must not trigger re-embedding; editing a description must.

---

## 3. Section 1 — Model Assets

**Purpose:** manage semantic/data models and their relationships, and be the home
of both lineage graphs.

### 3.1 Semantic model

Extends today's `SemanticModel` — dataset binding, grain contract, time contract
all stay.

```go
type ModelAsset struct {
    AssetEnvelope                          // Kind: FACT | DIMENSION | BRIDGE | AGGREGATE
    Layer                 string           // ODS | DWD | DWS | ADS
    DatasetID             string
    DatasetVersionID      string
    DatasetSchemaHash     askdata.ContentHash
    MaterializationID     string
    GrainContract         GrainContract    // was json.RawMessage — now typed
    PrimaryTimeFieldID    string
    TimeContractVersionID string
    EntityKeys            []EntityKey      // absorbed from the old Entity object
    Fields                []ModelField     // logical field projection of the dataset
    RowAccessPolicyIDs    []string
    QualityRuleIDs        []string
}

type EntityKey struct {
    Code       string   // "customer", "order"
    FieldIDs   []string
    Uniqueness string   // PRIMARY | ALTERNATE | FOREIGN
}

type ModelField struct {
    ID            string
    Code          string
    Name          string
    Role          string // KEY | MEASURE | DIMENSION | TIME | ATTRIBUTE
    CanonicalType string
    SemanticType  string
    PhysicalRef   PhysicalRef // dataset field → table.column, the physical anchor
    Nullable      bool
    Sensitivity   Sensitivity
}

type PhysicalRef struct {
    DataSourceID string
    TableID      string
    ColumnID     string
    Expression   string // non-empty when the field is computed, not a plain column
}
```

`PhysicalRef` is the join point between `internal/asset` (physical tables/columns)
and the registry. It is what makes physical lineage computable rather than
hand-maintained.

### 3.2 Relationships

Today's `Relationship` stays as-is (join type, cardinality, fanout policy, bridge)
and becomes a Model Assets sub-tab rather than a hidden object.

### 3.3 Lineage — two edge families

```go
type LineageEdgeKind string

const (
    // PHYSICAL — derived, never hand-authored. Recomputed from dataset build
    // graphs, materialization resolvers and PhysicalRef.
    EdgeTableDerives   LineageEdgeKind = "TABLE_DERIVES"    // table → table
    EdgeColumnDerives  LineageEdgeKind = "COLUMN_DERIVES"   // column → column
    EdgeModelReadsTable LineageEdgeKind = "MODEL_READS_TABLE"

    // SEMANTIC — authored or implied by governed definitions.
    EdgeMetricUsesModel     LineageEdgeKind = "METRIC_USES_MODEL"
    EdgeMetricUsesField     LineageEdgeKind = "METRIC_USES_FIELD"
    EdgeMetricDependsMetric LineageEdgeKind = "METRIC_DEPENDS_METRIC"
    EdgeMetricAllowsDim     LineageEdgeKind = "METRIC_ALLOWS_DIMENSION"
    EdgeDimensionBindsField LineageEdgeKind = "DIMENSION_BINDS_FIELD"
    EdgeHierarchyLevel      LineageEdgeKind = "HIERARCHY_LEVEL"
    EdgeModelJoinsModel     LineageEdgeKind = "MODEL_JOINS_MODEL"
    EdgeKnowledgeDescribes  LineageEdgeKind = "KNOWLEDGE_DESCRIBES"
)

type LineageEdge struct {
    ID         string
    TenantID   string
    DomainID   string
    Family     string          // PHYSICAL | SEMANTIC
    Kind       LineageEdgeKind
    FromType   string          // TABLE | COLUMN | MODEL | MODEL_FIELD | METRIC | DIMENSION | HIERARCHY | KNOWLEDGE
    FromID     string
    ToType     string
    ToID       string
    Derivation string          // COMPUTED | DECLARED | IMPORTED
    Evidence   json.RawMessage // build-run id, formula AST path, join AST path
    ValidFrom  time.Time
    ValidTo    *time.Time      // edges are historised, not deleted
}
```

Storage: one table `askdata.lineage_edges`, partitioned by `family`, with a
covering index on `(tenant_id, domain_id, from_type, from_id, family)` and its
mirror for `to_*`. Edges with `Derivation = COMPUTED` are rebuilt idempotently;
`DECLARED`/`IMPORTED` edges are governed and survive rebuild.

**Why one table rather than reusing `askdata.relationships`:** relationships are
*join semantics* (they compile into SQL); lineage edges are *provenance* (they
never compile). Keeping them apart preserves invariant 2.

### 3.4 Graph browsing and impact analysis

Two read APIs over the same edge table:

- **Neighbourhood** — `GET /semantic/graph/neighbourhood?nodeType&nodeId&family&depth&kinds[]`
  returns nodes + edges for the canvas. Depth capped at 4 (matching
  `graph.MaxJoinHops`), node count capped, with a `truncated` flag.
- **Impact** — `POST /semantic/graph/impact` with `{changeType, nodeType, nodeId}`
  walks *downstream* only and returns, per hop, the affected models, metrics,
  dimensions, knowledge entries, KPI views, saved questions, report components and
  evaluation cases. This subsumes today's `RegistryImpactReport`, which returns a
  flat four-list result with no path or hop information.

Impact severity is derived, not stored: `BREAKING` when the change removes a node
or narrows a contract on a path with no alternative; `DEGRADED` when it changes
caliber; `INFORMATIONAL` otherwise.

### 3.5 Model assets as reusable context

`GET /semantic/models/{id}/context?purpose=MODELING|QA|REPORT` returns a compact,
token-budgeted context pack: model grain, entity keys, field roles, joinable
models with fanout policy, top-N metrics and dimensions by usage, and the
authoritative knowledge entries attached to any of them. This is the single entry
point downstream modules use, replacing ad-hoc per-module assembly.

---

## 4. Section 2 — Metric Center

**Purpose:** canonical metric definitions with enough structured metadata for
deterministic execution.

### 4.1 Merged metric model

`Measure` and `Metric` collapse into one object with a `Kind`.

```go
type MetricKind string

const (
    MetricBase      MetricKind = "BASE"      // aggregation over one model field
    MetricDerived   MetricKind = "DERIVED"   // expression over other metrics
    MetricRatio     MetricKind = "RATIO"     // numerator / denominator, needs zero policy
    MetricTimeShift MetricKind = "TIME_SHIFT"// YoY/MoM/period-over-period of another metric
)

type MetricAsset struct {
    AssetEnvelope                 // Kind = MetricKind

    // ── binding ────────────────────────────────────────────────
    ModelVersionID   string       // owning semantic model
    SourceFieldID    string       // BASE only
    FormulaAST       FormulaAST   // typed; BASE = agg(field), others = expression tree
    DefaultFiltersAST FilterAST   // applied at compile time
    DependsOn        []MetricRef  // DERIVED/RATIO/TIME_SHIFT inputs, resolved by code

    // ── aggregation behaviour ──────────────────────────────────
    Aggregation                 Aggregation
    Additivity                  Additivity
    SemiAdditiveTimeAggregation SemiAdditiveTimeAggregation
    AggregationRestriction      AggregationRestriction
    NonAdditiveDimensions       []string
    ZeroDenominatorPolicy       ZeroDenominatorPolicy
    NullPolicy                  string

    // ── grain and time ─────────────────────────────────────────
    Grain                       GrainContract // inherited from model unless narrowed
    TimeGrain                   string        // finest supported grain
    SupportedTimeGrains         []string
    IncompletePeriodPolicy      IncompletePeriodPolicy

    // ── presentation ───────────────────────────────────────────
    Unit             string
    Currency         string
    DataType         NumericDataType
    DisplayPrecision int16
    Format           MetricFormat // pattern, percent, scale, positive-is-good

    // ── applicability ──────────────────────────────────────────
    ApplicableDimensions []DimensionCompatibility // absorbs MetricDimension
    ExcludedDimensions   []string

    // ── semantic (vectorized) ──────────────────────────────────
    BusinessDefinition string   // caliber prose — stays where 000340 put it
    UsageContext       string   // when to use, when not to
    PositiveQuestions  []string
    NegativeExamples   []string
}

type DimensionCompatibility struct {
    DimensionCode string
    Compatible    bool
    Role          string // GROUP_BY | FILTER_ONLY | SLICE
    Note          string
}
```

`MetricDimension` stops being a top-level versioned object and a separate import
type; it becomes `ApplicableDimensions` inside the metric version. This removes an
entire asset type, an import format and a join from every read path — while keeping
the same governed facts.

### 4.2 Metric dependency rules

- Dependencies are declared by **code**, resolved to version IDs at commit.
- The dependency graph must be acyclic; cycles are a hard import failure
  (`METRIC_DEPENDENCY_CYCLE`) with the cycle path in the error.
- A derived metric's grain is the **coarsest** grain among its inputs; narrowing
  below that is rejected.
- A derived metric inherits the **most restrictive** additivity of its inputs
  unless explicitly overridden with a confirmation (reusing the existing
  additivity confirmation flow).
- `RATIO` requires `ZeroDenominatorPolicy` and rejects `SUM` roll-up across a
  dimension unless both legs are additive on it.

### 4.3 Metric views (replacing KPIBundle)

```go
type MetricView struct {
    AssetEnvelope           // Section = METRIC, Kind = VIEW
    Items []MetricViewItem  // headline / trend / breakdown, same roles as today
    DefaultDimensionCodes []string
    QuestionPatterns      []string
}
```

Same semantics as `KPIBundle`, re-homed and re-labelled so the section list has
one shape (`metric` / `metric view`) instead of two unrelated object families.

### 4.4 Vectorized surface

Four document views per metric — `NAME_ALIAS`, `DEFINITION_QUESTION`,
`USAGE_CONTEXT`, `NEGATIVE` — built from name, aliases, description,
`BusinessDefinition`, `UsageContext`, `PositiveQuestions`, `NegativeExamples`.
Formula, filters, precision and policies are **excluded** from documents; they are
retrieval *filters*, not retrieval *text*.

---

## 5. Section 3 — Dimension Center

**Purpose:** reusable dimensions independent of any report or question, with
business definition separated from physical binding.

### 5.1 Dimension asset

```go
type DimensionAsset struct {
    AssetEnvelope             // Kind: CATEGORICAL | TIME | ENTITY | GEO

    // ── business-level definition (binding-independent) ────────
    BusinessMeaning  string
    ValueSemantics   string   // what the values mean, how they are assigned
    Cardinality      string   // LOW | MEDIUM | HIGH | VERY_HIGH
    Format           DimensionFormat
    TimeSemantics    *TimeSemantics // TIME kind only

    // ── physical bindings (many, one per model) ────────────────
    Bindings         []DimensionBinding

    // ── structure ──────────────────────────────────────────────
    HierarchyCode    string   // membership in a shared hierarchy
    HierarchyLevel   int      // 1-based level within it
    ParentCode       string   // level above, within the same hierarchy

    // ── members ────────────────────────────────────────────────
    MemberIndexPolicy MemberIndexPolicy
    Members           []DimensionMemberSpec // only when governed; else profiled

    // ── applicability ──────────────────────────────────────────
    ApplicableModelCodes  []string
    ApplicableMetricCodes []string // derived from metric ApplicableDimensions
}

type DimensionBinding struct {
    ModelVersionID string
    LogicalFieldID string
    KeyFieldID     string // surrogate/natural key when label ≠ key
    LabelFieldID   string
    Expression     string // for computed bindings
    Primary        bool   // the canonical binding used when the model is ambiguous
}

type TimeSemantics struct {
    Grain          string // DAY | WEEK | MONTH | QUARTER | YEAR
    FiscalCalendar string // calendar code, empty = Gregorian
    WeekStart      string
    TimeZone       string
    ContractVersionID string
}

type DimensionMemberSpec struct {
    Key            string
    CanonicalLabel string
    Aliases        []string
    ParentKey      string
    Definition     string // stays on the member, per 000340
    Sensitivity    Sensitivity
    ValidFrom      *time.Time
    ValidTo        *time.Time
}
```

**Key change:** `Bindings` is a list. Today a `Dimension` carries a single
`SemanticModelVersionID` + `LogicalFieldID`, which forces "region" to exist once
per model. A shared dimension is one business object bound in several models —
that is what makes it *shared*, and what lets the retriever return one candidate
instead of five near-duplicates.

### 5.2 Hierarchies

```go
type HierarchyAsset struct {
    AssetEnvelope          // Section = DIMENSION, Kind = HIERARCHY
    Levels []HierarchyLevel
    Strict bool            // strict = every child has exactly one parent
}

type HierarchyLevel struct {
    Ordinal       int
    DimensionCode string
    Name          string
}
```

`region → province → city` and `year → quarter → month` are both hierarchies;
the time one is generated from the time contract rather than hand-authored, and is
marked `SourceOrigin = DERIVED`.

Drill paths in Q&A and reports resolve against `HierarchyAsset`, so drill-down
stops being per-report configuration.

### 5.3 Members

Member handling stays policy-driven (`FULL` / `EXACT_ONLY` / `ON_DEMAND` / `NONE`)
because high-cardinality dimensions must not flood the vector index. Governed
members come from import; profiled members come from the existing dimension
profiling worker. Both write into the same member table with distinct
`SourceOrigin`, and only governed members can be referenced by knowledge entries.

### 5.4 Vectorized surface

`NAME_ALIAS`, `DEFINITION_QUESTION`, `HIERARCHY_PATH` (the full path text, e.g.
"地区 › 省份 › 城市"), and `DIMENSION_VALUE` for members under `FULL`/`EXACT_ONLY`.
`HIERARCHY_PATH` is new and matters: users ask by level name ("按省份") far more
often than by dimension code.

---

## 6. Section 4 — Business Knowledge

**Purpose:** the LLM-facing context layer — terminology, conventions, policies,
rules, domain knowledge, FAQs — with governed links into the other three sections.

### 6.1 Knowledge entry

```go
type KnowledgeKind string

const (
    KnowledgeTerm        KnowledgeKind = "TERM"         // jargon, abbreviation, synonym
    KnowledgeDefinition  KnowledgeKind = "DEFINITION"   // authoritative business definition
    KnowledgeConvention  KnowledgeKind = "CONVENTION"   // calculation convention
    KnowledgePolicy      KnowledgeKind = "POLICY"       // business rule / policy
    KnowledgeFAQ         KnowledgeKind = "FAQ"
    KnowledgeUsageExample KnowledgeKind = "USAGE_EXAMPLE" // absorbs CertifiedExample
    KnowledgeDomainNote  KnowledgeKind = "DOMAIN_NOTE"  // supplementary context
)

type KnowledgeAuthority string

const (
    // AUTHORITATIVE entries are governed definitions. They may be quoted to the
    // user as fact, they win conflicts, and they require review to change.
    AuthorityAuthoritative KnowledgeAuthority = "AUTHORITATIVE"
    // SUPPLEMENTARY entries are context. They inform the LLM but are never
    // presented as the definition of record.
    AuthoritySupplementary KnowledgeAuthority = "SUPPLEMENTARY"
)

type KnowledgeAsset struct {
    AssetEnvelope                  // Kind = KnowledgeKind
    Authority   KnowledgeAuthority
    Body        string             // the knowledge itself, markdown-free prose
    Synonyms    []string
    Antonyms    []string           // "not to be confused with"

    // ── deterministic matching (term-like kinds) ───────────────
    MatchMode        string        // EXACT | PREFIX | REGEX | NONE
    MatchPattern     string
    Priority         int
    NegativeContexts []string

    // ── scope ──────────────────────────────────────────────────
    Links            []KnowledgeLink
    ApplicableRoleIDs []string
    ValidFrom, ValidTo *time.Time

    // ── provenance ─────────────────────────────────────────────
    SourceRef    string  // document, ticket, policy number
    ReviewStatus string
    ReviewedBy   string
    ReviewedAt   *time.Time
}

type KnowledgeLink struct {
    TargetType string // MODEL | MODEL_FIELD | METRIC | DIMENSION | MEMBER | HIERARCHY | DATASET | TABLE | COLUMN
    TargetCode string
    Relation   string // DEFINES | CONSTRAINS | EXPLAINS | EXEMPLIFIES | DEPRECATES
}
```

`Relation = DEFINES` + `Authority = AUTHORITATIVE` is the strong case: this entry
*is* the definition of that metric. The read model enforces **at most one
authoritative DEFINES entry per target**; a second one is an import failure
(`KNOWLEDGE_DEFINITION_CONFLICT`), reusing the existing term-conflict machinery in
`registry/term_conflict.go`.

### 6.2 Relationship to `business_definition` columns

Migration 000340 put caliber prose on `metric_versions` and member definitions on
`dimension_members`, arguing correctly that a definition should not drift from the
object it describes. That stays. Business Knowledge does **not** copy them.
Instead:

- The metric detail panel shows `BusinessDefinition` (owned by the metric) and,
  below it, linked knowledge entries (owned by Business Knowledge).
- The retriever treats both as evidence, tagging each with its owner so the LLM
  can cite precisely.
- `KnowledgeKind = DEFINITION` is for concepts that are **not** a single registry
  object — "active customer", "GMV net of returns", "fiscal week" — which is
  exactly the gap the columns cannot fill.

### 6.3 Usage examples replace certified examples

`CertifiedExample` (question + expected metric/dimension version IDs) becomes
`KnowledgeUsageExample` with `Links` carrying `EXEMPLIFIES` relations. Same
retrieval role, same governance, one fewer asset family, and it gains the
knowledge envelope (authority, validity window, role scope) for free.

---

## 7. Unified JSON import contract

### 7.1 One bundle, all sections

```jsonc
{
  "contract": "semantic-bundle/v1",
  "tenant": "acme",
  "domain": "retail",
  "generatedAt": "2026-08-14T09:00:00Z",
  "source": { "system": "dbt", "runId": "…" },   // optional provenance
  "options": {
    "mode": "UPSERT",                 // UPSERT | CREATE_ONLY | REPLACE_SCOPE
    "onUnknownReference": "FAIL",     // FAIL | SKIP_ROW
    "dryRun": false,
    "scope": ["METRIC", "DIMENSION"]  // REPLACE_SCOPE only: what may be retired
  },
  "assets": [
    {
      "section": "MODEL",
      "kind": "FACT",
      "code": "fct_order",
      "name": "订单事实",
      "aliases": ["订单表"],
      "description": "…",
      "spec": {
        "layer": "DWD",
        "datasetCode": "ds_order",
        "grain": ["order_id"],
        "primaryTimeField": "order_date",
        "entityKeys": [{ "code": "order", "fields": ["order_id"], "uniqueness": "PRIMARY" }],
        "fields": [
          { "code": "order_amount", "role": "MEASURE", "physical": { "table": "dwd.order", "column": "amount" } }
        ]
      }
    },
    {
      "section": "METRIC",
      "kind": "BASE",
      "code": "gmv",
      "name": "GMV",
      "aliases": ["成交总额"],
      "spec": {
        "model": "fct_order",
        "aggregation": "SUM",
        "sourceField": "order_amount",
        "additivity": "FULLY_ADDITIVE",
        "unit": "CNY",
        "timeGrain": "DAY",
        "format": { "precision": 2, "pattern": "#,##0.00" },
        "applicableDimensions": [
          { "dimension": "region", "compatible": true, "role": "GROUP_BY" }
        ],
        "businessDefinition": "已支付订单金额合计，不含退款与取消订单。",
        "usageContext": "用于经营看板的规模指标；毛利分析请使用 gross_profit。",
        "positiveQuestions": ["上个月 GMV 是多少"]
      }
    },
    {
      "section": "DIMENSION",
      "kind": "CATEGORICAL",
      "code": "region",
      "name": "地区",
      "spec": {
        "hierarchy": "geo",
        "level": 1,
        "bindings": [
          { "model": "fct_order", "field": "region_code", "labelField": "region_name", "primary": true }
        ],
        "memberIndexPolicy": "FULL",
        "members": [{ "key": "EAST", "label": "华东", "aliases": ["东部"] }]
      }
    },
    {
      "section": "KNOWLEDGE",
      "kind": "DEFINITION",
      "code": "active_customer",
      "name": "活跃客户",
      "spec": {
        "authority": "AUTHORITATIVE",
        "body": "近 30 天内至少完成一次支付的客户。",
        "synonyms": ["活跃用户"],
        "links": [{ "targetType": "METRIC", "targetCode": "active_customer_count", "relation": "DEFINES" }]
      }
    }
  ]
}
```

**Design decisions:**

- **One envelope + one `spec` per section.** The envelope is validated by one
  schema for every asset; `spec` is validated by a section+kind schema. This is
  the "unified contract" the brief asks for without pretending a metric and a
  hierarchy have the same body.
- **References are by `code`, never by UUID.** A bundle exported from one
  environment imports into another unchanged. UUID references are accepted but
  discouraged and reported as `NON_PORTABLE_REFERENCE` warnings.
- **Order-independent.** `gmv` may reference `fct_order` before it appears.
  Resolution is a whole-bundle pass, not a streaming one.
- **`REPLACE_SCOPE` is explicit and bounded.** Full replacement requires naming
  the sections that may be retired; assets outside `scope` are never touched.
  There is no implicit "delete everything not in this file".

JSON Schemas live beside the existing ones in
`internal/askdata/registry/schemas/`: `semantic-bundle-v1.schema.json` plus
`spec/{model,metric,dimension,knowledge}-v1.schema.json`.

### 7.2 Lifecycle

```text
 upload ──▶ VALIDATING ──▶ VALIDATED ──▶ COMMITTING ──▶ COMMITTED ──▶ INDEXING ──▶ READY
   │            │              │                            │             │
   │            ▼              ▼                            ▼             ▼
   └──────▶  FAILED       WITHDRAWN               PARTIALLY_COMMITTED  INDEX_DEGRADED
```

Two states are new relative to today's machine: **INDEXING** and **READY**. Today
`COMMITTED` is terminal, which means an imported asset is "done" before it is
retrievable. The brief requires the opposite, so commit no longer ends the batch.

| Stage | What runs | Failure behaviour |
|---|---|---|
| **1. Upload** | size/extension check, SHA-256 dedup against prior batches | reject; identical hash returns the existing batch |
| **2. Schema validation (L1)** | envelope schema, then per-section `spec` schema; enum, range, control-char checks | per-row `INVALID`; batch continues |
| **3. Normalization (L2)** | trim/NFC-normalize text, canonicalize codes to `lower_snake`, sort alias sets, drop empty optionals, canonicalize ASTs, compute `SemanticHash` | deterministic; a normalization that changes meaning is an error, not a silent fix |
| **4. Identity resolution (L3)** | resolve `(tenant, domain, section, code)` → existing object; classify each row `CREATE` / `UPDATE` / `UNCHANGED` (by `ContentHash`) | ambiguous code → `IDENTITY_AMBIGUOUS` |
| **5. Dependency validation (L4)** | resolve all references bundle-first then registry; check cycles, grain compatibility, additivity inheritance, dimension binding validity, authoritative-definition uniqueness | broken reference → row `INVALID` with `code`, `path`, `missingRef`; **never a silent partial link** |
| **6. Persistence** | one transaction per section in dependency order (model → dimension → metric → knowledge); new versions written, `UNCHANGED` rows skipped | any section fails → `PARTIALLY_COMMITTED` with a per-section report |
| **7. Vectorization** | for rows whose `SemanticHash` changed, enqueue `embedding_outbox` entries; unchanged semantic content is not re-embedded even when `ContentHash` changed | outbox retries with backoff; exhausted → `INDEX_DEGRADED` |
| **8. Indexing** | worker writes embeddings + refreshes lexical `tsvector`; batch watcher flips to `READY` when every enqueued document is `SUCCEEDED` or `SKIPPED` | partial → `INDEX_DEGRADED`, with the failing documents listed |
| **9. Retrieval readiness** | assets become visible to the semantic retriever only at `READY`; `INDEX_DEGRADED` assets are exact-matchable but not vector-retrievable | reported, never hidden |

Stages 2–5 are pure validation: **nothing is written to the registry before
stage 6**, and `dryRun: true` stops after stage 5 with a full report.

### 7.3 Identity and idempotency

- Stable key: `(tenant_id, domain_id, section, code)`. Codes are immutable; a
  rename is `name` change, not `code` change.
- Re-importing an identical bundle produces all-`UNCHANGED` and enqueues no
  embedding work — provable from `file_hash` at stage 1 and `ContentHash` at
  stage 4.
- `code` collisions across sections are legal (`region` may be both a dimension
  and a knowledge term); collisions within a section are the identity.
- Renaming a code is supported explicitly via `"previousCode": "…"`, which
  performs a governed rename rather than create+orphan.

### 7.4 Import result

```jsonc
{
  "importId": "…",
  "state": "READY",
  "counts": { "created": 12, "updated": 5, "unchanged": 40, "skipped": 2, "failed": 3 },
  "bySection": { "MODEL": {…}, "METRIC": {…}, "DIMENSION": {…}, "KNOWLEDGE": {…} },
  "index": { "enqueued": 17, "succeeded": 17, "failed": 0, "readyAt": "…" },
  "failures": [
    {
      "row": 31,
      "section": "METRIC",
      "code": "gross_margin",
      "stage": "DEPENDENCY",
      "errorCode": "UNKNOWN_REFERENCE",
      "path": "spec.dependsOn[1]",
      "message": "metric code \"cogs\" does not exist in this bundle or in the registry",
      "suggestion": "cost_of_goods_sold"
    }
  ]
}
```

`suggestion` uses the existing lexical index — a broken reference should tell the
author what they probably meant.

### 7.5 What this replaces

The 12 per-type CSV/XLSX flows collapse to: one JSON bundle endpoint, plus the
existing spreadsheet path **retained only as a front-end convenience** that
converts a sheet into a bundle before stage 2. `AssetType` disappears from the
import batch; `section` on each row replaces it.

---

## 8. Vectorization and index state

### 8.1 Document model

Reuse `askdata.search_documents` unchanged in shape, with widened enums:

- `object_type` gains `HIERARCHY`, `KNOWLEDGE`, `MODEL_FIELD`; loses `MEASURE`
  and `ENTITY` (see §10 for the cutover).
- `view_type` gains `USAGE_CONTEXT`, `NEGATIVE`, `HIERARCHY_PATH`.
- `input_hash` is fed by `AssetEnvelope.SemanticHash`, not the full content hash.

### 8.2 Index state on the asset

```go
type IndexState struct {
    Status        string    // NOT_INDEXED | PENDING | INDEXING | READY | DEGRADED
    DocumentCount int
    ReadyCount    int
    EmbeddingModel   string
    EmbeddingVersion string
    LastIndexedAt *time.Time
    LastError     string
}
```

Computed from the asset's `search_documents` rows, materialized on the asset row
for list rendering, and surfaced in every section list as a badge. **An asset that
is not `READY` is visibly not `READY`** — the section list, the detail panel and
the import report all say so, rather than the module quietly under-retrieving.

### 8.3 Re-vectorization rule

On every write, compare the new `SemanticHash` against the stored one:

| Change | Re-embed? |
|---|---|
| description, aliases, business definition, usage context, member labels | yes |
| formula, filters, precision, additivity, bindings, policies | no |
| embedding model or document-builder version bump | yes, as a background backfill sweep |

The last row is why `embedding_version` exists on the document: a model upgrade is
a controlled re-index of everything, executed by the same outbox rather than a
migration script.

---

## 9. Hybrid retrieval

### 9.1 Pipeline

```text
request ──▶ ① deterministic constraint ──▶ ② exact ──▶ ③ lexical ──▶ ④ vector
                                                              │
                                                              ▼
                                              ⑤ graph expansion ──▶ ⑥ rank ──▶ evidence
```

1. **Deterministic constraint (always first).** Tenant, domain, active release,
   reader permissions, section/asset-type filter, and any known model, dataset or
   metric context from the calling module. This is a `WHERE` clause, not a
   scoring signal — an asset outside the constraint can never be returned.
2. **Exact.** Code, alias and governed term match via `MatchMode`, including
   `NegativeContexts` suppression. Exact hits carry a fixed top score and are
   marked `deterministic`.
3. **Lexical.** `tsvector` match on the document text.
4. **Vector.** pgvector cosine over `READY` documents only, per object type,
   `TopKPerType` bounded.
5. **Graph expansion.** For each surviving candidate, walk the semantic edge
   family one to two hops to pull in the assets that make it usable: a metric
   pulls its model, its applicable dimensions, its authoritative knowledge entry
   and its dependency metrics; a dimension pulls its hierarchy siblings. Expanded
   nodes enter ranking with a decay factor and are labelled `expanded`, never
   presented as direct matches.
6. **Rank.** RRF across the exact/lexical/vector lists (existing `rank.go`), then
   the existing reranker, then deterministic tie-breaks: certified before draft,
   authoritative before supplementary, higher usage before lower, newer version
   before older.

### 9.2 Retrieval request

```go
type SemanticRetrievalRequest struct {
    Scope       askdata.PolicyScope
    Purpose     string   // MODELING | QA | REPORT | DISCOVERY | IMPACT
    Query       string
    Sections    []AssetSection
    Constraints Constraints // model codes, dataset ids, metric codes, tags, layer, time range
    Expand      ExpandPolicy
    TopK        int
}
```

`Purpose` selects a preset: `QA` weights exact and term matches highest and
expands aggressively; `MODELING` weights models and dimensions and expands to
physical tables; `DISCOVERY` weights vector similarity and expands minimally.

### 9.3 Unifying the two vector stacks

`internal/assetembedding` (physical tables/columns) is **not** deleted — physical
assets have a different lifecycle and a much higher row count. It is instead put
behind the same facade:

```go
// internal/askdata/retrieval — new package
type Facade interface {
    Retrieve(context.Context, SemanticRetrievalRequest) (Evidence, error)
}
```

The facade fans out to the registry retriever and, when `Purpose = MODELING` or
the constraint names a dataset, to the physical retriever; results are merged by
the same RRF and labelled with their family. Callers stop choosing a stack.

This is the one place where "one retrieval layer" is worth the indirection: today
every consumer picks a stack by hand and none of them merge results.

---

## 10. Consolidation plan

### 10.1 Remove

| Target | Action |
|---|---|
| The `platform.*` semantic family (`metrics`, `metric_versions`, `dimensions`, `dimension_members`, `metric_semantic_documents`, `metric_candidates`, `dimension_where_decisions`, …) | **Already done.** Migration `000195_remove_decommissioned_features` dropped the entire family; the CREATE statements visible in `migrations/` are immutable history, not live schema. No action required. |
| `askdata.business_terms_legacy_000228` | **Drop** after confirming the migration to `business_term_versions` is complete. |
| `search.ObjectMeasureLegacy` | **Remove** once all releases are recomposed on the merged metric model. |
| `AssetType` (12 import types) | **Remove**; replaced by `section`. |
| `EvalCase` import type | **Remove** from semantic import; evaluation owns its own upload. |

### 10.2 Merge

| From | Into |
|---|---|
| `Measure` | `MetricAsset{Kind: BASE}` |
| `Entity` | `ModelAsset.EntityKeys` |
| `MetricDimension` | `MetricAsset.ApplicableDimensions` |
| `KPIBundle` | `MetricView` (Metric Center) |
| `CertifiedExample` | `KnowledgeAsset{Kind: USAGE_EXAMPLE}` |
| `Hierarchy` + `hierarchy_levels` | `HierarchyAsset` (Dimension Center) |
| `registry.RegistryImpactReport` | graph impact API (§3.4) |

### 10.3 Keep as-is

Release composition, gates, approvals, rollouts and projections; row access
policies; quality rules; time contracts; additivity confirmation; the dimension
profiling worker; the embedding outbox; the RRF ranker and reranker.

### 10.4 Backward compatibility stance

Per the brief, obsolete asset types are **not** preserved. The one concession is
release recomposition: existing ACTIVE releases keep working through their frozen
projections until recomposed, and `ObjectMeasureLegacy` survives exactly that long
— it is a rolling-upgrade affordance with a removal trigger, not a permanent
compatibility surface.

---

## 11. API and UI

### 11.1 API surface

```
GET    /api/v1/askdata/semantic/models                     list + filter + facets
GET    /api/v1/askdata/semantic/models/{code}
GET    /api/v1/askdata/semantic/models/{code}/context
GET    /api/v1/askdata/semantic/metrics                    (metrics + metric views)
GET    /api/v1/askdata/semantic/metrics/{code}
GET    /api/v1/askdata/semantic/dimensions                 (dimensions + hierarchies)
GET    /api/v1/askdata/semantic/dimensions/{code}
GET    /api/v1/askdata/semantic/knowledge
GET    /api/v1/askdata/semantic/knowledge/{code}

GET    /api/v1/askdata/semantic/graph/neighbourhood
POST   /api/v1/askdata/semantic/graph/impact
POST   /api/v1/askdata/semantic/retrieval                  hybrid facade

POST   /api/v1/askdata/semantic/imports                    JSON bundle upload
POST   /api/v1/askdata/semantic/imports/{id}/validate      dry run
POST   /api/v1/askdata/semantic/imports/{id}/commit
GET    /api/v1/askdata/semantic/imports/{id}/report
GET    /api/v1/askdata/semantic/imports/schema             the bundle JSON Schema
POST   /api/v1/askdata/semantic/exports                    bundle export (round-trips)
```

Export emits the same `semantic-bundle/v1` shape it imports. Round-trip identity
(export → import → all `UNCHANGED`) is the contract's acceptance test.

### 11.2 Interactions

The four sections share one page shell so they stop being isolated pages:

- **Shared left rail:** section switcher with counts and an index-state summary.
- **Shared filter bar:** domain, layer, tag, lifecycle, index state, owner —
  identical controls in all four sections.
- **Shared search box:** runs the hybrid retrieval facade across all sections,
  results grouped by section. Searching "GMV" in Dimension Center still finds the
  metric and offers to switch.
- **Shared detail drawer:** every asset opens the same drawer with tabs
  *Definition · Bindings · Lineage · Knowledge · Usage · Versions*. The Lineage tab
  renders the graph canvas with a physical/semantic toggle. The Knowledge tab
  shows both the object's own definition prose and linked knowledge entries.
- **Cross-navigation is a link, never a search.** Metric → its model, model →
  its metrics, dimension → the metrics that accept it, knowledge → its targets.
- **Impact preview on every edit and on import.** Before committing a change to a
  certified asset, the drawer shows the downstream blast radius from §3.4.
- **Import wizard:** upload → validation report (per-section, per-row, with
  suggestions) → dry-run diff (create/update/unchanged/skip) → commit → live
  indexing progress → ready. The wizard never lets a batch appear "done" while
  indexing is incomplete.

`SemanticCenterPage.tsx` is 6410 lines and currently holds all of this inline. The
rebuild splits it into a shell plus one component per section plus shared
drawer/graph/import components; that split is a precondition for the work, not a
cleanup afterwards.

---

## 12. Sequencing

Each phase is independently shippable and leaves the system working.

| Phase | Scope | Exit criterion |
|---|---|---|
| **P0** | Drop the dead `platform.*` semantic family; split `SemanticCenterPage.tsx` into shell + sections with no behaviour change | migrations green, UI unchanged |
| **P1** | `AssetEnvelope` + `SemanticHash` on all four families; index-state badge in every list | every asset shows a truthful readiness state |
| **P2** | Merge `Measure` → `Metric`; absorb `Entity` and `MetricDimension`; recompose releases | one metric family end to end; `ObjectMeasureLegacy` removable |
| **P3** | `semantic-bundle/v1` schema, validate/normalize/resolve stages, dry-run report | dry run produces a full per-row report with zero writes |
| **P4** | Commit + `INDEXING`/`READY` states; import result contract; export round-trip | export → import → all `UNCHANGED` |
| **P5** | `lineage_edges` table, computed physical edges, declared semantic edges, neighbourhood + impact APIs, Lineage tab | impact returns paths and hop counts; physical/semantic toggle works |
| **P6** | Dimension multi-binding, `HierarchyAsset`, `HIERARCHY_PATH` documents | one shared `region` across N models; drill paths resolve from the hierarchy |
| **P7** | Knowledge consolidation: `KnowledgeAsset`, `CertifiedExample` → `USAGE_EXAMPLE`, authority conflict enforcement | at most one authoritative DEFINES per target, enforced at import |
| **P8** | `internal/askdata/retrieval` facade, purpose presets, graph expansion; migrate Q&A, modeling and reports onto it | no consumer selects a retrieval stack by hand |

Risk concentrates in P2 (release recomposition) and P5 (physical lineage
derivation, which depends on dataset build-run provenance being complete). Both
should be spiked before their phase starts.

---

## 13. Open decisions

1. **Physical lineage source.** Column-level physical lineage requires either SQL
   parsing of dataset build definitions or complete `PhysicalRef` coverage on
   model fields. Table-level lineage is available today from build runs; column
   level is not. Recommendation: ship table-level in P5, column-level behind a
   flag in a later phase.
2. **Cross-domain assets.** A shared `region` dimension across domains conflicts
   with the domain governance fence (migration 000194). Recommendation: keep
   dimensions domain-scoped in v1 and add an explicit `SHARED` domain rather than
   weakening the fence.
3. **Member volume.** Governed members in the bundle are convenient but a bundle
   with 10⁵ members is not. Recommendation: cap governed members per dimension in
   the schema and require the profiling worker above that cap.
4. **Knowledge authoring.** Whether Business Knowledge gets a rich editor or stays
   import-plus-form. Recommendation: form in v1; the authority/conflict model
   matters more than the editor.

---

## 14. Implementation status (first pass, 2026-08-14)

Shipped in this pass — migrations `000350`/`000351`, backend, and frontend:

| Area | What shipped | Where |
|---|---|---|
| **Unified JSON import** | `semantic-bundle/v1` contract: parse → deterministic expansion into governed import rows (section-ranked, metric-topo-sorted, stable row numbers) → the existing four-layer validation → DRAFT commit. One bundle covers all four sections; the batch-level type is `BUNDLE`. | `registry/import/bundle.go`, `worker.go` |
| **Identity & idempotency** | Same-file re-upload dedups by hash (existing); per-row `CREATE`/`UPDATE` resolution (`IMPORT_WILL_UPDATE`), `CREATE_ONLY` mode enforcement, and `UNCHANGED` detection by comparing normalized rows against the current certified head's export representation — unchanged rows become `SKIPPED` and never re-version. | `validate_l4.go`, `validate_unchanged.go` |
| **Cross-section references** | Validation accepts same-bundle codes (batch index spans all row types); commit resolves batch-local DRAFTs first (`resolveImportCode`), then certified heads. Withdraw runs in reverse row order so dependents delete first. | `draft_creator.go`, `withdraw.go` |
| **Business Knowledge** | New `KNOWLEDGE` asset type on `business_term_versions` (+`knowledge_kind`, `authority`, `relation` columns; `CONCEPT` target for pure concepts). "At most one AUTHORITATIVE DEFINES per target" enforced at L4 against both the batch and the domain snapshot. Alias terms are `knowledge_kind='ALIAS'` and keep their own namespace. | migration `000350`, `createKnowledgeDraft` |
| **Import result** | `GET imports/{id}/rows` returns per-row facts with resolution + per-section counts (created/updated/unchanged/skipped/failed/pending) and a **derived** retrieval-readiness block computed from `search_documents` — readiness is never stored batch state. | `report_json.go` |
| **Round-trip export** | `GET exports?format=json` renders the current catalog (or a release) back into `semantic-bundle/v1`; measure+metric pairs merge back into `BASE` assets only when provably lossless. Round-trip is covered by a unit test (export → expand → compare). | `bundle_export.go` |
| **Lineage** | `askdata.lineage_edges` (physical/semantic families, historised `valid_to`, RLS); idempotent COMPUTED projection from the registry + dataset build provenance; neighbourhood (bidirectional, capped BFS) and downstream-only impact APIs with per-hop layering; rebuild endpoint. | migration `000351`, `internal/askdata/lineage` |
| **Frontend** | Bundle import wizard (upload → staged validation → per-row resolutions → commit → readiness), JSON-Schema/current-bundle download, lineage panel (SVG columns by node type, physical/semantic toggle, per-hop impact list) wired into the four existing section tabs. | `SemanticBundleImportPanel.tsx`, `SemanticLineagePanel.tsx` |
| **Hybrid discovery retrieval (P8, discovery scope)** | `internal/askdata/retrieval` facade: deterministic catalog lane (exact code/name/alias + trigram lexical over governed heads — drafts discoverable, always in sync with the registry), release-pinned vector lane (query embedding + the existing `search_documents` projection; degrades with an explicit reason when no ACTIVE release or no embedder), one-hop semantic lineage expansion labelled `EXPANDED` with score decay, RRF fusion with deterministic tie-breaks. Served at `POST /semantic/retrieval`; the section search box surfaces cross-section hits with jump-to-section. **Deliberate boundary:** this is the *discovery* facade — the certified Q&A binding path (`understanding`/`search`) is untouched, because rewiring it belongs behind an evaluation run, not a refactor. | `internal/askdata/retrieval`, `SemanticDiscoveryResults.tsx` |

Deliberate deviations from the earlier sections of this document:

1. **`INDEXING`/`READY` are derived, not stored** (§7.2 revised). The batch state
   machine keeps `COMMITTED` terminal; retrieval readiness is computed from
   `search_documents` on every report read. State that can be derived from the
   source of truth is not duplicated into a second machine that can drift.
2. **`RATIO` folded into `DERIVED`** (§4.1). A ratio is a DERIVED metric with a
   DIVIDE formula plus the already-mandatory `zeroDenominatorPolicy`; a separate
   kind added contract surface without adding facts.
3. **Knowledge lives on `business_term_versions`** (§6), not a new object family —
   the same argument migration 000340 made for caliber prose. `USAGE_EXAMPLE`
   stays on `certified_examples` for now (P7 remains open).
4. **`REPLACE_SCOPE` not implemented** (§7.1). Destructive retirement needs its
   own governance pass; `UPSERT`/`CREATE_ONLY` shipped.
5. **Dimension multi-binding (P6) and the Measure→Metric merge (P2) did not
   ship**; the bundle's `BASE` kind already presents measure+metric as one asset
   at the contract boundary, which is the forward-compatible surface for P2.
6. **The full `SemanticCenterPage` shell split was deliberately not executed.**
   All *new* semantic-center capability ships as separate components
   (`SemanticBundleImportPanel`, `SemanticLineagePanel`,
   `SemanticDiscoveryResults`), which is the modularity the split was meant to
   buy for new work. Splitting the remaining monolith without also restructuring
   its ~30 shared state atoms would produce 20-prop pass-through components —
   worse, not better — and the state restructuring it actually needs is a
   behaviour-affecting refactor that must be verified visually. Do it as its own
   change with a working browser loop, not as a rider.

Known v1 wrinkles:

1. A brand-new model and its `primaryTimeDimension` are mutually referential
   (model→dimension→model); a single bundle cannot introduce both with the
   pointer set. Import the model without `primaryTimeDimension`, certify, then
   re-import the pointer — same two-pass flow the per-type imports always
   required.
2. End-to-end testing exposed a latent export defect: term/knowledge MEMBER
   targets resolved by reading `dimension_members.member_key`, which the
   API/worker roles cannot SELECT (sensitive-member column ACL). Fixed with
   `askdata.resolve_member_export_target` (migration 000352), a SECURITY DEFINER
   resolver that returns targets only for PUBLIC/INTERNAL members — the same
   boundary as the export omission policy. The raw MEMBER *sheet* export
   (`loadMemberExportRows`) still reads member columns directly and remains
   restricted to roles with member access; unifying it onto definer-mediated
   access is follow-up work.
3. The same testing caught that `CONCEPT` targets had to be admitted in three
   places that enumerate term target types: certification (`certify.go`),
   authoring validation (`term.go`) and the Q&A dictionary loader
   (`understanding/dictionary.go`). Grep for the target-type CHECK list when
   adding another target kind.
