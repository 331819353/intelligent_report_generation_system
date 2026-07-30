package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

type scriptedDIMValidationInvoker struct {
	results []aiplatform.InvocationResult
	errors  []error
	calls   []aiplatform.Invocation
	models  string
}

func (invoker *scriptedDIMValidationInvoker) Configured() bool { return true }
func (invoker *scriptedDIMValidationInvoker) Model() string {
	return invoker.models
}

func (invoker *scriptedDIMValidationInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.calls = append(invoker.calls, invocation)
	index := len(invoker.calls) - 1
	if index >= len(invoker.results) {
		return aiplatform.InvocationResult{}, errors.New("unexpected invocation")
	}
	if index < len(invoker.errors) && invoker.errors[index] != nil {
		return invoker.results[index], invoker.errors[index]
	}
	return invoker.results[index], nil
}

func TestMandatoryDWDFieldCleaningAppliesDatasetHygiene(t *testing.T) {
	tests := []struct {
		name  string
		field dwdPlanningField
		want  []string
	}{
		{
			name: "nullable identifier text is trimmed and filled",
			field: dwdPlanningField{
				Code: "person_id", Role: "IDENTIFIER",
				CanonicalType: "STRING", Nullable: true,
			},
			want: []string{"TRIM", "COALESCE_DEFAULT"},
		},
		{
			name: "text measure is trimmed but null is preserved",
			field: dwdPlanningField{
				Code: "raw_measure", Role: "MEASURE",
				CanonicalType: "STRING", Nullable: true,
			},
			want: []string{"TRIM"},
		},
		{
			name: "datetime becomes a nullable day",
			field: dwdPlanningField{
				Code: "event_time", Role: "TIME",
				CanonicalType: "DATETIME", Nullable: true,
			},
			want: []string{"CAST_DATE"},
		},
		{
			name: "string time is trimmed then converted to a day",
			field: dwdPlanningField{
				Code: "snapshot_date", Role: "TIME",
				CanonicalType: "STRING", Nullable: true,
			},
			want: []string{"TRIM", "CAST_DATE"},
		},
		{
			name: "semantic date text is normalized even when role is attribute",
			field: dwdPlanningField{
				Code: "join_date", Role: "ATTRIBUTE",
				CanonicalType: "STRING", SemanticType: "DATE", Nullable: true,
			},
			want: []string{"TRIM", "CAST_DATE", "COALESCE_DEFAULT"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mandatoryDWDFieldCleaning(
				test.field,
				// 模型建议不能覆盖平台卫生合同。
				[]string{"CAST_DATETIME"},
			)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("cleaning = %#v, want %#v", got, test.want)
			}
			if err := validateDWDCleaning(test.field, got); err != nil {
				t.Fatalf("mandatory cleaning is invalid: %v", err)
			}
		})
	}
}

func TestDWDModeledDatasetTypeUsesPhysicalSourceIdentities(t *testing.T) {
	fact := dwdODSAsset{
		VersionID: "fact_version",
		Document: Document{Nodes: []Node{{
			ID: "fact", Type: "TABLE", DataSourceID: "oracle_source",
		}}},
	}
	dim := dwdODSAsset{
		VersionID: "dim_source_version",
		Document: Document{Nodes: []Node{{
			ID: "dimension", Type: "TABLE", DataSourceID: "mysql_source",
		}}},
	}
	output := dwdLLMOutput{
		Joins: []dwdLLMJoin{{
			DimensionDatasetVersionID: dim.VersionID,
		}},
	}
	if got := dwdModeledDatasetType(
		fact,
		map[string]dwdODSAsset{dim.VersionID: dim},
		output,
	); got != "CROSS_SOURCE" {
		t.Fatalf("dataset type = %q, want CROSS_SOURCE", got)
	}
}

func TestValidateDWDCleaningRequiresFullStringAndDayPolicy(t *testing.T) {
	stringMeasure := dwdPlanningField{
		Code: "raw_value", Role: "MEASURE", CanonicalType: "STRING",
	}
	if err := validateDWDCleaning(stringMeasure, nil); err == nil ||
		!strings.Contains(err.Error(), "requires TRIM") {
		t.Fatalf("missing STRING TRIM error = %v", err)
	}

	datetime := dwdPlanningField{
		Code: "event_time", Role: "TIME", CanonicalType: "DATETIME",
	}
	if err := validateDWDCleaning(
		datetime, []string{"CAST_DATETIME"},
	); err == nil || !strings.Contains(err.Error(), "requires CAST_DATE") {
		t.Fatalf("legacy DATETIME cleaning error = %v", err)
	}
	if err := validateDWDCleaning(
		datetime, []string{"CAST_DATE"},
	); err != nil {
		t.Fatalf("CAST_DATE day policy rejected: %v", err)
	}
}

func TestNormalizeDWDDimensionDesignOverridesModelCleaning(t *testing.T) {
	table := dwdPlanningTable{
		VersionID: "version_person",
		OutputGrain: OutputGrain{
			KeyFields: []string{"person_id"},
		},
		Fields: []dwdPlanningField{
			{
				Code: "person_id", Role: "IDENTIFIER",
				CanonicalType: "STRING", Nullable: true,
			},
			{
				Code: "event_time", Role: "TIME",
				CanonicalType: "DATETIME", Nullable: true,
			},
		},
	}
	design := dwdLLMDimensionDesign{
		SourceDatasetVersionID: "version_person",
		Name:                   "人员",
		Description:            "一人一行的人员稳定信息",
		GrainKeyFieldCodes:     []string{"person_id"},
		Fields: []dwdLLMDimensionFieldDesign{
			{
				SourceFieldCode: "person_id", OutputName: "人员编号",
				OutputDescription: "人员稳定业务键",
				Standardization:   []string{},
			},
			{
				SourceFieldCode: "event_time", OutputName: "业务日期",
				OutputDescription: "去除时分秒后的业务日期",
				Standardization:   []string{"CAST_DATETIME"},
			},
		},
	}

	normalized, err := normalizeDWDDimensionDesign(table, design)
	if err != nil {
		t.Fatalf("normalize dimension design: %v", err)
	}
	if got, want := normalized.Fields[0].Standardization,
		[]string{"TRIM", "COALESCE_DEFAULT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("identifier standardization = %#v, want %#v", got, want)
	}
	if got, want := normalized.Fields[1].Standardization,
		[]string{"CAST_DATE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("datetime standardization = %#v, want %#v", got, want)
	}
}

func TestDWDDatePolicyKeepsDateTypeAndYYYYMMDDFormat(t *testing.T) {
	source := Field{
		Code: "event_time", Role: "TIME",
		CanonicalType: "DATETIME", Nullable: true,
	}
	operations := mandatoryDIMCleaning(source)
	expression, canonicalType, nullable, err := applyLLMDWDCleaning(
		"node_fact", source, operations,
	)
	if err != nil {
		t.Fatalf("apply cleaning: %v", err)
	}
	if expression.Type != "CAST" || expression.TargetType != "DATE" {
		t.Fatalf("expression = %#v, want CAST DATE", expression)
	}
	if canonicalType != "DATE" || !nullable {
		t.Fatalf(
			"canonicalType = %q nullable = %t, want DATE/true",
			canonicalType, nullable,
		)
	}
	if got := normalizedDWDDatasetFieldFormat(canonicalType, ""); got != "YYYYMMDD" {
		t.Fatalf("date format = %q, want YYYYMMDD", got)
	}
}

