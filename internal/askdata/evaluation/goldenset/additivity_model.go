package goldenset

import (
	"encoding/json"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/registry"
)

// The synthetic model below is a shape, not a business definition. It carries
// the field roles and canonical types the compiler needs to reason about
// aggregation, and nothing that could be mistaken for a real company's data.
const (
	goldenTenantID       askdata.ID = "tenant-golden"
	goldenActorID        askdata.ID = "actor-golden"
	goldenRoleID         askdata.ID = "analyst"
	goldenDomainID       askdata.ID = "domain-golden"
	goldenReleaseID      askdata.ID = "release-golden-additivity-v1"
	goldenModelObjectID  askdata.ID = "model-golden-sales"
	goldenModelVersionID askdata.ID = "model-golden-sales-v1"
)

// Field identifiers must satisfy both askdata.ID and the Dataset DSL identifier
// pattern, because the compiler projects a model field id straight into the
// generated document. That intersection is letters, digits and underscores.
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
	dimensionRegionObject    askdata.ID = "dimension-golden-region"
	dimensionRegion          askdata.ID = "dimension-golden-region-v1"
	dimensionChannelObject   askdata.ID = "dimension-golden-channel"
	dimensionChannel         askdata.ID = "dimension-golden-channel-v1"
	dimensionOrderDateObject askdata.ID = "dimension-golden-order-date"
	dimensionOrderDate       askdata.ID = "dimension-golden-order-date-v1"
)

func goldenFields() ([]compiler.FieldContract, error) {
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
		// The hash is stamped by the compiler's own constructor rather than
		// recomputed here; validateSnapshot checks it, and a fixture that
		// reimplemented the algorithm would drift from the real store.
		field, err := compiler.NewFieldContract(compiler.FieldContract{
			FieldID: definition.id, Code: definition.code, Role: definition.role,
			CanonicalType: definition.canonicalType, Nullable: false, Visible: true,
		})
		if err != nil {
			return nil, err
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func goldenModel() (compiler.ModelContract, error) {
	fields, err := goldenFields()
	if err != nil {
		return compiler.ModelContract{}, err
	}
	grain, err := canonicalAST(`{"keys":["order_date","region","channel"]}`)
	if err != nil {
		return compiler.ModelContract{}, err
	}
	primaryTime := fieldOrderDate
	schemaHash := askdata.HashBytes([]byte("golden-model-schema-v1"))
	return compiler.ModelContract{
		ModelVersionID:     goldenModelVersionID,
		ContentHash:        askdata.HashBytes([]byte("golden-model-content-v1")),
		DatasetSchemaHash:  schemaHash,
		GrainContract:      grain,
		PrimaryTimeFieldID: &primaryTime,
		Fields:             fields,
		Materialization: compiler.MaterializationContract{
			MaterializationID: "materialization-golden-v1", DatasetID: "dataset-golden",
			DatasetVersionID: "dataset-version-golden-v1", Layer: "DWS", Status: "ACTIVE",
			PublishedSchema: "warehouse_published", PublishedName: "dws_golden_sales",
			SchemaHash: schemaHash, SnapshotHash: askdata.HashBytes([]byte("golden-snapshot-v1")),
			RowCount: 1000,
		},
	}, nil
}

func goldenDimensions() map[askdata.ID]compiler.DimensionContract {
	definitions := []struct {
		id      askdata.ID
		fieldID askdata.ID
		kind    registry.DimensionKind
	}{
		{dimensionRegion, fieldRegion, registry.DimensionCategorical},
		{dimensionChannel, fieldChannel, registry.DimensionCategorical},
		// The time dimension must point at the model's primary time field: the
		// resolver refuses a time dimension that names any other column.
		{dimensionOrderDate, fieldOrderDate, registry.DimensionTime},
	}
	dimensions := make(map[askdata.ID]compiler.DimensionContract, len(definitions))
	for _, definition := range definitions {
		dimensions[definition.id] = compiler.DimensionContract{
			DimensionVersionID: definition.id, ModelVersionID: goldenModelVersionID,
			LogicalFieldID: definition.fieldID, Kind: definition.kind,
			ContentHash:       askdata.HashBytes([]byte("golden-dimension|" + string(definition.id))),
			Sensitivity:       registry.SensitivityPublic,
			MemberIndexPolicy: registry.MemberIndexFull,
		}
	}
	return dimensions
}

// canonicalAST normalizes hand-written contract JSON. The resolver requires
// every governed AST to be byte-identical to its canonical form, so a fixture
// that skipped this would pass a suite that production would refuse to run.
func canonicalAST(raw string) (json.RawMessage, error) {
	canonical, err := registry.CanonicalJSON(json.RawMessage(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: canonical AST %q: %v", ErrAdditivityGoldenSet, raw, err)
	}
	return canonical, nil
}

func fieldReferenceAST(fieldID askdata.ID) (json.RawMessage, error) {
	return canonicalAST(fmt.Sprintf(`{"type":"FIELD_REF","fieldId":%q}`, fieldID))
}

func measureReferenceAST(measureVersionID askdata.ID) string {
	return fmt.Sprintf(`{"type":"MEASURE_REF","measureVersionId":%q}`, measureVersionID)
}

func goldenMeasure(
	suffix string,
	fieldID askdata.ID,
	aggregation registry.Aggregation,
	dataType registry.NumericDataType,
) (compiler.MeasureContract, error) {
	formula, err := fieldReferenceAST(fieldID)
	if err != nil {
		return compiler.MeasureContract{}, err
	}
	additivity, semi, restriction := measureAdditivityFor(aggregation)
	measureID := askdata.ID("measure-golden-" + suffix)
	return compiler.MeasureContract{
		MeasureID: measureID, MeasureVersionID: measureID + "-v1", ModelVersionID: goldenModelVersionID,
		ContentHash: askdata.HashBytes([]byte("golden-measure|" + suffix)),
		FormulaAST:  formula, Aggregation: aggregation, DataType: dataType,
		Additivity: additivity, SemiAdditiveTimeAggregation: semi, AggregationRestriction: restriction,
		// Measure units are uniform on purpose: the incompatible-unit rule is a
		// property of the metrics a single chart puts side by side, and
		// CheckUnitCompatibility reads the metric contract, not the measure.
		Unit:                  "UNIT",
		NonAdditiveDimensions: []string{}, ZeroDenominatorPolicy: registry.ZeroDenominatorNull,
	}, nil
}

// measureAdditivityFor derives the additivity a measure with this aggregation
// is allowed to declare. A distinct count or an average cannot be re-added, and
// a min or max is only meaningful once reduced over a period — the registry
// refuses the other combinations at authoring time, so a fixture that declared
// them would be describing a measure the platform would never certify.
func measureAdditivityFor(aggregation registry.Aggregation) (
	registry.Additivity, registry.SemiAdditiveTimeAggregation, registry.AggregationRestriction,
) {
	switch aggregation {
	case registry.AggregationCountDistinct, registry.AggregationAverage:
		return registry.NonAdditive, "", registry.PostAggregate
	case registry.AggregationMinimum, registry.AggregationMaximum:
		return registry.SemiAdditive, registry.SemiAdditivePeriodEnd, ""
	default:
		return registry.FullyAdditive, "", ""
	}
}
