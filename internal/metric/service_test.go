package metric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"intelligent-report-generation-system/internal/access"
	"intelligent-report-generation-system/internal/dataset"
)

const (
	testActorID    = "66666666-6666-4666-8666-666666666666"
	testTenantID   = "77777777-7777-4777-8777-777777777777"
	testDataSource = "88888888-8888-4888-8888-888888888888"
	testTableID    = "99999999-9999-4999-8999-999999999999"
	secondTableID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

type fakeStore struct {
	record         Record
	datasetVersion dataset.VersionRecord
	versionsByID   map[string]VersionRecord
	version        VersionRecord
	replay         VersionRecord
	replayFound    bool
	replayErr      error
	created        Prepared
	updated        Prepared
	published      PublishPlan
	createCalls    int
	publishCalls   int
}

func (store *fakeStore) Create(_ context.Context, _, _ string, prepared Prepared) (Record, error) {
	store.createCalls++
	store.created = prepared
	return store.record, nil
}
func (store *fakeStore) Get(context.Context, string, string) (Record, error) {
	return store.record, nil
}
func (store *fakeStore) List(context.Context, string, int, int) ([]Summary, int, error) {
	return []Summary{}, 0, nil
}
func (store *fakeStore) Update(_ context.Context, _, _, _ string, _ UpdateInput, prepared Prepared) (Record, error) {
	store.updated = prepared
	return store.record, nil
}
func (store *fakeStore) GetDatasetVersion(context.Context, string, string, string) (dataset.VersionRecord, error) {
	return store.datasetVersion, nil
}
func (store *fakeStore) GetVersionByID(_ context.Context, _ string, id string) (VersionRecord, error) {
	version, exists := store.versionsByID[id]
	if !exists {
		return VersionRecord{}, ErrVersionNotFound
	}
	return version, nil
}
func (store *fakeStore) ReplayPublication(context.Context, string, string, string, string, string) (VersionRecord, bool, error) {
	return store.replay, store.replayFound, store.replayErr
}
func (store *fakeStore) Publish(_ context.Context, _, _, _ string, plan PublishPlan) (VersionRecord, error) {
	store.publishCalls++
	store.published = plan
	return store.version, nil
}
func (store *fakeStore) GetVersion(context.Context, string, string, string) (VersionRecord, error) {
	return store.version, nil
}
func (store *fakeStore) ListVersions(context.Context, string, string, int, int) ([]VersionSummary, int, error) {
	return []VersionSummary{}, 0, nil
}
func (store *fakeStore) GetVersionUsage(context.Context, string, string, string) (VersionUsage, error) {
	return VersionUsage{}, nil
}
func (store *fakeStore) TransitionVersion(context.Context, string, string, string, string, VersionTransitionInput) (VersionRecord, error) {
	return store.version, nil
}

type fakePreviewer struct {
	candidate  QueryCandidate
	input      dataset.PreviewInput
	validation bool
	result     dataset.PreviewResult
	err        error
	cancelled  bool
}

type fakePermissionChecker struct {
	allowed bool
	answers map[string]bool
	checks  []access.Check
}

func (checker *fakePermissionChecker) Allowed(_ context.Context, check access.Check) (bool, error) {
	checker.checks = append(checker.checks, check)
	if allowed, exists := checker.answers[check.ResourceType+":"+check.ObjectID]; exists {
		return allowed, nil
	}
	return checker.allowed, nil
}

func (previewer *fakePreviewer) PreviewMetric(_ context.Context, _, _ string, candidate QueryCandidate, input dataset.PreviewInput, validation bool) (dataset.PreviewResult, error) {
	previewer.candidate, previewer.input, previewer.validation = candidate, input, validation
	return previewer.result, previewer.err
}
func (previewer *fakePreviewer) Cancel(context.Context, string, string, string, string) error {
	previewer.cancelled = true
	return nil
}

func baseDatasetVersion(t *testing.T) dataset.VersionRecord {
	t.Helper()
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "sales_detail", Name: "销售明细", Type: "SINGLE_SOURCE"},
		Nodes: []dataset.Node{{
			ID: "sales", Type: "TABLE", DataSourceID: testDataSource, TableID: testTableID, Alias: "s",
			Projection: []string{"amount", "region", "sold_at", "order_id"}, SourceFilters: []dataset.SourceFilter{},
		}},
		Joins: []dataset.Join{},
		Fields: []dataset.Field{
			{ID: "field_amount", Code: "amount", Name: "金额", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "sales", Field: "amount"}, CanonicalType: "DECIMAL", Nullable: false},
			{ID: "field_region", Code: "region", Name: "地区", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "sales", Field: "region"}, CanonicalType: "STRING", Nullable: false},
			{ID: "field_time", Code: "sold_at", Name: "销售时间", Role: "TIME", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "sales", Field: "sold_at"}, CanonicalType: "DATETIME", Nullable: false},
			{ID: "field_order_id", Code: "order_id", Name: "订单编号", Role: "IDENTIFIER", SemanticType: "IDENTIFIER", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "sales", Field: "order_id"}, CanonicalType: "STRING", Nullable: false},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{Description: "每行一条销售明细", KeyFields: []string{"region"}},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "REALTIME", TimeoutMS: 5000, PreviewLimit: 200, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{Enabled: false},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare dataset fixture: %v", err)
	}
	return dataset.VersionRecord{
		ID: testDatasetVersionID, DatasetID: testDatasetID, VersionNo: 1, Status: "PUBLISHED",
		DSLVersion: dataset.DSLVersion, DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash,
		DSL: prepared.DSLJSON, LogicalPlan: prepared.LogicalPlanJSON,
	}
}

