# Modeling Session Architecture — Review and Redesign

Scope: the LLM-driven DAG generation path used by the dataset designer
(`internal/datasetai`, `web/src/lib/dataset-ai.ts`, `web/src/pages/DatasetCenterPage.tsx`),
traced end to end from user input to persistence. The batch warehouse modeling path
(`internal/dataset/dwd_modeling*.go`, `dws_modeling.go`) is included for contrast because it
already solves several problems the interactive path does not.

---

## 1. What is actually built today

### 1.1 The interactive path, as-built

```
User types instruction in the AI dock
  └─ frontend serializes the ENTIRE live canvas → DatasetAIGraphPlan  (dataset-ai.ts:141)
       └─ POST /api/v1/datasets/ai/proposals   {instruction, current?}   (http.go:82)
            └─ Service.Plan                                              (service.go:174)
                 ├─ mode = current==nil ? CREATE : MODIFY
                 ├─ loadCatalog                                          (service.go:1275)
                 │     ├─ [optional] assetembedding.Retrieve             (retriever.go:25)
                 │     ├─ searchCatalogTables → up to 1000 tables, 5 pages, NO query filter
                 │     ├─ rankCatalogTables → substring scoring
                 │     ├─ take top 12, ListColumns per table (N+1)
                 │     └─ byte-budget fitting loop: re-marshal whole request per step
                 ├─ [MODIFY] extractChangeIntent  → LLM call #1 (+1 repair)
                 ├─ buildProviderRequest          → LLM call #2 (+1 repair)
                 ├─ materializeLocked*  ×7        → server rebuilds the graph deterministically
                 ├─ validateProposal / validateAndCanonicalizePlanChanges
                 └─ return Proposal (never persisted)
  └─ frontend: materializeDatasetAIPlan → POST /datasets/validate → setState
```

Persistence of the modeling *session* itself: none. Migration `000038_dataset_dag_ai.up.sql`
adds only an AI *audit* purpose (`DATASET_DAG_GENERATION`) on `platform.ai_requests`. There is
no table for a modeling session, a proposal, a revision, a decision, or an open question.

### 1.2 The batch path, for contrast

`OrchestratedDWDModelingPlanner` (`dwd_modeling_planner.go`) already does what the interactive
path does not: it decomposes modeling into named stages (`Classify` → `MergeClassifications` →
`DesignDimension` → `discoverDWDFactAssociations` → `DesignFact`), carries a
`dwdPlanningHistory` / `dwdHistoricalOutput` between stages, has a `resumableDWDModelingPlanner`
interface, and discovers fact-to-fact relationships as a *separate bounded decision* before
designing the output table.

The interactive path has none of this structure. Two architectures for the same problem exist
side by side in one repository, and the weaker one is the user-facing one.

---

## 2. Trace against the target flow

| Target stage | Current implementation | Verdict |
|---|---|---|
| Multi-turn conversation | none — stateless HTTP, no session id, no history | **Missing** |
| Structured modeling intent | `ChangeIntent.ChangeSet` (MODIFY only); CREATE has no intent object at all | **Partial / absent for CREATE** |
| Intent-driven semantic retrieval | full-catalog scan + substring rank; embeddings only reorder | **Broken** |
| Candidate graph / context construction | flat list of ≤12 tables × ≤160 columns; no relationships, no graph | **Missing** |
| LLM modeling proposal | `plannerSystemPrompt`, 22 rules | Present |
| Deterministic DAG validation | `validateGraphPlan` — strong on topology/types, blind on semantics | **Partial** |
| Clarification or selective repair | CLARIFY → HTTP 409 that discards all state; repair = one whole-graph retry | **Broken** |
| User refinement | user retypes a fresh instruction against the canvas | **Degraded** |
| Incremental DAG revision | full graph regenerated, then diffed against a locked changeSet | **Simulated, not real** |
| Executable validation | `/datasets/candidate/preview` exists but the AI loop never calls it | **Disconnected** |
| Confirmation | client-side "apply" button | Present |
| Persisted model | `/datasets/validate` + normal save endpoints | Present |

---

## 3. Problem 1 — why the workflow is effectively single-turn

