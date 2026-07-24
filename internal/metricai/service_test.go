package metricai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	aiplatform "intelligent-report-generation-system/internal/ai"
	"intelligent-report-generation-system/internal/metric"
)

const (
	testDatasetID             = "11111111-1111-4111-8111-111111111111"
	testDatasetVersionID      = "22222222-2222-4222-8222-222222222222"
	testMetricID              = "33333333-3333-4333-8333-333333333333"
	testMetricVersionID       = "44444444-4444-4444-8444-444444444444"
	testServiceDraftDatasetID = "55555555-5555-4555-8555-555555555555"
	testServiceDraftVersionID = "66666666-6666-4666-8666-666666666666"
	testMappedDatasetID       = "77777777-7777-4777-8777-777777777777"
	testMappedVersionID       = "88888888-8888-4888-8888-888888888888"
)

type retrieverStub struct {
	value RetrievalContext
	err   error
	got   AuthoringRequest
}

func (s *retrieverStub) Retrieve(_ context.Context, tenantID, actorID string, request AuthoringRequest, _ MetricIntent) (RetrievalContext, error) {
	if tenantID == "" || actorID == "" {
		return RetrievalContext{}, errors.New("missing identity")
	}
	s.got = request
	return s.value, s.err
}

type invokerStub struct {
	configured bool
	content    json.RawMessage
	contents   []json.RawMessage
	err        error
	got        aiplatform.Invocation
	calls      []aiplatform.Invocation
}

func (s *invokerStub) Configured() bool     { return s.configured }
func (s *invokerStub) ProviderName() string { return "stub" }
func (s *invokerStub) Model() string        { return "stub-model" }
func (s *invokerStub) Invoke(_ context.Context, value aiplatform.Invocation) (aiplatform.InvocationResult, error) {
	s.got = value
	callIndex := len(s.calls)
	s.calls = append(s.calls, value)
	content := s.content
	if callIndex < len(s.contents) {
		content = s.contents[callIndex]
	}
	return aiplatform.InvocationResult{
		RequestID: fmt.Sprintf("ai-request-%d", callIndex+1),
		ProviderResult: aiplatform.ProviderResult{
			Content: content, Model: "stub-model",
			Usage: aiplatform.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
		},
	}, s.err
}

func validRetrievalContext() RetrievalContext {
	existingDefinition := validDefinition()
	existingDefinition.Metric.Code = "paid_sales"
	existingDefinition.Metric.Name = "已支付销售额"
	existingDefinition.Metric.Description = "已支付订单金额"
	existingRaw, err := json.Marshal(existingDefinition)
	if err != nil {
		panic(err)
	}
	preparedExisting, err := metric.Prepare(existingRaw)
	if err != nil {
		panic(err)
	}
	return RetrievalContext{
		Datasets: []AuthorizedDataset{{
			ID: testDatasetID, VersionID: testDatasetVersionID, VersionNo: 3,
			Name: "订单明细", Description: "已支付订单明细", Status: "PUBLISHED",
			DSLHash: strings.Repeat("a", 64), Aggregated: false, Manageable: true,
		}},
		Fields: []AuthorizedField{
			{DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, ID: "field_amount", Code: "amount", Name: "订单金额", Description: "含税订单金额", CanonicalType: "DECIMAL", Role: "ATTRIBUTE", SemanticType: "AMOUNT"},
			{DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, ID: "field_order_id", Code: "order_id", Name: "订单编号", Description: "订单业务标识", CanonicalType: "STRING", Role: "IDENTIFIER", SemanticType: "IDENTIFIER"},
			{DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, ID: "field_region", Code: "region", Name: "地区", Description: "订单所属地区", CanonicalType: "STRING", Role: "DIMENSION", SemanticType: "REGION"},
			{DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, ID: "field_paid_at", Code: "paid_at", Name: "支付时间", Description: "支付完成时间", CanonicalType: "DATETIME", Role: "TIME", SemanticType: "DATETIME"},
		},
		AtomicFacts: []AuthorizedAtomicFact{{
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			SourceFieldIDs: []string{"field_amount"}, Name: "订单金额", Description: "订单明细中的含税金额",
			Caliber: "订单金额按行求和", Aggregation: "SUM", Dimensions: []string{"地区"},
			Period: "MONTH", Tags: []string{"销售", "金额"}, Confidence: 0.96,
		}},
		ExistingMetrics: []AuthorizedMetric{{
			ID: testMetricID, VersionID: testMetricVersionID, Code: "paid_sales", Name: "已支付销售额",
			Description: "已支付订单金额", Status: "PUBLISHED", DatasetID: testDatasetID,
			DatasetVersionID: testDatasetVersionID, DefinitionHash: preparedExisting.DefinitionHash,
			Definition: preparedExisting.Definition,
		}},
	}
}