func TestServiceCreateAllowsDirectCountOnIdentifierButNotNumericAggregation(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), datasetVersion: baseDatasetVersion(t), versionsByID: map[string]VersionRecord{}}
	service := NewService(store)

	count := validDefinition()
	count.Metric.Code, count.Metric.Name = "order_count", "订单数"
	count.Expression = Expression{Type: "FIELD_REF", FieldID: "field_order_id"}
	count.Aggregation, count.Additivity = "COUNT_DISTINCT", "NON_ADDITIVE"
	count.NumberFormat, count.DecimalScale, count.Unit = "#,##0", 0, ""
	if _, err := service.Create(context.Background(), testTenantID, testActorID, CreateInput{Definition: definitionJSON(t, count)}); err != nil {
		t.Fatalf("create identifier count metric: %v", err)
	}

	sum := count
	sum.Metric.Code, sum.Aggregation, sum.Additivity = "order_id_sum", "SUM", "ADDITIVE"
	_, err := service.Create(context.Background(), testTenantID, testActorID, CreateInput{Definition: definitionJSON(t, sum)})
	var validation *ValidationError
	if !errors.As(err, &validation) || len(validation.Issues) == 0 || validation.Issues[0].Code != "METRIC_NUMERIC_FIELD_REQUIRED" {
		t.Fatalf("string identifier SUM must be rejected, error=%v", err)
	}
}

func baseRecord(t *testing.T) Record {
	t.Helper()
	definition := definitionJSON(t, validDefinition())
	prepared, err := Prepare(definition)
	if err != nil {
		t.Fatal(err)
	}
	return Record{
		ID: testMetricID, Code: "revenue", Name: "营业收入", Type: "ATOMIC", Status: "DRAFT", Version: 3,
		DraftVersionID: testDraftVersionID, DraftVersionNo: 1, DraftRecordVersion: 2,
		DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
		DefinitionHash: prepared.DefinitionHash, Definition: prepared.DefinitionJSON,
	}
}

func TestServiceCreateValidatesExactDatasetFields(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), datasetVersion: baseDatasetVersion(t), versionsByID: map[string]VersionRecord{}}
	service := NewService(store)
	record, err := service.Create(context.Background(), testTenantID, testActorID, CreateInput{Definition: definitionJSON(t, validDefinition())})
	if err != nil {
		t.Fatalf("create metric: %v", err)
	}
	if record.ID != testMetricID || store.createCalls != 1 || store.created.DefinitionHash == "" {
		t.Fatalf("unexpected create result: %#v calls=%d", record, store.createCalls)
	}

	invalidDefinition := validDefinition()
	invalidDefinition.Expression.FieldID = "field_secret"
	_, err = service.Create(context.Background(), testTenantID, testActorID, CreateInput{Definition: definitionJSON(t, invalidDefinition)})
	var validation *ValidationError
	if !errors.As(err, &validation) || store.createCalls != 1 {
		t.Fatalf("invalid field must fail before persistence: %v calls=%d", err, store.createCalls)
	}
}

