package dataset

import (
	"context"
	"fmt"
)

const maxSourceDependencyDepth = 16

// SourceDependencyResolver loads the exact immutable document referenced by a
// DATASET node. Implementations must enforce tenant isolation and the accepted
// draft/published status before returning the document.
type SourceDependencyResolver interface {
	ResolveDatasetVersionDocument(context.Context, string) (Document, error)
}

// ValidateSourceDependencies verifies SINGLE_SOURCE/CROSS_SOURCE against the
// physical data-source identities below logical DATASET nodes. Base DSL
// validation cannot do this because a governed node intentionally contains
// only a dataset version ID, never a datasourceId.
func ValidateSourceDependencies(
	ctx context.Context,
	document Document,
	resolver SourceDependencyResolver,
) error {
	if resolver == nil {
		for _, node := range document.Nodes {
			if node.Type == "DATASET" {
				return fmt.Errorf(
					"%w: %w", ErrInvalidDocument,
					ErrLayerDependencyUnavailable,
				)
			}
		}
		return nil
	}

	collector := sourceDependencyCollector{
		resolver: resolver,
		cache:    map[string]map[string]bool{},
		visiting: map[string]bool{},
	}
	sourceIDs, err := collector.documentSources(ctx, document, 0)
	if err != nil {
		return err
	}
	issues := make([]ValidationIssue, 0, 1)
	if document.Dataset.Type == "SINGLE_SOURCE" && len(sourceIDs) > 1 {
		issues = append(issues, ValidationIssue{
			Path:   "dataset.type",
			Reason: "SINGLE_SOURCE 的全部逻辑依赖必须归属于同一物理数据源",
		})
	}
	if document.Dataset.Type == "CROSS_SOURCE" && len(sourceIDs) < 2 {
		issues = append(issues, ValidationIssue{
			Path:   "dataset.type",
			Reason: "CROSS_SOURCE 的逻辑依赖必须解析到至少两个不同物理数据源",
		})
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

type sourceDependencyCollector struct {
	resolver SourceDependencyResolver
	cache    map[string]map[string]bool
	visiting map[string]bool
}

func (collector *sourceDependencyCollector) documentSources(
	ctx context.Context,
	document Document,
	depth int,
) (map[string]bool, error) {
	if depth > maxSourceDependencyDepth {
		return nil, fmt.Errorf(
			"%w: dataset source dependency depth exceeds %d",
			ErrInvalidDocument, maxSourceDependencyDepth,
		)
	}
	result := map[string]bool{}
	for _, node := range document.Nodes {
		switch node.Type {
		case "TABLE":
			if node.DataSourceID == "" {
				return nil, fmt.Errorf(
					"%w: physical source dependency is incomplete",
					ErrInvalidDocument,
				)
			}
			result[node.DataSourceID] = true
		case "DATASET":
			sourceIDs, err := collector.versionSources(
				ctx, node.DatasetVersionID, depth+1,
			)
			if err != nil {
				return nil, err
			}
			for sourceID := range sourceIDs {
				result[sourceID] = true
			}
		default:
			return nil, fmt.Errorf(
				"%w: unsupported source dependency node",
				ErrInvalidDocument,
			)
		}
	}
	return result, nil
}

func (collector *sourceDependencyCollector) versionSources(
	ctx context.Context,
	versionID string,
	depth int,
) (map[string]bool, error) {
	if versionID == "" {
		return nil, fmt.Errorf(
			"%w: dataset source dependency version is empty",
			ErrInvalidDocument,
		)
	}
	if cached, exists := collector.cache[versionID]; exists {
		return copySourceIDs(cached), nil
	}
	if collector.visiting[versionID] {
		return nil, fmt.Errorf(
			"%w: dataset source dependency cycle detected",
			ErrInvalidDocument,
		)
	}
	collector.visiting[versionID] = true
	defer delete(collector.visiting, versionID)

	document, err := collector.resolver.ResolveDatasetVersionDocument(
		ctx, versionID,
	)
	if err != nil {
		return nil, err
	}
	sourceIDs, err := collector.documentSources(ctx, document, depth)
	if err != nil {
		return nil, err
	}
	collector.cache[versionID] = copySourceIDs(sourceIDs)
	return sourceIDs, nil
}

func copySourceIDs(source map[string]bool) map[string]bool {
	result := make(map[string]bool, len(source))
	for sourceID := range source {
		result[sourceID] = true
	}
	return result
}
