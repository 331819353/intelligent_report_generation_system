package goldenset

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// The synthetic model below is a shape, not a business definition. It carries
// the field roles and canonical types the compiler needs to reason about
// aggregation, and nothing that could be mistaken for a real company's data.
const (
	goldenReleaseID      = "release-golden-additivity-v1"
	goldenDomainID       = "domain-golden"
	goldenModelVersionID = "model-golden-sales-v1"
)

const (
	fieldOrderDate      askdata.ID = "field_golden_order_date"
	fieldRegion         askdata.ID = "field_golden_region"
	fieldChannel        askdata.ID = "field_golden_channel"
	fieldCustomer       askdata.ID = "field_golden_customer"
	fieldNetSales       askdata.ID = "field_golden_net_sales"
	fieldGrossProfit    askdata.ID = "field_golden_gross_profit"
	fieldOrderCount     askdata.ID = "field_golden_order_count"
	fieldInventoryUnits askdata.ID = "field_golden_inventory_units"
	fieldAccountBalance askdata.ID = "field_golden_account_balance"
)

const (
	dimensionRegion    askdata.ID = "dimension-golden-region-v1"
	dimensionChannel   askdata.ID = "dimension-golden-channel-v1"
	dimensionOrderDate askdata.ID = "dimension-golden-order-date-v1"
)

func goldenFields() []compiler.FieldContract {
	definitions := []struct {
		id            askdata.ID
		code          string
		role          string
		canonicalType string
	}{
		{fieldOrderDate, "order_date", "TIME", "DATE"},
		{fieldRegion, "region", "DIMENSION", "STRING"},
		{fieldChannel, "channel", "DIMENSION", "STRING"},
		{fieldCustomer, "customer_id", "IDENTIFIER", "STRING"},
		{fieldNetSales, "net_sales", "MEASURE", "DECIMAL"},
		{fieldGrossProfit, "gross_profit", "MEASURE", "DECIMAL"},
		{fieldOrderCount, "order_count", "MEASURE", "INTEGER"},
		{fieldInventoryUnits, "inventory_units", "MEASURE", "DECIMAL"},
		{fieldAccountBalance, "account_balance", "MEASURE", "DECIMAL"},
	}
	fields := make([]compiler.FieldContract, 0, len(definitions))
	for _, definition := range definitions {
		fields = append(fields, compiler.FieldContract{
			FieldID: definition.id, Code: definition.code, Role: definition.role,
			CanonicalType: definition.canonicalType, Nullable: false, Visible: true,
			ContractHash: askdata.HashBytes([]byte("golden-field|" + definition.code)),
		})
	}
	return fields
}

func goldenModel() compiler.ModelContract {
	primaryTime := fieldOrderDate
	schemaHash := askdata.HashBytes([]byte("golden-model-schema-v1"))
	return compiler.ModelContract{
		ModelVersionID:     goldenModelVersionID,
		ContentHash:        askdata.HashBytes([]byte("golden-model-content-v1")),
		DatasetSchemaHash:  schemaHash,
		GrainContract:      json.RawMessage(`{"keys":["order_date","region","channel"]}`),
		PrimaryTimeFieldID: &primaryTime,
		Fields:             goldenFields(),
		Materialization: compiler.MaterializationContract{
			MaterializationID: "materialization-golden-v1", DatasetID: "dataset-golden",
			DatasetVersionID: "dataset-version-golden-v1", Layer: "DWS", Status: "ACTIVE",
			PublishedSchema: "warehouse_published", PublishedName: "dws_golden_sales",
			SchemaHash: schemaHash, SnapshotHash: askdata.HashBytes([]byte("golden-snapshot-v1")),
			RowCount: 1000,
		},
	}
}

func goldenDimensions() []compiler.DimensionContract {
	definitions := []struct {
		id      askdata.ID
		fieldID askdata.ID
		kind    registry.DimensionKind
	}{
		{dimensionRegion, fieldRegion, registry.DimensionCategorical},
		{dimensionChannel, fieldChannel, registry.DimensionCategorical},
		{dimensionOrderDate, fieldOrderDate, registry.DimensionTime},
	}
	dimensions := make([]compiler.DimensionContract, 0, len(definitions))
	for _, definition := range definitions {
		dimensions = append(dimensions, compiler.DimensionContract{
			DimensionVersionID: definition.id, ModelVersionID: goldenModelVersionID,
			LogicalFieldID: definition.fieldID, Kind: definition.kind,
			ContentHash:       askdata.HashBytes([]byte("golden-dimension|" + string(definition.id))),
			Sensitivity:       registry.SensitivityPublic,
			MemberIndexPolicy: registry.MemberIndexFull,
		})
	}
	return dimensions
}