func TestServiceCreateRequiresDatasetRead(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), datasetVersion: baseDatasetVersion(t), versionsByID: map[string]VersionRecord{}}
	checker := &fakePermissionChecker{allowed: false}
	service := NewService(store)
	service.SetPermissionChecker(checker)

	_, err := service.Create(context.Background(), testTenantID, testActorID, CreateInput{Definition: definitionJSON(t, validDefinition())})
	if !errors.Is(err, ErrForbidden) || store.createCalls != 0 || len(checker.checks) != 1 ||
		checker.checks[0].ResourceType != "DATASET" || checker.checks[0].ObjectID != testDatasetID {
		t.Fatalf("数据集读取权限必须在持久化前失败关闭，error=%v calls=%d checks=%#v", err, store.createCalls, checker.checks)
	}
}

func TestServiceCreateRequiresTransitiveMetricRead(t *testing.T) {
	baseMetricID := uuid.NewString()
	base := validDefinition()
	base.Metric.Code, base.Metric.Name = "dependency_base", "依赖基础收入"
	basePrepared, err := Prepare(definitionJSON(t, base))
	if err != nil {
		t.Fatal(err)
	}
	intermediateVersionID, intermediateMetricID := uuid.NewString(), uuid.NewString()
	intermediate := validDefinition()
	intermediate.Metric.Code, intermediate.Metric.Name = "dependency_intermediate", "依赖中间收入"
	intermediate.Metric.Type, intermediate.Aggregation, intermediate.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	intermediate.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
	intermediatePrepared, err := Prepare(definitionJSON(t, intermediate))
	if err != nil {
		t.Fatal(err)
	}
	root := validDefinition()
	root.Metric.Code, root.Metric.Name = "derived_revenue", "派生收入"
	root.Metric.Type, root.Aggregation, root.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	root.Expression = Expression{Type: "METRIC_REF", MetricVersionID: intermediateVersionID}
	rootPrepared, err := Prepare(definitionJSON(t, root))
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeStore{
		record: baseRecord(t), datasetVersion: baseDatasetVersion(t),
		versionsByID: map[string]VersionRecord{
			testMetricVersionID: {
				ID: testMetricVersionID, MetricID: baseMetricID, Status: "PUBLISHED",
				DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
				DefinitionHash: basePrepared.DefinitionHash, Definition: basePrepared.DefinitionJSON,
			},
			intermediateVersionID: {
				ID: intermediateVersionID, MetricID: intermediateMetricID, Status: "PUBLISHED",
				DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
				DefinitionHash: intermediatePrepared.DefinitionHash, Definition: intermediatePrepared.DefinitionJSON,
			},
		},
	}
	checker := &fakePermissionChecker{
		allowed: true, answers: map[string]bool{"METRIC:" + baseMetricID: false},
	}
	service := NewService(store)
	service.SetPermissionChecker(checker)

	_, err = service.Create(context.Background(), testTenantID, testActorID, CreateInput{Definition: rootPrepared.DefinitionJSON})
	if !errors.Is(err, ErrForbidden) || store.createCalls != 0 {
		t.Fatalf("无权读取的传递指标依赖必须在持久化前失败关闭，error=%v calls=%d checks=%#v", err, store.createCalls, checker.checks)
	}
}

