package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	aiplatform "intelligent-report-generation-system/internal/ai"
)

type scriptedDWDPlannerInvoker struct {
	results []aiplatform.InvocationResult
	errors  []error
	calls   []aiplatform.Invocation
}

func (invoker *scriptedDWDPlannerInvoker) Configured() bool { return true }

func (invoker *scriptedDWDPlannerInvoker) Invoke(
	_ context.Context,
	invocation aiplatform.Invocation,
) (aiplatform.InvocationResult, error) {
	invoker.calls = append(invoker.calls, invocation)
	index := len(invoker.calls) - 1
	return invoker.results[index], invoker.errors[index]
}

func TestValidateDWDLLMPlanCompilesFactCleaningAndDimensionExpansion(t *testing.T) {
	input, assets, plan := validDWDPlanningFixture()
	if err := validateDWDLLMPlan(input, plan); err != nil {
		t.Fatalf("validate LLM plan: %v", err)
	}
	fact := assets[input.Tables[0].VersionID]
	document, inputHash, err := buildLLMDesignedDWDDocument(
		input.Domain, fact, assets, plan.Outputs[0],
	)
	if err != nil {
		t.Fatalf("build LLM document: %v", err)
	}
	if len(inputHash) != 64 || document.Dataset.Layer != LayerDWD ||
		len(document.Joins) != 1 || document.Joins[0].JoinType != "LEFT" {
		t.Fatalf("unexpected DWD document: hash=%q document=%+v", inputHash, document)
	}
	if len(document.Nodes) != 2 ||
		document.Nodes[0].Alias != "t1" ||
		document.Nodes[1].Alias != "t2" ||
		!document.Joins[0].ManualConfirmed {
		t.Fatalf("generated aliases/join readiness are invalid: nodes=%+v join=%+v", document.Nodes, document.Joins[0])
	}
	prepared, err := Prepare(mustMarshalDWDDocument(document))
	if err != nil {
		t.Fatalf("prepare LLM DWD DSL: %v", err)
	}
	if prepared.Document.Dataset.Layer != LayerDWD {
		t.Fatalf("prepared layer=%s", prepared.Document.Dataset.Layer)
	}
	customerID, ok := dwdDocumentFieldByCode(prepared.Document, "customer_id")
	if !ok || customerID.Expression.Type != "COALESCE" ||
		customerID.Expression.Arguments[0].Type != "TRIM" || customerID.Nullable {
		t.Fatalf("customer_id cleaning=%+v nullable=%v", customerID.Expression, customerID.Nullable)
	}
	orderDate, ok := dwdDocumentFieldByCode(prepared.Document, "order_date")
	if !ok || orderDate.Expression.Type != "DATE_TRUNC" ||
		orderDate.Expression.Argument == nil ||
		orderDate.Expression.Argument.Type != "CAST" {
		t.Fatalf("order_date cleaning=%+v", orderDate.Expression)
	}
	amount, ok := dwdDocumentFieldByCode(prepared.Document, "amount")
	if !ok || amount.Expression.Type != "ABS" ||
		amount.Expression.Argument == nil ||
		amount.Expression.Argument.Type != "ROUND" {
		t.Fatalf("amount processing=%+v", amount.Expression)
	}
	customerName, ok := dwdDocumentFieldByCode(prepared.Document, "customer_name")
	if !ok || customerName.Expression.Type != "UPPER" {
		t.Fatalf("customer_name processing=%+v", customerName.Expression)
	}
}

func TestDWDModelingResponseSchemaPassesCommonAIBoundary(t *testing.T) {
	input, _, _ := validDWDPlanningFixture()
	schema, err := dwdModelingResponseSchema(input)
	if err != nil {
		t.Fatalf("build response schema: %v", err)
	}
	if bytes.Contains(schema.Schema, []byte(`"uniqueItems"`)) {
		t.Fatal("DWD schema contains deepseek-v3 unsupported uniqueItems")
	}
	temperature := 0.0
	err = aiplatform.ValidateProviderRequest(aiplatform.ProviderRequest{
		Messages: []aiplatform.Message{
			{
				Role: aiplatform.MessageRoleSystem,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: "system",
				}},
			},
			{
				Role: aiplatform.MessageRoleUser,
				Parts: []aiplatform.ContentPart{{
					Type: aiplatform.ContentTypeText, Text: "{}",
				}},
			},
		},
		ResponseSchema: schema, Temperature: &temperature, MaxOutputTokens: 8000,
	})
	if err != nil {
		t.Fatalf("common AI boundary rejected DWD schema: %v", err)
	}
}

