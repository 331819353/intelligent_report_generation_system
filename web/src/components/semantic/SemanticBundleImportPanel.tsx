import { useCallback, useEffect, useRef, useState } from "react";
import {
  ArrowsClockwise,
  CheckCircle,
  CloudArrowUp,
  DownloadSimple,
  FileArrowDown,
  WarningCircle,
  X,
} from "@phosphor-icons/react";

import { AppButton } from "../AppButton";
import { semanticAPI } from "../../lib/semantic";
import {
  bundleExportAssetTypes,
  resolutionLabel,
  semanticBundleAPI,
  type BundleImportReport,
  type BundleImportRow,
} from "../../lib/semantic-bundle";

// SemanticBundleImportPanel 是 semantic-bundle/v1 的导入向导：
// 上传 → 四层校验（自动） → 逐行裁决（新建/更新/未变化/失败） → 提交 DRAFT
// → 检索就绪派生状态。批在索引就绪前不会被展示成“完成”。
type PanelProps = {
  domainId: string;
  onClose: () => void;
  onCommitted?: () => void;
};

type Phase =
  | "idle"
  | "uploading"
  | "validating"
  | "reviewing"
  | "committing"
  | "committed"
  | "failed";

const POLL_INTERVAL_MS = 2_000;
const MAX_VISIBLE_ROWS = 200;

export function SemanticBundleImportPanel({ domainId, onClose, onCommitted }: PanelProps) {
  const [phase, setPhase] = useState<Phase>("idle");
  const [file, setFile] = useState<File | null>(null);
  const [importId, setImportId] = useState("");
  const [report, setReport] = useState<BundleImportReport | null>(null);
  const [error, setError] = useState("");
  const pollTimer = useRef<number | undefined>(undefined);

  const refreshReport = useCallback(
    async (id: string) => {
      const next = await semanticBundleAPI.importRows(id);
      setReport(next);
      switch (next.import.state) {
        case "UPLOADED":
        case "VALIDATING":
          setPhase("validating");
          return false;
        case "FAILED":
          setPhase("failed");
          setError(next.import.failureReason ?? "Bundle 结构校验失败");
          return true;
        case "VALIDATED":
          setPhase("reviewing");
          return true;
        default:
          setPhase("committed");
          return true;
      }
    },
    [],
  );

  useEffect(() => {
    if (!importId || (phase !== "validating" && phase !== "uploading")) return;
    let cancelled = false;
    const poll = async () => {
      try {
        const settled = await refreshReport(importId);
        if (!cancelled && !settled) {
          pollTimer.current = window.setTimeout(poll, POLL_INTERVAL_MS);
        }
      } catch (cause) {
        if (!cancelled) {
          setPhase("failed");
          setError(cause instanceof Error ? cause.message : "导入状态查询失败");
        }
      }
    };
    poll();
    return () => {
      cancelled = true;
      window.clearTimeout(pollTimer.current);
    };
  }, [importId, phase, refreshReport]);

  const upload = async () => {
    if (!file || !domainId) return;
    setPhase("uploading");
    setError("");
    try {
      const uploaded = await semanticBundleAPI.uploadBundle(domainId, file);
      setImportId(uploaded.importId);
      setPhase("validating");
    } catch (cause) {
      setPhase("failed");
      setError(cause instanceof Error ? cause.message : "Bundle 上传失败");
    }
  };

  const commit = async () => {
    if (!importId || !report) return;
    setPhase("committing");
    setError("");
    try {
      await semanticAPI.commitImport(importId);
      await refreshReport(importId);
      setPhase("committed");
      onCommitted?.();
    } catch (cause) {
      setPhase("reviewing");
      setError(cause instanceof Error ? cause.message : "Bundle 提交失败");
    }
  };

  const downloadSchema = async () => {
    const schema = await semanticBundleAPI.bundleSchema();
    const blob = new Blob([JSON.stringify(schema, null, 2)], { type: "application/json" });
    triggerDownload(blob, "semantic-bundle-v1.schema.json");
  };

  const downloadCurrentBundle = async () => {
    try {
      const response = await semanticBundleAPI.exportBundle(domainId, bundleExportAssetTypes);
      triggerDownload(await response.blob(), "askdata-semantic-current.bundle.json");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "Bundle 导出失败");
    }
  };

  const commitReady =
    phase === "reviewing" && report !== null && report.counts.failed === 0 &&
    report.counts.pending > 0;
  const commitBlocked =
    phase === "reviewing" && report !== null && report.counts.failed > 0;

  return (
    <div className="semantic-bundle-overlay" role="dialog" aria-modal="true" aria-label="语义资产 Bundle 导入">
      <section className="semantic-bundle-panel">
        <header>
          <div>
            <strong>语义资产 Bundle 导入</strong>
            <small>
              semantic-bundle/v1 · 一个 JSON 覆盖模型 / 指标 / 维度 / 业务知识四个分区，引用一律使用稳定
              code；提交只创建 DRAFT，认证与 Release 流程保持不变。
            </small>
          </div>
          <AppButton className="semantic-bundle-close" onClick={onClose} aria-label="关闭导入向导">
            <X size={16} />
          </AppButton>
        </header>

        <div className="semantic-bundle-contract">
          <AppButton onClick={downloadSchema}>
            <FileArrowDown size={15} /> 下载 JSON Schema
          </AppButton>
          <AppButton onClick={downloadCurrentBundle}>
            <DownloadSimple size={15} /> 导出当前语义资产为 Bundle
          </AppButton>
        </div>

        {(phase === "idle" || phase === "uploading" || phase === "failed") && (
          <label className="semantic-bundle-drop">
            <CloudArrowUp size={26} />
            <strong>{file ? file.name : "选择 semantic-bundle/v1 JSON 文件"}</strong>
            <small>重复上传同一文件不会创建重复批次；未变化的资产会被自动跳过</small>
            <input
              type="file"
              accept="application/json,.json"
              onChange={(event) => setFile(event.target.files?.[0] ?? null)}
            />
          </label>
        )}

        {error && (
          <p className="semantic-bundle-error" role="alert">
            <WarningCircle size={16} /> {error}
          </p>
        )}

        {phase === "validating" && (
          <div className="semantic-bundle-progress" role="status">
            <ArrowsClockwise size={18} className="is-spinning" />
            <span>正在执行结构、引用、业务与治理四层校验……</span>
          </div>
        )}

        {report && (phase === "reviewing" || phase === "committing" || phase === "committed") && (
          <>
            <div className="semantic-bundle-counts" role="group" aria-label="导入裁决统计">
              <BundleCount label="待提交" value={report.counts.pending} tone="pending" />
              <BundleCount label="已新建" value={report.counts.created} tone="created" />
              <BundleCount label="已更新" value={report.counts.updated} tone="updated" />
              <BundleCount label="未变化" value={report.counts.unchanged} tone="unchanged" />
              <BundleCount label="失败" value={report.counts.failed} tone="failed" />
            </div>
            {phase === "committed" && (
              <div className="semantic-bundle-index" role="status">
                <strong>检索就绪</strong>
                <small>
                  新建 {report.index.createdVersions} 个版本；
                  {report.index.awaitingRelease > 0
                    ? `${report.index.awaitingRelease} 个等待认证并进入 Release 后才可被语义检索`
                    : report.index.embeddingFailed > 0
                      ? `${report.index.embeddingFailed} 个检索文档嵌入失败，需要重试`
                      : `${report.index.embeddingReady} 个检索文档已就绪`}
                </small>
              </div>
            )}
            <div className="semantic-bundle-rows" role="table" aria-label="导入行明细">
              {report.rows.slice(0, MAX_VISIBLE_ROWS).map((row) => (
                <BundleRow key={row.rowNo} row={row} />
              ))}
              {report.rows.length > MAX_VISIBLE_ROWS && (
                <small className="semantic-bundle-more">
                  其余 {report.rows.length - MAX_VISIBLE_ROWS} 行请下载校验报告查看
                </small>
              )}
            </div>
          </>
        )}

        <footer>
          {(phase === "idle" || phase === "failed") && (
            <AppButton variant="primary" disabled={!file} onClick={upload}>
              上传并校验
            </AppButton>
          )}
          {phase === "reviewing" && (
            <>
              {commitBlocked && (
                <small className="semantic-bundle-blocked">
                  存在失败行：修复后重新上传，或仅提交有效行前先确认失败原因。
                </small>
              )}
              <AppButton
                variant="primary"
                disabled={!commitReady && !commitBlocked}
                onClick={commit}
              >
                {commitBlocked ? "提交有效行（跳过失败行）" : "提交为 DRAFT"}
              </AppButton>
            </>
          )}
          {phase === "committing" && <small>正在提交……</small>}
          {phase === "committed" && (
            <span className="semantic-bundle-done">
              <CheckCircle size={16} /> 已提交为 DRAFT；请在各分区完成认证并纳入 Release。
            </span>
          )}
        </footer>
      </section>
    </div>
  );
}