func fieldReferenceAST(fieldID askdata.ID) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"FIELD_REF","fieldId":%q}`, fieldID))
}

func measureReferenceAST(measureVersionID askdata.ID) string {
	return fmt.Sprintf(`{"type":"MEASURE_REF","measureVersionId":%q}`, measureVersionID)
}

func goldenMeasure(
	suffix string,
	fieldID askdata.ID,
	aggregation registry.Aggregation,
	dataType registry.NumericDataType,
) compiler.MeasureContract {
	measureID := askdata.ID("measure-golden-" + suffix)
	return compiler.MeasureContract{
		MeasureID: measureID, MeasureVersionID: measureID + "-v1", ModelVersionID: goldenModelVersionID,
		ContentHash: askdata.HashBytes([]byte("golden-measure|" + suffix)),
		FormulaAST:  fieldReferenceAST(fieldID), Aggregation: aggregation, DataType: dataType,
		NonAdditiveDimensions: []string{}, ZeroDenominatorPolicy: registry.ZeroDenominatorNull,
	}
}

// metricAlias reproduces the alias the IR builder assigns. Diverging from it
// would make the golden plan a different plan than production compiles.
func metricAlias(metricVersionID askdata.ID) string {
	return "metric_" + string(askdata.HashBytes([]byte(metricVersionID))[:56])
}

func goldenScope() (askdata.PolicyScope, error) {
	release := askdata.ReleaseRef{
		ReleaseID: goldenReleaseID, ContentHash: askdata.HashBytes([]byte(goldenReleaseID)),
	}
	return askdata.NewPolicyScope(
		"tenant-golden", "actor-golden", []askdata.ID{goldenDomainID}, []askdata.ID{"analyst"}, release,
	)
}

func goldenSemanticIR(
	metrics []compiler.MetricContract,
	groupBy []askdata.ID,
	withTimeRange bool,
) ir.SemanticIR {
	selected := make([]ir.Metric, 0, len(metrics))
	for _, metric := range metrics {
		selected = append(selected, ir.Metric{
			MetricVersionID: metric.MetricVersionID, Alias: metricAlias(metric.MetricVersionID),
		})
	}
	groups := make([]ir.GroupBy, 0, len(groupBy))
	for _, dimensionVersionID := range groupBy {
		groups = append(groups, ir.GroupBy{DimensionVersionID: dimensionVersionID})
	}
	semanticIR := ir.SemanticIR{
		IRVersion: ir.Version, SemanticReleaseID: goldenReleaseID,
		SemanticContentHash: askdata.HashBytes([]byte(goldenReleaseID)),
		DomainID:            goldenDomainID, ModelVersionID: goldenModelVersionID,
		Metrics: selected, GroupBy: groups, Filters: []ir.Filter{}, Sort: []ir.Sort{},
		Limit: ir.DefaultLimit, OtherPolicy: ir.OtherNone, TieBreaking: ir.TieIncludeAll,
	}
	if withTimeRange {
		semanticIR.TimeRange = &ir.TimeRange{
			DimensionVersionID: dimensionOrderDate, Start: "2026-01-01", EndExclusive: "2026-08-01",
			Timezone: "Asia/Shanghai", RequestedPeriod: "ABSOLUTE", Grain: ir.TimeGrainMonth,
		}
	}
	return semanticIR
}

func goldenResolution(
	scope askdata.PolicyScope,
	metrics []compiler.MetricContract,
	dimensionIDs []askdata.ID,
	timeDimension *askdata.ID,
) compiler.Resolution {
	dimensions := make([]compiler.DimensionContract, 0, len(dimensionIDs))
	catalog := goldenDimensions()
	for _, id := range dimensionIDs {
		for _, dimension := range catalog {
			if dimension.DimensionVersionID == id {
				dimensions = append(dimensions, dimension)
			}
		}
	}
	return compiler.Resolution{
		Version: compiler.ResolutionVersion, Scope: scope, DomainID: goldenDomainID,
		IRHash:                 askdata.HashBytes([]byte("golden-ir")),
		BuildArtifactHash:      askdata.HashBytes([]byte("golden-build-artifact")),
		GraphPlanHash:          askdata.HashBytes([]byte("golden-graph-plan")),
		ResolutionHash:         askdata.HashBytes([]byte("golden-resolution")),
		TimeDimensionVersionID: timeDimension,
		MemberBindings:         []compiler.MemberBinding{},
		Model:                  goldenModel(), Metrics: metrics, Dimensions: dimensions,
		Members: []compiler.MemberContract{}, Relationships: []compiler.RelationshipContract{},
	}
}