func TestDWDModelingPlannerRepairsInvalidStructuredOutputOnce(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
	content, err := json.Marshal(validPlan)
	if err != nil {
		t.Fatalf("marshal valid plan: %v", err)
	}
	invoker := &scriptedDWDPlannerInvoker{
		results: []aiplatform.InvocationResult{
			{RequestID: "initial-invalid"},
			{
				RequestID: "repaired",
				ProviderResult: aiplatform.ProviderResult{
					Content: content,
				},
			},
		},
		errors: []error{
			&aiplatform.ProviderError{
				Code:    aiplatform.ErrorCodeInvalidOutput,
				Message: "AI structured output is invalid",
			},
			nil,
		},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("repair DWD plan: %v", err)
	}
	if completion.AIRequestID != "repaired" || len(invoker.calls) != 2 {
		t.Fatalf("completion/calls = %#v/%d", completion, len(invoker.calls))
	}
	repairMessages := invoker.calls[1].Request.Messages
	if len(repairMessages) != 3 ||
		repairMessages[2].Role != aiplatform.MessageRoleUser ||
		!strings.Contains(repairMessages[2].Parts[0].Text, "逐表覆盖") {
		t.Fatalf("unexpected repair messages: %#v", repairMessages)
	}
}

func TestDWDModelingPlannerReturnsInvalidCandidateAndSafeDiagnosticToRepair(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
	content, err := json.Marshal(validPlan)
	if err != nil {
		t.Fatalf("marshal valid plan: %v", err)
	}
	schema, err := dwdModelingResponseSchema(input)
	if err != nil {
		t.Fatalf("build schema: %v", err)
	}
	_, invalidErr := aiplatform.ValidateStructuredOutput(schema, []byte(`{}`))
	if invalidErr == nil {
		t.Fatal("invalid candidate unexpectedly passed")
	}
	invoker := &scriptedDWDPlannerInvoker{
		results: []aiplatform.InvocationResult{
			{},
			{
				RequestID: "repaired",
				ProviderResult: aiplatform.ProviderResult{
					Content: content,
				},
			},
		},
		errors: []error{invalidErr, nil},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	if _, err := planner.Plan(context.Background(), input); err != nil {
		t.Fatalf("repair DWD plan: %v", err)
	}
	messages := invoker.calls[1].Request.Messages
	if len(messages) != 4 ||
		messages[2].Role != aiplatform.MessageRoleAssistant ||
		messages[2].Parts[0].Text != "{}" ||
		!strings.Contains(messages[3].Parts[0].Text, "结构诊断") {
		t.Fatalf("invalid candidate/diagnostic were not returned safely: %#v", messages)
	}
}

func TestDWDModelingPlannerRepairsStructureThenDomainContract(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
	validContent, err := json.Marshal(validPlan)
	if err != nil {
		t.Fatalf("marshal valid plan: %v", err)
	}
	invalidDomainPlan := cloneDWDPlan(t, validPlan)
	invalidDomainPlan.Outputs[0].Joins = append(
		invalidDomainPlan.Outputs[0].Joins,
		invalidDomainPlan.Outputs[0].Joins[0],
	)
	invalidDomainContent, err := json.Marshal(invalidDomainPlan)
	if err != nil {
		t.Fatalf("marshal domain-invalid plan: %v", err)
	}
	schema, err := dwdModelingResponseSchema(input)
	if err != nil {
		t.Fatalf("build schema: %v", err)
	}
	_, invalidStructureErr := aiplatform.ValidateStructuredOutput(schema, []byte(`{}`))
	invoker := &scriptedDWDPlannerInvoker{
		results: []aiplatform.InvocationResult{
			{},
			{
				RequestID: "domain-invalid",
				ProviderResult: aiplatform.ProviderResult{
					Content: invalidDomainContent,
				},
			},
			{
				RequestID: "fully-repaired",
				ProviderResult: aiplatform.ProviderResult{
					Content: validContent,
				},
			},
		},
		errors: []error{invalidStructureErr, nil, nil},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("incrementally repair DWD plan: %v", err)
	}
	if completion.AIRequestID != "fully-repaired" || len(invoker.calls) != 3 {
		t.Fatalf("completion/calls=%#v/%d", completion, len(invoker.calls))
	}
	lastMessages := invoker.calls[2].Request.Messages
	if len(lastMessages) != 4 ||
		!strings.Contains(lastMessages[3].Parts[0].Text, "joined more than once") {
		t.Fatalf("third invocation did not receive precise domain repair: %#v", lastMessages)
	}
}

func TestValidateDWDLLMPlanRejectsFabricationDroppedFactsAndMeasureNullFilling(t *testing.T) {
	input, _, base := validDWDPlanningFixture()
	tests := []struct {
		name string
		edit func(*dwdLLMPlan)
	}{
		{
			name: "fabricated join field",
			edit: func(plan *dwdLLMPlan) {
				plan.Outputs[0].Joins[0].Conditions[0].DimensionFieldCode = "invented_id"
			},
		},
		{
			name: "dropped fact field",
			edit: func(plan *dwdLLMPlan) {
				plan.Outputs[0].Fields = plan.Outputs[0].Fields[:2]
			},
		},
		{
			name: "measure null filled",
			edit: func(plan *dwdLLMPlan) {
				for index := range plan.Outputs[0].Fields {
					if plan.Outputs[0].Fields[index].SourceFieldCode == "amount" {
						plan.Outputs[0].Fields[index].Cleaning = []string{"COALESCE_NEGATIVE_ONE"}
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := cloneDWDPlan(t, base)
			test.edit(&plan)
			if err := validateDWDLLMPlan(input, plan); err == nil {
				t.Fatal("unsafe LLM plan was accepted")
			}
		})
	}
}

func TestValidateDWDLLMPlanRequiresUniqueExactDimensionRelationship(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	plan.Outputs[0].Joins = nil
	plan.Outputs[0].Fields = plan.Outputs[0].Fields[:3]
	err := validateDWDLLMPlan(input, plan)
	if err == nil || !strings.Contains(err.Error(), "unique compatible dimension key") {
		t.Fatalf("missing deterministic dimension join was not rejected: %v", err)
	}
}

func TestValidateDWDLLMPlanRequiresDescriptiveDimensionExpansion(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	plan.Outputs[0].Fields = plan.Outputs[0].Fields[:3]
	err := validateDWDLLMPlan(input, plan)
	if err == nil || !strings.Contains(err.Error(), "descriptive fields") {
		t.Fatalf("join without dimension expansion was not rejected: %v", err)
	}
}

func TestNormalizeDWDJoinOutputProjectionKeepsOnlyFactSideCompositeKeys(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	dimensionVersionID := input.Tables[1].VersionID
	plan.Outputs[0].Fields = append(plan.Outputs[0].Fields, dwdLLMField{
		SourceDatasetVersionID: dimensionVersionID,
		SourceFieldCode:        "customer_id",
		OutputCode:             "dimension_customer_id",
		OutputName:             "维度客户编号",
		OutputDescription:      "维度侧重复关联键",
		Role:                   "IDENTIFIER",
		Cleaning:               []string{"TRIM"},
	})
	normalized := normalizeDWDJoinOutputProjection(plan)
	factKeyCount, dimensionKeyCount := 0, 0
	for _, field := range normalized.Outputs[0].Fields {
		if field.SourceFieldCode != "customer_id" {
			continue
		}
		if field.SourceDatasetVersionID == input.Tables[0].VersionID {
			factKeyCount++
		}
		if field.SourceDatasetVersionID == dimensionVersionID {
			dimensionKeyCount++
		}
	}
	if factKeyCount != 1 || dimensionKeyCount != 0 {
		t.Fatalf(
			"normalized fact/dimension key counts = %d/%d",
			factKeyCount, dimensionKeyCount,
		)
	}
}

func TestValidateAndBuildDWDCompositeJoin(t *testing.T) {
	input, assets, plan := validDWDPlanningFixture()
	factVersionID := input.Tables[0].VersionID
	dimensionVersionID := input.Tables[1].VersionID
	tenantField := Field{
		ID: "field_tenant_id", Code: "tenant_id", Name: "租户编号",
		Role: "IDENTIFIER", CanonicalType: "STRING", Nullable: false,
		Expression: Expression{
			Type: "FIELD_REF", NodeID: "source", Field: "tenant_id",
		},
	}
	fact := assets[factVersionID]
	fact.Document.Fields = append(fact.Document.Fields, tenantField)
	assets[factVersionID] = fact
	dimension := assets[dimensionVersionID]
	dimension.Document.Fields = append(dimension.Document.Fields, tenantField)
	assets[dimensionVersionID] = dimension
	input.Tables[0] = planningTableFromAsset(fact)
	input.Tables[1] = planningTableFromAsset(dimension)
	plan.Outputs[0].Joins[0].Conditions = append(
		plan.Outputs[0].Joins[0].Conditions,
		dwdLLMJoinCondition{
			FactFieldCode: "tenant_id", DimensionFieldCode: "tenant_id",
		},
	)
	plan.Outputs[0].Fields = append(plan.Outputs[0].Fields, dwdLLMField{
		SourceDatasetVersionID: factVersionID, SourceFieldCode: "tenant_id",
		OutputCode: "tenant_id", OutputName: "租户编号",
		OutputDescription: "事实侧复合关联键", Role: "IDENTIFIER",
		Cleaning: []string{"TRIM"},
	})
	if err := validateDWDLLMPlan(input, plan); err != nil {
		t.Fatalf("validate composite join plan: %v", err)
	}
	document, _, err := buildLLMDesignedDWDDocument(
		input.Domain, fact, assets, plan.Outputs[0],
	)
	if err != nil {
		t.Fatalf("build composite join DWD: %v", err)
	}
	if len(document.Joins) != 1 || len(document.Joins[0].Conditions) != 2 {
		t.Fatalf("composite join was not preserved: %+v", document.Joins)
	}
}

func TestApplyLLMDWDProcessingSupportsEveryDeclaredComponentOperation(t *testing.T) {
	secondaryVersionID := uuid.NewString()
	assets := map[string]dwdODSAsset{
		secondaryVersionID: {
			VersionID: secondaryVersionID,
			Document: Document{Fields: []Field{
				{Code: "text_value", CanonicalType: "STRING"},
				{Code: "numeric_value", CanonicalType: "DECIMAL"},
			}},
		},
	}
	nodes := map[string]string{secondaryVersionID: "node_secondary"}
	secondary := func(code string) dwdLLMProcessingStep {
		return dwdLLMProcessingStep{
			SecondarySourceDatasetVersionID: secondaryVersionID,
			SecondarySourceFieldCode:        code,
		}
	}
	tests := []struct {
		name      string
		canonical string
		step      dwdLLMProcessingStep
	}{
		{name: "date format", canonical: "DATE", step: dwdLLMProcessingStep{Operation: "DATE_FORMAT", Unit: "MONTH"}},
		{name: "date trunc", canonical: "DATETIME", step: dwdLLMProcessingStep{Operation: "DATE_TRUNC", Unit: "DAY"}},
		{name: "cast", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "CAST", TargetType: "DATE"}},
		{name: "trim", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "TRIM"}},
		{name: "upper", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "UPPER"}},
		{name: "lower", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "LOWER"}},
		{name: "replace", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "REPLACE", SearchValue: "旧", ReplacementValue: "新"}},
		{name: "substring", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "SUBSTRING", Start: 1, Length: 8}},
		{name: "concat", canonical: "STRING", step: func() dwdLLMProcessingStep {
			step := secondary("text_value")
			step.Operation, step.Separator = "CONCAT", "-"
			return step
		}()},
		{name: "coalesce", canonical: "STRING", step: dwdLLMProcessingStep{Operation: "COALESCE", FallbackValue: ""}},
		{name: "add", canonical: "DECIMAL", step: func() dwdLLMProcessingStep {
			step := secondary("numeric_value")
			step.Operation = "ADD"
			return step
		}()},
		{name: "subtract", canonical: "DECIMAL", step: func() dwdLLMProcessingStep {
			step := secondary("numeric_value")
			step.Operation = "SUBTRACT"
			return step
		}()},
		{name: "multiply", canonical: "DECIMAL", step: func() dwdLLMProcessingStep {
			step := secondary("numeric_value")
			step.Operation = "MULTIPLY"
			return step
		}()},
		{name: "divide", canonical: "DECIMAL", step: func() dwdLLMProcessingStep {
			step := secondary("numeric_value")
			step.Operation = "DIVIDE"
			return step
		}()},
		{name: "round", canonical: "DECIMAL", step: dwdLLMProcessingStep{Operation: "ROUND", Precision: 2}},
		{name: "absolute", canonical: "DECIMAL", step: dwdLLMProcessingStep{Operation: "ABS"}},
		{name: "floor", canonical: "DECIMAL", step: dwdLLMProcessingStep{Operation: "FLOOR"}},
		{name: "ceil", canonical: "DECIMAL", step: dwdLLMProcessingStep{Operation: "CEIL"}},
		{name: "case", canonical: "STRING", step: dwdLLMProcessingStep{
			Operation: "CASE", ConditionOperator: "EQUALS",
			MatchValue: "A", ThenValue: "有效", ElseValue: "其他",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expression, _, _, err := applyLLMDWDProcessing(
				Expression{Type: "FIELD_REF", NodeID: "node_fact", Field: "value"},
				test.canonical, true, []dwdLLMProcessingStep{test.step},
				assets, nodes,
			)
			if err != nil {
				t.Fatalf("compile processing: %v", err)
			}
			issues := []ValidationIssue{}
			validateExpression(
				&issues, "expression", expression,
				map[string]bool{"node_fact": true, "node_secondary": true}, nil,
			)
			if len(issues) != 0 {
				t.Fatalf("compiled expression is invalid: %+v (%+v)", expression, issues)
			}
		})
	}
}

