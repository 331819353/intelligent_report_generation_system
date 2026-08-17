import { useCallback, useEffect, useMemo, useState } from "react";
import { ArrowsClockwise, Graph, WarningCircle, X } from "@phosphor-icons/react";

import { AppButton } from "../AppButton";
import {
  lineageNodeTypeLabel,
  semanticBundleAPI,
  type LineageFamily,
  type LineageImpactReport,
  type LineageNeighbourhood,
  type LineageNode,
} from "../../lib/semantic-bundle";

// SemanticLineagePanel 渲染一个语义资产的血缘邻域与下游影响：
// 物理血缘（模型 ↔ 数据集、构建派生）与语义依赖（指标/维度/知识）是两个
// 可切换的边族；影响分析只沿下游遍历并按跳分层。
type PanelProps = {
  node: LineageNode;
  title: string;
  onClose: () => void;
};

type FamilyFilter = LineageFamily | "ALL";

const COLUMN_ORDER: LineageNode["type"][] = [
  "DATASET_VERSION",
  "MODEL_FIELD",
  "MODEL",
  "MEASURE",
  "DIMENSION",
  "HIERARCHY",
  "METRIC",
  "KNOWLEDGE",
];

const NODE_WIDTH = 168;
const NODE_HEIGHT = 44;
const COLUMN_GAP = 64;
const ROW_GAP = 16;