The single-turn behaviour is not an oversight in one function; it is enforced at four layers.

**3.1 The wire contract has no place to put a turn.**
`PlanRequest` is `{Instruction string; Current *GraphPlan}` (`model.go:79`). There is no
conversation id, no turn index, no prior-decision list, no answered-question list. The endpoint
is `POST .../ai/proposals` with no resource of its own — a proposal is a value, not an entity.

**3.2 The canvas is used as a substitute for conversation memory.**
`datasetAIRequestContext` (`dataset-ai.ts:231`) resolves exactly one baseline: the live canvas
if it exists, otherwise the staged proposal, otherwise nothing. This means every accumulated
decision must be re-derivable from the DAG's *shape*. Decisions that leave no structural trace
are lost every turn:

- "orders and refunds join on `order_no`, not `order_id`" — survives only as a join condition,
  with no record that the user chose it, so the next turn's LLM may re-propose the other key;
- "exclude test tenants" — if the user has not yet added the filter, gone;
- "this model is at order-line grain" — never captured anywhere (see §5.6);
- "do not use `dw_order_wide`, it is deprecated" — gone; the table is re-ranked into the
  candidate set on the very next request.

**3.3 The LLM receives no history — by explicit design.**
For MODIFY, `buildProviderRequest` sets `instruction = ""` (`service.go:1725`) with the comment
*"Natural-language interpretation is complete before this call."* The planner sees only
`{mode, current, changeSet, assets}`. The intent extractor sees `{instruction, current,
editContext, assets}` — one instruction, never a transcript. Neither call has a `messages`
history beyond system + one user turn (plus, on failure, a repair turn that is discarded).

**3.4 Clarification is a dead end.**
`extractChangeIntent` returns `ClarificationRequiredError` (`service.go:1208`), the handler maps
it to `409 DATASET_AI_CLARIFICATION_REQUIRED` (`http.go:159`), and the frontend renders it
through `datasetAIRequestIssue` as a generic red error card. There is no answer channel. The
question, the candidate component ids the model produced (`ChangeIntent.Candidates`), and the
partial intent are all thrown away. The user must compose a new full instruction that
anticipates the question. This is the single worst interaction defect in the system: the
model asked a question and the architecture cannot receive the answer.

**3.5 Consequence: cost scales with turns, quality does not.**
Every turn re-runs: full catalog scan, up to 12 `ListColumns` queries, the byte-fitting loop,
the intent LLM call, the planner LLM call. A "rename this group" request costs the same catalog
work and the same two LLM calls as building a five-table model from scratch.

---

## 4. Problem 2 — table discovery

### 4.1 Correcting the premise, precisely

The system does **not** put the entire catalog in the prompt. It does something subtly worse:
it *evaluates* the entire catalog with a weak ranker and then truncates by byte budget.

- `searchCatalogTables` (`service.go:1493`) pages `SearchTables` with
  `{Status: ACTIVE, ManagementStatus: ENABLED, EnrichedOnly: true}` and **no query, no domain,
  no data-source, no layer filter** — up to `maxCatalogCandidateTables = 1000` rows over up to
  5 round-trips, on every single request.
- `rankCatalogTables` (`service.go:1526`) scores by: +10000 if already on the canvas, +1000 if
  the table name appears as a substring of the instruction, +50 per matched token in a
  concatenated `name + description + tags` haystack. `meaningfulTokens` splits on whitespace
  and punctuation and adds Han bigrams — a bag-of-substrings, not retrieval.
- Top `12 - len(required)` tables are admitted. Each costs a `ListColumns` call.
- Columns start at 8 per table and expand round-robin in steps of 8 up to 160, and **every
  expansion step re-marshals the entire provider request** (`catalogFits` → `buildProviderRequest`
  → `json.Marshal`; for MODIFY it also builds the intent request). For 12 tables × ~20 expansion
  steps this is hundreds of full-payload serializations per user request.

So: full-catalog *scan* every request, naive lexical *ranking*, and a prompt that still contains
up to ~2000 columns of undifferentiated schema for the LLM to sift.

### 4.2 The embedding layer exists and is nearly unused