func TestServicePreviewBuildsServerSideAggregatePlan(t *testing.T) {
	store := &fakeStore{record: baseRecord(t), datasetVersion: baseDatasetVersion(t), versionsByID: map[string]VersionRecord{}}
	previewer := &fakePreviewer{result: dataset.PreviewResult{Columns: []string{"region", "revenue"}, Rows: [][]any{{"华东", "123.45"}}, RowCount: 1}}
	service := NewService(store, previewer)
	result, err := service.Preview(context.Background(), testTenantID, testActorID, testMetricID, PreviewInput{DimensionFieldIDs: []string{"field_region"}, MaxRows: 20})
	if err != nil {
		t.Fatalf("preview metric: %v", err)
	}
	if result.RowCount != 1 || previewer.validation || previewer.candidate.DatasetVersionID != testDatasetVersionID {
		t.Fatalf("unexpected preview call: %#v", previewer)
	}
	document, err := dataset.DecodeAndNormalize(previewer.candidate.DSL)
	if err != nil {
		t.Fatal(err)
	}
	if len(document.GroupBy) != 1 || document.GroupBy[0] != "field_region" || len(document.Fields) != 2 || document.Fields[1].Expression.Type != "AGGREGATE" {
		t.Fatalf("metric plan did not aggregate at source: %#v", document)
	}
}

func TestServicePreviewVersionMapsUnavailableDependencyToVersionConflict(t *testing.T) {
	definition := validDefinition()
	definition.Metric.Code, definition.Metric.Name = "published_derived", "已发布派生指标"
	definition.Metric.Type, definition.Aggregation, definition.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	definition.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
	prepared, err := Prepare(definitionJSON(t, definition))
	if err != nil {
		t.Fatal(err)
	}
	versionID := uuid.NewString()
	store := &fakeStore{
		datasetVersion: baseDatasetVersion(t), versionsByID: map[string]VersionRecord{},
		version: VersionRecord{
			ID: versionID, MetricID: testMetricID, Status: "PUBLISHED",
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			DefinitionHash: prepared.DefinitionHash, Definition: prepared.DefinitionJSON,
		},
	}
	previewer := &fakePreviewer{}
	service := NewService(store, previewer)

	_, err = service.PreviewVersion(context.Background(), testTenantID, testActorID, testMetricID, versionID, PreviewInput{})
	if !errors.Is(err, ErrVersionUnavailable) || previewer.candidate.MetricID != "" {
		t.Fatalf("已发布版本依赖失效必须返回版本不可用且不得执行试算，error=%v candidate=%#v", err, previewer.candidate)
	}
}

func TestServicePublishReplaysBeforePreview(t *testing.T) {
	record := baseRecord(t)
	replayed := VersionRecord{
		ID: testMetricVersionID, MetricID: testMetricID, Status: "PUBLISHED",
		DatasetID: record.DatasetID, DatasetVersionID: record.DatasetVersionID,
		DefinitionHash: record.DefinitionHash, Definition: record.Definition,
	}
	store := &fakeStore{record: record, replay: replayed, replayFound: true, versionsByID: map[string]VersionRecord{}}
	service := NewService(store)
	input := PublishInput{
		DraftVersionID: testDraftVersionID, ExpectedVersion: 3, ExpectedDraftRecordVersion: 2,
		ExpectedDefinitionHash: store.record.DefinitionHash, ValidationParameters: map[string]any{},
	}
	published, err := service.Publish(context.Background(), testTenantID, testActorID, testMetricID, "publish-retry", input)
	if err != nil || published.ID != testMetricVersionID || store.publishCalls != 0 {
		t.Fatalf("idempotent replay failed: %#v err=%v calls=%d", published, err, store.publishCalls)
	}
}