func TestDWDProcessingCannotChangeMandatoryDayContract(t *testing.T) {
	field := dwdPlanningField{
		Code: "event_time", Role: "TIME", CanonicalType: "DATETIME",
	}
	err := validateDWDProcessing(
		field,
		mandatoryDWDFieldCleaning(field, nil),
		[]dwdLLMProcessingStep{{
			Operation: "DATE_FORMAT", Arguments: []string{"MONTH"},
		}},
		nil, nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "YYYYMMDD") {
		t.Fatalf("DATE_FORMAT day-contract error = %v", err)
	}

	err = validateDWDProcessing(
		field,
		mandatoryDWDFieldCleaning(field, nil),
		[]dwdLLMProcessingStep{{
			Operation: "COALESCE", Arguments: []string{"1970-01-01"},
		}},
		nil, nil, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "preserve TIME/MEASURE nulls") {
		t.Fatalf("TIME null-preservation error = %v", err)
	}
}

func TestNormalizeDWDClassificationsDropsIncompleteOptionalFactDimension(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "运营",
		Tables: []dwdPlanningTable{{
			VersionID: "delivery_version",
			OutputGrain: OutputGrain{
				KeyFields: []string{"delivery_event_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "delivery_event_id", Role: "IDENTIFIER",
					CanonicalType: "STRING",
				},
				{
					Code: "courier_id", Role: "IDENTIFIER",
					CanonicalType: "STRING",
				},
				{
					Code: "courier_name", Role: "ATTRIBUTE",
					CanonicalType: "STRING",
				},
				{
					Code: "event_time", Role: "TIME",
					CanonicalType: "DATETIME",
				},
			},
		}},
	}
	tests := []struct {
		name       string
		keys       []string
		attributes []string
		wantKeys   []string
		wantAttrs  []string
	}{
		{
			name:     "key without attribute is ignored",
			keys:     []string{"courier_id"},
			wantKeys: []string{}, wantAttrs: []string{},
		},
		{
			name: "transactional attribute cannot complete extraction",
			keys: []string{"courier_id"}, attributes: []string{"event_time"},
			wantKeys: []string{}, wantAttrs: []string{},
		},
		{
			name: "stable key and attribute are retained",
			keys: []string{"courier_id"}, attributes: []string{"courier_name"},
			wantKeys:  []string{"courier_id"},
			wantAttrs: []string{"courier_name"},
		},
		{
			name:       "fact grain is not re-emitted as a dimension",
			keys:       []string{"delivery_event_id"},
			attributes: []string{"courier_name"},
			wantKeys:   []string{}, wantAttrs: []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizeDWDClassifications(
				input,
				[]dwdLLMClassification{{
					DatasetVersionID:             "delivery_version",
					Role:                         "FACT",
					DimensionKeyFieldCodes:       test.keys,
					DimensionAttributeFieldCodes: test.attributes,
					Rationale:                    "每行是一条配送事件",
				}},
			)
			if len(normalized) != 1 {
				t.Fatalf("classification count = %d, want 1", len(normalized))
			}
			if !reflect.DeepEqual(
				normalized[0].DimensionKeyFieldCodes, test.wantKeys,
			) {
				t.Fatalf(
					"keys = %#v, want %#v",
					normalized[0].DimensionKeyFieldCodes, test.wantKeys,
				)
			}
			if !reflect.DeepEqual(
				normalized[0].DimensionAttributeFieldCodes, test.wantAttrs,
			) {
				t.Fatalf(
					"attributes = %#v, want %#v",
					normalized[0].DimensionAttributeFieldCodes, test.wantAttrs,
				)
			}
			if err := validateDWDLLMClassifications(
				input, input.Domain, normalized,
			); err != nil {
				t.Fatalf("normalized classification is invalid: %v", err)
			}
		})
	}
}

func TestNormalizeDWDClassificationsCorrectsPeriodicSnapshotPrimaryDIMRole(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "运营",
		Tables: []dwdPlanningTable{{
			VersionID: "merchant_daily_version",
			OutputGrain: OutputGrain{
				KeyFields: []string{"METRIC_DATE", "MERCHANT_ID"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "METRIC_DATE", Role: "IDENTIFIER",
					CanonicalType: "DATE", SemanticType: "DATE",
				},
				{
					Code: "MERCHANT_ID", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "ZONE_ID", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "ORDER_COUNT", Role: "MEASURE",
					CanonicalType: "INTEGER", SemanticType: "QUANTITY",
				},
				{
					Code: "GROSS_REVENUE", Role: "MEASURE",
					CanonicalType: "INTEGER", SemanticType: "AMOUNT",
				},
			},
		}},
	}
	normalized := normalizeDWDClassifications(
		input,
		[]dwdLLMClassification{{
			DatasetVersionID:       "merchant_daily_version",
			Role:                   "DIMENSION",
			DimensionKeyFieldCodes: []string{"METRIC_DATE", "MERCHANT_ID"},
			DimensionAttributeFieldCodes: []string{
				"ZONE_ID", "ORDER_COUNT", "GROSS_REVENUE",
			},
			Rationale: "每行代表商户日度周期快照，符合 DWD 定义",
		}},
	)
	err := validateDWDLLMClassifications(
		input, input.Domain, normalized,
	)
	if err != nil {
		t.Fatalf("corrected periodic snapshot classification is invalid: %v", err)
	}
	if len(normalized) != 1 || normalized[0].Role != "FACT" ||
		len(normalized[0].DimensionKeyFieldCodes) != 0 ||
		len(normalized[0].DimensionAttributeFieldCodes) != 0 {
		t.Fatalf("periodic snapshot was not corrected to FACT: %#v", normalized)
	}
}

func factlessDeliveryEventPlanningInput() dwdPlanningInput {
	return dwdPlanningInput{
		Domain: "企业经营",
		Tables: []dwdPlanningTable{{
			DatasetID: "delivery_event_dataset", VersionID: "delivery_event_version",
			Name: "配送事件事实表", Description: "配送过程中的原子状态变更事件流水",
			OutputGrain: OutputGrain{
				KeyFields:   []string{"EVENT_ID"},
				TimeField:   "EVENT_TIME",
				Description: "每行代表一条配送事件事实记录",
			},
			Fields: []dwdPlanningField{
				{
					Code: "EVENT_ID", Name: "事件ID", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "ORDER_ID", Name: "订单ID", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "COURIER_ID", Name: "骑手ID", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "EVENT_TYPE", Name: "事件类型", Role: "DIMENSION",
					CanonicalType: "STRING", SemanticType: "CATEGORY",
				},
				{
					Code: "EVENT_TIME", Name: "事件时间", Role: "TIME",
					CanonicalType: "DATETIME", SemanticType: "DATETIME",
				},
				{
					Code: "LONGITUDE", Name: "经度", Role: "DIMENSION",
					CanonicalType: "INTEGER", SemanticType: "TEXT",
				},
			},
		}},
	}
}

func TestNormalizeDWDClassificationsCorrectsFactlessDeliveryEvent(
	t *testing.T,
) {
	input := factlessDeliveryEventPlanningInput()
	normalized := normalizeDWDClassifications(
		input,
		[]dwdLLMClassification{{
			DatasetVersionID:       "delivery_event_version",
			Role:                   "DIMENSION",
			DimensionKeyFieldCodes: []string{"EVENT_ID"},
			DimensionAttributeFieldCodes: []string{
				"ORDER_ID", "COURIER_ID", "EVENT_TYPE", "EVENT_TIME", "LONGITUDE",
			},
			Rationale: "每行是配送事件事实，不存在稳定实体，但输出角色误填为维度",
		}},
	)
	if len(normalized) != 1 || normalized[0].Role != "FACT" ||
		len(normalized[0].DimensionKeyFieldCodes) != 0 ||
		len(normalized[0].DimensionAttributeFieldCodes) != 0 ||
		!strings.Contains(normalized[0].Rationale, "原子事件/交易粒度") {
		t.Fatalf("factless event was not corrected to FACT: %#v", normalized)
	}
	if err := validateDWDLLMClassifications(
		input, input.Domain, normalized,
	); err != nil {
		t.Fatalf("corrected factless event classification is invalid: %v", err)
	}
}