func TestProposeAllowsDirectDistinctCountOnStringIdentifier(t *testing.T) {
	proposal := validCreateProposal()
	definition := proposal.CandidateMetricDefinition
	definition.Metric.Code, definition.Metric.Name = "order_count", "订单数"
	definition.Expression = metric.Expression{Type: "FIELD_REF", FieldID: "field_order_id"}
	definition.Aggregation, definition.Additivity = "COUNT_DISTINCT", "NON_ADDITIVE"
	definition.NumberFormat, definition.DecimalScale, definition.Unit = "#,##0", 0, ""
	proposal.RetrievalEvidence = append(proposal.RetrievalEvidence, fieldEvidence("field_order_id"))

	service := NewService(
		&retrieverStub{value: validRetrievalContext()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", AuthoringRequest{Requirement: "增加订单数量这一指标"})
	if err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if result.Proposal.CandidateMetricDefinition == nil || result.Proposal.CandidateMetricDefinition.Expression.FieldID != "field_order_id" ||
		result.Proposal.CandidateMetricDefinition.Aggregation != "COUNT_DISTINCT" {
		t.Fatalf("identifier count candidate was not retained: %#v", result.Proposal)
	}
}

func validRequest() AuthoringRequest {
	return AuthoringRequest{Requirement: "创建销售额指标，汇总已支付订单金额，按支付时间统计到月"}
}

func retrievalWithModifiableDraft() RetrievalContext {
	value := validRetrievalContext()
	value.ModifiableDraftDatasets = []AuthorizedDataset{{
		ID: testServiceDraftDatasetID, VersionID: testServiceDraftVersionID, VersionNo: 1,
		Name: "客户订单关联草稿", Description: "待补齐客户区域关联", Status: "DRAFT",
		DSLHash: strings.Repeat("b", 64), Aggregated: false, Manageable: true,
	}}
	value.ModifiableDraftFields = []AuthorizedField{
		{DatasetID: testServiceDraftDatasetID, DatasetVersionID: testServiceDraftVersionID, ID: "draft_amount", Code: "amount", Name: "销售额", Description: "草稿销售金额", CanonicalType: "DECIMAL", Role: "MEASURE", SemanticType: "AMOUNT"},
		{DatasetID: testServiceDraftDatasetID, DatasetVersionID: testServiceDraftVersionID, ID: "draft_customer_id", Code: "customer_id", Name: "客户ID", Description: "待关联客户主数据", CanonicalType: "STRING", Role: "IDENTIFIER", SemanticType: "ID"},
	}
	return value
}

func mappedRetrievalContext() RetrievalContext {
	value := validRetrievalContext()
	value.Datasets[0].Mapped = true
	value.MappedDatasets = value.Datasets
	value.MappedFields = value.Fields
	value.Datasets = []AuthorizedDataset{}
	value.Fields = []AuthorizedField{}
	return value
}

func unmanageableAndMappedRetrievalContext() RetrievalContext {
	value := validRetrievalContext()
	value.Datasets[0].Manageable = false
	value.MappedDatasets = []AuthorizedDataset{{
		ID: testMappedDatasetID, VersionID: testMappedVersionID, VersionNo: 1,
		Name: "支付明细映射表", Description: "支付业务只读映射来源", Status: "PUBLISHED",
		DSLHash: strings.Repeat("c", 64), Mapped: true,
	}}
	value.MappedFields = []AuthorizedField{{
		DatasetID: testMappedDatasetID, DatasetVersionID: testMappedVersionID,
		ID: "mapped_paid_amount", Code: "paid_amount", Name: "实付金额", Description: "订单实付金额",
		CanonicalType: "DECIMAL", Role: "MEASURE", SemanticType: "AMOUNT",
	}}
	return value
}

func validDefinition() metric.Definition {
	return metric.Definition{
		SchemaVersion: metric.DefinitionVersion,
		Metric:        metric.Descriptor{Code: "sales_amount", Name: "销售额", Description: "已支付订单金额", Type: "ATOMIC"},
		DatasetID:     testDatasetID, DatasetVersionID: testDatasetVersionID,
		Expression:  metric.Expression{Type: "FIELD_REF", FieldID: "field_amount"},
		Aggregation: "SUM", Unit: "元", NumberFormat: "#,##0.00",
		TimeFieldID: "field_paid_at", TimeGrain: "MONTH", Additivity: "ADDITIVE",
		NonAdditiveDimensionFieldIDs: []string{},
		AllowedDimensions: []metric.Dimension{
			{FieldID: "field_region", Name: "地区", HierarchyFieldIDs: []string{}, SortDirection: "ASC", NullLabel: "未知"},
			{FieldID: "field_paid_at", Name: "支付时间", HierarchyFieldIDs: []string{}, SortDirection: "ASC", NullLabel: "未知"},
		},
		DecimalScale: 2, RoundingMode: "HALF_UP", NullHandling: "IGNORE", DivisionByZero: "NULL",
	}
}

func datasetEvidence() RetrievalEvidence {
	return RetrievalEvidence{SourceType: "DATASET", SourceID: testDatasetID, DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "目标数据集"}
}

func fieldEvidence(id string) RetrievalEvidence {
	return RetrievalEvidence{SourceType: "FIELD", SourceID: id, DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "指标字段"}
}

func metricEvidence() RetrievalEvidence {
	return RetrievalEvidence{SourceType: "METRIC", SourceID: testMetricVersionID, DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "已存在相同指标"}
}

func draftDatasetEvidence() RetrievalEvidence {
	return RetrievalEvidence{SourceType: "DATASET", SourceID: testServiceDraftDatasetID, DatasetID: testServiceDraftDatasetID, DatasetVersionID: testServiceDraftVersionID, Reason: "可继续改造的草稿"}
}

func draftFieldEvidence(id string) RetrievalEvidence {
	return RetrievalEvidence{SourceType: "FIELD", SourceID: id, DatasetID: testServiceDraftDatasetID, DatasetVersionID: testServiceDraftVersionID, Reason: "草稿关联证据"}
}

func validCreateProposal() MetricAuthoringProposal {
	definition := validDefinition()
	return MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyCreateOnDataset, Summary: "可直接创建原子指标",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		RetrievalEvidence: []RetrievalEvidence{
			datasetEvidence(), fieldEvidence("field_amount"), fieldEvidence("field_region"), fieldEvidence("field_paid_at"),
		},
		CandidateMetricDefinition: &definition,
		ClarificationQuestions:    []string{}, Assumptions: []string{}, Warnings: []string{},
	}
}

func proposalJSON(t *testing.T, value MetricAuthoringProposal) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestProposeCreatesCanonicalReviewOnlyMetricProposal(t *testing.T) {
	retriever := &retrieverStub{value: validRetrievalContext()}
	proposal := validCreateProposal()
	invoker := &invokerStub{configured: true, content: proposalJSON(t, proposal)}
	service := NewService(retriever, invoker)

	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", AuthoringRequest{
		Requirement: " 创建销售额指标，汇总已支付订单金额，按支付时间统计到月 ",
	})
	if err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if result.RequestID != "ai-request-1" || !sha256Pattern.MatchString(result.RetrievalContextHash) {
		t.Fatalf("unexpected result identity: %#v", result)
	}
	if retriever.got != validRequest() {
		t.Fatalf("retriever request = %#v", retriever.got)
	}
	if invoker.got.Purpose != Purpose || invoker.got.PromptVersion != PromptVersion || invoker.got.ResourceType != "METRIC_AUTHORING" || invoker.got.ResourceID != result.RetrievalContextHash {
		t.Fatalf("invocation metadata = %#v", invoker.got)
	}
	if got := result.Proposal.CandidateMetricDefinition.Metric.Code; got != "sales_amount" {
		t.Fatalf("candidate definition was not canonicalized: %q", got)
	}
	if result.Proposal.CandidateMetricDefinition.DatasetVersionID != testDatasetVersionID {
		t.Fatalf("candidate lost exact dataset version: %#v", result.Proposal.CandidateMetricDefinition)
	}
}

