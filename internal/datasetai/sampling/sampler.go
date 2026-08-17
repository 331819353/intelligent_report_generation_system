// Package sampling adapts the governed asset sampling path (asset repository +
// datasource connectors) to the dataset AI TableSampler contract. It reads at most
// a handful of rows, never accepts SQL, and returns "no sample" for tables the
// connectors cannot sample (published dataset versions, files without a sampler).
package sampling

import (
	"context"
	"errors"
	"strings"

	"intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/datasetai"
	"intelligent-report-generation-system/internal/datasource"
)

// AssetReader is the subset of the asset repository the sampler needs.
type AssetReader interface {
	GetTable(ctx context.Context, tenantID, id string) (asset.Table, error)
	ListColumns(ctx context.Context, tenantID, tableID string) ([]asset.Column, error)
}

// SourceSampler is datasource.Service's governed sampling entry point.
type SourceSampler interface {
	SampleTable(ctx context.Context, tenantID, sourceID string, table datasource.MetadataTable, maxRows int) (datasource.SampleResult, error)
}

type Sampler struct {
	assets  AssetReader
	sources SourceSampler
}

func New(assets AssetReader, sources SourceSampler) (*Sampler, error) {
	if assets == nil || sources == nil {
		return nil, errors.New("sampler requires an asset reader and a source sampler")
	}
	return &Sampler{assets: assets, sources: sources}, nil
}

var _ datasetai.TableSampler = (*Sampler)(nil)

// SampleTable implements datasetai.TableSampler. Published dataset versions
// (`dataset-version:*`) have no connector sample; callers treat that as
// "metadata only".
func (sampler *Sampler) SampleTable(ctx context.Context, tenantID, tableID string, maxRows int) (datasetai.TableSample, error) {
	if strings.HasPrefix(tableID, "dataset-version:") {
		return datasetai.TableSample{}, errors.New("dataset versions are not sampled")
	}
	if maxRows < 1 {
		maxRows = 1
	}
	if maxRows > 5 {
		maxRows = 5
	}
	table, err := sampler.assets.GetTable(ctx, tenantID, tableID)
	if err != nil {
		return datasetai.TableSample{}, err
	}
	columns, err := sampler.assets.ListColumns(ctx, tenantID, tableID)
	if err != nil {
		return datasetai.TableSample{}, err
	}
	result, err := sampler.sources.SampleTable(ctx, tenantID, table.DataSourceID, asset.MetadataTableForPreview(table, columns), maxRows)
	if err != nil {
		return datasetai.TableSample{}, err
	}
	return datasetai.TableSample{Columns: result.Columns, Rows: result.Rows}, nil
}