func TestValidateDWDClassificationsRejectsRawFactlessEventDIM(
	t *testing.T,
) {
	input := factlessDeliveryEventPlanningInput()
	err := validateDWDLLMClassifications(
		input, input.Domain,
		[]dwdLLMClassification{{
			DatasetVersionID:             "delivery_event_version",
			Role:                         "DIMENSION",
			DimensionKeyFieldCodes:       []string{"EVENT_ID"},
			DimensionAttributeFieldCodes: []string{"EVENT_TYPE", "EVENT_TIME"},
			Rationale:                    "配送事件维度",
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "must be classified as FACT") {
		t.Fatalf("raw factless event DIM validation error = %v", err)
	}
}

func TestDimensionDesignBoundaryRejectsFactlessEventGrain(t *testing.T) {
	table := factlessDeliveryEventPlanningInput().Tables[0]
	_, err := normalizeDWDDimensionDesign(
		table,
		dwdLLMDimensionDesign{
			SourceDatasetVersionID: table.VersionID,
			Name:                   "配送事件维度表", Description: "错误的事件伪维度",
		},
	)
	if err == nil || !strings.Contains(err.Error(), "DIM design retains") {
		t.Fatalf("factless event DIM design error = %v", err)
	}
}

func TestFactOccurrenceKeyCannotBecomeEmbeddedDimension(t *testing.T) {
	input := factlessDeliveryEventPlanningInput()
	normalized := normalizeDWDClassifications(
		input,
		[]dwdLLMClassification{{
			DatasetVersionID:             "delivery_event_version",
			Role:                         "FACT",
			DimensionKeyFieldCodes:       []string{"EVENT_ID"},
			DimensionAttributeFieldCodes: []string{"EVENT_TYPE"},
			Rationale:                    "配送事件事实并错误抽取事件维度",
		}},
	)
	if len(normalized) != 1 ||
		len(normalized[0].DimensionKeyFieldCodes) != 0 ||
		len(normalized[0].DimensionAttributeFieldCodes) != 0 {
		t.Fatalf("fact occurrence key survived DIM projection: %#v", normalized)
	}
}

func TestFactOccurrenceGrainDoesNotMatchStableEntityKey(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{code: "EVENT_ID", want: true},
		{code: "ORDER_ID", want: true},
		{code: "DELIVERY_EVENT_ID", want: true},
		{code: "COURIER_ID", want: false},
		{code: "MERCHANT_ID", want: false},
		{code: "DELIVERY_ZONE_ID", want: false},
		{code: "EVENT_TYPE_ID", want: false},
		{code: "RECORD_ID", want: false},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			table := dwdPlanningTable{
				OutputGrain: OutputGrain{KeyFields: []string{test.code}},
				Fields: []dwdPlanningField{{
					Code: test.code, Role: "IDENTIFIER",
					CanonicalType: "STRING", SemanticType: "IDENTIFIER",
				}},
			}
			if got := hasDWDFactOccurrenceGrain(table); got != test.want {
				t.Fatalf(
					"hasDWDFactOccurrenceGrain(%s) = %t, want %t",
					test.code, got, test.want,
				)
			}
		})
	}
}

func TestPeriodicSnapshotFactCanStillEmitStableEmbeddedDimension(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "运营",
		Tables: []dwdPlanningTable{{
			VersionID: "merchant_daily_version",
			OutputGrain: OutputGrain{
				KeyFields: []string{"metric_date", "merchant_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "metric_date", Role: "TIME",
					CanonicalType: "DATE", SemanticType: "DATE",
				},
				{
					Code: "merchant_id", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "merchant_name", Role: "ATTRIBUTE",
					CanonicalType: "STRING",
				},
				{
					Code: "order_count", Role: "MEASURE",
					CanonicalType: "INTEGER", SemanticType: "QUANTITY",
				},
			},
		}},
	}
	normalized := normalizeDWDClassifications(
		input,
		[]dwdLLMClassification{{
			DatasetVersionID:             "merchant_daily_version",
			Role:                         "FACT",
			DimensionKeyFieldCodes:       []string{"merchant_id"},
			DimensionAttributeFieldCodes: []string{"merchant_name"},
			Rationale:                    "每行是商户日快照，并可抽取商户稳定信息",
		}},
	)
	if len(normalized) != 1 {
		t.Fatalf("classification count = %d, want 1", len(normalized))
	}
	if got := normalized[0]; got.Role != "FACT" ||
		!reflect.DeepEqual(got.DimensionKeyFieldCodes, []string{"merchant_id"}) ||
		!reflect.DeepEqual(
			got.DimensionAttributeFieldCodes, []string{"merchant_name"},
		) {
		t.Fatalf("mixed FACT/DIM classification = %#v", got)
	}
	if err := validateDWDLLMClassifications(
		input, input.Domain, normalized,
	); err != nil {
		t.Fatalf("mixed FACT/DIM classification is invalid: %v", err)
	}
}