func TestProposeAcceptsNonCreatingStrategies(t *testing.T) {
	tests := map[string]MetricAuthoringProposal{
		"reuse": {
			SchemaVersion: SchemaVersion, Strategy: StrategyReuseMetric, Summary: "复用现有指标",
			ReuseMetricVersionID: testMetricVersionID, RetrievalEvidence: []RetrievalEvidence{metricEvidence()},
			ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
		},
		"modify": {
			SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需增加退款状态过滤",
			TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
			RetrievalEvidence: []RetrievalEvidence{datasetEvidence()}, DatasetInstruction: "在订单明细数据集中增加退款状态字段与过滤配置",
			ClarificationQuestions: []string{"请确认退款完成后是否追溯冲减原支付月份。"}, Assumptions: []string{}, Warnings: []string{"应用后仍需重新发布数据集"},
		},
		"gap": {
			SchemaVersion: SchemaVersion, Strategy: StrategyDataGap, Summary: "授权数据中没有退款金额",
			RetrievalEvidence: []RetrievalEvidence{}, ClarificationQuestions: []string{"是否可以接入退款明细数据？"}, Assumptions: []string{}, Warnings: []string{"需要补充数据源"},
		},
		"clarify": {
			SchemaVersion: SchemaVersion, Strategy: StrategyNeedsClarification, Summary: "需确认收入确认时点",
			RetrievalEvidence: []RetrievalEvidence{datasetEvidence()}, ClarificationQuestions: []string{"按支付时间还是发货时间确认销售额？"},
			Assumptions: []string{}, Warnings: []string{},
		},
	}
	for name, proposal := range tests {
		t.Run(name, func(t *testing.T) {
			service := NewService(&retrieverStub{value: validRetrievalContext()}, &invokerStub{configured: true, content: proposalJSON(t, proposal)})
			result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
			if err != nil {
				t.Fatalf("Propose() error = %v", err)
			}
			if result.Proposal.Strategy != proposal.Strategy {
				t.Fatalf("strategy = %q", result.Proposal.Strategy)
			}
		})
	}
}