func TestValidateDWDCleaningAllowsStringTimeCastWithoutInventedSentinel(t *testing.T) {
	field := dwdPlanningField{
		Code: "event_time", Role: "TIME", CanonicalType: "STRING", Nullable: true,
	}
	if err := validateDWDCleaning(
		field, []string{"TRIM", "CAST_DATETIME"},
	); err != nil {
		t.Fatalf("valid string time cleaning was rejected: %v", err)
	}
	if err := validateDWDCleaning(
		field, []string{"TRIM", "COALESCE_UNKNOWN", "CAST_DATETIME"},
	); err == nil || !strings.Contains(err.Error(), "time field must not fill nulls") {
		t.Fatalf("string time sentinel was not rejected precisely: %v", err)
	}
}

func TestDWDPlanningSnapshotHashIncludesLLMMetadataAndTags(t *testing.T) {
	_, assetsByVersion, _ := validDWDPlanningFixture()
	assets := make([]dwdODSAsset, 0, len(assetsByVersion))
	for _, asset := range assetsByVersion {
		asset.Tags = []string{"领域:订单", "作用:事实明细"}
		asset.Domains = []string{"领域:订单"}
		assets = append(assets, asset)
	}
	baseline, err := dwdPlanningSnapshotHash(assets)
	if err != nil {
		t.Fatalf("baseline snapshot hash: %v", err)
	}
	assets[0].Tags = append(assets[0].Tags, "作用:客户主数据")
	changed, err := dwdPlanningSnapshotHash(assets)
	if err != nil {
		t.Fatalf("changed snapshot hash: %v", err)
	}
	if baseline == changed {
		t.Fatal("DWD planning snapshot ignored LLM metadata tag changes")
	}
}

