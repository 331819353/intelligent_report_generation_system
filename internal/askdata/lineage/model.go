// Package lineage owns the semantic-asset lineage graph: physical build
// provenance and semantic dependency edges, their idempotent projection from
// the registry, and the neighbourhood / impact read APIs.
//
// 血缘边是出处，不是连接语义：它从不参与编译，只服务图浏览与影响分析
// （docs/09 §3.3）。物理边与语义边是两个可分开遍历的边族。
package lineage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidLineageRequest = errors.New("lineage request is invalid")
	ErrLineageUnavailable    = errors.New("lineage store is unavailable")
)

type Family string

const (
	FamilyPhysical Family = "PHYSICAL"
	FamilySemantic Family = "SEMANTIC"
)

func (family Family) Valid() bool {
	return family == FamilyPhysical || family == FamilySemantic
}

type NodeType string

const (
	NodeDatasetVersion NodeType = "DATASET_VERSION"
	NodeModel          NodeType = "MODEL"
	NodeModelField     NodeType = "MODEL_FIELD"
	NodeMeasure        NodeType = "MEASURE"
	NodeMetric         NodeType = "METRIC"
	NodeDimension      NodeType = "DIMENSION"
	NodeHierarchy      NodeType = "HIERARCHY"
	NodeKnowledge      NodeType = "KNOWLEDGE"
)

func (nodeType NodeType) Valid() bool {
	switch nodeType {
	case NodeDatasetVersion, NodeModel, NodeModelField, NodeMeasure,
		NodeMetric, NodeDimension, NodeHierarchy, NodeKnowledge:
		return true
	default:
		return false
	}
}

type EdgeKind string

const (
	// PHYSICAL
	EdgeModelReadsDataset     EdgeKind = "MODEL_READS_DATASET"
	EdgeDatasetDerivesDataset EdgeKind = "DATASET_DERIVES_DATASET"
	// SEMANTIC
	EdgeMetricUsesModel     EdgeKind = "METRIC_USES_MODEL"
	EdgeMetricUsesMeasure   EdgeKind = "METRIC_USES_MEASURE"
	EdgeMeasureUsesField    EdgeKind = "MEASURE_USES_FIELD"
	EdgeMetricDependsMetric EdgeKind = "METRIC_DEPENDS_METRIC"
	EdgeMetricAllowsDim     EdgeKind = "METRIC_ALLOWS_DIMENSION"
	EdgeDimensionBindsField EdgeKind = "DIMENSION_BINDS_FIELD"
	EdgeDimensionUsesModel  EdgeKind = "DIMENSION_USES_MODEL"
	EdgeHierarchyLevel      EdgeKind = "HIERARCHY_LEVEL"
	EdgeModelJoinsModel     EdgeKind = "MODEL_JOINS_MODEL"
	EdgeKnowledgeDescribes  EdgeKind = "KNOWLEDGE_DESCRIBES"
)

func (kind EdgeKind) Family() Family {
	switch kind {
	case EdgeModelReadsDataset, EdgeDatasetDerivesDataset:
		return FamilyPhysical
	default:
		return FamilySemantic
	}
}

type Derivation string

const (
	DerivationComputed Derivation = "COMPUTED"
	DerivationDeclared Derivation = "DECLARED"
	DerivationImported Derivation = "IMPORTED"
)

type NodeRef struct {
	Type NodeType `json:"type"`
	ID   string   `json:"id"`
	Code string   `json:"code,omitempty"`
}

func (ref NodeRef) Validate() error {
	trimmed := strings.TrimSpace(ref.ID)
	if !ref.Type.Valid() || trimmed == "" || trimmed != ref.ID || len(ref.ID) > 512 {
		return fmt.Errorf("%w: node ref", ErrInvalidLineageRequest)
	}
	return nil
}

func (ref NodeRef) key() string { return string(ref.Type) + "\x00" + ref.ID }

type Edge struct {
	ID         string     `json:"id"`
	Family     Family     `json:"family"`
	Kind       EdgeKind   `json:"kind"`
	From       NodeRef    `json:"from"`
	To         NodeRef    `json:"to"`
	Derivation Derivation `json:"derivation"`
	ValidFrom  time.Time  `json:"validFrom"`
}

// NeighbourhoodRequest 是图浏览请求：以一个节点为中心向两个方向展开。
type NeighbourhoodRequest struct {
	TenantID string
	DomainID string
	Node     NodeRef
	// Families 为空表示两族都要。
	Families []Family
	Depth    int
	MaxNodes int
}

const (
	MaxNeighbourhoodDepth = 4
	MaxNeighbourhoodNodes = 400
	MaxImpactNodes        = 2000
)

func (request *NeighbourhoodRequest) Normalize() error {
	if !canonicalScope(request.TenantID, request.DomainID) {
		return ErrInvalidLineageRequest
	}
	if err := request.Node.Validate(); err != nil {
		return err
	}
	if request.Depth < 1 {
		request.Depth = 2
	}
	if request.Depth > MaxNeighbourhoodDepth {
		request.Depth = MaxNeighbourhoodDepth
	}
	if request.MaxNodes < 1 || request.MaxNodes > MaxNeighbourhoodNodes {
		request.MaxNodes = MaxNeighbourhoodNodes
	}
	if len(request.Families) == 0 {
		request.Families = []Family{FamilyPhysical, FamilySemantic}
	}
	for _, family := range request.Families {
		if !family.Valid() {
			return ErrInvalidLineageRequest
		}
	}
	return nil
}

type Neighbourhood struct {
	Center    NodeRef   `json:"center"`
	Nodes     []NodeRef `json:"nodes"`
	Edges     []Edge    `json:"edges"`
	Truncated bool      `json:"truncated"`
}

