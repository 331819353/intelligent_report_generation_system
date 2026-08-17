// Package retrieval is the four-section semantic asset discovery facade
// (docs/09 §9)：结构化约束 → 精确/词法（目录巷道）→ 向量（Release 巷道）→
// 血缘图扩展 → 融合排序。
//
// 巷道边界是刻意的：目录巷道读治理头（DRAFT/CERTIFIED 最新版），随时可用、
// 权威、无索引依赖；向量巷道只读 ACTIVE Release 的检索投影，缺 Release 或
// 缺嵌入服务时降级而不是失败。问数的绑定检索链路（understanding/search）
// 保持不变——这里是发现（discovery），不是绑定（binding）。
package retrieval

import (
	"context"
	"errors"
	"sort"
	"strings"
)

var (
	ErrInvalidRequest = errors.New("semantic discovery request is invalid")
	ErrUnavailable    = errors.New("semantic discovery facade is unavailable")
)

type Section string

const (
	SectionModel     Section = "MODEL"
	SectionMetric    Section = "METRIC"
	SectionDimension Section = "DIMENSION"
	SectionKnowledge Section = "KNOWLEDGE"
)

func (section Section) Valid() bool {
	switch section {
	case SectionModel, SectionMetric, SectionDimension, SectionKnowledge:
		return true
	default:
		return false
	}
}

var AllSections = []Section{SectionModel, SectionMetric, SectionDimension, SectionKnowledge}

// Source 标注候选来自哪条巷道；EXPANDED 候选永远不冒充直接命中。
type Source string

const (
	SourceExact    Source = "EXACT"
	SourceLexical  Source = "LEXICAL"
	SourceVector   Source = "VECTOR"
	SourceExpanded Source = "EXPANDED"
)