func TestValidateDWDClassificationsRejectsInvalidEntityProjection(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "运营",
		Tables: []dwdPlanningTable{{
			VersionID: "entity_version",
			OutputGrain: OutputGrain{
				KeyFields: []string{"entity_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "entity_id", Role: "IDENTIFIER",
					CanonicalType: "STRING", SemanticType: "IDENTIFIER",
				},
				{
					Code: "snapshot_date", Role: "IDENTIFIER",
					CanonicalType: "DATE", SemanticType: "DATE",
				},
				{
					Code: "entity_name", Role: "ATTRIBUTE",
					CanonicalType: "STRING",
				},
				{
					Code: "order_count", Role: "MEASURE",
					CanonicalType: "INTEGER", SemanticType: "QUANTITY",
				},
			},
		}},
	}
	tests := []struct {
		name       string
		keys       []string
		attributes []string
		wantError  string
	}{
		{
			name:       "temporal field cannot be an entity key",
			keys:       []string{"snapshot_date"},
			attributes: []string{"entity_name"},
			wantError:  "dimension key snapshot_date is temporal",
		},
		{
			name:       "measure cannot be a dimension attribute",
			keys:       []string{"entity_id"},
			attributes: []string{"order_count"},
			wantError:  "dimension attribute order_count is transactional",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDWDLLMClassifications(
				input,
				input.Domain,
				[]dwdLLMClassification{{
					DatasetVersionID:             "entity_version",
					Role:                         "DIMENSION",
					DimensionKeyFieldCodes:       test.keys,
					DimensionAttributeFieldCodes: test.attributes,
					Rationale:                    "每行代表一个实体",
				}},
			)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validation error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestNormalizeDWDClassificationsFiltersTransactionalEntityAttributes(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "企业经营",
		Tables: []dwdPlanningTable{{
			VersionID: "merchant_version",
			OutputGrain: OutputGrain{
				KeyFields: []string{"merchant_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "merchant_id", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "merchant_name", Role: "DIMENSION",
					CanonicalType: "STRING", SemanticType: "COMPANY_NAME",
				},
				{
					Code: "onboard_time", Role: "TIME",
					CanonicalType: "DATETIME", SemanticType: "DATETIME",
				},
				{
					Code: "rating", Role: "MEASURE",
					CanonicalType: "DECIMAL", SemanticType: "QUANTITY",
				},
				{
					Code: "commission_rate", Role: "MEASURE",
					CanonicalType: "DECIMAL", SemanticType: "PERCENTAGE",
				},
				{
					Code: "status", Role: "DIMENSION",
					CanonicalType: "STRING", SemanticType: "CATEGORY",
				},
				{
					Code: "order_id", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
			},
		}},
	}
	normalized := normalizeDWDClassifications(
		input,
		[]dwdLLMClassification{{
			DatasetVersionID:       "merchant_version",
			Role:                   "MASTER",
			DimensionKeyFieldCodes: []string{"merchant_id"},
			DimensionAttributeFieldCodes: []string{
				"merchant_name", "onboard_time", "rating",
				"commission_rate", "status", "order_id",
			},
			Rationale: "每行代表一个商户实体",
		}},
	)
	if len(normalized) != 1 {
		t.Fatalf("classification count = %d, want 1", len(normalized))
	}
	if got := normalized[0].DimensionAttributeFieldCodes; !reflect.DeepEqual(
		got, []string{"merchant_name", "onboard_time", "status"},
	) {
		t.Fatalf("dimension attributes = %#v", got)
	}
	if err := validateDWDLLMClassifications(
		input, input.Domain, normalized,
	); err != nil {
		t.Fatalf("normalized merchant dimension is invalid: %v", err)
	}
}

func TestDWDMeasureContractOverridesAdditiveGuessForCumulativeAndPointValues(
	t *testing.T,
) {
	tests := []struct {
		name, code, displayName, proposed, behavior string
	}{
		{
			name: "cumulative", code: "sales_ytd",
			displayName: "本年累计销售额", proposed: "FLOW",
			behavior: "CUMULATIVE",
		},
		{
			name: "point in time", code: "ending_inventory",
			displayName: "期末库存", proposed: "FLOW",
			behavior: "POINT_IN_TIME",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contract := dwdAtomicMeasureContract(
				dwdLLMField{
					OutputCode: test.code, OutputName: test.displayName,
					MeasureBehavior: test.proposed,
				},
				Field{
					Code: test.code, Name: test.displayName,
					Role: "MEASURE", CanonicalType: "DECIMAL",
				},
			)
			if contract.ValueBehavior != test.behavior ||
				contract.Additivity != "SEMI_ADDITIVE" ||
				contract.DefaultAggregation != "SUM" ||
				contract.TimeAggregation != "LAST" {
				t.Fatalf("measure contract = %#v", contract)
			}
		})
	}
}

func TestConsolidateContainedDimensionsKeepsIndependentKeysSeparate(
	t *testing.T,
) {
	table := dwdPlanningTable{
		VersionID: "entity_version",
		Fields: []dwdPlanningField{
			{Code: "country_id", Role: "IDENTIFIER", CanonicalType: "STRING"},
			{Code: "city_id", Role: "IDENTIFIER", CanonicalType: "STRING"},
			{Code: "product_id", Role: "IDENTIFIER", CanonicalType: "STRING"},
			{Code: "country_name", Role: "ATTRIBUTE", CanonicalType: "STRING"},
			{Code: "city_name", Role: "ATTRIBUTE", CanonicalType: "STRING"},
			{Code: "product_name", Role: "ATTRIBUTE", CanonicalType: "STRING"},
		},
	}
	contained := consolidateContainedDWDDimensions(
		table,
		dwdLLMClassification{
			Role:                         "DIMENSION",
			DimensionKeyFieldCodes:       []string{"country_id"},
			DimensionAttributeFieldCodes: []string{"country_name"},
			AdditionalDimensions: []dwdLLMDimensionProjection{{
				Code: "city", Name: "城市",
				DimensionKeyFieldCodes:       []string{"country_id", "city_id"},
				DimensionAttributeFieldCodes: []string{"city_name"},
			}},
		},
	)
	if len(contained.AdditionalDimensions) != 0 ||
		!sameDWDStringSet(
			contained.DimensionKeyFieldCodes,
			[]string{"country_id", "city_id"},
		) ||
		!sameDWDStringSet(
			contained.DimensionAttributeFieldCodes,
			[]string{"country_name", "city_name"},
		) {
		t.Fatalf("contained dimensions were not unified: %#v", contained)
	}

	independent := consolidateContainedDWDDimensions(
		table,
		dwdLLMClassification{
			Role:                         "DIMENSION",
			DimensionKeyFieldCodes:       []string{"country_id"},
			DimensionAttributeFieldCodes: []string{"country_name"},
			AdditionalDimensions: []dwdLLMDimensionProjection{{
				Code: "product", Name: "商品",
				DimensionKeyFieldCodes:       []string{"product_id"},
				DimensionAttributeFieldCodes: []string{"product_name"},
			}},
		},
	)
	if len(independent.AdditionalDimensions) != 1 {
		t.Fatalf("independent dimensions were incorrectly merged: %#v", independent)
	}
}

func TestExpandedDWDClassificationsUsesLatestPublishedDIMContract(
	t *testing.T,
) {
	classification := dwdLLMClassification{
		DatasetVersionID:       "courier_ods_version",
		Role:                   "MASTER",
		DimensionKeyFieldCodes: []string{"courier_id"},
		DimensionAttributeFieldCodes: []string{
			"courier_name", "phone_masked",
		},
		Rationale: "每行一个骑手",
	}
	publishedDIM := dwdODSAsset{
		DatasetID: "courier_dim",
		Document: Document{
			OutputGrain: OutputGrain{
				KeyFields: []string{"courier_id"},
			},
			Fields: []Field{
				{
					Code: "courier_id", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "courier_name", Role: "DIMENSION",
					CanonicalType: "STRING", SemanticType: "TEXT",
				},
				{
					Code: "status", Role: "DIMENSION",
					CanonicalType: "STRING", SemanticType: "CATEGORY",
				},
			},
		},
	}
	input := dwdPlanningInput{
		Domain: "企业经营",
		Tables: []dwdPlanningTable{{
			DatasetID: "courier_ods", VersionID: "courier_ods_version",
			OutputGrain: OutputGrain{
				KeyFields: []string{"courier_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "courier_id", Role: "IDENTIFIER",
					CanonicalType: "INTEGER", SemanticType: "IDENTIFIER",
				},
				{
					Code: "courier_name", Role: "DIMENSION",
					CanonicalType: "STRING", SemanticType: "TEXT",
				},
				{
					Code: "phone_masked", Role: "IDENTIFIER",
					CanonicalType: "STRING", SemanticType: "IDENTIFIER",
				},
			},
		}},
	}
	stage := dwdDimensionStageResult{
		AssetsBySourceVersion: map[string]dwdODSAsset{
			"courier_ods_version": publishedDIM,
		},
	}
	got := expandedDWDClassifications(
		[]dwdLLMClassification{classification},
		stage,
	)
	if len(got) != 1 {
		t.Fatalf("classification count = %d, want 1", len(got))
	}
	if !reflect.DeepEqual(
		got[0].DimensionKeyFieldCodes, []string{"courier_id"},
	) {
		t.Fatalf("dimension keys = %#v", got[0].DimensionKeyFieldCodes)
	}
	if !reflect.DeepEqual(
		got[0].DimensionAttributeFieldCodes,
		[]string{"courier_name", "status"},
	) {
		t.Fatalf(
			"dimension attributes = %#v",
			got[0].DimensionAttributeFieldCodes,
		)
	}
	factPlanningInput := planningInputWithModeledDimensions(
		input, stage, []dwdLLMClassification{classification},
	)
	if err := validateDWDLLMClassifications(
		factPlanningInput, input.Domain, got,
	); err != nil {
		t.Fatalf("latest published DIM contract is invalid: %v", err)
	}
}

func TestClassificationDimensionSpecsSupportsTwoEntitiesFromOneODS(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "交易",
		Trigger: dwdPlanningTrigger{
			DatasetID: "order_dataset", VersionID: "order_version",
		},
		Tables: []dwdPlanningTable{{
			DatasetID: "order_dataset", VersionID: "order_version",
			Name: "订单交易",
			OutputGrain: OutputGrain{
				KeyFields: []string{"order_id"}, TimeField: "event_time",
			},
			Fields: []dwdPlanningField{
				{Code: "order_id", Role: "IDENTIFIER", CanonicalType: "STRING"},
				{Code: "customer_id", Role: "IDENTIFIER", CanonicalType: "STRING"},
				{Code: "customer_name", Role: "DIMENSION", CanonicalType: "STRING"},
				{Code: "merchant_id", Role: "IDENTIFIER", CanonicalType: "STRING"},
				{Code: "merchant_name", Role: "DIMENSION", CanonicalType: "STRING"},
				{Code: "amount", Role: "MEASURE", CanonicalType: "DECIMAL"},
				{Code: "event_time", Role: "TIME", CanonicalType: "DATETIME"},
			},
		}},
	}
	normalized := normalizeDWDClassifications(
		input, []dwdLLMClassification{{
			DatasetVersionID:       "order_version",
			Role:                   "FACT",
			DimensionKeyFieldCodes: []string{"customer_id"},
			DimensionAttributeFieldCodes: []string{
				"customer_name",
			},
			AdditionalDimensions: []dwdLLMDimensionProjection{{
				Code: "merchant", Name: "商户",
				DimensionKeyFieldCodes:       []string{"merchant_id"},
				DimensionAttributeFieldCodes: []string{"merchant_name"},
				Rationale:                    "每行一个稳定商户实体",
			}},
			Rationale: "每行一笔订单，同时包含客户和商户实体属性",
		}},
	)
	if err := validateDWDLLMClassifications(
		input, input.Domain, normalized,
	); err != nil {
		t.Fatalf("two-entity classification is invalid: %v", err)
	}
	specs := dwdDimensionSpecs(normalized)
	if len(specs) != 2 {
		t.Fatalf("dimension spec count = %d, want 2", len(specs))
	}
	if specs[0].MappingKey != "primary" ||
		specs[1].MappingKey != "merchant" ||
		specs[0].Identity == specs[1].Identity {
		t.Fatalf("dimension specs = %#v", specs)
	}
}

