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
//	DWD <- ODS
//	DWS <- DWD
//
// 显式声明 layer 的新 DWD/DWS 文档由基础校验强制只使用 DATASET 节点；这里继续
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
	var expected Layer
	switch layer {
	case LayerDWD:
		expected = LayerODS
	case LayerDWS:
		expected = LayerDWD
	default:
		// ODS 的单物理表约束由 Validate 处理；非法枚举也由基础校验报告。
		return nil
	}

	issues := make([]ValidationIssue, 0)
	for index, node := range document.Nodes {
		if node.Type != "DATASET" {
			continue
		}
		upstream, err := resolver.ResolveDatasetVersionLayer(ctx, node.DatasetVersionID)
		if err != nil {
			return err
		}
		if upstream != expected {
			issues = append(issues, ValidationIssue{
				Path: fmt.Sprintf("nodes[%d].datasetVersionId", index),
				Reason: fmt.Sprintf("%s 只能引用 %s 层的已发布数据集版本，实际为 %s",
					layer, expected, upstream),
			})
		}
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}
