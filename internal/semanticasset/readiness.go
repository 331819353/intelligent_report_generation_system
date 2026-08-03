package semanticasset

import (
	"fmt"
	"strings"
	"time"
)

const (
	ReadinessPass    = "PASS"
	ReadinessWarn    = "WARN"
	ReadinessBlocked = "BLOCKED"
)

// ReadinessSnapshot is the raw control-plane watermark read in one tenant
// transaction. It is intentionally not an HTTP contract; the service turns it
// into stable checks and an overall gate decision.
type ReadinessSnapshot struct {
	MetricTotal           int
	MetricReady           int
	DimensionTotal        int
	DimensionPublished    int
	DimensionReady        int
	TermActive            int
	TermProjected         int
	ParsingRuleTotal      int
	ParsingRuleActive     int
	DecisionCount         int
	DecisionGroupTotal    int
	DecisionGroupReady    int
	GraphState            string
	GraphGenerationID     string
	GraphGeneration       int64
	GraphGenerationState  string
	GraphRequestedVersion int64
	GraphAppliedVersion   int64
	GraphNodeCount        int
	GraphEdgeCount        int
	GraphErrorCode        string
}

type CatalogAssetCounts struct {
	Metrics         ReadinessCount `json:"metrics"`
	Dimensions      ReadinessCount `json:"dimensions"`
	Terms           ReadinessCount `json:"terms"`
	ParsingRules    ReadinessCount `json:"parsingRules"`
	DecisionGraph   ReadinessCount `json:"decisionGraph"`
	DecisionEntries int            `json:"decisionEntries"`
}

type ReadinessCount struct {
	Total int `json:"total"`
	Ready int `json:"ready"`
}

type CatalogGraphReadiness struct {
	Status                string `json:"status"`
	GenerationID          string `json:"generationId,omitempty"`
	Generation            int64  `json:"generation,omitempty"`
	RequestedEventVersion int64  `json:"requestedEventVersion"`
	AppliedEventVersion   int64  `json:"appliedEventVersion"`
	NodeCount             int    `json:"nodeCount"`
	EdgeCount             int    `json:"edgeCount"`
	ErrorCode             string `json:"errorCode,omitempty"`
}

type CatalogReadinessCheck struct {
	Code     string `json:"code"`
	Label    string `json:"label"`
	Status   string `json:"status"`
	Current  int64  `json:"current"`
	Required int64  `json:"required"`
	Detail   string `json:"detail"`
	Route    string `json:"route,omitempty"`
}

type CatalogReadiness struct {
	Status          string                  `json:"status"`
	QuestionEnabled bool                    `json:"questionEnabled"`
	SemanticVersion string                  `json:"semanticVersion,omitempty"`
	GeneratedAt     time.Time               `json:"generatedAt"`
	Counts          CatalogAssetCounts      `json:"counts"`
	Graph           CatalogGraphReadiness   `json:"graph"`
	Checks          []CatalogReadinessCheck `json:"checks"`
	BlockerCodes    []string                `json:"blockerCodes"`
}