func TestValidateDWDLLMPlanReportsAllFieldCleaningGapsForOneRepair(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	for index := range plan.Outputs[0].Fields {
		switch plan.Outputs[0].Fields[index].OutputCode {
		case "customer_id", "customer_name":
			plan.Outputs[0].Fields[index].Cleaning = []string{}
		}
	}
	err := validateDWDLLMPlan(input, plan)
	if err == nil {
		t.Fatal("invalid cleaning plan was accepted")
	}
	for _, fieldCode := range []string{"customer_id", "customer_name"} {
		if !strings.Contains(err.Error(), "field "+fieldCode+" cleaning is invalid") {
			t.Fatalf("aggregated validation error omitted %s: %v", fieldCode, err)
		}
	}
}

func TestCompleteMandatoryDWDPolicyCleaningDoesNotPlanJoinsOrFields(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	originalJoin := plan.Outputs[0].Joins[0]
	originalFieldCount := len(plan.Outputs[0].Fields)
	for index := range plan.Outputs[0].Fields {
		switch plan.Outputs[0].Fields[index].OutputCode {
		case "customer_id":
			plan.Outputs[0].Fields[index].Cleaning = []string{}
		case "amount":
			plan.Outputs[0].Fields[index].Cleaning = []string{
				"TRIM", "COALESCE_UNKNOWN",
			}
		case "order_date":
			plan.Outputs[0].Fields[index].Cleaning = []string{
				"TRIM", "COALESCE_UNKNOWN",
			}
		}
	}
	completed := completeMandatoryDWDPolicyCleaning(input, plan)
	if !reflect.DeepEqual(completed.Outputs[0].Joins[0], originalJoin) ||
		len(completed.Outputs[0].Fields) != originalFieldCount {
		t.Fatal("mandatory cleaning completion changed the LLM graph design")
	}
	customerID := completed.Outputs[0].Fields[0]
	if strings.Join(customerID.Cleaning, ",") != "TRIM,COALESCE_UNKNOWN" {
		t.Fatalf("mandatory customer cleaning = %#v", customerID.Cleaning)
	}
	for _, field := range completed.Outputs[0].Fields {
		switch field.OutputCode {
		case "amount":
			if len(field.Cleaning) != 0 {
				t.Fatalf("measure cleaning was not normalized: %#v", field.Cleaning)
			}
		case "order_date":
			if strings.Join(field.Cleaning, ",") != "CAST_DATE" {
				t.Fatalf("date cleaning was not normalized: %#v", field.Cleaning)
			}
		}
	}
	if err := validateDWDLLMPlan(input, completed); err != nil {
		t.Fatalf("policy-completed plan is invalid: %v", err)
	}
}