func TestValidateDWDFactRelationBatchPreservesPrimaryOnlyContract(
	t *testing.T,
) {
	current := dwdPlanningTable{
		VersionID: "order_version",
		Fields: []dwdPlanningField{{
			Code: "order_id", Role: "IDENTIFIER", CanonicalType: "STRING",
		}},
	}
	candidate := dwdPlanningTable{
		VersionID: "payment_version",
		Fields: []dwdPlanningField{{
			Code: "source_order_id", Role: "IDENTIFIER",
			CanonicalType: "STRING",
		}},
	}
	batch := dwdFactRelationBatch{
		Relations: []dwdFactRelationDecision{{
			CandidateDatasetVersionID: "payment_version",
			Related:                   true, CurrentRole: "PRIMARY",
			Conditions: []dwdLLMJoinCondition{{
				FactFieldCode:      "order_id",
				DimensionFieldCode: "source_order_id",
			}},
			Rationale: "每个订单至多匹配一条汇总支付记录",
		}},
	}
	if err := validateDWDFactRelationBatch(
		current, []dwdPlanningTable{candidate}, batch,
	); err != nil {
		t.Fatalf("primary fact relation is invalid: %v", err)
	}
	associations := []dwdFactAssociation{}
	for _, decision := range batch.Relations {
		if decision.Related && decision.CurrentRole == "PRIMARY" {
			associations = append(associations, dwdFactAssociation{
				SecondaryDatasetVersionID: decision.CandidateDatasetVersionID,
				Conditions:                decision.Conditions,
			})
		}
	}
	if len(dwdFactAssociationMap(associations)) != 1 {
		t.Fatal("primary relationship was not retained")
	}
	batch.Relations[0].CurrentRole = "SECONDARY"
	associations = associations[:0]
	for _, decision := range batch.Relations {
		if decision.Related && decision.CurrentRole == "PRIMARY" {
			associations = append(associations, dwdFactAssociation{})
		}
	}
	if len(associations) != 0 {
		t.Fatal("secondary relationship must not be retained")
	}
}

func TestDWDSingleTableClassificationScope(t *testing.T) {
	input := dwdPlanningInput{
		TenantID: "tenant", ActorID: "actor", ResourceID: "workflow",
		Domain: "企业经营",
		Trigger: dwdPlanningTrigger{
			DatasetID: "dataset_a", VersionID: "version_a",
		},
		Tables: []dwdPlanningTable{
			{
				DatasetID: "dataset_a", VersionID: "version_a",
				Fields: []dwdPlanningField{{Code: "merchant_id"}},
			},
			{
				DatasetID: "dataset_b", VersionID: "version_b",
				Fields: []dwdPlanningField{{Code: "order_id"}},
			},
		},
	}
	scoped, err := dwdSingleTableClassificationScope(input, "version_b")
	if err != nil {
		t.Fatalf("single table scope: %v", err)
	}
	if scoped.ResourceID != "version_b" ||
		scoped.Trigger.DatasetID != "dataset_b" ||
		scoped.Trigger.VersionID != "version_b" ||
		len(scoped.Tables) != 1 ||
		scoped.Tables[0].VersionID != "version_b" {
		t.Fatalf("single table scope = %#v", scoped)
	}
	if len(input.Tables) != 2 || input.ResourceID != "workflow" {
		t.Fatalf("source input was mutated: %#v", input)
	}
	if _, err := dwdSingleTableClassificationScope(
		input, "missing_version",
	); err == nil {
		t.Fatal("missing ODS version unexpectedly produced a scope")
	}
}

func TestRunBoundedDWDTasksCollectDoesNotCancelPeerTasks(t *testing.T) {
	expectedFailure := errors.New("classification output is invalid")
	tasks := []func(context.Context) (string, error){
		func(context.Context) (string, error) {
			return "first", nil
		},
		func(context.Context) (string, error) {
			return "", expectedFailure
		},
		func(context.Context) (string, error) {
			return "third", nil
		},
	}

	results, taskErrors := runBoundedDWDTasksCollect(
		context.Background(), 2, tasks,
	)
	if !reflect.DeepEqual(results, []string{"first", "", "third"}) {
		t.Fatalf("results = %#v", results)
	}
	if len(taskErrors) != 3 ||
		taskErrors[0] != nil ||
		!errors.Is(taskErrors[1], expectedFailure) ||
		taskErrors[2] != nil {
		t.Fatalf("task errors = %#v", taskErrors)
	}
}