func evaluateCatalogReadiness(
	snapshot ReadinessSnapshot,
	now time.Time,
) CatalogReadiness {
	graphReady := snapshot.GraphState == "READY" &&
		snapshot.GraphGenerationState == "READY" &&
		snapshot.GraphGenerationID != "" &&
		snapshot.GraphRequestedVersion > 0 &&
		snapshot.GraphAppliedVersion == snapshot.GraphRequestedVersion
	result := CatalogReadiness{
		Status:          ReadinessPass,
		QuestionEnabled: true,
		GeneratedAt:     now.UTC(),
		Counts: CatalogAssetCounts{
			Metrics: ReadinessCount{
				Total: snapshot.MetricTotal, Ready: snapshot.MetricReady,
			},
			Dimensions: ReadinessCount{
				Total: snapshot.DimensionPublished, Ready: snapshot.DimensionReady,
			},
			Terms: ReadinessCount{
				Total: snapshot.TermActive, Ready: snapshot.TermProjected,
			},
			ParsingRules: ReadinessCount{
				Total: snapshot.ParsingRuleTotal, Ready: snapshot.ParsingRuleActive,
			},
			DecisionGraph: ReadinessCount{
				Total: snapshot.DecisionGroupTotal, Ready: snapshot.DecisionGroupReady,
			},
			DecisionEntries: snapshot.DecisionCount,
		},
		Graph: CatalogGraphReadiness{
			Status: snapshot.GraphState, GenerationID: snapshot.GraphGenerationID,
			Generation:            snapshot.GraphGeneration,
			RequestedEventVersion: snapshot.GraphRequestedVersion,
			AppliedEventVersion:   snapshot.GraphAppliedVersion,
			NodeCount:             snapshot.GraphNodeCount, EdgeCount: snapshot.GraphEdgeCount,
			ErrorCode: snapshot.GraphErrorCode,
		},
		Checks:       []CatalogReadinessCheck{},
		BlockerCodes: []string{},
	}
	if graphReady {
		result.SemanticVersion = fmt.Sprintf(
			"graph:%d:event:%d", snapshot.GraphGeneration,
			snapshot.GraphAppliedVersion,
		)
	}
	result.Checks = append(result.Checks,
		coverageCheck(
			"METRIC_CONTRACT_READY", "认证指标合同", snapshot.MetricReady,
			snapshot.MetricTotal, true, "/assets/metrics",
			"已发布且固定到不可变指标版本",
		),
		coverageCheck(
			"DIMENSION_INDEX_READY", "维度与成员策略", snapshot.DimensionReady,
			snapshot.DimensionPublished, false, "/assets/metrics",
			"发布维度已声明并完成成员索引策略",
		),
		coverageCheck(
			"TERM_PROJECTION_READY", "业务词汇检索投影", snapshot.TermProjected,
			snapshot.TermActive, false, "/assets/semantics",
			"生效词汇已生成可用检索投影",
		),
		coverageCheck(
			"DIMENSION_DECISION_READY", "维值决策关系", snapshot.DecisionGroupReady,
			snapshot.DecisionGroupTotal, false, "/assets/dimension-values",
			"维值决策组没有待构建或失败策略",
		),
	)
	graphStatus := ReadinessBlocked
	if graphReady {
		graphStatus = ReadinessPass
	}
	graphDetail := fmt.Sprintf(
		"Generation %d，事件水位 %d/%d",
		snapshot.GraphGeneration, snapshot.GraphAppliedVersion,
		snapshot.GraphRequestedVersion,
	)
	if snapshot.GraphGenerationID == "" {
		graphDetail = "尚无可供查询固定的语义图版本"
	} else if strings.TrimSpace(snapshot.GraphErrorCode) != "" {
		graphDetail += "，错误码 " + snapshot.GraphErrorCode
	}
	result.Checks = append(result.Checks, CatalogReadinessCheck{
		Code: "SEMANTIC_GRAPH_READY", Label: "运行时语义图",
		Status: graphStatus, Current: snapshot.GraphAppliedVersion,
		Required: snapshot.GraphRequestedVersion, Detail: graphDetail,
	})
	for _, check := range result.Checks {
		if check.Status == ReadinessBlocked {
			result.Status = ReadinessBlocked
			result.QuestionEnabled = false
			result.BlockerCodes = append(result.BlockerCodes, check.Code)
		} else if check.Status == ReadinessWarn && result.Status == ReadinessPass {
			result.Status = ReadinessWarn
		}
	}
	return result
}

func coverageCheck(
	code, label string,
	ready, total int,
	blocking bool,
	route, readyDescription string,
) CatalogReadinessCheck {
	status := ReadinessPass
	if total == 0 || ready < total {
		status = ReadinessWarn
		if blocking && ready == 0 {
			status = ReadinessBlocked
		}
	}
	detail := fmt.Sprintf("%d / %d，%s", ready, total, readyDescription)
	return CatalogReadinessCheck{
		Code: code, Label: label, Status: status,
		Current: int64(ready), Required: int64(total), Detail: detail,
		Route: route,
	}
}