func validDWDPlanningFixture() (
	dwdPlanningInput,
	map[string]dwdODSAsset,
	dwdLLMPlan,
) {
	factDatasetID, factVersionID := uuid.NewString(), uuid.NewString()
	dimensionDatasetID, dimensionVersionID := uuid.NewString(), uuid.NewString()
	factFields := []Field{
		{
			ID: "field_customer_id", Code: "customer_id", Name: "客户编号",
			Role: "IDENTIFIER", CanonicalType: "STRING", Nullable: true,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "customer_id"},
		},
		{
			ID: "field_order_date", Code: "order_date", Name: "订单日期",
			Role: "TIME", CanonicalType: "DATE", SemanticType: "DATE", Nullable: true,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "order_date"},
		},
		{
			ID: "field_amount", Code: "amount", Name: "订单金额",
			Role: "MEASURE", CanonicalType: "DECIMAL", SemanticType: "AMOUNT", Nullable: true,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "amount"},
		},
	}
	dimensionFields := []Field{
		{
			ID: "field_customer_id", Code: "customer_id", Name: "客户编号",
			Role: "IDENTIFIER", CanonicalType: "STRING", Nullable: false,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "customer_id"},
		},
		{
			ID: "field_customer_name", Code: "customer_name", Name: "客户名称",
			Role: "ATTRIBUTE", CanonicalType: "STRING", Nullable: true,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "customer_name"},
		},
	}
	fact := dwdODSAsset{
		DatasetID: factDatasetID, VersionID: factVersionID,
		SchemaHash: strings.Repeat("a", 64), Name: "订单事实",
		Document: Document{
			Dataset: Descriptor{Layer: LayerODS}, Fields: factFields,
			OutputGrain: OutputGrain{
				Description: "每行一笔订单", KeyFields: []string{"customer_id"},
				TimeField: "order_date", DefaultTimeGrain: "DAY",
			},
		},
	}
	dimension := dwdODSAsset{
		DatasetID: dimensionDatasetID, VersionID: dimensionVersionID,
		SchemaHash: strings.Repeat("b", 64), Name: "客户主数据",
		Document: Document{Dataset: Descriptor{Layer: LayerODS}, Fields: dimensionFields},
	}
	input := dwdPlanningInput{
		TenantID: uuid.NewString(), ActorID: uuid.NewString(),
		ResourceID: factVersionID, Domain: "领域:订单",
		Trigger: dwdPlanningTrigger{DatasetID: factDatasetID, VersionID: factVersionID},
		Tables: []dwdPlanningTable{
			planningTableFromAsset(fact),
			planningTableFromAsset(dimension),
		},
	}
	plan := dwdLLMPlan{
		Domain: input.Domain,
		Classifications: []dwdLLMClassification{
			{DatasetVersionID: factVersionID, Role: "FACT", Rationale: "订单事件明细及金额字段"},
			{DatasetVersionID: dimensionVersionID, Role: "MASTER", Rationale: "客户稳定主体信息"},
		},
		Outputs: []dwdLLMOutput{{
			FactDatasetVersionID: factVersionID,
			Name:                 "订单 DWD 明细", Description: "清洗订单字段并扩充客户信息",
			Joins: []dwdLLMJoin{{
				DimensionDatasetVersionID: dimensionVersionID,
				Conditions: []dwdLLMJoinCondition{{
					FactFieldCode:      "customer_id",
					DimensionFieldCode: "customer_id",
				}},
				JoinType: "LEFT", Rationale: "客户编号类型一致且语义明确",
			}},
			Fields: []dwdLLMField{
				{
					SourceDatasetVersionID: factVersionID, SourceFieldCode: "customer_id",
					OutputCode: "customer_id", OutputName: "客户编号",
					OutputDescription: "清洗后的客户关联编号", Role: "IDENTIFIER",
					Cleaning: []string{"TRIM", "COALESCE_UNKNOWN"},
				},
				{
					SourceDatasetVersionID: factVersionID, SourceFieldCode: "order_date",
					OutputCode: "order_date", OutputName: "订单日期",
					OutputDescription: "标准订单日期", Role: "TIME",
					Cleaning: []string{"CAST_DATE"},
					Processing: []dwdLLMProcessingStep{
						{Operation: "DATE_TRUNC", Arguments: []string{"DAY"}},
					},
				},
				{
					SourceDatasetVersionID: factVersionID, SourceFieldCode: "amount",
					OutputCode: "amount", OutputName: "订单金额",
					OutputDescription: "原始订单金额，不擅自补零", Role: "MEASURE",
					Cleaning: []string{},
					Processing: []dwdLLMProcessingStep{
						{Operation: "ROUND", Arguments: []string{"2"}},
						{Operation: "ABS", Arguments: []string{}},
					},
				},
				{
					SourceDatasetVersionID: dimensionVersionID, SourceFieldCode: "customer_name",
					OutputCode: "customer_name", OutputName: "客户名称",
					OutputDescription: "由客户主数据扩充", Role: "ATTRIBUTE",
					Cleaning: []string{"TRIM", "COALESCE_UNKNOWN"},
					Processing: []dwdLLMProcessingStep{
						{Operation: "UPPER", Arguments: []string{}},
					},
				},
			},
			GrainKeyOutputCodes: []string{"customer_id"},
			TimeOutputCode:      "order_date", Rationale: "保持订单明细粒度",
		}},
	}
	return input, map[string]dwdODSAsset{
		factVersionID: fact, dimensionVersionID: dimension,
	}, plan
}

func planningTableFromAsset(asset dwdODSAsset) dwdPlanningTable {
	table := dwdPlanningTable{
		DatasetID: asset.DatasetID, VersionID: asset.VersionID, Name: asset.Name,
		Description: asset.Description, OutputGrain: asset.Document.OutputGrain,
	}
	for _, field := range asset.Document.Fields {
		table.Fields = append(table.Fields, dwdPlanningField{
			Code: field.Code, Name: field.Name, Description: field.Description,
			Role: field.Role, CanonicalType: field.CanonicalType,
			SemanticType: field.SemanticType, Nullable: field.Nullable,
		})
	}
	return table
}

func cloneDWDPlan(t *testing.T, plan dwdLLMPlan) dwdLLMPlan {
	t.Helper()
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var clone dwdLLMPlan
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