`internal/assetembedding` implements exactly the right primitive: hybrid table-first retrieval
with RRF fusion over vector and lexical ranks, then column ranking *within selected tables*
(`retriever.go:25`). `DATASET_AI_RETRIEVAL_MODE` defaults to `HYBRID`. But in `loadCatalog`:

- retrieval results only **reorder** `searched` (`rankCatalogTablesByRetrieval`, `service.go:1567`)
  — the 1000-row scan still happens first and still bounds the candidate set;
- retrieval is skipped entirely whenever the canvas contains only `dataset-version:*` nodes;
- `ColumnScores` are used only as a sort key inside `loadCatalogCandidate`, after the table set
  is already fixed.

The retriever cannot narrow, it can only re-sort. That is why it does not solve the problem.

### 4.3 The most valuable signals are captured and then discarded

`migrations/000008_metadata_assets.up.sql` stores `metadata_columns.is_primary_key`,
`is_foreign_key`, `is_unique`, and `metadata_tables.primary_key_columns`. The ingestion path
writes all of them (`datasource/metadata_repository.go:413`).

`internal/asset` never reads any of them — `asset.Column` (`asset/model.go:46`) has no key
fields, so `CatalogColumn` (`datasetai/model.go:336`) cannot have them either. The LLM is asked
to infer joins with **no primary key, no foreign key, no uniqueness, no row count, no usage
signal** — only `semanticType == "IDENTIFIER"` and `nullable`. The prompts compensate with prose
(planner rule 4, intent rules 17/24), which is exactly the wrong place to compensate.

### 4.4 Curated models are invisible to discovery

`VersionAwareAssetCatalog.SearchTables` delegates straight to the physical catalog
(`version_catalog.go:50`) — published DIM/DWD/DWS dataset versions are resolvable by id but
**not discoverable by search**. Combined with the guard at `service.go:1337`
(search is skipped when the canvas holds only dataset-version nodes), the result is:

- creating a new DWS/ADS model from an empty canvas: the LLM sees **only raw physical tables**
  and can never propose a published DWD or DIM;
- editing an existing DWS: the LLM sees **only the tables already on the canvas** and can never
  add a DIM.

The semantic layer the platform spends its whole DWD/DIM pipeline producing is unreachable from
the interactive modeler. This is the largest semantic-reuse gap in the system.

---

## 5. Additional findings

**F1 — The MODIFY planner call is largely vestigial.**
After the planner returns, the server runs seven materializers
(`materializeLockedComponentState`, `…ScalarChanges`, `…NodeTableMigrations`, `…FieldChanges`,
`…GraphStructure`, `…TransformRouting`, `preserveProtectedDatasetMetadata`) that rebuild the
graph from `current` + the locked changeSet, and then a diff validator rejects anything else.
`materializeLockedScalarChanges` even takes new names from `operation.ComponentName` — the
*intent* output — not from the planner. For a rename, a component removal, a rewire, or a
selected-column change, the planner contributes nothing, yet it is still invoked, still must
emit a complete valid graph, and can still fail validation and burn a repair round-trip. A
rename can fail because of a planner hallucination in an unrelated subtree.

**F2 — `edit_scope.go` is 3088 lines of change-scope enforcement.**
Combined with the two system prompts (52 numbered rules; the intent prompt skips rule 16, the
planner prompt skips rule 7 — deletions left holes, so the prompt is being used as a mutable
policy store). This is the cost of asking a free-form generator to produce a constrained edit
and then proving after the fact that it did. A patch-shaped contract removes most of it.

**F3 — No fan-out or cardinality analysis anywhere.**
`joinCardinalityForType` is a pure function of join type in both the frontend
(`datasets.ts:47`) and the DSL validator (`dsl.go:1715`), and `dsl.go:285` validates that
`cardinality` equals that function. `INNER → ONE_TO_ONE` is simply false. The field carries zero
information. `materializeDatasetAIPlan` sets `manualConfirmed: true` on every AI-generated join
(`dataset-ai.ts:353`), bypassing the human confirmation gate. A `SUM` after a 1:N join produces
silently multiplied totals and nothing in the stack can detect it.