// ImpactRequest 是影响分析请求：只沿下游（from → to 的反向依赖，即“谁依赖
// 我”）遍历，返回逐跳分层结果。
type ImpactRequest struct {
	TenantID string
	DomainID string
	Node     NodeRef
	Families []Family
	MaxDepth int
}

func (request *ImpactRequest) Normalize() error {
	if !canonicalScope(request.TenantID, request.DomainID) {
		return ErrInvalidLineageRequest
	}
	if err := request.Node.Validate(); err != nil {
		return err
	}
	if request.MaxDepth < 1 || request.MaxDepth > MaxNeighbourhoodDepth {
		request.MaxDepth = MaxNeighbourhoodDepth
	}
	if len(request.Families) == 0 {
		request.Families = []Family{FamilyPhysical, FamilySemantic}
	}
	for _, family := range request.Families {
		if !family.Valid() {
			return ErrInvalidLineageRequest
		}
	}
	return nil
}

type ImpactHop struct {
	Hop   int       `json:"hop"`
	Nodes []NodeRef `json:"nodes"`
	Edges []Edge    `json:"edges"`
}

type ImpactReport struct {
	Root      NodeRef     `json:"root"`
	Hops      []ImpactHop `json:"hops"`
	Total     int         `json:"total"`
	Truncated bool        `json:"truncated"`
}

// EdgeReader 是遍历依赖的最小读取面：给定一批节点，返回与之相邻的活跃边。
// direction 为 out 时匹配 from ∈ nodes（下游遍历读 in：谁指向这些节点由
// direction=in 表达）。
type EdgeReader interface {
	AdjacentEdges(
		ctx context.Context,
		tenantID, domainID string,
		nodes []NodeRef,
		families []Family,
		direction Direction,
	) ([]Edge, error)
}

type Direction string

const (
	// DirectionOut：这些节点作为 from 的出边（它依赖什么，上游）。
	DirectionOut Direction = "OUT"
	// DirectionIn：这些节点作为 to 的入边（谁依赖它，下游）。
	DirectionIn Direction = "IN"
)

// ExpandNeighbourhood 以中心节点做双向 BFS，节点数与深度双重封顶。遍历是
// 纯函数，边读取通过 EdgeReader 注入，因此可以在无数据库环境下验证。
func ExpandNeighbourhood(ctx context.Context, reader EdgeReader, request NeighbourhoodRequest) (Neighbourhood, error) {
	if reader == nil {
		return Neighbourhood{}, ErrLineageUnavailable
	}
	if err := request.Normalize(); err != nil {
		return Neighbourhood{}, err
	}
	result := Neighbourhood{Center: request.Node, Nodes: []NodeRef{request.Node}, Edges: []Edge{}}
	seenNodes := map[string]struct{}{request.Node.key(): {}}
	seenEdges := map[string]struct{}{}
	frontier := []NodeRef{request.Node}
	for depth := 0; depth < request.Depth && len(frontier) > 0; depth++ {
		next := []NodeRef{}
		for _, direction := range []Direction{DirectionOut, DirectionIn} {
			edges, err := reader.AdjacentEdges(
				ctx, request.TenantID, request.DomainID, frontier, request.Families, direction,
			)
			if err != nil {
				return Neighbourhood{}, err
			}
			for _, edge := range edges {
				if _, duplicate := seenEdges[edge.ID]; duplicate {
					continue
				}
				seenEdges[edge.ID] = struct{}{}
				result.Edges = append(result.Edges, edge)
				for _, node := range []NodeRef{edge.From, edge.To} {
					if _, exists := seenNodes[node.key()]; exists {
						continue
					}
					if len(result.Nodes) >= request.MaxNodes {
						result.Truncated = true
						continue
					}
					seenNodes[node.key()] = struct{}{}
					result.Nodes = append(result.Nodes, node)
					next = append(next, node)
				}
			}
		}
		frontier = next
	}
	return result, nil
}

// WalkImpact 只沿下游遍历：某节点变化会波及哪些资产，按跳分层。
func WalkImpact(ctx context.Context, reader EdgeReader, request ImpactRequest) (ImpactReport, error) {
	if reader == nil {
		return ImpactReport{}, ErrLineageUnavailable
	}
	if err := request.Normalize(); err != nil {
		return ImpactReport{}, err
	}
	report := ImpactReport{Root: request.Node, Hops: []ImpactHop{}}
	seen := map[string]struct{}{request.Node.key(): {}}
	frontier := []NodeRef{request.Node}
	for depth := 1; depth <= request.MaxDepth && len(frontier) > 0; depth++ {
		edges, err := reader.AdjacentEdges(
			ctx, request.TenantID, request.DomainID, frontier, request.Families, DirectionIn,
		)
		if err != nil {
			return ImpactReport{}, err
		}
		hop := ImpactHop{Hop: depth, Nodes: []NodeRef{}, Edges: []Edge{}}
		next := []NodeRef{}
		for _, edge := range edges {
			hop.Edges = append(hop.Edges, edge)
			if _, exists := seen[edge.From.key()]; exists {
				continue
			}
			if report.Total >= MaxImpactNodes {
				report.Truncated = true
				continue
			}
			seen[edge.From.key()] = struct{}{}
			hop.Nodes = append(hop.Nodes, edge.From)
			next = append(next, edge.From)
			report.Total++
		}
		if len(hop.Nodes) > 0 || len(hop.Edges) > 0 {
			report.Hops = append(report.Hops, hop)
		}
		frontier = next
	}
	return report, nil
}

func canonicalScope(tenantID, domainID string) bool {
	return uuidLike(tenantID) && uuidLike(domainID)
}

func uuidLike(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		switch index {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
				return false
			}
		}
	}
	return true
}