func TestServicePublishReplayRechecksDependencyRead(t *testing.T) {
	dependencyMetricID := uuid.NewString()
	dependency := validDefinition()
	dependency.Metric.Code, dependency.Metric.Name = "replay_dependency", "重放依赖指标"
	dependencyPrepared, err := Prepare(definitionJSON(t, dependency))
	if err != nil {
		t.Fatal(err)
	}
	replayedDefinition := validDefinition()
	replayedDefinition.Metric.Code, replayedDefinition.Metric.Name = "replayed_metric", "重放派生指标"
	replayedDefinition.Metric.Type, replayedDefinition.Aggregation, replayedDefinition.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	replayedDefinition.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
	replayedPrepared, err := Prepare(definitionJSON(t, replayedDefinition))
	if err != nil {
		t.Fatal(err)
	}
	record := baseRecord(t)
	store := &fakeStore{
		record: record, replayFound: true,
		replay: VersionRecord{
			ID: uuid.NewString(), MetricID: testMetricID, Status: "PUBLISHED",
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			DefinitionHash: replayedPrepared.DefinitionHash, Definition: replayedPrepared.DefinitionJSON,
		},
		versionsByID: map[string]VersionRecord{testMetricVersionID: {
			ID: testMetricVersionID, MetricID: dependencyMetricID, Status: "DEPRECATED",
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			DefinitionHash: dependencyPrepared.DefinitionHash, Definition: dependencyPrepared.DefinitionJSON,
		}},
	}
	checker := &fakePermissionChecker{
		allowed: true, answers: map[string]bool{"METRIC:" + dependencyMetricID: false},
	}
	service := NewService(store)
	service.SetPermissionChecker(checker)

	_, err = service.Publish(context.Background(), testTenantID, testActorID, testMetricID, "publish-retry-revoked", PublishInput{
		DraftVersionID: testDraftVersionID, ExpectedVersion: record.Version,
		ExpectedDraftRecordVersion: record.DraftRecordVersion, ExpectedDefinitionHash: record.DefinitionHash,
		ValidationParameters: map[string]any{},
	})
	if !errors.Is(err, ErrForbidden) || store.publishCalls != 0 {
		t.Fatalf("幂等重放必须复核已撤销的依赖读取权限，error=%v calls=%d", err, store.publishCalls)
	}
}