**F4 — Grain is fabricated, not inferred.**
`materializeDatasetAIPlan` writes
`grainDescription: base.grainDescription.trim() || "每一行代表一条${plan.dataset.name}记录"` and
`grainKeys: [outputCodes[0]]` (`dataset-ai.ts:506`). The grain of the output model is a template
string and the first output column. Nothing in the pipeline establishes the grain of each source
before joining them, which is the precondition for detecting F3.

**F5 — Multi-hop join discovery is impossible by construction.**
The prompt requires all conditions of one join to use a single leaf-node pair
(`model.go:966`), and there is no relationship graph, so a path `A → B → C` can only be found if
the LLM guesses both hops from column names within the ≤12 admitted tables. Meanwhile
`internal/askdata/graph` already implements bounded multi-hop path resolution
(`MaxJoinHops = 4`, `MaxJoinPaths = 32`, Nebula primary + certified cache + Postgres fallback,
`graph/model.go:23`). It is not reachable from `datasetai`.

**F6 — Executable feedback is disconnected.**
`POST /api/v1/datasets/candidate/preview` exists (`dataset/http.go:346`) and the editor uses it
for the end-node preview. The AI loop never calls it. The system can produce a structurally
valid model that returns zero rows, or 40× the expected rows, and report success.

**F7 — Confidence is not represented.**
`Proposal` carries `Assumptions []string` and `Warnings []string` — prose. There is no
per-decision confidence, so the system cannot decide *which* decision was weak enough to
warrant a question. CLARIFY is a whole-request outcome, not a per-decision one, and it exists
only in MODIFY. **CREATE never asks a clarifying question at all** — a blank-canvas request
with an ambiguous entity silently produces a guess.

**F8 — Repair is whole-graph, not selective.**
On validation failure the service appends a prose instruction (`repairInstruction`) plus, if it
fits, the previous response, and asks for a complete regenerated graph
(`service.go:294`–`318`). One bad transform rule costs a full regeneration and re-validation of
the entire DAG. `repairInstruction` maps a reason code to a paragraph of Chinese guidance —
prose repair of a machine-checkable defect.

**F9 — Duplicated validation, three times over.**
`validateGraphPlan` (Go), `validateDesignerGraph` (TypeScript, `dataset-graph.ts`), and
`buildDatasetDSL` + `/datasets/validate` (Go, `dsl.go`) each independently check topology and
field availability, with different messages and different coverage. `materializeDatasetAIPlan`
throws seven distinct hand-written errors for conditions the server already validated.

**F10 — `ErrContextStale` fails the whole turn.**
`ensureCatalogFresh` (`service.go:1811`) compares `StructureHash` after generation and discards
everything on any drift. With a session model this becomes a per-node re-resolution.

**F11 — Progress events are cosmetic.**
`PlanProgressEvent` reports seven stages but carries no structured payload — no candidate table
list, no rejected candidates, no scores. The user cannot see *why* a table was chosen, and
therefore cannot correct retrieval, only the final DAG.

**F12 — `deriveCreateTransformRequirements` is a keyword matcher.**
CREATE-mode transform obligations are derived from string matching on the instruction
(`transform_requirements.go`), then enforced post-hoc by `validateTransformRequirements`. This
is intent extraction implemented as regex, in the one mode that has no intent object.

**F13 — 16-node / 32-component ceiling with no decomposition.**
`maxPlanNodes = 16`, `maxPlanComponents = 32`. There is no notion of building a model from
already-built sub-models, so the ceiling is a hard wall rather than a layering prompt.

**F14 — Up to four LLM calls per turn, each carrying the full catalog.**
intent + intent-repair + planner + planner-repair, each with the same ≤12 tables × ≤160 columns
payload and a 25 s timeout per phase (`defaultPlannerTimeout`), i.e. up to 50 s of model time
before the user sees an error.

---

## 6. Target architecture

Principle: **the LLM proposes; deterministic code retrieves, expands, validates, repairs, and
persists.** Every decision that can be checked against metadata or the warehouse must be made or
verified outside the model.

### 6.1 The modeling session becomes a first-class aggregate

New schema (`platform.dataset_modeling_sessions` and children):