function BundleCount({ label, value, tone }: { label: string; value: number; tone: string }) {
  return (
    <article className={`semantic-bundle-count is-${tone}`}>
      <strong>{value}</strong>
      <small>{label}</small>
    </article>
  );
}

function BundleRow({ row }: { row: BundleImportRow }) {
  const blocking = row.issues.filter(
    (issue) =>
      issue.code !== "IMPORT_IMPACT_REQUIRES_REVIEW" &&
      issue.code !== "IMPORT_WILL_UPDATE" &&
      issue.code !== "IMPORT_CONTENT_UNCHANGED",
  );
  return (
    <article className={`semantic-bundle-row is-${row.resolution.toLowerCase()}`} role="row">
      <span role="cell" className="semantic-bundle-row-identity">
        <strong>{row.name || row.code || `第 ${row.rowNo} 行`}</strong>
        <small>
          {row.assetType}
          {row.code ? ` · ${row.code}` : ""}
        </small>
      </span>
      <span role="cell">
        <b className={`semantic-bundle-resolution is-${row.resolution.toLowerCase()}`}>
          {resolutionLabel[row.resolution]}
        </b>
      </span>
      <span role="cell" className="semantic-bundle-row-issues">
        {blocking.length > 0 ? (
          blocking.slice(0, 2).map((issue) => (
            <small key={`${issue.column}-${issue.code}-${issue.actual ?? ""}`}>
              {issue.column}: {issue.message}
            </small>
          ))
        ) : row.resolution === "UPDATE" ? (
          <small>将为既有对象创建新草稿版本</small>
        ) : row.resolution === "UNCHANGED" ? (
          <small>内容与当前认证版本一致</small>
        ) : (
          <small>校验通过</small>
        )}
      </span>
    </article>
  );
}

function triggerDownload(blob: Blob, filename: string) {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}
