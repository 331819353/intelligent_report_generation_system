import { useEffect, useRef, useState } from "react";
import { ArrowsClockwise, MagnifyingGlass, TreeStructure } from "@phosphor-icons/react";

import {
  semanticBundleAPI,
  type DiscoveryCandidate,
  type DiscoveryResult,
  type DiscoverySection,
} from "../../lib/semantic-bundle";

// SemanticDiscoveryResults 是四分区共享搜索：同一个搜索词跨模型 / 指标 /
// 维度 / 业务知识做混合检索（精确 + 词法 + 向量 + 血缘扩展），当前分区之外
// 的命中也能一键跳转——在维度中心搜「GMV」仍然找得到指标。
type Props = {
  query: string;
  activeSection: DiscoverySection;
  onNavigate: (section: DiscoverySection, candidate: DiscoveryCandidate) => void;
};

const DEBOUNCE_MS = 350;
const MIN_QUERY_RUNES = 2;

const sectionLabels: Record<DiscoverySection, string> = {
  MODEL: "模型资产",
  METRIC: "指标中心",
  DIMENSION: "维度中心",
  KNOWLEDGE: "业务知识",
};

const degradedLabels: Record<string, string> = {
  NO_ACTIVE_RELEASE: "尚无 ACTIVE Release，向量检索未参与；结果来自治理目录",
  EMBEDDING_UNAVAILABLE: "嵌入服务未配置，向量检索未参与；结果来自治理目录",
  EMBEDDING_FAILED: "查询嵌入失败，向量检索未参与；结果来自治理目录",
  VECTOR_LANE_FAILED: "向量检索暂不可用；结果来自治理目录",
  VECTOR_LANE_ABSENT: "向量检索未启用；结果来自治理目录",
};

export function SemanticDiscoveryResults({ query, activeSection, onNavigate }: Props) {
  const [result, setResult] = useState<DiscoveryResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const timer = useRef<number | undefined>(undefined);

  const trimmed = query.trim();
  const eligible = [...trimmed].length >= MIN_QUERY_RUNES;

  useEffect(() => {
    window.clearTimeout(timer.current);
    if (!eligible) {
      return;
    }
    let cancelled = false;
    timer.current = window.setTimeout(() => {
      setLoading(true);
      semanticBundleAPI
        .discover(trimmed)
        .then((next) => {
          if (!cancelled) {
            setResult(next);
            setError("");
          }
        })
        .catch((cause) => {
          if (!cancelled) {
            setError(cause instanceof Error ? cause.message : "跨分区检索失败");
          }
        })
        .finally(() => {
          if (!cancelled) setLoading(false);
        });
    }, DEBOUNCE_MS);
    return () => {
      cancelled = true;
      window.clearTimeout(timer.current);
    };
  }, [trimmed, eligible]);

  if (!eligible) return null;

  // 只有当前分区之外存在命中（或扩展命中）时才值得占用版面：当前分区的
  // 直接命中已由下方目录表格的本地过滤呈现。
  const crossSection = (result?.candidates ?? []).filter(
    (candidate) => candidate.section !== activeSection || candidate.expandedFrom,
  );
  if (!loading && !error && crossSection.length === 0) return null;

  return (
    <section className="semantic-discovery" aria-label="跨分区语义检索">
      <header>
        <strong>
          <MagnifyingGlass size={14} /> 跨分区命中
        </strong>
        {loading && <ArrowsClockwise size={14} className="is-spinning" />}
        {result?.degraded && (
          <small>{degradedLabels[result.degradedReason ?? ""] ?? "部分检索巷道降级"}</small>
        )}
      </header>
      {error && <small className="semantic-discovery-error">{error}</small>}
      <div className="semantic-discovery-list">
        {crossSection.slice(0, 12).map((candidate) => (
          <button
            key={`${candidate.section}:${candidate.objectId}`}
            type="button"
            className="semantic-discovery-hit"
            onClick={() => onNavigate(candidate.section, candidate)}
          >
            <em>{sectionLabels[candidate.section]}</em>
            <strong>{candidate.name || candidate.code}</strong>
            <small>{candidate.code}</small>
            {candidate.expandedFrom ? (
              <span className="semantic-discovery-expanded">
                <TreeStructure size={12} /> 经 {candidate.expandedFrom} 关联
              </span>
            ) : (
              <span className={`semantic-discovery-status is-${candidate.status.toLowerCase()}`}>
                {candidate.status === "CERTIFIED" ? "已认证" : candidate.status === "DRAFT" ? "草稿" : candidate.status}
              </span>
            )}
          </button>
        ))}
      </div>
    </section>
  );
}