```
dataset_modeling_sessions
  id, tenant_id, actor_id, dataset_id (nullable for new), layer, status,
  created_at, updated_at, record_version

dataset_modeling_turns
  id, session_id, turn_index, kind,           -- USER_REQUEST | ANSWER | SYSTEM_EVENT
  utterance, intent_id, proposal_id, created_at

dataset_modeling_intents                       -- structured, versioned, diffable (§6.2)
  id, session_id, turn_id, payload jsonb, superseded_by

dataset_modeling_revisions                     -- immutable DAG snapshots
  id, session_id, revision_no, parent_revision_id,
  plan jsonb, plan_hash, origin,               -- LLM | USER_EDIT | REPAIR | ROLLBACK
  validation jsonb, execution jsonb, created_at

dataset_modeling_decisions                     -- durable, machine-readable commitments
  id, session_id, kind,                        -- TABLE_CHOICE | JOIN_KEY | GRAIN | METRIC_DEF
                                               -- | EXCLUSION | SCOPE_FILTER
  subject jsonb, value jsonb, source,          -- USER_CONFIRMED | LLM_ASSUMED | DERIVED
  confidence, confirmed_at, revoked_at

dataset_modeling_questions                     -- open clarifications
  id, session_id, revision_id, kind, question, options jsonb,
  blocking bool, answered_with jsonb, answered_at
```

This makes the four things that are currently unrepresentable representable: *what was decided*,
*why*, *what is still open*, and *what the DAG looked like when it was decided*.

Retention: revisions and decisions are kept for the session's lifetime and pruned on dataset
publish; utterances are subject to the existing AI audit retention policy.

### 6.2 Structured modeling intent

Replace the CREATE path's raw instruction and generalize the MODIFY `ChangeSet` into one object
that both modes produce and that accumulates across turns:

```go
type ModelingIntent struct {
    SessionID   string
    TurnIndex   int
    Goal        string            // one-sentence restatement, for display only
    Entities    []BusinessEntity  // {term, role: FACT|DIMENSION|BRIDGE, confidence, resolvedTo}
    Measures    []MeasureIntent   // {term, aggregation, grainQualifier, confidence}
    Dimensions  []DimensionIntent // {term, granularity, confidence}
    Filters     []FilterIntent
    TimeRange   *TimeIntent
    TargetGrain *GrainIntent      // explicit, see §6.6
    Scope       ScopeFilter       // domain, layer, dataSource — deterministic pre-filters
    Edits       []EditIntent      // MODIFY only; the current ChangeOperation/FieldChange
    Unresolved  []Ambiguity       // {kind, subject, candidates, blocking}
}
```

Rules:
- Intent is produced by one LLM call whose **only** job is NL → this struct. It never sees a DAG
  and never emits one (the current `changeIntentSystemPrompt` boundary, generalized to CREATE).
- Intent is *merged* across turns, not rebuilt: turn N+1's intent is a patch over turn N's,
  with `Ambiguity` entries resolved by recorded answers.
- Confirmed `dataset_modeling_decisions` are injected as pre-resolved fields; the model is told
  they are settled and must not re-litigate them.

### 6.3 Intent-driven retrieval pipeline

Replace `loadCatalog` entirely with a staged, deterministic retriever
(new package `internal/modelingcontext`):

```
ModelingIntent
 → 1. deterministic scope filter   (tenant, domain, layer, dataSource, sensitivity, ACL)
 → 2. concept expansion            (business terms, aliases, dictionary — reuse
                                    internal/askdata/understanding/dictionary.go)
 → 3. candidate object retrieval   (hybrid RRF over BOTH physical tables AND published
                                    dataset versions — see §6.4)
 → 4. per-object column retrieval  (scoped to selected objects, ranked by intent role:
                                    keys, measures, dimensions, time)
 → 5. relationship expansion       (FK/PK graph + verified relationships + lineage +
                                    multi-hop paths, bounded — see §6.5)
 → 6. relevance ranking + budget   (score-ordered admission with an explicit floor)
 → 7. MinimalModelingContext       (objects, columns, relationships, grain facts, scores,
                                    and an explicit `expansionHandles` list)
```

Concrete changes required:

- **Push filters into the query.** `asset.Search` already carries `Query`, `DataSourceID`,
  `Tag`, `Visibility`. Stop calling `SearchTables` with an empty query; drive it from
  `Intent.Scope` + expanded concepts. Delete `maxCatalogCandidateTables = 1000` paging.
