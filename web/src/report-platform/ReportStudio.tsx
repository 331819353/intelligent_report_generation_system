import { useEffect, useMemo, useState } from "react";
import { Provider, shallowEqual, useDispatch, useSelector, useStore } from "react-redux";
import {
  ArrowCounterClockwise,
  ArrowUUpRight,
  CheckCircle,
  FloppyDisk,
  Play,
  UploadSimple,
  WarningCircle,
} from "@phosphor-icons/react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { AppShell } from "../components/AppShell";
import { RequestError } from "../lib/api";
import { createCard } from "./template";
import { PropertyPanel } from "./PropertyPanel";
import { ReportRenderer, ReportRuntimeModal } from "./ReportRenderer";
import { executeReportInteraction, useReportQueries } from "./query-client";
import { reportCardRegistry } from "./registry";
import { validateReportDefinition, validateReportForPublish } from "./schema";
import {
  commitReportEdit,
  createReportEditorStore,
  redoReportEdit,
  reportEditorActions,
  undoReportEdit,
  type ReportEditorState,
  type ReportEditorStore,
} from "./editor-store";
import {
  createIdempotencyKey,
  createReportDraft,
  getReportDraft,
  publishReport,
  saveReportDraft,
  validatePublication,
  type PublishedReportVersion,
  type ReportDraftChange,
  type ReportDraftRecord,
  type ReportPublicationIssue,
} from "./api";
import { createReportDefinition } from "./template";
import type {
  CardType,
  GlobalFilterDefinition,
  ReportBreakpoint,
  ReportCardDefinition,
  ReportDefinition,
  ReportGrid,
  ReportInteractionContext,
} from "./types";

const NEW_REPORT_IDS = new Set(["draft", "new", "demo"]);
type Phase =
  | "loading"
  | "ready"
  | "saving"
  | "publishing"
  | "conflict"
  | "error"
  | "readonly";
type LoadedState = {
  definition: ReportDefinition;
  record?: ReportDraftRecord;
  pendingChanges: ReportDraftChange[];
  warnings: string[];
};

export function ReportStudioPage() {
  const { reportId = "draft" } = useParams();
  const startsNew = NEW_REPORT_IDS.has(reportId);
  const [loaded, setLoaded] = useState<LoadedState>(() => ({
    definition: createReportDefinition(),
    pendingChanges: [],
    warnings: [],
  }));
  const [phase, setPhase] = useState<Phase>(startsNew ? "ready" : "loading");
  const [error, setError] = useState("");

  useEffect(() => {
    if (startsNew) return;
    let active = true;
    void getReportDraft(reportId)
      .then((result) => {
        if (!active) return;
        setLoaded({
          definition: result.record.definition,
          record: result.record,
          pendingChanges: result.migrationChange
            ? [result.migrationChange]
            : [],
          warnings: result.migrationWarnings,
        });
        setPhase(result.record.capabilities.edit ? "ready" : "readonly");
      })
      .catch((reason) => {
        if (active) {
          setError(
            reason instanceof Error ? reason.message : "报告草稿加载失败",
          );
          setPhase("error");
        }
      });
    return () => {
      active = false;
    };
  }, [reportId, startsNew]);

  const store = useMemo(
    () => createReportEditorStore(loaded.definition, loaded.pendingChanges),
    [loaded.definition, loaded.pendingChanges],
  );
  return (
    <Provider store={store}>
      <StudioWorkspace
        initialRecord={loaded.record}
        initialWarnings={loaded.warnings}
        phase={phase}
        error={error}
        setPhase={setPhase}
        setError={setError}
      />
    </Provider>
  );
}