func TestProposeCreatesNewDatasetFromAuthorizedMappedEvidence(t *testing.T) {
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyCreateDataset,
		Summary: "需要先新建订单区域明细数据集，再创建指标。",
		RetrievalEvidence: []RetrievalEvidence{
			datasetEvidence(), fieldEvidence("field_amount"), fieldEvidence("field_region"),
		},
		DatasetInstruction:     "新建普通数据集，以订单明细为输入，保留订单金额、地区和支付时间，输出粒度保持每笔订单。",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(
		&retrieverStub{value: mappedRetrievalContext()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if result.Proposal.Strategy != StrategyCreateDataset || result.Proposal.TargetDatasetID != "" || result.Proposal.TargetDatasetVersionID != "" {
		t.Fatalf("unexpected CREATE_DATASET proposal: %#v", result.Proposal)
	}
}

func TestProposeRejectsDirectMetricOnPublishedMappedDataset(t *testing.T) {
	service := NewService(
		&retrieverStub{value: mappedRetrievalContext()},
		&invokerStub{configured: true, content: proposalJSON(t, validCreateProposal())},
	)
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("mapped dataset direct metric error = %v, want ErrInvalidOutput", err)
	}
}

func TestProposeSafelyDowngradesMappedDatasetModification(t *testing.T) {
	instruction := "在订单明细中关联地区信息。"
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需要补充地区字段。",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		RetrievalEvidence:      []RetrievalEvidence{},
		DatasetInstruction:     instruction,
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	retrieval, err := normalizeRetrievalContext(mappedRetrievalContext())
	if err != nil {
		t.Fatalf("normalize retrieval error = %v", err)
	}
	result, err := validateProposal(validRequest(), retrieval, proposal)
	if err != nil {
		t.Fatalf("mapped modification downgrade error = %v", err)
	}
	if result.Strategy != StrategyCreateDataset || result.TargetDatasetID != "" ||
		result.TargetDatasetVersionID != "" || result.DatasetInstruction != instruction {
		t.Fatalf("unexpected downgraded proposal: %#v", result)
	}
	if len(result.RetrievalEvidence) != 1 ||
		result.RetrievalEvidence[0].SourceID != testDatasetID ||
		result.RetrievalEvidence[0].DatasetVersionID != testDatasetVersionID {
		t.Fatalf("mapped source evidence was not synthesized: %#v", result.RetrievalEvidence)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "已安全调整为基于映射表新建普通数据集") {
		t.Fatalf("downgrade warning = %#v", result.Warnings)
	}
}

func TestProposeSafelyDowngradesInvalidModificationWhenMappedEvidenceIsGrounded(t *testing.T) {
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需要从支付明细构建客户月支付数据。",
		// This ordinary dataset is readable but not manageable, so it cannot be modified.
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		RetrievalEvidence: []RetrievalEvidence{{
			SourceType: "FIELD", SourceID: "mapped_paid_amount",
			DatasetID: testMappedDatasetID, DatasetVersionID: testMappedVersionID,
			Reason: "支付金额来源",
		}},
		DatasetInstruction:     "以支付明细映射表为输入，保留客户、支付时间和实付金额，新建普通支付分析数据集。",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	invoker := &invokerStub{configured: true, content: proposalJSON(t, proposal)}
	service := NewService(&retrieverStub{value: unmanageableAndMappedRetrievalContext()}, invoker)

	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("grounded mapped evidence downgrade error = %v", err)
	}
	if result.Proposal.Strategy != StrategyCreateDataset || result.Proposal.TargetDatasetID != "" ||
		len(result.Proposal.RetrievalEvidence) != 1 ||
		result.Proposal.RetrievalEvidence[0].SourceID != "mapped_paid_amount" ||
		result.Proposal.RetrievalEvidence[0].DatasetID != testMappedDatasetID {
		t.Fatalf("unexpected grounded evidence downgrade: %#v", result.Proposal)
	}
	if len(invoker.calls) != 1 {
		t.Fatalf("grounded deterministic downgrade should use one call: %d", len(invoker.calls))
	}
}

func TestProposeRepairsMappedDatasetModificationIntoSafeCreateDataset(t *testing.T) {
	invalid := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需要补充客户和月份。",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		CandidateMetricDefinition: func() *metric.Definition {
			value := validDefinition()
			return &value
		}(),
		RetrievalEvidence:      []RetrievalEvidence{datasetEvidence()},
		DatasetInstruction:     "在支付明细中补充客户与月份后汇总金额。",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	repaired := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyCreateDataset, Summary: "需要基于映射表新建普通分析数据集。",
		RetrievalEvidence: []RetrievalEvidence{
			datasetEvidence(), fieldEvidence("field_amount"), fieldEvidence("field_paid_at"),
		},
		DatasetInstruction:     "以支付明细映射表为只读来源，新建普通数据集，保留客户、支付时间和支付金额，并按客户与月份输出可聚合明细。",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	invoker := &invokerStub{
		configured: true,
		contents: []json.RawMessage{
			proposalJSON(t, invalid),
			proposalJSON(t, repaired),
		},
	}
	service := NewService(&retrieverStub{value: mappedRetrievalContext()}, invoker)

	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("Propose() repair error = %v", err)
	}
	if result.RequestID != "ai-request-2" || result.Proposal.Strategy != StrategyCreateDataset {
		t.Fatalf("unexpected repaired result: %#v", result)
	}
	if len(invoker.calls) != 2 {
		t.Fatalf("invocation count = %d, want 2", len(invoker.calls))
	}
	messages := invoker.calls[1].Request.Messages
	if len(messages) != 4 || messages[2].Role != aiplatform.MessageRoleAssistant || messages[3].Role != aiplatform.MessageRoleUser {
		t.Fatalf("repair messages = %#v", messages)
	}
	if !strings.Contains(messages[3].Parts[0].Text, "mappedDatasets/mappedFields") ||
		!strings.Contains(messages[3].Parts[0].Text, "FIELD 证据的三个标识必须来自同一个字段对象") {
		t.Fatalf("repair instruction = %q", messages[3].Parts[0].Text)
	}
}