- **Make retrieval authoritative, not decorative.** The candidate set is the retriever's output,
  not a re-sort of a full scan. `HYBRID` becomes the only mode; `LEXICAL` remains the documented
  degradation when embeddings are unavailable, driven by `RetrievalResult.Degraded` rather than
  by config.
- **Surface PK/FK/unique.** Add `PrimaryKey`, `ForeignKey`, `Unique`, and
  `Table.PrimaryKeyColumns` to `asset.Column` / `asset.Table` and the store's SELECT list; the
  columns already exist. Propagate to `CatalogColumn`. This is a small change with the largest
  single quality impact in this document.
- **Add usage signal.** Rank with a `usage_count` derived from existing dataset-node references
  and query-run history; a table used by 40 published models is a better candidate than a
  same-named one used by none.
- **Bounded expansion.** `MinimalModelingContext` carries `expansionHandles` — object ids the
  retriever scored just below the cut. The planner may request one expansion round
  ("I need a table containing customer region") which is served deterministically from the
  handles, not by widening the initial prompt. This is how the context stays narrow *and* can
  grow when necessary.
- **Cache per session.** Retrieval is keyed by `(session, intent_hash, catalog_watermark)`.
  A refinement turn that does not change entities reuses the previous context, which removes the
  per-turn full-catalog cost entirely.

### 6.4 Curated models become retrievable

- `VersionAwareAssetCatalog.SearchTables` must union physical tables with published dataset
  versions rather than delegating (`version_catalog.go:50`).
- Index dataset versions in `assetembedding` alongside physical tables, with layer as a facet.
- Rank by layer preference derived from the target layer: building a DWS should prefer published
  DWD/DIM over raw ODS; building a DWD should prefer ODS. Replace the current all-or-nothing
  guard at `service.go:1337` with this ranking rule.
- Expose existing metrics/dimensions/terms from `internal/askdata/registry` as retrievable
  semantic assets so a metric definition is reused rather than re-derived.

### 6.5 Relationship and join discovery

Build a `RelationshipGraph` from, in descending order of trust:

1. verified relationships in `askdata/registry/relationship.go` (certified, human-approved);
2. joins used by existing published dataset versions (an FK proven by prior modeling);
3. declared physical FKs (`is_foreign_key` + referenced table);
4. PK/unique + name/type match heuristics (inferred, must be labelled as such).

Then:
- run bounded k-shortest-path search (`k ≤ 16`, `hops ≤ 4`) between the entities in the intent —
  reuse the algorithm in `internal/askdata/graph`, not the Nebula dependency;
- annotate each edge with **measured or declared cardinality**, never with a function of join
  type; delete `joinCardinalityForType` from both the frontend and `dsl.go` and store the real
  value;
- give the LLM *ranked candidate join paths* to choose among, with provenance and cardinality.
  Path *discovery* is a graph algorithm; path *selection* among a handful of scored candidates
  is a reasoning task. That is the correct split.

### 6.6 Grain, first-class and before joins

Add to the context, per candidate object, a `GrainFact`:
`{keys []string, source: DECLARED_PK | UNIQUE_INDEX | MEASURED | ASSUMED, confidence}`.
`MEASURED` comes from a cheap `COUNT(*) vs COUNT(DISTINCT k)` probe on the candidate preview
runtime, cached per `(table, structure_hash)`.

Then make grain a validation gate rather than a template string:
- `ModelingIntent.TargetGrain` is explicit and, if absent for an aggregating model, is a
  **blocking** clarification;
- a join whose right side is not unique on the join key is a **fan-out edge**; if any additive
  measure from the left side is aggregated downstream of it, the validator rejects the plan with
  `FAN_OUT_RISK` and offers two deterministic repairs: pre-aggregate the right branch, or switch
  the measure to `COUNT_DISTINCT` on a key;
- delete the fabricated `grainDescription` / `grainKeys` defaults in `dataset-ai.ts:506` and
  derive them from the validated grain.

### 6.7 Incremental editing: patches, not regeneration

