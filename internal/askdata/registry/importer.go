package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"intelligent-report-generation-system/internal/askdata"
)

type ImportedDraft struct {
	SemanticModel SemanticModel `json:"semanticModel"`
	Measures      []Measure     `json:"measures"`
	Dimensions    []Dimension   `json:"dimensions"`
}

type ImportResult struct {
	TenantID string          `json:"tenantId"`
	DomainID string          `json:"domainId"`
	Drafts   []ImportedDraft `json:"drafts"`
}

type ImportedDraftStore interface {
	SaveImportedDraft(context.Context, ImportedDraft) error
}

type Importer struct {
	inventory *InventoryService
	store     ImportedDraftStore
}

func NewImporter(inventoryStore InventoryStore, draftStore ImportedDraftStore) *Importer {
	return &Importer{inventory: NewInventoryService(inventoryStore), store: draftStore}
}

// Import reads only current published DWS/ADS control-plane metadata. It does
// not connect to the warehouse and never certifies a generated object.
func (importer *Importer) Import(ctx context.Context, tenantID, domainID, actorID string) (ImportResult, error) {
	if importer == nil || importer.inventory == nil || importer.store == nil {
		return ImportResult{}, errors.New("semantic registry importer is not configured")
	}
	for name, value := range map[string]string{"tenant ID": tenantID, "domain ID": domainID, "actor ID": actorID} {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed.String() != strings.ToLower(value) {
			return ImportResult{}, fmt.Errorf("%s must be a canonical UUID", name)
		}
	}
	inventory, err := importer.inventory.List(ctx, tenantID, InventoryOptions{})
	if err != nil {
		return ImportResult{}, err
	}
	result := ImportResult{TenantID: tenantID, DomainID: domainID, Drafts: []ImportedDraft{}}
	for _, asset := range inventory.Assets {
		if asset.DomainID != domainID {
			continue
		}
		draft, err := buildImportedDraft(tenantID, actorID, asset)
		if err != nil {
			return ImportResult{}, fmt.Errorf("import dataset version %s: %w", asset.DatasetVersionID, err)
		}
		if err := importer.store.SaveImportedDraft(ctx, draft); err != nil {
			return ImportResult{}, fmt.Errorf("save dataset version %s draft: %w", asset.DatasetVersionID, err)
		}
		result.Drafts = append(result.Drafts, draft)
	}
	sort.Slice(result.Drafts, func(left, right int) bool {
		return result.Drafts[left].SemanticModel.Code < result.Drafts[right].SemanticModel.Code
	})
	return result, nil
}

func buildImportedDraft(tenantID, actorID string, asset InventoryAsset) (ImportedDraft, error) {
	modelID := stableImportID("semantic-model", asset.DatasetID)
	modelVersionID := stableImportID("semantic-model-version", asset.DatasetVersionID)
	primaryTimeFieldID := ""
	for _, field := range asset.Fields {
		if field.Code == asset.OutputGrain.TimeField {
			primaryTimeFieldID = field.FieldID
			break
		}
	}
	grainContract, _ := json.Marshal(struct {
		Description      string   `json:"description"`
		KeyFieldIDs      []string `json:"keyFieldIds"`
		TimeFieldID      string   `json:"timeFieldId,omitempty"`
		DefaultTimeGrain string   `json:"defaultTimeGrain,omitempty"`
	}{
		Description: asset.OutputGrain.Description,
		KeyFieldIDs: mapFieldCodesToIDs(asset.Fields, asset.OutputGrain.KeyFields),
		TimeFieldID: primaryTimeFieldID, DefaultTimeGrain: asset.OutputGrain.DefaultTimeGrain,
	})
	model := SemanticModel{
		VersionIdentity: VersionIdentity{
			ID: modelVersionID, TenantID: tenantID, DomainID: asset.DomainID,
			ObjectID: modelID, VersionNo: asset.VersionNo, Status: VersionStatusDraft,
			OwnerID: actorID,
		},
		Code: asset.DatasetCode, Name: asset.DatasetName,
		DatasetID: asset.DatasetID, DatasetVersionID: asset.DatasetVersionID,
		MaterializationID: asset.MaterializationID,
		DatasetSchemaHash: askdata.ContentHash(asset.SchemaHash), Layer: asset.Layer,
		GrainContract: grainContract, PrimaryTimeFieldID: primaryTimeFieldID,
	}
	model.ContentHash = contentHashForContract(semanticModelContract(model))
	if err := model.Validate(); err != nil {
		return ImportedDraft{}, err
	}

	draft := ImportedDraft{SemanticModel: model, Measures: []Measure{}, Dimensions: []Dimension{}}
	for _, field := range asset.Fields {
		if !field.Visible {
			continue
		}
		if field.Role == "MEASURE" {
			measure, err := importedMeasure(model, asset, field, actorID)
			if err != nil {
				return ImportedDraft{}, err
			}
			draft.Measures = append(draft.Measures, measure)
			continue
		}
		dimension, err := importedDimension(model, asset, field, actorID)
		if err != nil {
			return ImportedDraft{}, err
		}
		draft.Dimensions = append(draft.Dimensions, dimension)
	}
	sort.Slice(draft.Measures, func(left, right int) bool { return draft.Measures[left].Code < draft.Measures[right].Code })
	sort.Slice(draft.Dimensions, func(left, right int) bool { return draft.Dimensions[left].Code < draft.Dimensions[right].Code })
	return draft, nil
}