func TestProposeFailsClosedWhenRepairRemainsUnsafe(t *testing.T) {
	invalid := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "直接修改映射表。",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		CandidateMetricDefinition: func() *metric.Definition {
			value := validDefinition()
			return &value
		}(),
		RetrievalEvidence:      []RetrievalEvidence{datasetEvidence()},
		DatasetInstruction:     "直接修改映射表。",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	invoker := &invokerStub{configured: true, content: proposalJSON(t, invalid)}
	service := NewService(&retrieverStub{value: mappedRetrievalContext()}, invoker)

	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrInvalidOutput", err)
	}
	if len(invoker.calls) != 2 {
		t.Fatalf("invocation count = %d, want one bounded repair", len(invoker.calls))
	}
}

func TestProposeDoesNotNormalizeUnsafeMappedModificationShape(t *testing.T) {
	definition := validDefinition()
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需要补充地区字段。",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		CandidateMetricDefinition: &definition, DatasetInstruction: "在订单明细中关联地区信息。",
		RetrievalEvidence:      []RetrievalEvidence{datasetEvidence()},
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(
		&retrieverStub{value: mappedRetrievalContext()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrInvalidOutput", err)
	}
}

func TestProposeRejectsUngroundedOrTargetedCreateDatasetProposal(t *testing.T) {
	tests := map[string]func(*MetricAuthoringProposal){
		"missing evidence": func(value *MetricAuthoringProposal) { value.RetrievalEvidence = []RetrievalEvidence{} },
		"existing target": func(value *MetricAuthoringProposal) {
			value.TargetDatasetID = testDatasetID
			value.TargetDatasetVersionID = testDatasetVersionID
		},
		"missing instruction": func(value *MetricAuthoringProposal) { value.DatasetInstruction = "" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			proposal := MetricAuthoringProposal{
				SchemaVersion: SchemaVersion, Strategy: StrategyCreateDataset, Summary: "需要新建数据集。",
				RetrievalEvidence: []RetrievalEvidence{datasetEvidence()}, DatasetInstruction: "新建订单分析数据集。",
				ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
			}
			mutate(&proposal)
			service := NewService(
				&retrieverStub{value: mappedRetrievalContext()},
				&invokerStub{configured: true, content: proposalJSON(t, proposal)},
			)
			if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("error = %v, want ErrInvalidOutput", err)
			}
		})
	}
}

func TestProposeRejectsHallucinatedOrUnsafeCandidateReferences(t *testing.T) {
	tests := map[string]func(*RetrievalContext, *MetricAuthoringProposal){
		"unknown field": func(_ *RetrievalContext, proposal *MetricAuthoringProposal) {
			proposal.CandidateMetricDefinition.Expression.FieldID = "invented_amount"
		},
		"aggregated dataset": func(retrieval *RetrievalContext, _ *MetricAuthoringProposal) {
			retrieval.Datasets[0].Aggregated = true
		},
		"non numeric value field": func(retrieval *RetrievalContext, _ *MetricAuthoringProposal) {
			retrieval.Fields[0].CanonicalType = "STRING"
		},
		"derived metric": func(_ *RetrievalContext, proposal *MetricAuthoringProposal) {
			proposal.CandidateMetricDefinition.Metric.Type = "DERIVED"
			proposal.CandidateMetricDefinition.Expression = metric.Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
			proposal.CandidateMetricDefinition.Aggregation = "NONE"
		},
		"candidate stale dataset version": func(_ *RetrievalContext, proposal *MetricAuthoringProposal) {
			proposal.CandidateMetricDefinition.DatasetVersionID = "55555555-5555-4555-8555-555555555555"
		},
		"metric code conflicts with dimension": func(_ *RetrievalContext, proposal *MetricAuthoringProposal) {
			proposal.CandidateMetricDefinition.Metric.Code = "region"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			retrieval := validRetrievalContext()
			proposal := validCreateProposal()
			mutate(&retrieval, &proposal)
			service := NewService(&retrieverStub{value: retrieval}, &invokerStub{configured: true, content: proposalJSON(t, proposal)})
			_, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
			if !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("error = %v, want ErrInvalidOutput", err)
			}
		})
	}
}