Replace "regenerate the whole graph, then prove it only changed what was allowed" with a real
patch contract:

```go
type GraphPatch struct {
    BaseRevisionID string
    Ops            []PatchOp   // ADD_NODE, REMOVE_NODE, SET_NODE_COLUMNS, ADD_JOIN,
                               // SET_JOIN_CONDITIONS, REWIRE_INPUT, ADD_TRANSFORM,
                               // SET_GROUP_DIMENSIONS, SET_END_OUTPUTS, RENAME, …
}
```

- The LLM emits a `GraphPatch`, not a `GraphPlan`. Output size drops from a whole DAG to a few
  operations; hallucination surface drops with it.
- The server applies the patch to the base revision deterministically and produces revision N+1.
- Patches whose every op is deterministic (rename, remove, rewire, toggle a selected column,
  change an aggregation) are applied **without any planner call at all** — this deletes F1 and
  removes the second LLM call from the majority of refinement turns.
- The seven `materializeLocked*` functions collapse into one patch applicator. Most of
  `edit_scope.go`'s post-hoc diff enforcement becomes unnecessary: scope is enforced by the shape
  of `PatchOp`, not proven afterwards.
- User canvas edits between turns are recorded as `origin: USER_EDIT` revisions, so the LLM's
  next base is exactly what the user sees — and the "canvas changed during generation"
  fingerprint failure (`DatasetCenterPage.tsx:3844`) becomes a rebase instead of a discard.

### 6.8 Validation and selective repair

Keep `validateGraphPlan`'s topology and type checks; restructure the output:

```go
type ValidationReport struct {
    Findings []Finding   // {code, severity, scope: ComponentRef, detail, repairs []RepairAction}
}
```

- Findings are scoped to components, so repair is a **patch against the offending subtree**, not
  a whole-graph regeneration.
- Deterministic repairs (`RepairAction`) are applied without a model call: fix a column-case
  mismatch, drop an orphan component, insert a required `DATE_FORMAT` transform before a group,
  rewire a consumer past a removed component. Only findings with no deterministic repair reach
  the model, and then only with the offending subtree in context.
- New semantic validators, all deterministic: `FAN_OUT_RISK` (§6.6), `AMBIGUOUS_GRAIN`,
  `NON_ADDITIVE_AGGREGATION` (reuse `askdata/registry/additivity_*`), `DUPLICATED_FACT`
  (same fact table reachable by two branches), `UNUSED_JOIN_KEY`, `CROSS_DOMAIN_JOIN`.
- Delete the duplicated TS validator (F9); the client renders the server's `ValidationReport`.

### 6.9 Clarification as a protocol

- `Ambiguity` entries in the intent, and validation findings marked `blocking`, become rows in
  `dataset_modeling_questions` — **not** an HTTP error.
- The turn still returns `200` with a proposal where possible: a partial DAG plus open questions
  is far more useful than a 409. Only a question that makes the whole model meaningless blocks.
- Questions carry `options` (candidate table ids, candidate join paths, candidate grains) so the
  UI renders choices, not a text box. Answering posts to the session and creates an `ANSWER` turn.
- An answer writes a `dataset_modeling_decisions` row. Decisions are injected into every
  subsequent intent extraction, which is what stops the model re-asking and re-guessing.
- Add clarification to CREATE (F7): an entity that resolves to more than one table above a
  confidence floor is a question, not a coin flip.

### 6.10 Executable feedback in the loop

After validation, before confirmation, run the candidate through the existing
`PreviewCandidate` path with `LIMIT` and a row-count probe, and record it on the revision:

```
execution: { status, rowCount, nullRateByOutput, distinctKeyRatio, elapsedMs, error }
```

Feed the result back as validation findings: `EMPTY_RESULT` (likely wrong join or over-filtered),
`GRAIN_MISMATCH` (`rowCount > expected × threshold` — the empirical fan-out check),
`HIGH_NULL_RATE` on a join key (wrong key or wrong join type). These become the next turn's
context, which is what closes the loop the target flow asks for.

### 6.11 Responsibility split