func TestDWDStageRepairMessagesUseFreshFinalRegeneration(t *testing.T) {
	base := []aiplatform.Message{{
		Role: aiplatform.MessageRoleSystem,
		Parts: []aiplatform.ContentPart{{
			Type: aiplatform.ContentTypeText,
			Text: "base instructions",
		}},
	}}
	result := aiplatform.InvocationResult{
		ProviderResult: aiplatform.ProviderResult{
			Content: json.RawMessage(`{"invalid":"candidate"}`),
		},
	}

	incremental := dwdStageRepairMessages(
		base, result, errDWDModelingInvalid, "repair contract", false,
	)
	if len(incremental) != 3 ||
		incremental[1].Role != aiplatform.MessageRoleAssistant {
		t.Fatalf("incremental repair messages = %#v", incremental)
	}

	fresh := dwdStageRepairMessages(
		base, result, errDWDModelingInvalid, "repair contract", true,
	)
	if len(fresh) != 2 ||
		fresh[1].Role != aiplatform.MessageRoleUser {
		t.Fatalf("fresh repair messages = %#v", fresh)
	}
	freshInstruction := fresh[1].Parts[0].Text
	if strings.Contains(freshInstruction, `"invalid":"candidate"`) ||
		!strings.Contains(freshInstruction, "忽略之前的候选答案") ||
		!strings.Contains(freshInstruction, "repair contract") {
		t.Fatalf("fresh repair instruction = %q", freshInstruction)
	}
}

func TestDesignDimensionFallsBackToDeepSeekAfterM2InvalidOutput(
	t *testing.T,
) {
	invoker := &scriptedDIMValidationInvoker{
		models: "MiniMax-M2,deepseek-v3",
		results: []aiplatform.InvocationResult{
			{},
			{
				RequestID: "deepseek_request",
				ProviderResult: aiplatform.ProviderResult{
					Model: "deepseek-v3",
					Content: json.RawMessage(`{"output":{
						"sourceDatasetVersionId":"merchant_version",
						"name":"商户维度",
						"description":"每行代表一个稳定商户实体",
						"grainKeyFieldCodes":["merchant_id"],
						"fields":[
							{"sourceFieldCode":"merchant_id","outputName":"商户ID","outputDescription":"商户唯一业务标识","standardization":[]},
							{"sourceFieldCode":"merchant_name","outputName":"商户名称","outputDescription":"商户标准名称","standardization":["TRIM"]}
						],
						"rationale":"商户ID稳定标识一个商户实体"
					}}`),
				},
			},
		},
		errors: []error{
			&aiplatform.ProviderError{
				Code:      aiplatform.ErrorCodeInvalidOutput,
				Message:   "AI structured output is invalid",
				Retryable: false,
			},
			nil,
		},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	input := dwdPlanningInput{
		TenantID: "tenant", ActorID: "actor", ResourceID: "merchant_version",
		Domain: "企业经营",
		Tables: []dwdPlanningTable{{
			DatasetID: "merchant_dataset", VersionID: "merchant_version",
			Name: "商户主数据",
			OutputGrain: OutputGrain{
				KeyFields: []string{"merchant_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "merchant_id", Role: "IDENTIFIER",
					CanonicalType: "STRING", SemanticType: "IDENTIFIER",
				},
				{
					Code: "merchant_name", Role: "ATTRIBUTE",
					CanonicalType: "STRING", SemanticType: "COMPANY_NAME",
				},
			},
		}},
	}
	completion, err := planner.DesignDimension(
		context.Background(), input,
		[]dwdLLMClassification{{
			DatasetVersionID:             "merchant_version",
			Role:                         "MASTER",
			DimensionKeyFieldCodes:       []string{"merchant_id"},
			DimensionAttributeFieldCodes: []string{"merchant_name"},
			Rationale:                    "每行一个稳定商户实体",
		}},
		"merchant_version",
	)
	if err != nil {
		t.Fatalf("design dimension with fallback: %v", err)
	}
	if completion.AIRequestID != "deepseek_request" ||
		len(invoker.calls) != 2 ||
		invoker.calls[0].PreferredModel != "" ||
		invoker.calls[1].PreferredModel != "deepseek-v3" {
		t.Fatalf(
			"completion=%#v calls=%#v",
			completion, invoker.calls,
		)
	}
	if len(invoker.calls[1].Request.Messages) != 3 ||
		invoker.calls[1].Request.Messages[2].Role !=
			aiplatform.MessageRoleUser {
		t.Fatalf(
			"fallback repair messages = %#v",
			invoker.calls[1].Request.Messages,
		)
	}
}

func TestDWDModelingFallbackRejectsPermanentProviderErrors(t *testing.T) {
	for _, code := range []aiplatform.ErrorCode{
		aiplatform.ErrorCodeAuthentication,
		aiplatform.ErrorCodeInvalidRequest,
		aiplatform.ErrorCodeCanceled,
		aiplatform.ErrorCodeRefusal,
		aiplatform.ErrorCodeResponseTooLarge,
	} {
		if dwdModelingFallbackEligible(
			context.Background(),
			&aiplatform.ProviderError{
				Code: code, Message: "permanent error",
			},
		) {
			t.Fatalf("provider error %s unexpectedly allowed fallback", code)
		}
	}
	if !dwdModelingFallbackEligible(
		context.Background(),
		&aiplatform.ProviderError{
			Code: aiplatform.ErrorCodeTimeout,
		},
	) {
		t.Fatal("provider timeout did not allow fallback")
	}
}

func TestRetryableDWDClassificationFailureRequiresBatchInvalidOutput(
	t *testing.T,
) {
	invalidOutput := &aiplatform.ProviderError{
		Code:      aiplatform.ErrorCodeInvalidOutput,
		Message:   "AI structured output is invalid",
		Retryable: false,
	}
	batchFailure := &dwdClassificationBatchError{
		FailureCount: 1,
		Cause:        invalidOutput,
	}
	if !retryableDWDClassificationFailure(batchFailure) {
		t.Fatal("classification invalid output was not stage-retryable")
	}
	if retryableDWDClassificationFailure(invalidOutput) {
		t.Fatal("non-classification invalid output was stage-retryable")
	}
	timeout := &dwdClassificationBatchError{
		FailureCount: 1,
		Cause: &aiplatform.ProviderError{
			Code:      aiplatform.ErrorCodeTimeout,
			Message:   "AI provider timed out",
			Retryable: true,
		},
	}
	if retryableDWDClassificationFailure(timeout) {
		t.Fatal("provider timeout was classified as invalid output")
	}
}

func TestCanonicalizeMergedDWDClassificationsPreservesAllDIMCandidates(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "企业经营",
		Tables: []dwdPlanningTable{
			{
				VersionID: "merchant_master_version",
				Name:      "商户维度表",
				OutputGrain: OutputGrain{
					KeyFields: []string{"merchant_id"},
				},
				Fields: []dwdPlanningField{
					{
						Code: "merchant_id", Role: "IDENTIFIER",
						CanonicalType: "STRING",
					},
					{
						Code: "merchant_name", Role: "ATTRIBUTE",
						CanonicalType: "STRING",
					},
					{
						Code: "merchant_status", Role: "ATTRIBUTE",
						CanonicalType: "STRING",
					},
				},
			},
			{
				VersionID: "merchant_daily_version",
				Name:      "商户日度运营聚合表",
				OutputGrain: OutputGrain{
					KeyFields: []string{"metric_date", "MERCHANT_ID"},
				},
				Fields: []dwdPlanningField{
					{
						Code: "metric_date", Role: "TIME",
						CanonicalType: "DATE", SemanticType: "DATE",
					},
					{
						Code: "MERCHANT_ID", Role: "IDENTIFIER",
						CanonicalType: "STRING",
					},
					{
						Code: "ZONE_ID", Role: "ATTRIBUTE",
						CanonicalType: "STRING",
					},
					{
						Code: "order_count", Role: "MEASURE",
						CanonicalType: "INTEGER", SemanticType: "QUANTITY",
					},
				},
			},
		},
	}
	authority := []dwdLLMClassification{
		{
			DatasetVersionID:             "merchant_master_version",
			Role:                         "MASTER",
			DimensionKeyFieldCodes:       []string{"merchant_id"},
			DimensionAttributeFieldCodes: []string{"merchant_name", "merchant_status"},
			Rationale:                    "每行代表一个权威商户实体",
		},
		{
			DatasetVersionID:             "merchant_daily_version",
			Role:                         "FACT",
			DimensionKeyFieldCodes:       []string{"MERCHANT_ID"},
			DimensionAttributeFieldCodes: []string{"ZONE_ID"},
			Rationale:                    "每行代表一个商户日度快照",
		},
	}
	// Simulate a poor merge response that selected the sparse FACT projection.
	merged := []dwdLLMClassification{
		{
			DatasetVersionID: "merchant_master_version",
			Role:             "OTHER",
			Rationale:        "不生成维度",
		},
		authority[1],
	}
	got := canonicalizeMergedDWDClassifications(
		input, merged, authority,
	)
	if len(got) != 2 {
		t.Fatalf("classification count = %d, want 2", len(got))
	}
	if got[0].Role != "MASTER" ||
		!reflect.DeepEqual(
			got[0].DimensionKeyFieldCodes, []string{"merchant_id"},
		) ||
		!reflect.DeepEqual(
			got[0].DimensionAttributeFieldCodes,
			[]string{"merchant_name", "merchant_status"},
		) {
		t.Fatalf("authoritative merchant classification = %#v", got[0])
	}
	if got[1].Role != "FACT" ||
		!reflect.DeepEqual(
			got[1].DimensionKeyFieldCodes, []string{"MERCHANT_ID"},
		) ||
		!reflect.DeepEqual(
			got[1].DimensionAttributeFieldCodes, []string{"ZONE_ID"},
		) {
		t.Fatalf("candidate fact projection was suppressed early = %#v", got[1])
	}
	if err := validateDWDLLMClassifications(
		input, input.Domain, got,
	); err != nil {
		t.Fatalf("canonical merged classifications are invalid: %v", err)
	}
}