func TestProposeSynthesizesActionEvidenceInsteadOfRejectingSafeProposal(t *testing.T) {
	proposal := validCreateProposal()
	proposal.RetrievalEvidence = []RetrievalEvidence{}
	service := NewService(
		&retrieverStub{value: validRetrievalContext()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if len(result.Proposal.RetrievalEvidence) != 1 {
		t.Fatalf("system evidence = %#v", result.Proposal.RetrievalEvidence)
	}
	evidence := result.Proposal.RetrievalEvidence[0]
	if evidence.SourceType != "DATASET" || evidence.SourceID != testDatasetID || evidence.DatasetVersionID != testDatasetVersionID {
		t.Fatalf("unexpected synthesized evidence: %#v", evidence)
	}
}

func TestProposeRepairsUniqueDraftPairsAndDeduplicatesEvidence(t *testing.T) {
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "关联客户区域",
		// Both values are independently allowed by the structured schema, but the version
		// belongs to the published dataset rather than the selected draft dataset.
		TargetDatasetID: testServiceDraftDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		RetrievalEvidence: []RetrievalEvidence{
			{SourceType: "DATASET", SourceID: testServiceDraftDatasetID, DatasetID: testServiceDraftDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "目标订单草稿"},
			{SourceType: "DATASET", SourceID: testServiceDraftDatasetID, DatasetID: testServiceDraftDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "重复目标订单草稿"},
			{SourceType: "FIELD", SourceID: "draft_customer_id", DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "客户关联键"},
		},
		DatasetInstruction:     "将客户区域关联到订单草稿，保留销售额、区域与月份分析字段",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(
		&retrieverStub{value: retrievalWithModifiableDraft()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("Propose() pair repair error = %v", err)
	}
	if result.Proposal.TargetDatasetID != testServiceDraftDatasetID || result.Proposal.TargetDatasetVersionID != testServiceDraftVersionID {
		t.Fatalf("target pair was not repaired: %#v", result.Proposal)
	}
	if len(result.Proposal.RetrievalEvidence) != 2 {
		t.Fatalf("evidence was not deduplicated: %#v", result.Proposal.RetrievalEvidence)
	}
	for _, evidence := range result.Proposal.RetrievalEvidence {
		if evidence.DatasetID != testServiceDraftDatasetID || evidence.DatasetVersionID != testServiceDraftVersionID {
			t.Fatalf("evidence owner pair was not repaired: %#v", evidence)
		}
	}
}

func TestProposeRepairsAmbiguousFieldOwnerFromDatasetHint(t *testing.T) {
	retrieval := retrievalWithModifiableDraft()
	retrieval.ModifiableDraftFields[0].ID = "field_amount"
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyNeedsClarification, Summary: "需要确认金额口径。",
		RetrievalEvidence: []RetrievalEvidence{{
			SourceType: "FIELD", SourceID: "field_amount",
			DatasetID: testServiceDraftDatasetID, DatasetVersionID: testDatasetVersionID,
			Reason: "候选销售金额字段",
		}},
		ClarificationQuestions: []string{"应使用订单金额还是支付金额？"}, Assumptions: []string{}, Warnings: []string{},
	}
	invoker := &invokerStub{configured: true, content: proposalJSON(t, proposal)}
	service := NewService(&retrieverStub{value: retrieval}, invoker)

	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("Propose() owner repair error = %v", err)
	}
	evidence := result.Proposal.RetrievalEvidence[0]
	if evidence.DatasetID != testServiceDraftDatasetID || evidence.DatasetVersionID != testServiceDraftVersionID {
		t.Fatalf("field owner was not safely repaired: %#v", evidence)
	}
	if len(invoker.calls) != 1 {
		t.Fatalf("safe deterministic repair should not invoke model twice: %d", len(invoker.calls))
	}
}

func TestValidateEvidenceDropsFieldThatCannotBeGroundedToOneOwner(t *testing.T) {
	fields := map[string]AuthorizedField{}
	for _, field := range []AuthorizedField{
		{DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, ID: "shared_amount"},
		{DatasetID: testMappedDatasetID, DatasetVersionID: testMappedVersionID, ID: "shared_amount"},
	} {
		fields[fieldKey(field.DatasetID, field.DatasetVersionID, field.ID)] = field
	}
	result, err := validateEvidence([]RetrievalEvidence{{
		SourceType: "FIELD", SourceID: "shared_amount", DatasetID: "", DatasetVersionID: "",
		Reason: "模型未能给出唯一字段归属",
	}}, map[string]AuthorizedDataset{}, fields, map[string]AuthorizedMetric{}, nil)
	if err != nil {
		t.Fatalf("validateEvidence() error = %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("ambiguous evidence should be discarded, got %#v", result)
	}
}

func TestProposeNormalizesLegacyPartialIntentIntoOneRequirement(t *testing.T) {
	proposal := validCreateProposal()
	retriever := &retrieverStub{value: validRetrievalContext()}
	invoker := &invokerStub{configured: true, content: proposalJSON(t, proposal)}
	service := NewService(retriever, invoker)

	_, err := service.Propose(context.Background(), "tenant-1", "actor-1", AuthoringRequest{
		DefinitionIntent: " 汇总已支付订单金额 ",
	})
	if err != nil {
		t.Fatalf("Propose() legacy compatibility error = %v", err)
	}
	want := AuthoringRequest{Requirement: "业务需求：汇总已支付订单金额"}
	if retriever.got != want {
		t.Fatalf("normalized legacy request = %#v, want %#v", retriever.got, want)
	}
	requestPayload, err := json.Marshal(invoker.got.Request.Messages[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(requestPayload), `\"requirement\"`) || strings.Contains(string(requestPayload), "definitionIntent") {
		t.Fatalf("provider received legacy request shape: %s", requestPayload)
	}
}

func TestProposeAllowsGeneratedNameAndNonBlockingQuestions(t *testing.T) {
	proposal := validCreateProposal()
	proposal.CandidateMetricDefinition.Metric.Name = "AI 推断的已支付销售额"
	proposal.ClarificationQuestions = []string{"请确认金额是否含税；当前候选按字段说明推断为含税。"}
	proposal.Assumptions = []string{"名称、格式与时间粒度由 AI 根据授权字段语义补齐。"}
	proposal.Warnings = []string{"含税口径需要人工确认。"}
	service := NewService(
		&retrieverStub{value: validRetrievalContext()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", AuthoringRequest{Requirement: "帮我做一个销售指标"})
	if err != nil {
		t.Fatalf("Propose() error = %v", err)
	}
	if result.Proposal.CandidateMetricDefinition == nil || result.Proposal.CandidateMetricDefinition.Metric.Name != "AI 推断的已支付销售额" {
		t.Fatalf("generated candidate was lost: %#v", result.Proposal)
	}
	if len(result.Proposal.ClarificationQuestions) != 1 || len(result.Proposal.Assumptions) != 1 || len(result.Proposal.Warnings) != 1 {
		t.Fatalf("review notes were lost: %#v", result.Proposal)
	}
}

func TestProposeRejectsDatasetModificationOnAggregatedDataset(t *testing.T) {
	retrieval := validRetrievalContext()
	retrieval.Datasets[0].Aggregated = true
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需增加退款状态过滤",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		RetrievalEvidence: []RetrievalEvidence{datasetEvidence()}, DatasetInstruction: "在订单明细数据集中增加退款状态字段与过滤配置",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(&retrieverStub{value: retrieval}, &invokerStub{configured: true, content: proposalJSON(t, proposal)})
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrInvalidOutput", err)
	}
}

func TestProposeRejectsDatasetModificationWithoutManagePermission(t *testing.T) {
	retrieval := validRetrievalContext()
	retrieval.Datasets[0].Manageable = false
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "需增加退款状态过滤",
		TargetDatasetID: testDatasetID, TargetDatasetVersionID: testDatasetVersionID,
		RetrievalEvidence: []RetrievalEvidence{datasetEvidence()}, DatasetInstruction: "在订单明细数据集中增加退款状态字段与过滤配置",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(&retrieverStub{value: retrieval}, &invokerStub{configured: true, content: proposalJSON(t, proposal)})
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("error = %v, want ErrInvalidOutput", err)
	}
}

func TestProposeAllowsModificationOfAuthorizedDraftDataset(t *testing.T) {
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyModifyDataset, Summary: "继续改造客户订单草稿",
		TargetDatasetID: testServiceDraftDatasetID, TargetDatasetVersionID: testServiceDraftVersionID,
		RetrievalEvidence:      []RetrievalEvidence{draftDatasetEvidence(), draftFieldEvidence("draft_customer_id")},
		DatasetInstruction:     "在现有草稿中关联客户区域，并保留销售额与月份所需字段",
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(
		&retrieverStub{value: retrievalWithModifiableDraft()},
		&invokerStub{configured: true, content: proposalJSON(t, proposal)},
	)
	result, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest())
	if err != nil {
		t.Fatalf("Propose() draft modification error = %v", err)
	}
	if result.Proposal.Strategy != StrategyModifyDataset || result.Proposal.TargetDatasetVersionID != testServiceDraftVersionID {
		t.Fatalf("unexpected draft modification proposal: %#v", result.Proposal)
	}
}

func TestProposeRejectsCreateOnDraftDatasetAndDraftFieldExpression(t *testing.T) {
	t.Run("draft dataset cannot be a create target", func(t *testing.T) {
		definition := validDefinition()
		definition.DatasetID = testServiceDraftDatasetID
		definition.DatasetVersionID = testServiceDraftVersionID
		definition.Expression.FieldID = "draft_amount"
		definition.TimeFieldID = ""
		definition.TimeGrain = "NONE"
		definition.AllowedDimensions = []metric.Dimension{}
		proposal := validCreateProposal()
		proposal.TargetDatasetID = testServiceDraftDatasetID
		proposal.TargetDatasetVersionID = testServiceDraftVersionID
		proposal.RetrievalEvidence = []RetrievalEvidence{draftDatasetEvidence(), draftFieldEvidence("draft_amount")}
		proposal.CandidateMetricDefinition = &definition
		service := NewService(
			&retrieverStub{value: retrievalWithModifiableDraft()},
			&invokerStub{configured: true, content: proposalJSON(t, proposal)},
		)
		if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("error = %v, want ErrInvalidOutput", err)
		}
	})

	t.Run("draft field cannot enter a published candidate expression", func(t *testing.T) {
		proposal := validCreateProposal()
		proposal.CandidateMetricDefinition.Expression.FieldID = "draft_amount"
		proposal.RetrievalEvidence = append(proposal.RetrievalEvidence, draftFieldEvidence("draft_amount"))
		service := NewService(
			&retrieverStub{value: retrievalWithModifiableDraft()},
			&invokerStub{configured: true, content: proposalJSON(t, proposal)},
		)
		if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("error = %v, want ErrInvalidOutput", err)
		}
	})
}