func TestServicePublishRejectsRuntimeFanoutWarning(t *testing.T) {
	store := &fakeStore{
		record: baseRecord(t), datasetVersion: baseDatasetVersion(t), versionsByID: map[string]VersionRecord{},
		version: VersionRecord{ID: testMetricVersionID, MetricID: testMetricID, Status: "PUBLISHED"},
	}
	previewer := &fakePreviewer{result: dataset.PreviewResult{Warnings: []dataset.PreviewWarning{{Code: "JOIN_FANOUT_RISK"}}}}
	service := NewService(store, previewer)
	_, err := service.Publish(context.Background(), testTenantID, testActorID, testMetricID, "publish-fanout", PublishInput{
		DraftVersionID: testDraftVersionID, ExpectedVersion: 3, ExpectedDraftRecordVersion: 2,
		ExpectedDefinitionHash: store.record.DefinitionHash, ValidationParameters: map[string]any{},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || store.publishCalls != 0 || !previewer.validation {
		t.Fatalf("fanout warning must block publication: %v calls=%d validation=%v", err, store.publishCalls, previewer.validation)
	}
}

func TestServiceRejectsMetricReferenceCycle(t *testing.T) {
	root := validDefinition()
	root.Metric.Type, root.Aggregation, root.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	root.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
	rootPrepared, err := Prepare(definitionJSON(t, root))
	if err != nil {
		t.Fatal(err)
	}
	dependency := root
	dependency.Metric.Code, dependency.Metric.Name = "profit", "利润"
	dependency.Expression = Expression{Type: "METRIC_REF", MetricVersionID: testMetricVersionID}
	dependencyPrepared, err := Prepare(definitionJSON(t, dependency))
	if err != nil {
		t.Fatal(err)
	}
	record := baseRecord(t)
	record.Code, record.Definition, record.DefinitionHash = root.Metric.Code, rootPrepared.DefinitionJSON, rootPrepared.DefinitionHash
	store := &fakeStore{
		record: record, datasetVersion: baseDatasetVersion(t),
		versionsByID: map[string]VersionRecord{testMetricVersionID: {
			ID: testMetricVersionID, MetricID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", Status: "PUBLISHED",
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			DefinitionHash: dependencyPrepared.DefinitionHash, Definition: dependencyPrepared.DefinitionJSON,
		}},
	}
	service := NewService(store)
	_, err = service.ValidateCurrent(context.Background(), testTenantID, testActorID, testMetricID)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("expected dependency cycle error, got %v", err)
	}
}

func TestServiceRejectsExponentiallyExpandedDependencies(t *testing.T) {
	versions := map[string]VersionRecord{}
	base := validDefinition()
	base.Metric.Code, base.Metric.Name = "expanded_base", "展开预算基础指标"
	basePrepared, err := Prepare(definitionJSON(t, base))
	if err != nil {
		t.Fatal(err)
	}
	previousVersionID := uuid.NewString()
	versions[previousVersionID] = VersionRecord{
		ID: previousVersionID, MetricID: uuid.NewString(), Status: "PUBLISHED",
		DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
		DefinitionHash: basePrepared.DefinitionHash, Definition: basePrepared.DefinitionJSON,
	}

	// 每层定义自身只有三个节点，但重复引用会让最终执行表达式指数增长。
	for index := 0; index < 12; index++ {
		definition := validDefinition()
		definition.Metric.Code = fmt.Sprintf("expanded_%d", index)
		definition.Metric.Name = fmt.Sprintf("展开预算派生指标 %d", index)
		definition.Metric.Type, definition.Aggregation, definition.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
		definition.Expression = Expression{Type: "ADD", Arguments: []Expression{
			{Type: "METRIC_REF", MetricVersionID: previousVersionID},
			{Type: "METRIC_REF", MetricVersionID: previousVersionID},
		}}
		prepared, err := Prepare(definitionJSON(t, definition))
		if err != nil {
			t.Fatal(err)
		}
		versionID := uuid.NewString()
		versions[versionID] = VersionRecord{
			ID: versionID, MetricID: uuid.NewString(), Status: "PUBLISHED",
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			DefinitionHash: prepared.DefinitionHash, Definition: prepared.DefinitionJSON,
		}
		previousVersionID = versionID
	}

	root := validDefinition()
	root.Metric.Code, root.Metric.Name = "expanded_root", "展开预算根指标"
	root.Metric.Type, root.Aggregation, root.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	root.Expression = Expression{Type: "ADD", Arguments: []Expression{
		{Type: "METRIC_REF", MetricVersionID: previousVersionID},
		{Type: "METRIC_REF", MetricVersionID: previousVersionID},
	}}
	rootPrepared, err := Prepare(definitionJSON(t, root))
	if err != nil {
		t.Fatal(err)
	}
	record := baseRecord(t)
	record.Code, record.Type = root.Metric.Code, root.Metric.Type
	record.Definition, record.DefinitionHash = rootPrepared.DefinitionJSON, rootPrepared.DefinitionHash
	service := NewService(&fakeStore{
		record: record, datasetVersion: baseDatasetVersion(t), versionsByID: versions,
	})

	_, err = service.ValidateCurrent(context.Background(), testTenantID, testActorID, testMetricID)
	var validation *ValidationError
	if !errors.As(err, &validation) {
		t.Fatalf("指数展开必须在生成查询计划前失败，error=%v", err)
	}
	for _, issue := range validation.Issues {
		if issue.Code == "METRIC_EXPANDED_EXPRESSION_COMPLEXITY_EXCEEDED" {
			return
		}
	}
	t.Fatalf("缺少展开后总预算错误，issues=%#v", validation.Issues)
}

func TestMetricDependencyDepthIncludesPreviouslyVisitedSharedSubtree(t *testing.T) {
	versions := map[string]VersionRecord{}
	makeVersion := func(code string, expression Expression) (string, Prepared) {
		definition := validDefinition()
		definition.Metric.Code, definition.Metric.Name = code, code
		if expression.Type != "FIELD_REF" {
			definition.Metric.Type, definition.Aggregation, definition.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
		}
		definition.Expression = expression
		prepared, err := Prepare(definitionJSON(t, definition))
		if err != nil {
			t.Fatal(err)
		}
		versionID := uuid.NewString()
		versions[versionID] = VersionRecord{
			ID: versionID, MetricID: uuid.NewString(), Status: "PUBLISHED",
			DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
			DefinitionHash: prepared.DefinitionHash, Definition: prepared.DefinitionJSON,
		}
		return versionID, prepared
	}

	leafID, _ := makeVersion("shared_leaf", Expression{Type: "FIELD_REF", FieldID: "field_amount"})
	sharedID, _ := makeVersion("shared_parent", Expression{Type: "METRIC_REF", MetricVersionID: leafID})
	deepID := sharedID
	// 根到链首算第 1 层；63 个中间节点再接两层共享子树，最长路径为 65。
	for index := 62; index >= 0; index-- {
		deepID, _ = makeVersion(fmt.Sprintf("deep_%d", index), Expression{Type: "METRIC_REF", MetricVersionID: deepID})
	}
	root := validDefinition()
	root.Metric.Code, root.Metric.Name = "shared_depth_root", "共享深度根指标"
	root.Metric.Type, root.Aggregation, root.Additivity = "DERIVED", "NONE", "NON_ADDITIVE"
	// 先从浅路径加载共享子树，复现旧实现命中 state==2 后漏算子树深度的问题。
	root.Expression = Expression{Type: "ADD", Arguments: []Expression{
		{Type: "METRIC_REF", MetricVersionID: sharedID},
		{Type: "METRIC_REF", MetricVersionID: deepID},
	}}
	rootPrepared, err := Prepare(definitionJSON(t, root))
	if err != nil {
		t.Fatal(err)
	}
	record := baseRecord(t)
	record.Code, record.Type = root.Metric.Code, root.Metric.Type
	record.Definition, record.DefinitionHash = rootPrepared.DefinitionJSON, rootPrepared.DefinitionHash
	service := NewService(&fakeStore{record: record, datasetVersion: baseDatasetVersion(t), versionsByID: versions})

	_, err = service.ValidateCurrent(context.Background(), testTenantID, testActorID, testMetricID)
	var validation *ValidationError
	if !errors.As(err, &validation) || len(validation.Issues) != 1 ||
		validation.Issues[0].Code != "METRIC_DEPENDENCY_COMPLEXITY_EXCEEDED" {
		t.Fatalf("共享依赖子树也必须计入最长路径预算，error=%v", err)
	}
}

func TestDependencyDimensionsRejectTimeGrainMismatch(t *testing.T) {
	root := validDefinition()
	root.TimeFieldID, root.TimeGrain = "field_time", "MONTH"
	root.AllowedDimensions = append(root.AllowedDimensions, Dimension{
		FieldID: "field_time", Name: "销售时间", HierarchyFieldIDs: []string{}, SortDirection: "ASC",
	})
	dependency := root
	dependency.TimeFieldID, dependency.TimeGrain = "", "NONE"

	err := validateDependencyDimensions(root, map[string]Prepared{
		testMetricVersionID: {Definition: dependency},
	})
	var validation *ValidationError
	if !errors.As(err, &validation) || len(validation.Issues) != 1 ||
		validation.Issues[0].Code != "METRIC_REFERENCE_TIME_GRAIN_MISMATCH" {
		t.Fatalf("派生指标不得改写依赖版本时间口径，error=%v", err)
	}
}

func TestMetricJoinFanoutRiskIncludesTransitiveJoin(t *testing.T) {
	joins := []dataset.Join{
		{LeftNodeID: "orders", RightNodeID: "customers", Cardinality: "MANY_TO_ONE"},
		{LeftNodeID: "customers", RightNodeID: "contacts", Cardinality: "ONE_TO_MANY"},
	}
	index, risky := firstMetricJoinFanoutRisk(joins, map[string]bool{"orders": true})
	if !risky || index != 1 {
		t.Fatalf("多跳 Join 必须识别事实指标经客户节点到联系方式节点的扇出，index=%d risky=%v", index, risky)
	}

	joins[1].Cardinality = "MANY_TO_ONE"
	if index, risky := firstMetricJoinFanoutRisk(joins, map[string]bool{"orders": true}); risky {
		t.Fatalf("指标始终位于多侧时不应误报扇出，index=%d", index)
	}
	joins[1].Cardinality = "UNKNOWN"
	if index, risky := firstMetricJoinFanoutRisk(joins, map[string]bool{"orders": true}); !risky || index != 1 {
		t.Fatalf("未知基数必须对指标扇出保守失败，index=%d risky=%v", index, risky)
	}
}