export function SemanticLineagePanel({ node, title, onClose }: PanelProps) {
  const [family, setFamilyState] = useState<FamilyFilter>("ALL");
  const [graph, setGraph] = useState<LineageNeighbourhood | null>(null);
  const [impact, setImpact] = useState<LineageImpactReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  // reloadNonce 驱动重新加载（重建后）；loading 只在事件处理器内同步置位，
  // 效果体内的状态更新全部发生在网络往返之后。
  const [reloadNonce, setReloadNonce] = useState(0);

  const fetchLineage = useCallback(async (selected: FamilyFilter) => {
    const filter = selected === "ALL" ? undefined : selected;
    return Promise.all([
      semanticBundleAPI.lineageNeighbourhood(node, filter),
      semanticBundleAPI.lineageImpact(node, filter),
    ]);
  }, [node]);

  useEffect(() => {
    let cancelled = false;
    fetchLineage(family)
      .then(([neighbourhood, impactReport]) => {
        if (cancelled) return;
        setGraph(neighbourhood);
        setImpact(impactReport);
        setError("");
      })
      .catch((cause) => {
        if (!cancelled) {
          setError(cause instanceof Error ? cause.message : "血缘图加载失败");
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [fetchLineage, family, reloadNonce]);

  const setFamily = (next: FamilyFilter) => {
    setLoading(true);
    setFamilyState(next);
  };

  const rebuild = async () => {
    setLoading(true);
    try {
      await semanticBundleAPI.rebuildLineage();
      setReloadNonce((nonce) => nonce + 1);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "血缘重建失败");
      setLoading(false);
    }
  };

  const layout = useMemo(() => (graph ? layoutGraph(graph) : null), [graph]);

  return (
    <div className="semantic-bundle-overlay" role="dialog" aria-modal="true" aria-label="语义血缘">
      <section className="semantic-lineage-panel">
        <header>
          <div>
            <strong>
              <Graph size={17} /> {title} 的血缘
            </strong>
            <small>
              {lineageNodeTypeLabel[node.type]} · {node.code || node.id}
            </small>
          </div>
          <div className="semantic-lineage-actions">
            <div className="semantic-lineage-family" role="radiogroup" aria-label="血缘边族">
              {(["ALL", "SEMANTIC", "PHYSICAL"] as FamilyFilter[]).map((option) => (
                <button
                  key={option}
                  type="button"
                  role="radio"
                  aria-checked={family === option}
                  className={family === option ? "is-active" : ""}
                  onClick={() => setFamily(option)}
                >
                  {option === "ALL" ? "全部" : option === "SEMANTIC" ? "语义依赖" : "物理血缘"}
                </button>
              ))}
            </div>
            <AppButton onClick={rebuild} aria-label="重建血缘边">
              <ArrowsClockwise size={15} /> 重建
            </AppButton>
            <AppButton className="semantic-bundle-close" onClick={onClose} aria-label="关闭血缘面板">
              <X size={16} />
            </AppButton>
          </div>
        </header>

        {loading && (
          <div className="semantic-bundle-progress" role="status">
            <ArrowsClockwise size={18} className="is-spinning" />
            <span>正在展开血缘邻域……</span>
          </div>
        )}
        {!loading && error && (
          <p className="semantic-bundle-error" role="alert">
            <WarningCircle size={16} /> {error}
          </p>
        )}
        {!loading && !error && layout && (
          <>
            {layout.nodes.length <= 1 ? (
              <div className="semantic-lineage-empty">
                <small>
                  暂无血缘边。若刚导入或修改了语义资产，请点击「重建」重新推导 COMPUTED 边。
                </small>
              </div>
            ) : (
              <svg
                className="semantic-lineage-canvas"
                viewBox={`0 0 ${layout.width} ${layout.height}`}
                role="img"
                aria-label={`${title} 的血缘图，共 ${layout.nodes.length} 个节点`}
              >
                {layout.edges.map((edge) => (
                  <g key={edge.id} className={`lineage-edge is-${edge.family.toLowerCase()}`}>
                    <path d={edge.path} fill="none" />
                    <title>{`${edge.kind}（${edge.derivation}）`}</title>
                  </g>
                ))}
                {layout.nodes.map((placed) => (
                  <g
                    key={`${placed.node.type}:${placed.node.id}`}
                    className={`lineage-node is-${placed.node.type.toLowerCase()}${
                      placed.node.id === node.id && placed.node.type === node.type ? " is-center" : ""
                    }`}
                    transform={`translate(${placed.x},${placed.y})`}
                  >
                    <rect width={NODE_WIDTH} height={NODE_HEIGHT} rx={8} />
                    <text x={10} y={18}>
                      {truncateLabel(placed.node.code || placed.node.id)}
                    </text>
                    <text x={10} y={34} className="lineage-node-type">
                      {lineageNodeTypeLabel[placed.node.type]}
                    </text>
                  </g>
                ))}
              </svg>
            )}
            {graph?.truncated && (
              <small className="semantic-bundle-more">邻域过大已截断；缩小深度或按边族过滤。</small>
            )}
            {impact && impact.total > 0 && (
              <div className="semantic-lineage-impact" aria-label="下游影响">
                <strong>下游影响（{impact.total} 个资产）</strong>
                {impact.hops.map((hop) => (
                  <div key={hop.hop} className="semantic-lineage-hop">
                    <em>第 {hop.hop} 跳</em>
                    <div>
                      {hop.nodes.map((impacted) => (
                        <span key={`${impacted.type}:${impacted.id}`}>
                          {lineageNodeTypeLabel[impacted.type]} · {impacted.code || impacted.id}
                        </span>
                      ))}
                    </div>
                  </div>
                ))}
                {impact.truncated && <small>影响面过大已截断。</small>}
              </div>
            )}
            {impact && impact.total === 0 && (
              <div className="semantic-lineage-impact">
                <strong>下游影响</strong>
                <small>没有资产依赖该对象；变更不会波及其他语义资产。</small>
              </div>
            )}
          </>
        )}
      </section>
    </div>
  );
}

type PlacedNode = { node: LineageNode; x: number; y: number };
type PlacedEdge = {
  id: string;
  family: LineageFamily;
  kind: string;
  derivation: string;
  path: string;
};

// layoutGraph 按节点类型分列布局：物理端（数据集/字段）在左，语义消费端
// （指标/知识）在右，与依赖方向的阅读习惯一致。
function layoutGraph(graph: LineageNeighbourhood) {
  const columns = new Map<LineageNode["type"], LineageNode[]>();
  for (const node of graph.nodes) {
    const column = columns.get(node.type) ?? [];
    column.push(node);
    columns.set(node.type, column);
  }
  const activeColumns = COLUMN_ORDER.filter((type) => columns.has(type));
  const positions = new Map<string, PlacedNode>();
  let width = COLUMN_GAP;
  let height = 0;
  activeColumns.forEach((type, columnIndex) => {
    const columnNodes = [...(columns.get(type) ?? [])].sort((left, right) =>
      (left.code || left.id).localeCompare(right.code || right.id),
    );
    const x = COLUMN_GAP + columnIndex * (NODE_WIDTH + COLUMN_GAP);
    columnNodes.forEach((node, rowIndex) => {
      const y = ROW_GAP + rowIndex * (NODE_HEIGHT + ROW_GAP);
      positions.set(`${node.type}:${node.id}`, { node, x, y });
      height = Math.max(height, y + NODE_HEIGHT + ROW_GAP);
    });
    width = x + NODE_WIDTH + COLUMN_GAP;
  });
  const edges: PlacedEdge[] = [];
  for (const edge of graph.edges) {
    const from = positions.get(`${edge.from.type}:${edge.from.id}`);
    const to = positions.get(`${edge.to.type}:${edge.to.id}`);
    if (!from || !to) continue;
    const startX = from.x + (from.x <= to.x ? NODE_WIDTH : 0);
    const endX = to.x + (from.x <= to.x ? 0 : NODE_WIDTH);
    const startY = from.y + NODE_HEIGHT / 2;
    const endY = to.y + NODE_HEIGHT / 2;
    const bend = (endX - startX) / 2;
    edges.push({
      id: edge.id,
      family: edge.family,
      kind: edge.kind,
      derivation: edge.derivation,
      path: `M ${startX} ${startY} C ${startX + bend} ${startY}, ${endX - bend} ${endY}, ${endX} ${endY}`,
    });
  }
  return { nodes: [...positions.values()], edges, width, height: Math.max(height, 120) };
}

function truncateLabel(value: string) {
  return value.length > 20 ? `${value.slice(0, 19)}…` : value;
}