func TestNormalizeRetrievalContextEnforcesDraftBoundary(t *testing.T) {
	t.Run("accepts separately authorized manageable draft", func(t *testing.T) {
		got, err := normalizeRetrievalContext(retrievalWithModifiableDraft())
		if err != nil {
			t.Fatalf("normalizeRetrievalContext() error = %v", err)
		}
		if len(got.ModifiableDraftDatasets) != 1 || got.ModifiableDraftDatasets[0].Status != "DRAFT" || !got.ModifiableDraftDatasets[0].Manageable {
			t.Fatalf("draft context was not preserved: %#v", got.ModifiableDraftDatasets)
		}
	})

	tests := map[string]func(*RetrievalContext){
		"draft must be manageable": func(value *RetrievalContext) {
			value.ModifiableDraftDatasets[0].Manageable = false
		},
		"draft collection only accepts draft status": func(value *RetrievalContext) {
			value.ModifiableDraftDatasets[0].Status = "PUBLISHED"
		},
		"draft field must belong to draft collection": func(value *RetrievalContext) {
			value.ModifiableDraftFields[0].DatasetID = testDatasetID
			value.ModifiableDraftFields[0].DatasetVersionID = testDatasetVersionID
		},
		"dataset cannot repeat across collections": func(value *RetrievalContext) {
			value.ModifiableDraftDatasets[0].ID = testDatasetID
			value.ModifiableDraftDatasets[0].VersionID = testDatasetVersionID
		},
		"atomic fact source must be an authorized published field": func(value *RetrievalContext) {
			value.AtomicFacts[0].SourceFieldIDs = []string{"field_secret"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := retrievalWithModifiableDraft()
			mutate(&value)
			if _, err := normalizeRetrievalContext(value); !errors.Is(err, ErrInvalidRetrievalContext) {
				t.Fatalf("error = %v, want ErrInvalidRetrievalContext", err)
			}
		})
	}

	t.Run("published and draft collections share the dataset budget", func(t *testing.T) {
		value := RetrievalContext{
			Datasets:                make([]AuthorizedDataset, maxDatasets),
			ModifiableDraftDatasets: make([]AuthorizedDataset, 1),
		}
		if _, err := normalizeRetrievalContext(value); !errors.Is(err, ErrInvalidRetrievalContext) {
			t.Fatalf("error = %v, want ErrInvalidRetrievalContext", err)
		}
	})
}

