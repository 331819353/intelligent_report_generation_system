package dataset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
		input.Domain, fact, assets, nil, plan.Outputs[0],
	)
	if err != nil {
		t.Fatalf("build LLM document: %v", err)
	}
	if len(inputHash) != 64 || document.Dataset.Layer != LayerDWD ||
		len(document.Joins) != 1 || document.Joins[0].JoinType != "LEFT" {
		t.Fatalf("unexpected DWD document: hash=%q document=%+v", inputHash, document)
	}
	if document.Dataset.Name != "订单 DWD 明细" ||
		document.Dataset.Code != "dwd_order_business_analysis_orders" {
		t.Fatalf(
			"DWD business identity=%q/%q",
			document.Dataset.Code, document.Dataset.Name,
		)
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

func TestBuildLLMDesignedDWDDocumentUsesProcessedPublishedDIM(t *testing.T) {
	input, assets, plan := validDWDPlanningFixture()
	fact := assets[input.Tables[0].VersionID]
	sourceDIM := assets[input.Tables[1].VersionID]
	dimDocument, _, err := buildLLMClassifiedDIMDocument(
		input.Domain, sourceDIM,
	)
	if err != nil {
		t.Fatalf("build processed DIM: %v", err)
	}
	publishedDIMVersionID := uuid.NewString()
	publishedDIM := sourceDIM
	publishedDIM.DatasetID = uuid.NewString()
	publishedDIM.VersionID = publishedDIMVersionID
	publishedDIM.SchemaHash = strings.Repeat("a", 64)
	publishedDIM.Document = dimDocument
	publishedDIM.Code = dimDocument.Dataset.Code
	publishedDIM.Name = dimDocument.Dataset.Name
	publishedDIM.Description = dimDocument.Dataset.Description

	document, _, err := buildLLMDesignedDWDDocument(
		input.Domain, fact, assets,
		map[string]dwdODSAsset{sourceDIM.VersionID: publishedDIM},
		plan.Outputs[0],
	)
	if err != nil {
		t.Fatalf("build DWD from processed DIM: %v", err)
	}
	if len(document.Nodes) != 2 ||
		document.Nodes[1].DatasetVersionID != publishedDIMVersionID {
		t.Fatalf(
			"DWD dimension dependency=%+v, want published DIM %s",
			document.Nodes, publishedDIMVersionID,
		)
	}
	factInput := planningInputWithModeledDimensions(
		input,
		dwdDimensionStageResult{
			AssetsBySourceVersion: map[string]dwdODSAsset{
				sourceDIM.VersionID: publishedDIM,
			},
		},
		plan.Classifications,
	)
	if factInput.Tables[1].VersionID != sourceDIM.VersionID ||
		factInput.Tables[1].DimensionStage != "STANDARDIZED_DIM_CONTRACT" ||
		factInput.Tables[1].Description != dimDocument.Dataset.Description {
		t.Fatalf("fact planner did not receive processed DIM metadata: %+v",
			factInput.Tables[1])
	}
}

func TestBuildLLMClassifiedDIMDocumentPreservesEntityContract(t *testing.T) {
	input, assets, _ := validDWDPlanningFixture()
	source := assets[input.Tables[1].VersionID]
	source.SourceTableName = "dim_customer"
	document, inputHash, err := buildLLMClassifiedDIMDocument(input.Domain, source)
	if err != nil {
		t.Fatalf("build DIM document: %v", err)
	}
	if len(inputHash) != 64 || document.Dataset.Layer != LayerDIM ||
		len(document.Nodes) != 1 ||
		document.Nodes[0].DatasetVersionID != source.VersionID ||
		len(document.Fields) != len(source.Document.Fields) {
		t.Fatalf("unexpected DIM document: hash=%q document=%+v", inputHash, document)
	}
	if !reflect.DeepEqual(document.OutputGrain.KeyFields, []string{"customer_id"}) {
		t.Fatalf("DIM keys=%v, want customer_id", document.OutputGrain.KeyFields)
	}
	if document.Dataset.Code != "dim_order_customer_customer" ||
		document.Dataset.Name != "客户主数据" {
		t.Fatalf("DIM business identity=%q/%q", document.Dataset.Code, document.Dataset.Name)
	}
	prepared, err := Prepare(mustMarshalDWDDocument(document))
	if err != nil {
		t.Fatalf("prepare DIM document: %v", err)
	}
	customerName, exists := dwdDocumentFieldByCode(
		prepared.Document, "customer_name",
	)
	if !exists || customerName.Expression.Type != "COALESCE" ||
		len(customerName.Expression.Arguments) != 2 ||
		customerName.Expression.Arguments[0].Type != "TRIM" ||
		customerName.Nullable || strings.TrimSpace(customerName.Description) == "" {
		t.Fatalf("DIM description cleaning=%+v nullable=%v",
			customerName.Expression, customerName.Nullable)
	}
}

func TestBusinessModeledDatasetCodeUsesPhysicalBusinessIdentity(t *testing.T) {
	_, assets, plan := validDWDPlanningFixture()
	fact := assets[plan.Outputs[0].FactDatasetVersionID]
	fact.SourceTableName = "FACT_ORDER_ITEM"
	code, err := businessModeledDatasetCode(
		LayerDWD, "领域:运营", fact, plan.Outputs[0].GrainKeyOutputCodes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "dwd_operations_business_analysis_order_item" {
		t.Fatalf(
			"DWD code=%q, want dwd_operations_business_analysis_order_item",
			code,
		)
	}
	fact.SourceTableName = "AGG_MERCHANT_DAILY_OPS"
	code, err = businessModeledDatasetCode(
		LayerDWD, "领域:运营", fact, plan.Outputs[0].GrainKeyOutputCodes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "dwd_operations_business_analysis_merchant_daily_ops" {
		t.Fatalf(
			"aggregate DWD code=%q, want dwd_operations_business_analysis_merchant_daily_ops",
			code,
		)
	}
	fact.SourceTableName = ""
	fact.Code = "mapped_4af38f8ad5774bb7bdf7998030ca6a1a"
	code, err = businessModeledDatasetCode(
		LayerDIM, "领域:订单", fact, []string{"customer_id"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if code != "dim_order_business_analysis_customer" {
		t.Fatalf(
			"fallback DIM code=%q, want dim_order_business_analysis_customer",
			code,
		)
	}
}

func TestNormalizeDWDSafeJoinAssociationsDropsTypeOnlyBusinessMismatch(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	factVersionID := plan.Outputs[0].FactDatasetVersionID
	dimensionVersionID := plan.Outputs[0].Joins[0].DimensionDatasetVersionID
	input.Tables[0].Fields[0].Code = "order_id"
	input.Tables[1].Fields[0].Code = "merchant_id"
	output := &plan.Outputs[0]
	output.Joins[0].Conditions[0] = dwdLLMJoinCondition{
		FactFieldCode: "order_id", DimensionFieldCode: "merchant_id",
	}
	output.Fields[0].SourceFieldCode = "order_id"
	output.Fields[0].OutputCode = "order_id"
	output.Fields[3].SourceDatasetVersionID = dimensionVersionID
	output.GrainKeyOutputCodes = []string{"order_id"}
	normalized := normalizeDWDSafeJoinAssociations(input, plan)
	if len(normalized.Outputs[0].Joins) != 0 {
		t.Fatalf("unsafe joins were retained: %+v", normalized.Outputs[0].Joins)
	}
	for _, field := range normalized.Outputs[0].Fields {
		if field.SourceDatasetVersionID != factVersionID {
			t.Fatalf("unsafe dimension field was retained: %+v", field)
		}
	}
	normalized = completeMandatoryDWDPolicyCleaning(input, normalized)
	if err := validateDWDLLMPlan(input, normalized); err != nil {
		t.Fatalf("safe fact-only fallback is invalid: %v", err)
	}
}

func TestCompleteDWDOutputContractRepairsOmittedSafeMetadata(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	factVersionID := plan.Outputs[0].FactDatasetVersionID
	output := &plan.Outputs[0]
	output.Joins = nil
	output.Fields = []dwdLLMField{{
		SourceDatasetVersionID: factVersionID,
		SourceFieldCode:        "amount",
		OutputCode:             "amount",
		OutputName:             "订单金额",
		OutputDescription:      "订单金额",
		Role:                   "MEASURE",
		Processing: []dwdLLMProcessingStep{{
			Operation: "ROUND", Arguments: []string{},
		}},
	}}
	output.GrainKeyOutputCodes = []string{"CUSTOMER_ID"}
	output.TimeOutputCode = "ORDER_DATE"

	completed := normalizeDWDSafeJoinAssociations(input, plan)
	completed = completeDWDOutputContract(input, completed)
	completed = normalizeDWDJoinOutputProjection(completed)
	completed = completeMandatoryDWDPolicyCleaning(input, completed)
	completed = dropInvalidDWDProcessing(input, completed)
	if err := validateDWDLLMPlan(input, completed); err != nil {
		t.Fatalf("completed DWD contract is invalid: %v", err)
	}
	result := completed.Outputs[0]
	if len(result.Joins) != 1 || len(result.Fields) != 4 {
		t.Fatalf("completed joins/fields=%d/%d, want 1/4: %+v",
			len(result.Joins), len(result.Fields), result)
	}
	for _, code := range []string{"customer_id", "order_date", "amount", "customer_name"} {
		found := false
		for _, field := range result.Fields {
			if field.OutputCode == code {
				found = true
				if code == "amount" && len(field.Processing) != 0 {
					t.Fatalf("invalid amount processing was retained: %+v", field.Processing)
				}
				break
			}
		}
		if !found {
			t.Fatalf("completed output is missing %s: %+v", code, result.Fields)
		}
	}
	if !reflect.DeepEqual(result.GrainKeyOutputCodes, []string{"customer_id"}) ||
		result.TimeOutputCode != "order_date" {
		t.Fatalf("completed grain/time=%v/%q",
			result.GrainKeyOutputCodes, result.TimeOutputCode)
	}
}

func TestNormalizeDWDFactCheckpointRefreshesCaseSensitiveDSLReferences(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	output := plan.Outputs[0]
	output.GrainKeyOutputCodes = []string{"CUSTOMER_ID"}
	output.TimeOutputCode = "ORDER_DATE"
	normalized, err := normalizeDWDFactCheckpoint(
		input, plan.Classifications, output.FactDatasetVersionID, output,
	)
	if err != nil {
		t.Fatalf("normalize FACT checkpoint: %v", err)
	}
	if !reflect.DeepEqual(normalized.GrainKeyOutputCodes, []string{"customer_id"}) ||
		normalized.TimeOutputCode != "order_date" {
		t.Fatalf("normalized checkpoint grain/time=%v/%q",
			normalized.GrainKeyOutputCodes, normalized.TimeOutputCode)
	}
}

func TestIncrementalFactSelectionSkipsUnchangedAndRedesignsChangedSources(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	fact := input.Tables[0]
	dimension := input.Tables[1]
	historical := dwdHistoricalOutput{
		FactDatasetID:  fact.DatasetID,
		DWDDatasetID:   uuid.NewString(),
		DWDDatasetCode: "dwd_order_business_analysis_orders",
		SourceVersionByDataset: map[string]string{
			fact.DatasetID:      fact.VersionID,
			dimension.DatasetID: dimension.VersionID,
		},
	}
	input.History = dwdPlanningHistory{
		OutputsByFactDataset: map[string]dwdHistoricalOutput{
			fact.DatasetID: historical,
		},
		DomainVersionByDataset: map[string]string{
			fact.DatasetID:      fact.VersionID,
			dimension.DatasetID: dimension.VersionID,
		},
	}
	design, unchanged := selectIncrementalDWDFacts(input, plan.Classifications)
	if len(design) != 0 || len(unchanged) != 1 {
		t.Fatalf("unchanged selection design=%v unchanged=%+v", design, unchanged)
	}
	codeDrift := historical
	codeDrift.DWDDatasetCode = "dwd_fact_orders"
	input.History.OutputsByFactDataset[fact.DatasetID] = codeDrift
	design, unchanged = selectIncrementalDWDFacts(input, plan.Classifications)
	if !reflect.DeepEqual(design, []string{fact.VersionID}) || len(unchanged) != 0 {
		t.Fatalf("business-code drift selection design=%v unchanged=%+v", design, unchanged)
	}
	input.History.OutputsByFactDataset[fact.DatasetID] = dwdHistoricalOutput{
		FactDatasetID:  fact.DatasetID,
		DWDDatasetID:   historical.DWDDatasetID,
		DWDDatasetCode: historical.DWDDatasetCode,
		SourceVersionByDataset: map[string]string{
			fact.DatasetID:      fact.VersionID,
			dimension.DatasetID: uuid.NewString(),
		},
	}
	design, unchanged = selectIncrementalDWDFacts(input, plan.Classifications)
	if !reflect.DeepEqual(design, []string{fact.VersionID}) || len(unchanged) != 0 {
		t.Fatalf("changed selection design=%v unchanged=%+v", design, unchanged)
	}
	input.History.OutputsByFactDataset[fact.DatasetID] = historical
	newPublishedDIMVersionID := uuid.NewString()
	design, unchanged = selectIncrementalDWDFacts(
		input, plan.Classifications,
		map[string]string{dimension.DatasetID: newPublishedDIMVersionID},
	)
	if !reflect.DeepEqual(design, []string{fact.VersionID}) || len(unchanged) != 0 {
		t.Fatalf(
			"new published DIM must redesign facts: design=%v unchanged=%+v",
			design, unchanged,
		)
	}
}

func TestPartialDWDPlanAllowsOneFactFailureWithoutInvalidatingDomain(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	plan.Outputs = nil
	if err := validateDWDPartialLLMPlan(input, plan); err != nil {
		t.Fatalf("partial plan should preserve valid classification: %v", err)
	}
	if err := validateDWDLLMPlan(input, plan); err == nil {
		t.Fatal("full plan unexpectedly accepted missing FACT output")
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

func TestDWDStageSchemasExcludeDeepSeekUnsupportedUniqueItems(t *testing.T) {
	input, _, plan := validDWDPlanningFixture()
	classificationSchema, err := dwdClassificationResponseSchema(input)
	if err != nil {
		t.Fatalf("build classification schema: %v", err)
	}
	dimensionTable, _, err := dwdDimensionPlanningScope(
		input, plan.Classifications, input.Tables[1].VersionID,
	)
	if err != nil {
		t.Fatalf("scope dimension schema: %v", err)
	}
	dimensionSchema, err := dwdDimensionDesignResponseSchema(dimensionTable)
	if err != nil {
		t.Fatalf("build dimension schema: %v", err)
	}
	factInput, _, err := dwdFactPlanningScope(
		input, plan.Classifications, input.Tables[0].VersionID,
	)
	if err != nil {
		t.Fatalf("scope fact schema: %v", err)
	}
	factSchema, err := dwdFactDesignResponseSchema(
		factInput, input.Tables[0].VersionID,
	)
	if err != nil {
		t.Fatalf("build fact schema: %v", err)
	}
	for name, schema := range map[string]aiplatform.JSONSchema{
		"classification": classificationSchema,
		"dimension":      dimensionSchema,
		"fact":           factSchema,
	} {
		if bytes.Contains(schema.Schema, []byte(`"uniqueItems"`)) {
			t.Fatalf(
				"%s schema contains deepseek-v3 unsupported uniqueItems",
				name,
			)
		}
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

func TestDWDModelingPlannerRepairsStructureThenNormalizesDuplicateJoin(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
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
		},
		errors: []error{invalidStructureErr, nil},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("incrementally repair DWD plan: %v", err)
	}
	if completion.AIRequestID != "domain-invalid" || len(invoker.calls) != 2 {
		t.Fatalf("completion/calls=%#v/%d", completion, len(invoker.calls))
	}
	if len(completion.Plan.Outputs[0].Joins) != 1 {
		t.Fatalf("duplicate join was not normalized: %+v", completion.Plan.Outputs[0].Joins)
	}
}

func TestDWDModelingClassifierUsesIndependentBoundedRepairStage(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
	validContent, err := json.Marshal(dwdLLMClassificationPlan{
		Domain: input.Domain, Classifications: validPlan.Classifications,
	})
	if err != nil {
		t.Fatalf("marshal classification: %v", err)
	}
	schema, err := dwdClassificationResponseSchema(input)
	if err != nil {
		t.Fatalf("classification schema: %v", err)
	}
	_, invalidErr := aiplatform.ValidateStructuredOutput(schema, []byte(`{}`))
	if invalidErr == nil {
		t.Fatal("empty classification unexpectedly passed")
	}
	invoker := &scriptedDWDPlannerInvoker{
		results: []aiplatform.InvocationResult{
			{},
			{
				RequestID:      uuid.NewString(),
				ProviderResult: aiplatform.ProviderResult{Content: validContent},
			},
		},
		errors: []error{invalidErr, nil},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.Classify(context.Background(), input)
	if err != nil {
		t.Fatalf("classify ODS: %v", err)
	}
	if len(completion.Classifications) != len(input.Tables) ||
		len(invoker.calls) != 2 {
		t.Fatalf(
			"classification/calls=%d/%d",
			len(completion.Classifications), len(invoker.calls),
		)
	}
	if invoker.calls[0].PromptVersion != dwdClassificationPromptVersion ||
		invoker.calls[0].Request.MaxOutputTokens != 3000 {
		t.Fatalf("unexpected classification invocation: %#v", invoker.calls[0])
	}
	if !strings.Contains(
		invoker.calls[0].Request.Messages[0].Parts[0].Text,
		"订单商品/订单行项目",
	) || !strings.Contains(
		invoker.calls[0].Request.Messages[0].Parts[0].Text,
		"同一 ODS 可以同时产生 DWD 与 DIM",
	) || !strings.Contains(
		invoker.calls[0].Request.Messages[0].Parts[0].Text,
		"dimensionKeyFieldCodes",
	) {
		t.Fatalf(
			"classification prompt lost multi-output grain contract: %#v",
			invoker.calls[0].Request.Messages[0],
		)
	}
	lastMessages := invoker.calls[1].Request.Messages
	if len(lastMessages) != 4 ||
		!strings.Contains(lastMessages[3].Parts[0].Text, "不要返回 outputs") {
		t.Fatalf("classification repair was not stage-specific: %#v", lastMessages)
	}
}

func TestDWDModelingDimensionDesignerAddsDescriptionsAndStandardization(t *testing.T) {
	input, assets, validPlan := validDWDPlanningFixture()
	dimensionTable := input.Tables[1]
	designedFields := make(
		[]dwdLLMDimensionFieldDesign, 0, len(dimensionTable.Fields),
	)
	for _, field := range dimensionTable.Fields {
		designedFields = append(designedFields, dwdLLMDimensionFieldDesign{
			SourceFieldCode:   field.Code,
			OutputName:        "标准" + field.Name,
			OutputDescription: "客户实体的" + field.Name + "标准说明",
			Standardization: mandatoryDIMCleaning(
				assets[dimensionTable.VersionID].Document.Fields[len(designedFields)],
			),
		})
	}
	content, err := json.Marshal(dwdLLMDimensionDesignPayload{
		Output: dwdLLMDimensionDesign{
			SourceDatasetVersionID: dimensionTable.VersionID,
			Name:                   "客户维度",
			Description:            "统一客户标识与说明属性",
			GrainKeyFieldCodes: append(
				[]string(nil), defaultDWDDimensionKeys(dimensionTable)...,
			),
			Fields:    designedFields,
			Rationale: "客户表保持一客一行实体粒度",
		},
	})
	if err != nil {
		t.Fatalf("marshal dimension design: %v", err)
	}
	invoker := &scriptedDWDPlannerInvoker{
		results: []aiplatform.InvocationResult{{
			RequestID: uuid.NewString(),
			ProviderResult: aiplatform.ProviderResult{
				Content: content,
			},
		}},
		errors: []error{nil},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.DesignDimension(
		context.Background(), input, validPlan.Classifications,
		dimensionTable.VersionID,
	)
	if err != nil {
		t.Fatalf("design one DIM: %v", err)
	}
	if completion.Output.Name != "客户维度" ||
		len(completion.Output.Fields) != len(dimensionTable.Fields) {
		t.Fatalf("unexpected DIM design: %+v", completion.Output)
	}
	call := invoker.calls[0]
	if call.PromptVersion != dwdDimensionDesignPromptVersion ||
		call.ResourceID != dimensionTable.VersionID ||
		!strings.Contains(
			call.Request.Messages[0].Parts[0].Text,
			"字段值标准化",
		) {
		t.Fatalf("unexpected DIM invocation: %#v", call)
	}
	if err := aiplatform.ValidateProviderRequest(call.Request); err != nil {
		t.Fatalf("DIM stage request rejected by common AI boundary: %v", err)
	}
	document, _, err := buildLLMDesignedDIMDocument(
		input.Domain, assets[dimensionTable.VersionID], completion.Output,
	)
	if err != nil {
		t.Fatalf("compile designed DIM: %v", err)
	}
	if document.Dataset.Name != "客户维度" ||
		document.Fields[0].Name != "标准客户编号" ||
		!strings.Contains(document.Fields[0].Description, "标准说明") {
		t.Fatalf("DIM design metadata was not compiled: %+v", document)
	}
}

func TestOrderItemFactCanAlsoExtractProductDimension(t *testing.T) {
	datasetID, versionID := uuid.NewString(), uuid.NewString()
	fields := []Field{
		{
			ID: "field_item_id", Code: "item_id", Name: "订单商品行编号",
			Role: "IDENTIFIER", CanonicalType: "STRING", Nullable: false,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "item_id"},
		},
		{
			ID: "field_order_id", Code: "order_id", Name: "订单编号",
			Role: "IDENTIFIER", CanonicalType: "STRING", Nullable: false,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "order_id"},
		},
		{
			ID: "field_sku_id", Code: "sku_id", Name: "商品 SKU 编号",
			Role: "IDENTIFIER", CanonicalType: "STRING", Nullable: false,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "sku_id"},
		},
		{
			ID: "field_item_name", Code: "item_name", Name: "商品名称",
			Role: "ATTRIBUTE", CanonicalType: "STRING", Nullable: true,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "item_name"},
		},
		{
			ID: "field_category", Code: "category", Name: "商品分类",
			Role: "ATTRIBUTE", CanonicalType: "STRING", Nullable: true,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "category"},
		},
		{
			ID: "field_quantity", Code: "quantity", Name: "购买数量",
			Role: "MEASURE", CanonicalType: "INTEGER", SemanticType: "QUANTITY",
			Nullable:   false,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "quantity"},
		},
		{
			ID: "field_line_amount", Code: "line_amount", Name: "行金额",
			Role: "MEASURE", CanonicalType: "DECIMAL", SemanticType: "AMOUNT",
			Nullable:   false,
			Expression: Expression{Type: "FIELD_REF", NodeID: "source", Field: "line_amount"},
		},
	}
	source := dwdODSAsset{
		DatasetID: datasetID, VersionID: versionID,
		SchemaHash: strings.Repeat("c", 64), Name: "订单商品明细事实表",
		SourceTableName: "FACT_ORDER_ITEM",
		Tags:            []string{"领域:运营", "主题:经营分析"},
		Document: Document{
			Dataset: Descriptor{Layer: LayerODS},
			Fields:  fields,
			OutputGrain: OutputGrain{
				Description: "每行代表一条订单商品明细",
				KeyFields:   []string{"item_id"},
			},
		},
	}
	input := dwdPlanningInput{
		TenantID: uuid.NewString(), ActorID: uuid.NewString(),
		ResourceID: versionID, Domain: "领域:运营",
		Trigger: dwdPlanningTrigger{
			DatasetID: datasetID, VersionID: versionID,
		},
		Tables: []dwdPlanningTable{planningTableFromAsset(source)},
	}
	classification := dwdLLMClassification{
		DatasetVersionID:             versionID,
		Role:                         "FACT",
		DimensionKeyFieldCodes:       []string{"sku_id"},
		DimensionAttributeFieldCodes: []string{"item_name", "category"},
		Rationale:                    "订单商品行为事实粒度，同时包含可按 SKU 去重治理的商品属性",
	}
	classifications := []dwdLLMClassification{classification}
	if err := validateDWDLLMClassifications(
		input, input.Domain, classifications,
	); err != nil {
		t.Fatalf("validate multi-output classification: %v", err)
	}
	scoped, scopedClassification, err := dwdDimensionPlanningScope(
		input, classifications, versionID,
	)
	if err != nil {
		t.Fatalf("scope embedded product dimension: %v", err)
	}
	gotCodes := make([]string, 0, len(scoped.Fields))
	for _, field := range scoped.Fields {
		gotCodes = append(gotCodes, field.Code)
	}
	if !reflect.DeepEqual(
		gotCodes, []string{"sku_id", "item_name", "category"},
	) || !reflect.DeepEqual(
		scoped.OutputGrain.KeyFields, []string{"sku_id"},
	) {
		t.Fatalf(
			"embedded dimension scope=%v grain=%v",
			gotCodes, scoped.OutputGrain.KeyFields,
		)
	}
	designFields := make([]dwdLLMDimensionFieldDesign, 0, len(scoped.Fields))
	for _, field := range scoped.Fields {
		sourceField, exists := dwdDocumentFieldByCode(
			source.Document, field.Code,
		)
		if !exists {
			t.Fatalf("scoped field %s is missing from source", field.Code)
		}
		designFields = append(designFields, dwdLLMDimensionFieldDesign{
			SourceFieldCode:   field.Code,
			OutputName:        field.Name,
			OutputDescription: "商品实体的" + field.Name,
			Standardization:   mandatoryDIMCleaning(sourceField),
		})
	}
	document, _, err := buildLLMDesignedDIMDocument(
		input.Domain, source, dwdLLMDimensionDesign{
			SourceDatasetVersionID: versionID,
			Name:                   "商品维度",
			Description:            "按 SKU 抽取并治理的商品说明属性",
			GrainKeyFieldCodes: append(
				[]string(nil), scopedClassification.DimensionKeyFieldCodes...,
			),
			Fields:    designFields,
			Rationale: "商品维度保持一 SKU 一行",
		},
	)
	if err != nil {
		t.Fatalf("build embedded product dimension: %v", err)
	}
	if document.Dataset.Layer != LayerDIM ||
		document.Dataset.Code != "dim_operations_business_analysis_sku" ||
		!document.Distinct || len(document.Fields) != 3 {
		t.Fatalf("unexpected product dimension document: %+v", document)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := Prepare(raw)
	if err != nil {
		t.Fatalf("prepare product dimension: %v", err)
	}
	hasDeduplicate := false
	for _, step := range prepared.LogicalPlan.Steps {
		hasDeduplicate = hasDeduplicate || step.Kind == "DEDUPLICATE"
	}
	if !hasDeduplicate {
		t.Fatalf(
			"product dimension plan lacks deduplication: %+v",
			prepared.LogicalPlan.Steps,
		)
	}
	dimensionIdentity := classificationDimensionIdentity(classification)
	factInput := planningInputWithModeledDimensions(
		input,
		dwdDimensionStageResult{AssetsBySourceVersion: map[string]dwdODSAsset{
			dimensionIdentity: {
				DatasetID: uuid.NewString(), VersionID: uuid.NewString(),
				SchemaHash: prepared.DSLHash, Code: document.Dataset.Code,
				Name: document.Dataset.Name, Document: document,
			},
		}},
		classifications,
	)
	expanded := expandedDWDClassifications(classifications)
	factScope, factClassifications, err := dwdFactPlanningScope(
		factInput, expanded, versionID,
	)
	if err != nil {
		t.Fatalf("scope fact with extracted product dimension: %v", err)
	}
	if len(factScope.Tables) != 2 || len(factClassifications) != 2 ||
		factClassifications[0].Role != "FACT" ||
		factClassifications[1].Role != "DIMENSION" ||
		factScope.Tables[1].VersionID != dimensionIdentity {
		t.Fatalf(
			"fact context did not retain both products: tables=%+v classifications=%+v",
			factScope.Tables, factClassifications,
		)
	}
}

func TestClassificationAllowsNumericAttributesOnEntityODSButNotFactProjection(
	t *testing.T,
) {
	input, _, plan := validDWDPlanningFixture()
	dimension := &input.Tables[1]
	dimension.Fields = append(dimension.Fields, dwdPlanningField{
		Code: "credit_limit", Name: "信用额度", Role: "MEASURE",
		CanonicalType: "DECIMAL", SemanticType: "AMOUNT", Nullable: true,
	})
	plan.Classifications[1].DimensionKeyFieldCodes =
		[]string{"customer_id"}
	plan.Classifications[1].DimensionAttributeFieldCodes =
		[]string{"customer_name", "credit_limit"}
	if err := validateDWDLLMClassifications(
		input, input.Domain, plan.Classifications,
	); err != nil {
		t.Fatalf("entity ODS numeric attribute was rejected: %v", err)
	}

	invalid := append(
		[]dwdLLMClassification(nil), plan.Classifications...,
	)
	invalid[0].DimensionKeyFieldCodes = []string{"customer_id"}
	invalid[0].DimensionAttributeFieldCodes = []string{"amount"}
	err := validateDWDLLMClassifications(input, input.Domain, invalid)
	if err == nil || !strings.Contains(err.Error(), "amount") ||
		!strings.Contains(err.Error(), input.Tables[0].VersionID) {
		t.Fatalf("FACT transactional attribute lacks precise diagnostic: %v", err)
	}

	overlap := append(
		[]dwdLLMClassification(nil), plan.Classifications...,
	)
	overlap[1].DimensionAttributeFieldCodes =
		[]string{"customer_id", "customer_name"}
	normalized := normalizeDWDClassifications(input, overlap)
	if containsString(
		normalized[1].DimensionAttributeFieldCodes, "customer_id",
	) {
		t.Fatalf(
			"entity key was not removed from attributes: %+v",
			normalized[1],
		)
	}
}

func TestDWDModelingFactDesignerScopesOneFactAndValidatedDimensions(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
	otherVersionID := uuid.NewString()
	input.Tables = append(input.Tables, dwdPlanningTable{
		DatasetID: uuid.NewString(), VersionID: otherVersionID,
		Name: "同步日志", Description: "技术同步状态",
		Fields: []dwdPlanningField{{
			Code: "log_id", Name: "日志编号", Role: "IDENTIFIER",
			CanonicalType: "STRING",
		}},
	})
	classifications := append(
		append([]dwdLLMClassification(nil), validPlan.Classifications...),
		dwdLLMClassification{
			DatasetVersionID: otherVersionID, Role: "OTHER",
			Rationale: "技术同步记录，不是业务事实或分析实体",
		},
	)
	content, err := json.Marshal(dwdLLMFactDesign{
		Output: validPlan.Outputs[0],
	})
	if err != nil {
		t.Fatalf("marshal fact design: %v", err)
	}
	invoker := &scriptedDWDPlannerInvoker{
		results: []aiplatform.InvocationResult{{
			RequestID:      uuid.NewString(),
			ProviderResult: aiplatform.ProviderResult{Content: content},
		}},
		errors: []error{nil},
	}
	planner := NewOrchestratedDWDModelingPlanner(invoker, time.Second)
	completion, err := planner.DesignFact(
		context.Background(), input, classifications,
		validPlan.Outputs[0].FactDatasetVersionID,
	)
	if err != nil {
		t.Fatalf("design one FACT: %v", err)
	}
	if completion.Output.FactDatasetVersionID !=
		validPlan.Outputs[0].FactDatasetVersionID {
		t.Fatalf("unexpected fact output: %#v", completion.Output)
	}
	call := invoker.calls[0]
	if call.PromptVersion != dwdFactDesignPromptVersion ||
		call.ResourceID != validPlan.Outputs[0].FactDatasetVersionID {
		t.Fatalf("unexpected fact invocation identity: %#v", call)
	}
	if err := aiplatform.ValidateProviderRequest(call.Request); err != nil {
		t.Fatalf("fact stage request rejected by common AI boundary: %v", err)
	}
	var request struct {
		Tables []dwdPlanningTable `json:"tables"`
	}
	if err := json.Unmarshal(
		[]byte(call.Request.Messages[1].Parts[0].Text), &request,
	); err != nil {
		t.Fatalf("decode fact request: %v", err)
	}
	if len(request.Tables) != 2 {
		t.Fatalf("fact request table count=%d, want FACT + MASTER", len(request.Tables))
	}
	for _, table := range request.Tables {
		if table.VersionID == otherVersionID {
			t.Fatal("OTHER table leaked into the independent FACT design stage")
		}
	}
}

func TestDWDModelingCheckpointHashSurvivesJSONBNormalization(t *testing.T) {
	input, _, validPlan := validDWDPlanningFixture()
	raw, hash, err := marshalDWDCheckpoint(dwdLLMClassificationPlan{
		Domain: input.Domain, Classifications: validPlan.Classifications,
	})
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	jsonbStyle, err := json.Marshal(normalized)
	if err != nil {
		t.Fatalf("normalize checkpoint: %v", err)
	}
	if err := validateDWDCheckpointHash(jsonbStyle, hash); err != nil {
		t.Fatalf("normalized checkpoint hash mismatch: %v", err)
	}
	jsonbStyle[len(jsonbStyle)-2] ^= 1
	if err := validateDWDCheckpointHash(jsonbStyle, hash); err == nil {
		t.Fatal("tampered checkpoint was accepted")
	}
}

func TestDWDModelingFailureDecisionDoesNotRepeatPermanentCalls(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		terminal  bool
		errorCode string
	}{
		{
			name: "invalid local output", err: errDWDModelingInvalid,
			terminal: true, errorCode: "WAREHOUSE_MODELING_INVALID_OUTPUT",
		},
		{
			name: "provider authentication",
			err: &aiplatform.ProviderError{
				Code: aiplatform.ErrorCodeAuthentication, Retryable: false,
			},
			terminal: true, errorCode: string(aiplatform.ErrorCodeAuthentication),
		},
		{
			name: "provider timeout",
			err: &aiplatform.ProviderError{
				Code: aiplatform.ErrorCodeTimeout, Retryable: true,
			},
		},
		{
			name: "worker cancellation",
			err: &aiplatform.ProviderError{
				Code: aiplatform.ErrorCodeCanceled, Retryable: false,
			},
		},
		{name: "database transient", err: errors.New("database temporarily unavailable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			terminal, errorCode := terminalDWDModelingFailure(test.err)
			if terminal != test.terminal || errorCode != test.errorCode {
				t.Fatalf(
					"decision=(%v,%q), want (%v,%q)",
					terminal, errorCode, test.terminal, test.errorCode,
				)
			}
		})
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
		input.Domain, fact, assets, nil, plan.Outputs[0],
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

func TestValidateDWDProcessingRejectsInvalidNumericCoalesceLiteral(
	t *testing.T,
) {
	err := validateDWDProcessing(
		dwdPlanningField{
			Code: "rating", Role: "MEASURE",
			CanonicalType: "DECIMAL", Nullable: true,
		},
		nil,
		[]dwdLLMProcessingStep{{
			Operation: "COALESCE",
			Arguments: []string{"rating"},
		}},
		map[string]dwdPlanningTable{},
		map[string]bool{},
		map[string]map[string]bool{},
	)
	if err == nil || !strings.Contains(err.Error(), "fallback literal") {
		t.Fatalf("invalid numeric COALESCE fallback was accepted: %v", err)
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
		SourceTableName: "FACT_ORDERS",
		Tags:            []string{"领域:订单", "主题:经营分析"},
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
		SourceTableName: "DIM_CUSTOMER",
		Tags:            []string{"领域:订单", "主题:客户"},
		Document:        Document{Dataset: Descriptor{Layer: LayerODS}, Fields: dimensionFields},
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
		SourceCode: asset.Code, SourceTableName: asset.SourceTableName,
		Tags: append([]string(nil), asset.Tags...),
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