func TestCanonicalizeMergedDWDClassificationsDoesNotMergeGenericIDs(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "企业经营",
		Tables: []dwdPlanningTable{
			{
				VersionID: "customer_version", Name: "客户",
				OutputGrain: OutputGrain{KeyFields: []string{"id"}},
				Fields: []dwdPlanningField{
					{Code: "id", Role: "IDENTIFIER", CanonicalType: "STRING"},
					{Code: "name", Role: "ATTRIBUTE", CanonicalType: "STRING"},
				},
			},
			{
				VersionID: "store_version", Name: "门店",
				OutputGrain: OutputGrain{KeyFields: []string{"id"}},
				Fields: []dwdPlanningField{
					{Code: "id", Role: "IDENTIFIER", CanonicalType: "STRING"},
					{Code: "name", Role: "ATTRIBUTE", CanonicalType: "STRING"},
				},
			},
		},
	}
	classifications := []dwdLLMClassification{
		{
			DatasetVersionID:             "customer_version",
			Role:                         "MASTER",
			DimensionKeyFieldCodes:       []string{"id"},
			DimensionAttributeFieldCodes: []string{"name"},
			Rationale:                    "每行代表一个客户",
		},
		{
			DatasetVersionID:             "store_version",
			Role:                         "DIMENSION",
			DimensionKeyFieldCodes:       []string{"id"},
			DimensionAttributeFieldCodes: []string{"name"},
			Rationale:                    "每行代表一个门店",
		},
	}
	got := canonicalizeMergedDWDClassifications(
		input, classifications, classifications,
	)
	if got[0].Role != "MASTER" || got[1].Role != "DIMENSION" {
		t.Fatalf("generic IDs were incorrectly merged: %#v", got)
	}
	if err := validateDWDLLMClassifications(
		input, input.Domain, got,
	); err != nil {
		t.Fatalf("generic classifications are invalid: %v", err)
	}
}

func TestCanonicalizeMergedDWDClassificationsRestoresUniqueProjection(
	t *testing.T,
) {
	input := dwdPlanningInput{
		Domain: "企业经营",
		Tables: []dwdPlanningTable{{
			VersionID: "order_item_version",
			Name:      "订单商品明细事实表",
			OutputGrain: OutputGrain{
				KeyFields: []string{"order_id", "line_id"},
			},
			Fields: []dwdPlanningField{
				{
					Code: "order_id", Role: "IDENTIFIER",
					CanonicalType: "STRING",
				},
				{
					Code: "line_id", Role: "IDENTIFIER",
					CanonicalType: "STRING",
				},
				{
					Code: "sku_id", Role: "IDENTIFIER",
					CanonicalType: "STRING",
				},
				{
					Code: "item_name", Role: "ATTRIBUTE",
					CanonicalType: "STRING",
				},
				{
					Code: "quantity", Role: "MEASURE",
					CanonicalType: "INTEGER", SemanticType: "QUANTITY",
				},
			},
		}},
	}
	authority := []dwdLLMClassification{{
		DatasetVersionID:             "order_item_version",
		Role:                         "FACT",
		DimensionKeyFieldCodes:       []string{"sku_id"},
		DimensionAttributeFieldCodes: []string{"item_name"},
		Rationale:                    "每行是订单商品项，可抽取唯一商品实体",
	}}
	merged := []dwdLLMClassification{{
		DatasetVersionID: "order_item_version",
		Role:             "FACT",
		Rationale:        "没有独立商品主表，因此不生成商品维度",
	}}
	got := canonicalizeMergedDWDClassifications(
		input, merged, authority,
	)
	if len(got) != 1 ||
		!reflect.DeepEqual(
			got[0].DimensionKeyFieldCodes, []string{"sku_id"},
		) ||
		!reflect.DeepEqual(
			got[0].DimensionAttributeFieldCodes, []string{"item_name"},
		) {
		t.Fatalf("unique projection was erased by merge: %#v", got)
	}
	if err := validateDWDLLMClassifications(
		input, input.Domain, got,
	); err != nil {
		t.Fatalf("restored unique projection is invalid: %v", err)
	}
}

func TestCanonicalizeDIMNameValidationAddsExactDuplicateOmittedByLLM(
	t *testing.T,
) {
	candidates := []dwdDIMValidationCandidate{
		{
			SourceDatasetVersionID: "version_a",
			Name:                   "商户维度表",
		},
		{
			SourceDatasetVersionID: "version_b",
			Name:                   " 商户维度 ",
		},
		{
			SourceDatasetVersionID: "version_c",
			Name:                   "用户维度表",
		},
	}
	plan, err := canonicalizeDIMNameValidation(
		candidates, dwdDIMNameValidationPlan{Groups: []dwdDIMDuplicateNameGroup{}},
	)
	if err != nil {
		t.Fatalf("canonicalize DIM names: %v", err)
	}
	if len(plan.Groups) != 1 ||
		!reflect.DeepEqual(
			plan.Groups[0].SourceDatasetVersionIDs,
			[]string{"version_a", "version_b"},
		) {
		t.Fatalf("duplicate name groups = %#v", plan.Groups)
	}
}