func TestProposeRejectsUnknownEvidenceAndAmbiguousJSON(t *testing.T) {
	proposal := MetricAuthoringProposal{
		SchemaVersion: SchemaVersion, Strategy: StrategyDataGap, Summary: "没有数据",
		RetrievalEvidence:      []RetrievalEvidence{{SourceType: "FIELD", SourceID: "secret", DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID, Reason: "不存在"}},
		ClarificationQuestions: []string{}, Assumptions: []string{}, Warnings: []string{},
	}
	service := NewService(&retrieverStub{value: validRetrievalContext()}, &invokerStub{configured: true, content: proposalJSON(t, proposal)})
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("unknown evidence error = %v", err)
	}

	raw := append(proposalJSON(t, validCreateProposal()), []byte(`{}`)...)
	service = NewService(&retrieverStub{value: validRetrievalContext()}, &invokerStub{configured: true, content: raw})
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("trailing JSON error = %v", err)
	}

	duplicate := json.RawMessage(`{"schemaVersion":"1.0","strategy":"DATA_GAP","strategy":"NEEDS_CLARIFICATION","summary":"没有数据","targetDatasetId":"","targetDatasetVersionId":"","reuseMetricVersionId":"","retrievalEvidence":[],"candidateMetricDefinition":null,"datasetInstruction":"","clarificationQuestions":[],"assumptions":[],"warnings":[]}`)
	service = NewService(&retrieverStub{value: validRetrievalContext()}, &invokerStub{configured: true, content: duplicate})
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("duplicate JSON error = %v", err)
	}
}

func TestProposeFailsClosedOnInvalidRequestContextAndProvider(t *testing.T) {
	configured := &invokerStub{configured: true, content: proposalJSON(t, validCreateProposal())}
	service := NewService(&retrieverStub{value: validRetrievalContext()}, configured)
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", AuthoringRequest{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid request error = %v", err)
	}
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", AuthoringRequest{
		Requirement: "销售额", Name: "旧名称",
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("mixed request error = %v, want ErrInvalidRequest", err)
	}

	badContext := validRetrievalContext()
	badContext.Datasets[0].DSLHash = "changed"
	service = NewService(&retrieverStub{value: badContext}, configured)
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrInvalidRetrievalContext) {
		t.Fatalf("invalid context error = %v", err)
	}

	service = NewService(&retrieverStub{value: validRetrievalContext()}, &invokerStub{configured: false})
	if _, err := service.Propose(context.Background(), "tenant-1", "actor-1", validRequest()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("unconfigured provider error = %v", err)
	}
}