| Decision | Owner |
|---|---|
| Scope filtering (tenant, domain, layer, ACL) | deterministic code |
| Concept/term expansion | dictionary + embeddings |
| Candidate table & column retrieval | retrieval + ranking |
| Join path enumeration | graph algorithm |
| Cardinality & grain | metadata + measured probe |
| Fan-out, cycles, orphans, type compatibility | validators |
| Patch application, rewiring, field propagation | deterministic code |
| Naming, codes, layout | deterministic code |
| **Which entities the user meant** | **LLM** |
| **Which of N ranked join paths fits the business question** | **LLM** |
| **Which measure/aggregation expresses the intent** | **LLM** |
| **What to ask when confidence is low** | **LLM (from structured ambiguities)** |
| **Explaining the proposal in business language** | **LLM** |

---

## 7. Migration plan

Each phase is independently shippable and independently valuable.

**Phase 1 — unblock retrieval (no session work).**
Surface PK/FK/unique in `asset`; make dataset versions searchable; push intent scope into
`asset.Search`; make `assetembedding` authoritative and delete the 1000-row scan; cache context
per request. *Outcome: better candidates, large latency and token reduction, no contract change.*

**Phase 2 — session persistence and clarification.**
Add the session/turn/revision/decision/question tables and
`POST /api/v1/datasets/modeling-sessions`, `.../turns`, `.../answers`. Convert
`ClarificationRequiredError` from a 409 to a question row. Frontend: proposal panel becomes a
turn list with an answer affordance. *Outcome: genuine multi-turn; the clarification dead end
disappears.*

**Phase 3 — structured intent for both modes.**
Generalize `ChangeIntent` to `ModelingIntent`; delete
`deriveCreateTransformRequirements`'s keyword matching in favour of intent-derived obligations;
merge intents across turns; inject confirmed decisions. *Outcome: CREATE gains an intent object
and clarification.*

**Phase 4 — patch contract.**
Introduce `GraphPatch`; route deterministic patches around the planner entirely; collapse the
seven materializers into one applicator; retire the bulk of `edit_scope.go`. *Outcome: F1, F2,
F8 resolved; refinement turns become fast and cheap.*

**Phase 5 — relationship graph, grain, and execution feedback.**
Build `RelationshipGraph`; add k-shortest-path candidate joins; add `GrainFact` probes; add the
fan-out and additivity validators; wire `PreviewCandidate` into the loop; delete
`joinCardinalityForType` and the fabricated grain defaults. *Outcome: correctness guarantees the
current architecture cannot express.*

---

## 8. What gets deleted

- `searchCatalogTables`' unfiltered 1000-row paging and `rankCatalogTables`' substring scoring.
- The byte-budget expansion loop's repeated whole-request marshalling (replace with an estimator).
- `rankCatalogTablesByRetrieval` (retrieval becomes primary, not a re-sort).
- Six of the seven `materializeLocked*` functions.
- The majority of `edit_scope.go`'s post-hoc diff proof.
- `joinCardinalityForType` (frontend, `dsl.go`, and the validator that enforces the tautology).
- The fabricated `grainDescription` / `grainKeys` defaults in `materializeDatasetAIPlan`.
- `validateDesignerGraph`'s duplication of server topology rules.
- Roughly half of the 52 numbered prompt rules — the ones restating constraints that become
  structurally unexpressible under the patch contract and the retrieval context.

---

## 9. Risks and open questions

1. **Nebula dependency.** `askdata/graph` reuse should take the algorithm and the Postgres
   fallback, not the Nebula requirement. Confirm the fallback planner is sufficient standalone.
2. **Grain probes cost warehouse queries.** Cache on `(table, structure_hash)` and make probing
   opt-in per data source; declared PK/unique must be preferred when present.
3. **Session storage growth.** Revisions are full DAG snapshots; store deltas beyond a retention
   window, or prune on publish.
4. **Two modeling architectures.** Phase 5 makes the interactive path strictly more capable than
   `dwd_modeling_planner.go`'s ad-hoc staging. Decide whether the batch path adopts the same
   session/intent/patch primitives or stays separate; keeping both indefinitely is the current
   cost being paid twice.
5. **Prompt-version migration.** `PromptVersion`/`IntentPromptVersion` are recorded on
   `ai_requests`; each phase must bump them so audit and evaluation suites can segment results.