type Candidate struct {
	Section    Section  `json:"section"`
	ObjectType string   `json:"objectType"` // MODEL/MEASURE/METRIC/DIMENSION/HIERARCHY/KNOWLEDGE
	ObjectID   string   `json:"objectId"`
	VersionID  string   `json:"versionId,omitempty"`
	Code       string   `json:"code"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Summary    string   `json:"summary,omitempty"`
	Score      float64  `json:"score"`
	Sources    []Source `json:"sources"`
	// ExpandedFrom 是图扩展锚点的 code：扩展候选是“让命中可用的邻居”，
	// 不是查询的直接匹配。
	ExpandedFrom string `json:"expandedFrom,omitempty"`
}

func (candidate Candidate) identity() string {
	return string(candidate.Section) + "\x00" + candidate.ObjectID
}

type Request struct {
	TenantID string
	DomainID string
	ActorID  string
	Query    string
	Sections []Section
	Limit    int
	Expand   bool
}

const (
	DefaultLimit      = 20
	MaxLimit          = 50
	maxQueryRunes     = 512
	expansionAnchors  = 5
	expansionPerHit   = 4
	expansionDecay    = 0.5
	rrfConstant       = 60.0
	catalogLaneWeight = 1.0
	vectorLaneWeight  = 0.8
)

func (request *Request) Normalize() error {
	request.Query = strings.TrimSpace(request.Query)
	if request.TenantID == "" || request.DomainID == "" || request.Query == "" ||
		len([]rune(request.Query)) > maxQueryRunes {
		return ErrInvalidRequest
	}
	if request.Limit < 1 || request.Limit > MaxLimit {
		request.Limit = DefaultLimit
	}
	if len(request.Sections) == 0 {
		request.Sections = append([]Section{}, AllSections...)
	}
	for _, section := range request.Sections {
		if !section.Valid() {
			return ErrInvalidRequest
		}
	}
	return nil
}

type Result struct {
	Candidates     []Candidate `json:"candidates"`
	Degraded       bool        `json:"degraded"`
	DegradedReason string      `json:"degradedReason,omitempty"`
}

// CatalogSearcher 是确定性巷道：精确 code/名称/别名命中 + 词法相似，
// 面向治理头，与 Release 状态无关。返回按分数降序。
type CatalogSearcher interface {
	Search(ctx context.Context, tenantID, domainID, query string, sections []Section, limit int) ([]Candidate, error)
}

// VectorSearcher 是语义相似巷道：查询嵌入 + ACTIVE Release 检索投影。
// 不可用时返回空候选与降级原因，不返回错误——缺 Release 不是故障。
type VectorSearcher interface {
	Search(ctx context.Context, tenantID, domainID, actorID, query string, sections []Section, limit int) ([]Candidate, string, error)
}

// GraphExpander 沿语义血缘把命中扩展到让它可用的邻居（指标的模型与维度、
// 维度的层级同伴、对象的权威知识）。
type GraphExpander interface {
	Expand(ctx context.Context, tenantID, domainID string, anchors []Candidate, perAnchor int) ([]Candidate, error)
}

type Facade struct {
	catalog  CatalogSearcher
	vector   VectorSearcher
	expander GraphExpander
}

// NewFacade 组装发现门面。catalog 是必需巷道；vector 与 expander 可缺席
// （分别表现为持续降级与不扩展），门面本身仍然可用。
func NewFacade(catalog CatalogSearcher, vector VectorSearcher, expander GraphExpander) (*Facade, error) {
	if catalog == nil {
		return nil, ErrUnavailable
	}
	return &Facade{catalog: catalog, vector: vector, expander: expander}, nil
}

func (facade *Facade) Retrieve(ctx context.Context, request Request) (Result, error) {
	if facade == nil || facade.catalog == nil {
		return Result{}, ErrUnavailable
	}
	if err := request.Normalize(); err != nil {
		return Result{}, err
	}
	catalogHits, err := facade.catalog.Search(
		ctx, request.TenantID, request.DomainID, request.Query, request.Sections, request.Limit,
	)
	if err != nil {
		return Result{}, err
	}
	result := Result{}
	vectorHits := []Candidate{}
	if facade.vector != nil {
		hits, degradedReason, err := facade.vector.Search(
			ctx, request.TenantID, request.DomainID, request.ActorID,
			request.Query, request.Sections, request.Limit,
		)
		if err != nil {
			// 向量巷道失败降级为“只有确定性结果”，不放弃整个请求。
			result.Degraded, result.DegradedReason = true, "VECTOR_LANE_FAILED"
		} else {
			vectorHits = hits
			if degradedReason != "" {
				result.Degraded, result.DegradedReason = true, degradedReason
			}
		}
	} else {
		result.Degraded, result.DegradedReason = true, "VECTOR_LANE_ABSENT"
	}
	fused := fuseLanes(catalogHits, vectorHits)
	if len(fused) > request.Limit {
		fused = fused[:request.Limit]
	}
	if request.Expand && facade.expander != nil && len(fused) > 0 {
		anchorCount := len(fused)
		if anchorCount > expansionAnchors {
			anchorCount = expansionAnchors
		}
		expanded, err := facade.expander.Expand(
			ctx, request.TenantID, request.DomainID, fused[:anchorCount], expansionPerHit,
		)
		if err == nil {
			fused = appendExpansion(fused, expanded, request.Sections)
		}
		// 扩展失败不降级结果：扩展是增益，不是承诺。
	}
	result.Candidates = fused
	return result, nil
}

// fuseLanes 用 RRF 融合两条巷道的有序候选。同一对象的巷道证据合并，
// 平分时确定性裁决：已认证优先，其后按分区序与 code。
func fuseLanes(catalog, vector []Candidate) []Candidate {
	type fusion struct {
		candidate Candidate
		score     float64
	}
	fused := map[string]*fusion{}
	accumulate := func(lane []Candidate, weight float64) {
		for index, candidate := range lane {
			key := candidate.identity()
			entry, exists := fused[key]
			if !exists {
				copied := candidate
				copied.Sources = append([]Source{}, candidate.Sources...)
				entry = &fusion{candidate: copied}
				fused[key] = entry
			} else {
				entry.candidate.Sources = mergeSources(entry.candidate.Sources, candidate.Sources)
				// 巷道各自的补充事实：目录巷道带治理身份，向量巷道可能只带
				// 版本 ID——保留更完整的一侧。
				if entry.candidate.Name == "" {
					entry.candidate.Name = candidate.Name
				}
				if entry.candidate.Summary == "" {
					entry.candidate.Summary = candidate.Summary
				}
			}
			entry.score += weight / (rrfConstant + float64(index+1))
		}
	}
	accumulate(catalog, catalogLaneWeight)
	accumulate(vector, vectorLaneWeight)
	ordered := make([]*fusion, 0, len(fused))
	for _, entry := range fused {
		entry.candidate.Score = entry.score
		ordered = append(ordered, entry)
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].score != ordered[right].score {
			return ordered[left].score > ordered[right].score
		}
		leftCertified := ordered[left].candidate.Status == "CERTIFIED"
		rightCertified := ordered[right].candidate.Status == "CERTIFIED"
		if leftCertified != rightCertified {
			return leftCertified
		}
		if ordered[left].candidate.Section != ordered[right].candidate.Section {
			return sectionRank(ordered[left].candidate.Section) < sectionRank(ordered[right].candidate.Section)
		}
		return ordered[left].candidate.Code < ordered[right].candidate.Code
	})
	result := make([]Candidate, len(ordered))
	for index, entry := range ordered {
		result[index] = entry.candidate
	}
	return result
}

// appendExpansion 把图扩展候选追加到直接命中之后：分数按锚点衰减，
// 已直接命中或分区不在请求范围内的邻居丢弃。
func appendExpansion(direct []Candidate, expanded []Candidate, sections []Section) []Candidate {
	allowed := map[Section]struct{}{}
	for _, section := range sections {
		allowed[section] = struct{}{}
	}
	seen := map[string]struct{}{}
	anchorScores := map[string]float64{}
	for _, candidate := range direct {
		seen[candidate.identity()] = struct{}{}
		anchorScores[candidate.Code] = candidate.Score
	}
	result := direct
	for _, candidate := range expanded {
		if _, duplicate := seen[candidate.identity()]; duplicate {
			continue
		}
		if _, ok := allowed[candidate.Section]; !ok {
			continue
		}
		seen[candidate.identity()] = struct{}{}
		candidate.Sources = []Source{SourceExpanded}
		candidate.Score = anchorScores[candidate.ExpandedFrom] * expansionDecay
		result = append(result, candidate)
	}
	return result
}

func mergeSources(existing, incoming []Source) []Source {
	seen := map[Source]struct{}{}
	result := append([]Source{}, existing...)
	for _, source := range existing {
		seen[source] = struct{}{}
	}
	for _, source := range incoming {
		if _, duplicate := seen[source]; !duplicate {
			seen[source] = struct{}{}
			result = append(result, source)
		}
	}
	return result
}

func sectionRank(section Section) int {
	for index, ordered := range AllSections {
		if ordered == section {
			return index
		}
	}
	return len(AllSections)
}