func TestValidateDimensionNamesRepairsInvalidOutputIncrementally(
	t *testing.T,
) {
	invoker := &scriptedDIMValidationInvoker{
		results: []aiplatform.InvocationResult{
			{
				RequestID: "request_1",
				ProviderResult: aiplatform.ProviderResult{
					Content: json.RawMessage(
						`{"groups":[{"sourceDatasetVersionIds":["version_a"],"rationale":"不完整"}]}`,
					),
				},
			},
			{
				RequestID: "request_2",
				ProviderResult: aiplatform.ProviderResult{
					Content: json.RawMessage(`{"groups":[]}`),
				},
			},
		},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.ValidateDimensionNames(
		context.Background(),
		dwdPlanningInput{
			TenantID: "tenant", ActorID: "actor",
			ResourceID: "resource", Domain: "企业经营",
			Tables: []dwdPlanningTable{{
				VersionID: "version_a",
				Fields: []dwdPlanningField{{
					Code: "merchant_id",
				}},
			}},
		},
		[]dwdDIMValidationCandidate{
			{
				SourceDatasetVersionID: "version_a",
				DimensionDatasetID:     "dim_a", Name: "商户维度表",
			},
			{
				SourceDatasetVersionID: "version_b",
				DimensionDatasetID:     "dim_b", Name: "商户维度",
			},
		},
	)
	if err != nil {
		t.Fatalf("validate DIM names: %v", err)
	}
	if completion.AIRequestID != "request_2" ||
		len(completion.Plan.Groups) != 1 ||
		len(invoker.calls) != 2 {
		t.Fatalf(
			"completion=%#v calls=%d", completion, len(invoker.calls),
		)
	}
	secondMessages := invoker.calls[1].Request.Messages
	if len(secondMessages) != 4 ||
		secondMessages[2].Role != aiplatform.MessageRoleAssistant ||
		!strings.Contains(
			secondMessages[3].Parts[0].Text, "只修复 DIM 表名重复组",
		) {
		t.Fatalf("repair messages = %#v", secondMessages)
	}
}

func TestBuildCanonicalDIMDocumentKeepsOneODSWithoutCrossSourceJoin(
	t *testing.T,
) {
	visible := true
	candidate := func(
		sourceVersion, datasetID string,
		fields []Field,
	) dwdDIMValidationCandidate {
		projection := make([]string, 0, len(fields))
		for index := range fields {
			fields[index].ID = "source_field_" + fields[index].Code
			fields[index].Expression = Expression{
				Type: "FIELD_REF", NodeID: "node_entity",
				Field: fields[index].Code,
			}
			fields[index].Visible = &visible
			projection = append(projection, fields[index].Code)
		}
		document := Document{
			DSLVersion: DSLVersion,
			Dataset: Descriptor{
				Code: "dim_enterprise_merchant_" + datasetID,
				Name: "商户维度表", Description: "商户信息",
				Type: "SINGLE_SOURCE", Layer: LayerDIM,
			},
			Nodes: []Node{{
				ID: "node_entity", Type: "DATASET",
				DatasetVersionID: sourceVersion,
				Alias:            "t1", Projection: projection,
				SourceFilters: []SourceFilter{},
			}},
			Joins: []Join{}, Fields: fields,
			Filters: []Filter{}, GroupBy: []string{},
			Having: []Filter{}, Sorts: []Sort{},
			Parameters: []Parameter{},
			OutputGrain: OutputGrain{
				Description: "每行一个商户",
				KeyFields:   []string{"merchant_id"},
			},
			ExecutionPolicy: ExecutionPolicy{
				Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 30000,
				PreviewLimit: 200, ResultLimit: 100000,
				Materialization: MaterializationPolicy{
					Enabled: true, RefreshMode: "ON_DEMAND",
				},
			},
		}
		planningFields := make([]dwdPlanningField, 0, len(fields))
		for _, field := range fields {
			planningFields = append(planningFields, dwdPlanningField{
				Code: field.Code, Name: field.Name, Role: field.Role,
				CanonicalType: field.CanonicalType,
			})
		}
		return dwdDIMValidationCandidate{
			SourceDatasetID:        "source_" + datasetID,
			SourceDatasetVersionID: sourceVersion,
			DimensionDatasetID:     datasetID,
			Name:                   "商户维度表", Description: "商户信息",
			OutputGrain: document.OutputGrain,
			Fields:      planningFields, Document: document,
		}
	}
	first := candidate("version_a", "dataset_a", []Field{
		{
			Code: "merchant_id", Name: "商户编码",
			Role: "IDENTIFIER", CanonicalType: "STRING",
		},
		{
			Code: "merchant_name", Name: "商户名称",
			Role: "ATTRIBUTE", CanonicalType: "STRING",
		},
	})
	second := candidate("version_b", "dataset_b", []Field{
		{
			Code: "merchant_id", Name: "商户编码",
			Role: "IDENTIFIER", CanonicalType: "STRING",
		},
		{
			Code: "zone_id", Name: "配送区域",
			Role: "ATTRIBUTE", CanonicalType: "STRING",
		},
	})
	plan := dwdDIMDuplicateValidationPlan{
		Decision:                        "KEEP_ONE",
		CanonicalSourceDatasetVersionID: "version_a",
		FinalName:                       "商户维度表", FinalDescription: "统一商户主数据",
		SeparateNames: []dwdDIMSeparateName{},
		Rationale:     "同一商户业务键且字段互补",
	}
	document, inputHash, err := buildCanonicalDIMDocument(
		[]dwdDIMValidationCandidate{first, second}, plan,
	)
	if err != nil {
		t.Fatalf("build canonical DIM: %v", err)
	}
	if len(inputHash) != 64 || len(document.Nodes) != 1 ||
		len(document.Joins) != 0 || len(document.Fields) != 2 {
		t.Fatalf("canonical DIM shape = %#v", document)
	}
	if document.Nodes[0].DatasetVersionID != "version_a" {
		t.Fatalf("canonical DIM source = %#v", document.Nodes)
	}
	if _, err := Prepare(mustMarshalDWDDocument(document)); err != nil {
		t.Fatalf("canonical DIM does not pass DSL preparation: %#v", err)
	}
}

func TestBuildCanonicalDIMDocumentRejectsIncompatibleEntityKey(t *testing.T) {
	base := func(version, canonicalType string) dwdDIMValidationCandidate {
		field := Field{
			ID: "field_1", Code: "merchant_id", Name: "商户编码",
			Role: "IDENTIFIER", CanonicalType: canonicalType,
			Expression: Expression{
				Type: "FIELD_REF", NodeID: "node_entity",
				Field: "merchant_id",
			},
		}
		document := Document{
			DSLVersion: DSLVersion,
			Dataset: Descriptor{
				Code: "dim_merchant_" + version, Name: "商户维度表",
				Type: "SINGLE_SOURCE", Layer: LayerDIM,
			},
			Nodes: []Node{{
				ID: "node_entity", Type: "DATASET",
				DatasetVersionID: version, Alias: "t1",
				Projection:    []string{"merchant_id"},
				SourceFilters: []SourceFilter{},
			}},
			Fields: []Field{field},
			OutputGrain: OutputGrain{
				Description: "每行一个商户",
				KeyFields:   []string{"merchant_id"},
			},
		}
		return dwdDIMValidationCandidate{
			SourceDatasetVersionID: version,
			DimensionDatasetID:     "dataset_" + version,
			Name:                   "商户维度表", OutputGrain: document.OutputGrain,
			Fields: []dwdPlanningField{{
				Code: "merchant_id", CanonicalType: canonicalType,
			}},
			Document: document,
		}
	}
	_, _, err := buildCanonicalDIMDocument(
		[]dwdDIMValidationCandidate{
			base("version_a", "STRING"),
			base("version_b", "DATE"),
		},
		dwdDIMDuplicateValidationPlan{
			Decision:                        "KEEP_ONE",
			CanonicalSourceDatasetVersionID: "version_a",
			FinalName:                       "商户维度表", FinalDescription: "统一商户",
			SeparateNames: []dwdDIMSeparateName{},
			Rationale:     "同名候选",
		},
	)
	if !errors.Is(err, errDWDModelingInvalid) {
		t.Fatalf("incompatible key error = %v", err)
	}
}
