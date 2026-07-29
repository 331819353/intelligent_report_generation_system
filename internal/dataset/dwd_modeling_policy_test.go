package dataset

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
)

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

func TestValidateDWDClassificationsRejectsPeriodicSnapshotPrimaryDIMRole(
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
	if err == nil || !strings.Contains(
		err.Error(), "periodic snapshot FACT",
	) {
		t.Fatalf("validation error = %v, want periodic snapshot FACT", err)
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
				"commission_rate", "status",
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

func TestCanonicalizeMergedDWDClassificationsKeepsAuthoritativeEntity(
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
		len(got[1].DimensionKeyFieldCodes) != 0 ||
		len(got[1].DimensionAttributeFieldCodes) != 0 ||
		!strings.Contains(got[1].Rationale, "唯一权威 DIM") {
		t.Fatalf("redundant fact projection = %#v", got[1])
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
