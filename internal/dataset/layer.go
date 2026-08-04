package dataset

import (
	"context"
	"fmt"
)

// LayerDependencyResolver 把精确发布版本解析为其不可变层级。领域校验不接受
// “当前发布版本”指针，避免上游重新发布后悄悄改变下游的层级合同。
type LayerDependencyResolver interface {
	ResolveDatasetVersionLayer(context.Context, string) (Layer, error)
}

// ValidateLayerDependencies 校验显式 DATASET 上游的层级方向：
//
//	DIM <- ODS
//	DWD <- at least one ODS + optional DIM
//	DWS <- DWD or DIM (DIM is limited to factless entity-count aggregation)
//	ADS <- DWS
//
// 显式声明 layer 的新 DIM/DWD/DWS/ADS 文档由基础校验强制只使用 DATASET 节点；这里继续
// 解析每个精确版本的实际层级。未声明 layer 的历史正文可按稳定推断结果
// grandfather，但不会因此放宽它已声明的 DATASET 上游。
func ValidateLayerDependencies(ctx context.Context, document Document, resolver LayerDependencyResolver) error {
	if resolver == nil {
		for _, node := range document.Nodes {
			if node.Type == "DATASET" {
				return fmt.Errorf("%w: %w", ErrInvalidDocument, ErrLayerDependencyUnavailable)
			}
		}
		return nil
	}

	layer := document.Dataset.Layer
	if layer == "" {
		layer = InferLayer(document)
	}
	if IsPreAggregatedSourceMapping(document) {
		// The physical table itself is the immutable analytical-grain source;
		// there is no governed DWD/DIM version to resolve for this narrow mode.
		return nil
	}
	var expected map[Layer]bool
	switch layer {
	case LayerDIM:
		expected = map[Layer]bool{LayerODS: true}
	case LayerDWD:
		expected = map[Layer]bool{LayerODS: true, LayerDIM: true}
	case LayerDWS:
		expected = map[Layer]bool{LayerDWD: true, LayerDIM: true}
	case LayerADS:
		expected = map[Layer]bool{LayerDWS: true}
	default:
		// ODS 的单物理表约束由 Validate 处理；非法枚举也由基础校验报告。
		return nil
	}

	issues := make([]ValidationIssue, 0)
	hasODSInput := false
	hasDWDInput := false
	hasDIMInput := false
	for index, node := range document.Nodes {
		if node.Type != "DATASET" {
			continue
		}
		upstream, err := resolver.ResolveDatasetVersionLayer(ctx, node.DatasetVersionID)
		if err != nil {
			return err
		}
		if upstream == LayerODS {
			hasODSInput = true
		}
		if upstream == LayerDWD {
			hasDWDInput = true
		}
		if upstream == LayerDIM {
			hasDIMInput = true
		}
		if !expected[upstream] {
			expectedText := "ODS"
			if layer == LayerDWD {
				expectedText = "ODS 或 DIM"
			} else if layer == LayerDWS {
				expectedText = "DWD 或 DIM"
			} else if layer == LayerADS {
				expectedText = "DWS"
			}
			issues = append(issues, ValidationIssue{
				Path: fmt.Sprintf("nodes[%d].datasetVersionId", index),
				Reason: fmt.Sprintf("%s 只能引用 %s 层的已发布数据集版本，实际为 %s",
					layer, expectedText, upstream),
			})
		}
	}
	if layer == LayerDWD && !hasODSInput {
		issues = append(issues, ValidationIssue{
			Path:   "nodes",
			Reason: "DWD 至少需要一个已发布 ODS 数据集版本作为事实输入",
		})
	}
	if layer == LayerDWS && !hasDWDInput && !hasDIMInput {
		issues = append(issues, ValidationIssue{
			Path:   "nodes",
			Reason: "DWS 至少需要一个已发布 DWD 或 DIM 数据集版本作为分析输入",
		})
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