func importedMeasure(model SemanticModel, asset InventoryAsset, field InventoryField, actorID string) (Measure, error) {
	dataType := NumericDataType(field.CanonicalType)
	if dataType != NumericInteger && dataType != NumericDecimal {
		return Measure{}, fmt.Errorf("measure field %s must be INTEGER or DECIMAL", field.FieldID)
	}
	aggregation := Aggregation(strings.ToUpper(firstNonEmpty(field.Aggregation, "SUM")))
	additivitySuggestion := Additivity(strings.ToUpper(field.Additivity))
	if additivitySuggestion == "ADDITIVE" {
		additivitySuggestion = FullyAdditive
	}
	if additivitySuggestion == "" {
		switch aggregation {
		case AggregationAverage, AggregationCountDistinct:
			additivitySuggestion = NonAdditive
		case AggregationMinimum, AggregationMaximum:
			additivitySuggestion = SemiAdditive
		default:
			additivitySuggestion = FullyAdditive
		}
	}
	measureID := stableImportID("measure", asset.DatasetID, field.FieldID)
	versionID := stableImportID("measure-version", asset.DatasetVersionID, field.FieldID)
	formula, _ := json.Marshal(struct {
		Type    string `json:"type"`
		FieldID string `json:"fieldId"`
	}{"FIELD_REF", field.FieldID})
	measure := Measure{
		VersionIdentity: VersionIdentity{
			ID: versionID, TenantID: model.TenantID, DomainID: model.DomainID,
			ObjectID: measureID, VersionNo: asset.VersionNo, Status: VersionStatusDraft,
			OwnerID: actorID,
		},
		SemanticModelVersionID: model.ID, Code: field.Code, Name: firstNonEmpty(field.Name, field.Code),
		FormulaAST: formula, Aggregation: aggregation,
		AdditivitySuggestion:  additivitySuggestion,
		ZeroDenominatorPolicy: ZeroDenominatorNull,
		DataType:              dataType, Unit: field.Unit,
	}
	measure.ContentHash = contentHashForContract(measureContract(measure))
	return measure, measure.Validate()
}

func importedDimension(model SemanticModel, asset InventoryAsset, field InventoryField, actorID string) (Dimension, error) {
	kind := DimensionCategorical
	if field.Role == "TIME" || field.CanonicalType == "DATE" || field.CanonicalType == "DATETIME" {
		kind = DimensionTime
	} else if field.Role == "IDENTIFIER" {
		kind = DimensionEntity
	}
	dimensionID := stableImportID("dimension", asset.DatasetID, field.FieldID)
	versionID := stableImportID("dimension-version", asset.DatasetVersionID, field.FieldID)
	dimension := Dimension{
		VersionIdentity: VersionIdentity{
			ID: versionID, TenantID: model.TenantID, DomainID: model.DomainID,
			ObjectID: dimensionID, VersionNo: asset.VersionNo, Status: VersionStatusDraft,
			OwnerID: actorID,
		},
		SemanticModelVersionID: model.ID, LogicalFieldID: field.FieldID,
		Code: field.Code, Name: firstNonEmpty(field.Name, field.Code), Kind: kind,
		Sensitivity: SensitivityInternal, MemberIndexPolicy: MemberIndexExactOnly,
	}
	dimension.ContentHash = contentHashForContract(dimensionContract(dimension))
	return dimension, dimension.Validate()
}

func stableImportID(parts ...string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("askdata/import/v1/"+strings.Join(parts, "\x00"))).String()
}

func mapFieldCodesToIDs(fields []InventoryField, codes []string) []string {
	idsByCode := make(map[string]string, len(fields))
	for _, field := range fields {
		idsByCode[field.Code] = field.FieldID
	}
	result := make([]string, 0, len(codes))
	for _, code := range codes {
		if id := idsByCode[code]; id != "" {
			result = append(result, id)
		}
	}
	return result
}
