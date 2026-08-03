import { useEffect, useState } from "react";
import { ArrowLeft, DownloadSimple } from "@phosphor-icons/react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  getReportManifest,
  listPublishedVersions,
  loadPublishedDefinition,
  type PublishedReportVersion,
} from "../report-platform/api";
import {
  executeReportInteraction,
  useReportQueries,
} from "../report-platform/query-client";
import {
  ReportRenderer,
  ReportRuntimeModal,
  useReportBreakpoint,
} from "../report-platform/ReportRenderer";
import type {
  ReportDefinition,
  ReportInteractionContext,
} from "../report-platform/types";

type RuntimeState =
  | { key: string; phase: "loading" }
  | {
      key: string;
      phase: "ready";
      definition: ReportDefinition;
      version: PublishedReportVersion;
    }
  | { key: string; phase: "error"; message: string };

export function ViewerPage() {
  const navigate = useNavigate();
  const { reportId = "" } = useParams();
  const [searchParams] = useSearchParams();
  const requestedVersion = searchParams.get("version");
  const loadKey = `${reportId}:${requestedVersion ?? "current"}`;
  const [state, setState] = useState<RuntimeState>({
    key: loadKey,
    phase: "loading",
  });
  const current =
    state.key === loadKey ? state : { key: loadKey, phase: "loading" as const };

  useEffect(() => {
    const controller = new AbortController();
    void loadRuntimeReport(reportId, requestedVersion, controller.signal)
      .then((result) => setState({ key: loadKey, phase: "ready", ...result }))
      .catch((error) => {
        if (!controller.signal.aborted)
          setState({
            key: loadKey,
            phase: "error",
            message:
              error instanceof Error ? error.message : "报告运行时加载失败",
          });
      });
    return () => controller.abort();
  }, [loadKey, reportId, requestedVersion]);

  return (
    <main className="viewer-page">
      <header className="viewer-header">
        <button
          className="viewer-back"
          type="button"
          aria-label="返回工作台"
          onClick={() => navigate("/admin")}
        >
          <ArrowLeft size={18} />
        </button>
        <div>
          <span className="eyebrow">
            经营分析中心
            {current.phase === "ready" ? ` · V${current.version.version}` : ""}
          </span>
          <h1>
            {current.phase === "ready"
              ? current.definition.report.title
              : "在线报告"}
          </h1>
        </div>
        <div>
          <button className="quiet-button" disabled={current.phase !== "ready"} title="使用浏览器打印为 PDF" onClick={() => window.print()}>
            <DownloadSimple aria-hidden="true" size={17} />
            导出 PDF
          </button>
        </div>
      </header>
      <section className="viewer-canvas">
        {current.phase === "loading" && (
          <RuntimeStatus
            title="正在校验发布制品"
            detail="加载 Manifest、验证 SHA-256 并初始化共享 Renderer…"
          />
        )}
        {current.phase === "error" && (
          <RuntimeStatus
            error
            title="报告暂时无法打开"
            detail={current.message}
            action={() =>
              navigate(`/report-studio/${encodeURIComponent(reportId)}`)
            }
          />
        )}
        {current.phase === "ready" && (
          <PublishedRuntime
            reportId={reportId}
            definition={current.definition}
            version={current.version.version}
          />
        )}
      </section>
    </main>
  );
}

function PublishedRuntime({
  reportId,
  definition,
  version,
}: {
  reportId: string;
  definition: ReportDefinition;
  version: number;
}) {
  const breakpoint = useReportBreakpoint(definition);
  const [filters, setFilters] = useState<Record<string, unknown>>(() =>
    Object.fromEntries(
      definition.globalFilters.map((filter) => [
        filter.id,
        filter.defaultValue,
      ]),
    ),
  );
  const [interactionContext, setInteractionContext] = useState<
    Record<string, ReportInteractionContext>
  >({});
  const [modalReportId, setModalReportId] = useState<string>();
  const queries = useReportQueries(
    { kind: "published", reportId, version },
    definition,
    filters,
    interactionContext,
  );
  return (
    <div className="rpt-published-runtime">
      {(queries.loading || queries.error) && (
        <div
          className={`rpt-runtime-query-state${queries.error ? " error" : ""}`}
        >
          {queries.loading ? "正在加载卡片数据…" : queries.error}
        </div>
      )}
      <ReportRenderer
        definition={definition}
        mode="runtime"
        breakpoint={breakpoint}
        results={queries.results}
        filters={filters}
        onFilterChange={(id, value) =>
          setFilters((current) => ({ ...current, [id]: value }))
        }
        onInteraction={(event) =>
          executeReportInteraction(
            definition,
            event,
            (cardId, value) =>
              setInteractionContext((current) => ({
                ...current,
                [cardId]: value,
              })),
            { openModal: setModalReportId },
          )
        }
      />
      {modalReportId && (
        <ReportRuntimeModal
          reportId={modalReportId}
          onClose={() => setModalReportId(undefined)}
        />
      )}
    </div>
  );
}

function RuntimeStatus({
  title,
  detail,
  error,
  action,
}: {
  title: string;
  detail: string;
  error?: boolean;
  action?: () => void;
}) {
  return (
    <div
      className={`report-runtime-state${error ? " report-runtime-state--error" : ""}`}
      role={error ? "alert" : "status"}
    >
      <strong>{title}</strong>
      <span>{detail}</span>
      {action && (
        <button type="button" onClick={action}>
          返回设计器
        </button>
      )}
    </div>
  );
}

async function loadRuntimeReport(
  reportId: string,
  requestedVersion: string | null,
  signal: AbortSignal,
): Promise<{ definition: ReportDefinition; version: PublishedReportVersion }> {
  if (!reportId) throw new Error("缺少报告标识");
  const versions = await listPublishedVersions(reportId);
  const versionNo =
    requestedVersion === null ? undefined : Number(requestedVersion);
  if (
    requestedVersion !== null &&
    (!Number.isInteger(versionNo) || Number(versionNo) < 1)
  )
    throw new Error("请求的报告版本无效");
  const version =
    versionNo === undefined
      ? versions.items.find((item) => item.current)
      : versions.items.find((item) => item.version === versionNo);
  if (!version)
    throw new Error(
      versionNo === undefined
        ? "报告尚未发布在线版本"
        : `报告版本 V${versionNo} 不存在`,
    );
  const manifest = await getReportManifest(reportId, version.version);
  const definition = await loadPublishedDefinition(manifest, signal);
  return { definition, version };
}