function StudioWorkspace({
  initialRecord,
  initialWarnings,
  phase,
  error,
  setPhase,
  setError,
}: {
  initialRecord?: ReportDraftRecord;
  initialWarnings: string[];
  phase: Phase;
  error: string;
  setPhase: (phase: Phase) => void;
  setError: (message: string) => void;
}) {
  const navigate = useNavigate();
  const store = useStore() as ReportEditorStore;
  const dispatch = useDispatch<typeof store.dispatch>();
  const state = useSelector(
    (value: ReportEditorState) => ({
      definition: value.definition,
      pendingChanges: value.pendingChanges,
      breakpoint: value.breakpoint,
      selectedCardId: value.selectedCardId,
      past: value.past,
      future: value.future,
    }),
    shallowEqual,
  );
  const [record, setRecord] = useState(initialRecord);
  const [publicationIssues, setPublicationIssues] = useState<
    ReportPublicationIssue[]
  >([]);
  const [published, setPublished] = useState<PublishedReportVersion>();
  const [filters, setFilters] = useState<Record<string, unknown>>(() =>
    Object.fromEntries(
      state.definition.globalFilters.map((filter) => [
        filter.id,
        filter.defaultValue,
      ]),
    ),
  );
  const [interactionContext, setInteractionContext] = useState<
    Record<string, ReportInteractionContext>
  >({});
  const [modalReportId, setModalReportId] = useState<string>();
  const dirty = !record || state.pendingChanges.length > 0;
  const scope =
    record && !dirty
      ? {
          kind: "draft" as const,
          reportId: record.id,
          revision: record.revision,
        }
      : undefined;
  const queryState = useReportQueries(
    scope,
    state.definition,
    filters,
    interactionContext,
  );
  const canEdit =
    phase !== "readonly" && phase !== "loading" && phase !== "error";
  const validation = validateReportDefinition(state.definition);

  useEffect(() => {
    if (!dirty) return;
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  async function save(): Promise<ReportDraftRecord | undefined> {
    if (!canEdit || phase === "saving" || phase === "publishing") return record;
    if (!validation.definition) {
      setError(
        validation.errors
          .map((issue) => `${issue.path}: ${issue.reason}`)
          .join("；"),
      );
      return undefined;
    }
    setPhase("saving");
    setError("");
    const submitted = [...state.pendingChanges];
    try {
      const saved = record
        ? await saveReportDraft(
            record.id,
            record.revision,
            state.definition,
            submitted,
            createIdempotencyKey(),
          )
        : await createReportDraft(state.definition, createIdempotencyKey());
      setRecord(saved);
      dispatch(
        reportEditorActions.acknowledge(
          submitted.map((change) => change.clientOperationId),
        ),
      );
      if (!record) {
        dispatch(reportEditorActions.replaceFromServer(saved.definition));
        navigate(`/report-studio/${encodeURIComponent(saved.id)}`, {
          replace: true,
        });
      }
      setPhase(saved.capabilities.edit ? "ready" : "readonly");
      return saved;
    } catch (reason) {
      if (reason instanceof RequestError && reason.status === 409)
        setPhase("conflict");
      else setPhase("ready");
      setError(reason instanceof Error ? reason.message : "保存失败");
      return undefined;
    }
  }

  async function publish() {
    const clientIssues = validateReportForPublish(state.definition);
    if (clientIssues.length) {
      setPublicationIssues(
        clientIssues.map((issue) => ({
          level: "error",
          code: "REPORT_SEMANTIC_INVALID",
          path: issue.path,
          message: issue.reason,
        })),
      );
      return;
    }
    const saved = dirty ? await save() : record;
    if (!saved) return;
    setPhase("publishing");
    setPublicationIssues([]);
    setError("");
    try {
      const checked = await validatePublication(saved.id, saved.revision);
      if (!checked.valid) {
        setPublicationIssues(checked.issues);
        setPhase("ready");
        return;
      }
      const version = await publishReport(
        saved.id,
        saved.revision,
        createIdempotencyKey(),
      );
      setPublished(version);
      setPhase("ready");
    } catch (reason) {
      if (reason instanceof RequestError && reason.detail.details)
        setPublicationIssues(
          reason.detail.details.map((issue) => ({
            level: "error",
            code: issue.code || "REPORT_INVALID",
            path: issue.path,
            message: issue.reason || issue.message || "发布校验失败",
          })),
        );
      setError(reason instanceof Error ? reason.message : "发布失败");
      setPhase("ready");
    }
  }

  function commit(
    definition: ReportDefinition,
    operation: Parameters<typeof commitReportEdit>[2],
    target?: Parameters<typeof commitReportEdit>[3],
  ) {
    if (canEdit) commitReportEdit(store, definition, operation, target);
  }
  function addCard(type: CardType, anchor?: Pick<ReportGrid, "x" | "y">) {
    const card = createCard(type);
    for (const breakpoint of ["lg", "md", "sm"] as ReportBreakpoint[]) {
      const y = nextAvailableY(state.definition.cards, breakpoint);
      card.layout[breakpoint] = {
        ...card.layout[breakpoint],
        x:
          breakpoint === "lg"
            ? Math.min(anchor?.x ?? 0, 12 - card.layout[breakpoint].w)
            : 0,
        y: breakpoint === "lg" ? Math.max(anchor?.y ?? y, y) : y,
      };
    }
    commit(
      { ...state.definition, cards: [...state.definition.cards, card] },
      "CARD_CREATE",
      { cardId: card.id },
    );
    dispatch(reportEditorActions.selectCard(card.id));
  }
  function updateCard(card: ReportCardDefinition) {
    commit(
      {
        ...state.definition,
        cards: state.definition.cards.map((item) =>
          item.id === card.id ? card : item,
        ),
      },
      "CARD_CONFIG_UPDATE",
      { cardId: card.id },
    );
  }
  function updateLayout(cardId: string, grid: ReportGrid) {
    const card = state.definition.cards.find((item) => item.id === cardId);
    if (!card || sameGrid(card.layout[state.breakpoint], grid)) return;
    const cards = state.definition.cards.map((item) =>
      item.id === cardId
        ? { ...item, layout: { ...item.layout, [state.breakpoint]: grid } }
        : item,
    );
    commit({ ...state.definition, cards }, "CARD_LAYOUT_UPDATE", { cardId });
  }
  function deleteCard(cardId: string) {
    commit(
      {
        ...state.definition,
        cards: state.definition.cards.filter((card) => card.id !== cardId),
      },
      "CARD_DELETE",
      { cardId },
    );
    dispatch(reportEditorActions.selectCard(undefined));
  }

  return (
    <AppShell>
      <main className="rpt-studio-page">
        <header className="rpt-studio-toolbar">
          <div>
            <span className="eyebrow">REPORT STUDIO · DSL 1.0.0</span>
            <input
              aria-label="报表标题"
              value={state.definition.report.title}
              disabled={!canEdit}
              onChange={(event) =>
                commit(
                  {
                    ...state.definition,
                    report: {
                      ...state.definition.report,
                      title: event.target.value,
                      name: event.target.value,
                    },
                  },
                  "REPORT_SETTINGS_UPDATE",
                )
              }
            />
          </div>
          <div className="rpt-breakpoints">
            {(["lg", "md", "sm"] as ReportBreakpoint[]).map((item) => (
              <button
                type="button"
                className={state.breakpoint === item ? "active" : ""}
                onClick={() =>
                  dispatch(reportEditorActions.selectBreakpoint(item))
                }
                key={item}
              >
                {item.toUpperCase()}
              </button>
            ))}
          </div>
          <div className="rpt-toolbar-actions">
            <button
              type="button"
              disabled={!canEdit || !state.past.length}
              onClick={() => undoReportEdit(store)}
              title="撤销"
            >
              <ArrowCounterClockwise size={17} />
            </button>
            <button
              type="button"
              disabled={!canEdit || !state.future.length}
              onClick={() => redoReportEdit(store)}
              title="重做"
            >
              <ArrowUUpRight size={17} />
            </button>
            <button
              type="button"
              className="quiet-button"
              disabled={!canEdit || !dirty || phase === "saving"}
              onClick={() => void save()}
            >
              <FloppyDisk size={17} />
              {phase === "saving" ? "保存中" : "保存"}
            </button>
            <button
              type="button"
              className="primary-button"
              disabled={!record || dirty || phase === "publishing"}
              onClick={() => void publish()}
            >
              <UploadSimple size={17} />
              {phase === "publishing" ? "发布中" : "发布"}
            </button>
            {published && (
              <Link
                className="rpt-runtime-link"
                to={`/reports/${published.reportId}?version=${published.version}`}
              >
                <Play size={16} />
                查看 V{published.version}
              </Link>
            )}
          </div>
        </header>
        {(error ||
          initialWarnings.length > 0 ||
          publicationIssues.length > 0 ||
          phase === "conflict") && (
          <section
            className={`rpt-studio-message${error || publicationIssues.length ? " error" : ""}`}
          >
            <WarningCircle size={18} />
            <div>
              {phase === "conflict" && (
                <strong>
                  草稿已被其他会话更新，请刷新后重新应用当前修改。
                </strong>
              )}
              {error && <strong>{error}</strong>}
              {initialWarnings.map((warning) => (
                <span key={warning}>{warning}</span>
              ))}
              {publicationIssues.map((issue) => (
                <span key={`${issue.path}-${issue.code}`}>
                  {issue.path}：{issue.message}
                </span>
              ))}
            </div>
          </section>
        )}
        <div className="rpt-studio-shell">
          <aside className="rpt-card-palette">
            <div>
              <strong>业务卡片</strong>
              <span>拖入画布或点击添加</span>
            </div>
            {reportCardRegistry.list().map((plugin) => (
              <button
                type="button"
                draggable={canEdit}
                onDragStart={(event) =>
                  event.dataTransfer.setData(
                    "application/x-report-card",
                    plugin.type,
                  )
                }
                onClick={() => addCard(plugin.type)}
                disabled={!canEdit}
                key={plugin.type}
              >
                <b>{plugin.label}</b>
                <small>{plugin.description}</small>
              </button>
            ))}
          </aside>
          <section className="rpt-studio-canvas">
            {phase === "loading" ? (
              <div className="rpt-loading">正在加载 Report DSL…</div>
            ) : phase === "error" ? (
              <div className="rpt-loading error">{error}</div>
            ) : (
              <>
                <div className="rpt-query-status">
                  {queryState.loading ? (
                    "正在批量查询…"
                  ) : queryState.error ? (
                    `查询失败：${queryState.error}`
                  ) : queryState.results &&
                    Object.keys(queryState.results).length ? (
                    <>
                      <CheckCircle size={14} />
                      已返回 {Object.keys(queryState.results).length} 张卡片
                    </>
                  ) : (
                    "保存后执行真实数据预览"
                  )}
                </div>
                <ReportRenderer
                  definition={state.definition}
                  mode="designer"
                  breakpoint={state.breakpoint}
                  results={queryState.results}
                  filters={filters}
                  selectedCardId={state.selectedCardId}
                  onFilterChange={(id, value) =>
                    setFilters((current) => ({ ...current, [id]: value }))
                  }
                  onInteraction={(event) =>
                    executeReportInteraction(
                      state.definition,
                      event,
                      (cardId, value) =>
                        setInteractionContext((current) => ({
                          ...current,
                          [cardId]: value,
                        })),
                      { openModal: setModalReportId },
                    )
                  }
                  onSelectCard={(id) =>
                    dispatch(reportEditorActions.selectCard(id))
                  }
                  onLayoutChange={updateLayout}
                  onAddCard={addCard}
                />
              </>
            )}
          </section>
          <aside className="rpt-studio-properties">
            <PropertyPanel
              definition={state.definition}
              selectedCardId={state.selectedCardId}
              onReportChange={(report) =>
                commit(
                  { ...state.definition, report },
                  "REPORT_SETTINGS_UPDATE",
                )
              }
              onCardChange={updateCard}
              onFiltersChange={(globalFilters: GlobalFilterDefinition[]) =>
                commit({ ...state.definition, globalFilters }, "FILTER_UPDATE")
              }
              onDeleteCard={deleteCard}
            />
          </aside>
        </div>
      </main>
      {modalReportId && (
        <ReportRuntimeModal
          reportId={modalReportId}
          onClose={() => setModalReportId(undefined)}
        />
      )}
    </AppShell>
  );
}

function nextAvailableY(
  cards: ReportCardDefinition[],
  breakpoint: ReportBreakpoint,
) {
  return cards.reduce(
    (maximum, card) =>
      Math.max(
        maximum,
        card.layout[breakpoint].y + card.layout[breakpoint].h + 2,
      ),
    0,
  );
}
function sameGrid(left: ReportGrid, right: ReportGrid) {
  return (
    left.x === right.x &&
    left.y === right.y &&
    left.w === right.w &&
    left.h === right.h
  );
}
