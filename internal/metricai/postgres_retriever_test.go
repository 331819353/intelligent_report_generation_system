package metricai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/metric"
)

const (
	testTenantID       = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	testActorID        = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	testDataSourceID   = "66666666-6666-4666-8666-666666666666"
	testTableID        = "77777777-7777-4777-8777-777777777777"
	testDraftDatasetID = "88888888-8888-4888-8888-888888888888"
	testDraftVersionID = "99999999-9999-4999-8999-999999999999"
)

type retrievalRowsStub struct {
	values [][]any
	index  int
	err    error
}

func newRetrievalRows(values ...[]any) *retrievalRowsStub {
	return &retrievalRowsStub{values: values, index: -1}
}

func (r *retrievalRowsStub) Next() bool {
	if r.index+1 >= len(r.values) {
		return false
	}
	r.index++
	return true
}

func (r *retrievalRowsStub) Scan(destinations ...any) error {
	if r.index < 0 || r.index >= len(r.values) || len(destinations) != len(r.values[r.index]) {
		return errors.New("invalid retrieval row scan")
	}
	for index, destination := range destinations {
		target := reflect.ValueOf(destination)
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("scan destination must be a pointer")
		}
		source := reflect.ValueOf(r.values[r.index][index])
		if !source.IsValid() || !source.Type().AssignableTo(target.Elem().Type()) {
			return fmt.Errorf("cannot assign %T to %T", r.values[r.index][index], destination)
		}
		target.Elem().Set(source)
	}
	return nil
}

func (r *retrievalRowsStub) Err() error { return r.err }
func (r *retrievalRowsStub) Close()     {}

type retrievalQueryerStub struct {
	metricRows, datasetRows, draftDatasetRows, atomicFactRows *retrievalRowsStub
	queries                                                   []string
	arguments                                                 [][]any
	validationErrors                                          map[string]error
	validations                                               []string
}

func (q *retrievalQueryerStub) Query(_ context.Context, sql string, arguments ...any) (retrievalRows, error) {
	q.queries = append(q.queries, sql)
	q.arguments = append(q.arguments, append([]any(nil), arguments...))
	if sql == authorizedMetricsSQL {
		if q.metricRows == nil {
			return newRetrievalRows(), nil
		}
		return q.metricRows, nil
	}
	if sql == authorizedDatasetsSQL {
		if q.datasetRows == nil {
			return newRetrievalRows(), nil
		}
		return q.datasetRows, nil
	}
	if sql == authorizedDraftDatasetsSQL {
		if q.draftDatasetRows == nil {
			return newRetrievalRows(), nil
		}
		return q.draftDatasetRows, nil
	}
	if sql == authorizedAtomicFactsSQL {
		if q.atomicFactRows == nil {
			return newRetrievalRows(), nil
		}
		return q.atomicFactRows, nil
	}
	return nil, errors.New("unexpected retrieval query")
}

func (q *retrievalQueryerStub) ValidateDatasetVersion(_ context.Context, datasetID, versionID string) error {
	key := datasetKey(datasetID, versionID)
	q.validations = append(q.validations, key)
	return q.validationErrors[key]
}

func publishedRetrieverDataset(t *testing.T) dataset.Prepared {
	t.Helper()
	visible, hidden := true, false
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset:    dataset.Descriptor{Code: "orders_detail", Name: "订单明细", Description: "已支付订单明细", Type: "SINGLE_SOURCE"},
		Nodes: []dataset.Node{{
			ID: "orders", Type: "TABLE", DataSourceID: testDataSourceID, TableID: testTableID, Alias: "o",
			Projection: []string{"amount", "region", "paid_at", "internal_margin"}, SourceFilters: []dataset.SourceFilter{},
		}},
		Joins:           []dataset.Join{},
		PreAggregations: []dataset.PreAggregation{},
		Fields: []dataset.Field{
			{ID: "field_amount", Code: "amount", Name: "订单金额", Description: "含税订单金额", Role: "ATTRIBUTE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "amount"}, CanonicalType: "DECIMAL", SemanticType: "AMOUNT", Visible: &visible},
			{ID: "field_region", Code: "region", Name: "地区", Role: "DIMENSION", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "region"}, CanonicalType: "STRING", Visible: &visible},
			{ID: "field_paid_at", Code: "paid_at", Name: "支付时间", Role: "TIME", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "paid_at"}, CanonicalType: "DATETIME", Visible: &visible},
			{ID: "field_internal_margin", Code: "internal_margin", Name: "内部毛利", Role: "MEASURE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: "internal_margin"}, CanonicalType: "DECIMAL", Visible: &hidden},
		},
		Filters: []dataset.Filter{}, GroupBy: []string{}, Having: []dataset.Filter{}, Sorts: []dataset.Sort{}, Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{Description: "每行一笔订单", KeyFields: []string{"region"}, TimeField: "paid_at", DefaultTimeGrain: "MONTH"},
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
	return prepared
}

func retrieverDatasetWithVisibleFields(t *testing.T, count int) dataset.Prepared {
	t.Helper()
	document := publishedRetrieverDataset(t).Document
	visible := true
	document.Fields = make([]dataset.Field, 0, count)
	document.Nodes[0].Projection = make([]string, 0, count)
	for index := 0; index < count; index++ {
		code := fmt.Sprintf("column_%03d", index)
		document.Nodes[0].Projection = append(document.Nodes[0].Projection, code)
		document.Fields = append(document.Fields, dataset.Field{
			ID: fmt.Sprintf("field_%03d", index), Code: code, Name: fmt.Sprintf("字段 %d", index),
			Role: "ATTRIBUTE", Expression: dataset.Expression{Type: "FIELD_REF", NodeID: "orders", Field: code},
			CanonicalType: "DECIMAL", Visible: &visible,
		})
	}
	document.OutputGrain = dataset.OutputGrain{Description: "每行一条明细", KeyFields: []string{"column_000"}}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := dataset.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare %d-field dataset fixture: %v", count, err)
	}
	return prepared
}

func publishedRetrieverMetric(t *testing.T) metric.Prepared {
	t.Helper()
	definition := validDefinition()
	definition.Metric.Code = "paid_sales"
	definition.Metric.Name = "已支付销售额"
	definition.Metric.Description = "已支付订单金额"
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := metric.Prepare(raw)
	if err != nil {
		t.Fatalf("prepare metric fixture: %v", err)
	}
	return prepared
}

func retrievalDatasetRow(prepared dataset.Prepared) []any {
	return []any{
		testDatasetID, testDatasetVersionID, 3, "当前订单草稿名称", "不应覆盖版本描述", "PUBLISHED",
		true, false, prepared.DSLHash, prepared.PlanHash, json.RawMessage(prepared.DSLJSON),
	}
}

func retrievalDraftDatasetRow(prepared dataset.Prepared) []any {
	return []any{
		testDraftDatasetID, testDraftVersionID, 1, "订单与客户草稿", "可由指标 AI 改造", "DRAFT",
		true, false, prepared.DSLHash, prepared.PlanHash, json.RawMessage(prepared.DSLJSON),
	}
}

func retrievalMetricRow(datasetPrepared dataset.Prepared, metricPrepared metric.Prepared) []any {
	return []any{
		testMetricID, testMetricVersionID, "PUBLISHED", metricPrepared.DefinitionHash, json.RawMessage(metricPrepared.DefinitionJSON),
		testDatasetID, testDatasetVersionID, 3, "当前订单草稿名称", "不应覆盖版本描述", "PUBLISHED",
		true, false, datasetPrepared.DSLHash, datasetPrepared.PlanHash, json.RawMessage(datasetPrepared.DSLJSON),
	}
}

func retrievalAtomicFactRow() []any {
	return []any{
		testDatasetID, testDatasetVersionID, []string{"field_amount"},
		"订单金额", "订单明细中的含税金额", "订单金额按行求和", "SUM",
		[]string{"地区"}, "MONTH", []string{"销售", "金额"}, 0.96,
	}
}

func TestPostgresRetrieverReturnsPermissionScopedMinimalExactContext(t *testing.T) {
	datasetPrepared := publishedRetrieverDataset(t)
	metricPrepared := publishedRetrieverMetric(t)
	queryer := &retrievalQueryerStub{
		metricRows:       newRetrievalRows(retrievalMetricRow(datasetPrepared, metricPrepared)),
		datasetRows:      newRetrievalRows(retrievalDatasetRow(datasetPrepared)),
		atomicFactRows:   newRetrievalRows(retrievalAtomicFactRow()),
		validationErrors: map[string]error{},
	}
	retriever := &PostgresRetriever{runTenant: func(_ context.Context, tenantID string, work func(retrievalQueryer) error) error {
		if tenantID != testTenantID {
			return errors.New("wrong tenant")
		}
		return work(queryer)
	}}

	result, err := retriever.Retrieve(context.Background(), testTenantID, testActorID, validRequest())
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.Datasets) != 1 || result.Datasets[0].VersionID != testDatasetVersionID || result.Datasets[0].Name != "订单明细" || !result.Datasets[0].Manageable {
		t.Fatalf("unexpected exact datasets: %#v", result.Datasets)
	}
	if len(result.Fields) != 3 {
		t.Fatalf("hidden fields were not removed: %#v", result.Fields)
	}
	for _, field := range result.Fields {
		if field.ID == "field_internal_margin" {
			t.Fatal("hidden field reached the authorized context")
		}
	}
	if len(result.ExistingMetrics) != 1 || result.ExistingMetrics[0].DefinitionHash != metricPrepared.DefinitionHash {
		t.Fatalf("unexpected exact metrics: %#v", result.ExistingMetrics)
	}
	if len(result.AtomicFacts) != 1 || result.AtomicFacts[0].Aggregation != "SUM" || result.AtomicFacts[0].SourceFieldIDs[0] != "field_amount" {
		t.Fatalf("unexpected internal atomic facts: %#v", result.AtomicFacts)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{testDataSourceID, testTableID, "internal_margin", "sourceFilters", "credentials"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("retrieval context leaked %q: %s", forbidden, payload)
		}
	}
	if len(result.ModifiableDraftDatasets) != 0 || len(result.ModifiableDraftFields) != 0 {
		t.Fatalf("unexpected draft context: %#v / %#v", result.ModifiableDraftDatasets, result.ModifiableDraftFields)
	}
	if len(queryer.validations) != 1 || len(queryer.queries) != 4 {
		t.Fatalf("validation/query calls = %#v / %d", queryer.validations, len(queryer.queries))
	}
	for _, arguments := range queryer.arguments {
		if arguments[0] != testTenantID || arguments[1] != testActorID {
			t.Fatalf("query identity = %#v", arguments[:2])
		}
		if arguments[2] != validRequest().Requirement || arguments[3] != validRequest().Requirement {
			t.Fatalf("retrieval did not use the single requirement: %#v", arguments[2:4])
		}
	}
}

func TestPostgresRetrieverMarksMappedDatasetFromOriginTableFlag(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	row := retrievalDatasetRow(prepared)
	row[7] = true
	queryer := &retrievalQueryerStub{
		metricRows: newRetrievalRows(), datasetRows: newRetrievalRows(row), validationErrors: map[string]error{},
	}
	retriever := &PostgresRetriever{runTenant: func(_ context.Context, _ string, work func(retrievalQueryer) error) error {
		return work(queryer)
	}}
	result, err := retriever.Retrieve(context.Background(), testTenantID, testActorID, validRequest())
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.Datasets) != 0 || len(result.MappedDatasets) != 1 || !result.MappedDatasets[0].Mapped {
		t.Fatalf("mapped origin flag was lost: %#v", result.Datasets)
	}
}

func TestPostgresRetrieverReturnsManageableDraftWithoutPublishedDependencyValidation(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	queryer := &retrievalQueryerStub{
		metricRows: newRetrievalRows(), datasetRows: newRetrievalRows(),
		draftDatasetRows: newRetrievalRows(retrievalDraftDatasetRow(prepared)),
		validationErrors: map[string]error{},
	}
	retriever := &PostgresRetriever{runTenant: func(_ context.Context, _ string, work func(retrievalQueryer) error) error {
		return work(queryer)
	}}

	result, err := retriever.Retrieve(context.Background(), testTenantID, testActorID, validRequest())
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.Datasets) != 0 || len(result.Fields) != 0 || len(result.ExistingMetrics) != 0 {
		t.Fatalf("draft was mixed into published context: %#v", result)
	}
	if len(result.ModifiableDraftDatasets) != 1 {
		t.Fatalf("modifiable draft datasets = %#v", result.ModifiableDraftDatasets)
	}
	draft := result.ModifiableDraftDatasets[0]
	if draft.ID != testDraftDatasetID || draft.VersionID != testDraftVersionID || draft.Status != "DRAFT" || !draft.Manageable {
		t.Fatalf("unexpected modifiable draft: %#v", draft)
	}
	if len(result.ModifiableDraftFields) != 3 {
		t.Fatalf("hidden draft fields were not removed: %#v", result.ModifiableDraftFields)
	}
	if len(queryer.validations) != 0 {
		t.Fatalf("draft incorrectly used published dependency validation: %#v", queryer.validations)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{testDataSourceID, testTableID, "internal_margin", "sourceFilters", "credentials"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("draft retrieval context leaked %q: %s", forbidden, payload)
		}
	}
}

func TestPostgresRetrieverDropsPublishedEnvelopeWithUnavailableDependencies(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	key := datasetKey(testDatasetID, testDatasetVersionID)
	queryer := &retrievalQueryerStub{
		metricRows: newRetrievalRows(), datasetRows: newRetrievalRows(retrievalDatasetRow(prepared)),
		validationErrors: map[string]error{key: dataset.ErrVersionUnavailable},
	}
	retriever := &PostgresRetriever{runTenant: func(_ context.Context, _ string, work func(retrievalQueryer) error) error { return work(queryer) }}
	result, err := retriever.Retrieve(context.Background(), testTenantID, testActorID, validRequest())
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if len(result.Datasets) != 0 || len(result.Fields) != 0 || len(result.ExistingMetrics) != 0 {
		t.Fatalf("unavailable dependency snapshot was returned: %#v", result)
	}
}

func TestPostgresRetrieverRejectsSnapshotHashDrift(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	row := retrievalDatasetRow(prepared)
	row[8] = strings.Repeat("f", 64)
	_, err := buildRetrievalContext(nil, []datasetSnapshot{{
		ID: row[0].(string), VersionID: row[1].(string), VersionNo: row[2].(int),
		SearchName: row[3].(string), SearchDescription: row[4].(string), Status: row[5].(string),
		Manageable: row[6].(bool), Mapped: row[7].(bool), DSLHash: row[8].(string), PlanHash: row[9].(string), DSL: row[10].(json.RawMessage),
	}}, nil)
	if !errors.Is(err, ErrInvalidRetrievalContext) {
		t.Fatalf("error = %v, want invalid retrieval context", err)
	}
}

func TestPostgresRetrieverSQLContainsEffectiveReadAndPolicyFences(t *testing.T) {
	for name, sql := range map[string]string{"datasets": authorizedDatasetsSQL, "metrics": authorizedMetricsSQL, "atomic facts": authorizedAtomicFactsSQL} {
		for _, expected := range []string{
			"actor_role.status='ACTIVE'", "permission.resource_type='DATASET'", "object_grant.object_type='DATASET'",
			"object_grant.action='READ'", "data_row_policies", "data_column_policies", "status='PUBLISHED'",
		} {
			if !strings.Contains(sql, expected) {
				t.Fatalf("%s query is missing %q", name, expected)
			}
		}
	}
	for _, expected := range []string{
		"platform.metric_candidates", "platform.metric_semantic_documents", "candidate.status IN ('READY','NEEDS_REVIEW')",
		"d.current_published_version_id=candidate.dataset_version_id", "version.status='PUBLISHED'",
	} {
		if !strings.Contains(authorizedAtomicFactsSQL, expected) {
			t.Fatalf("atomic-fact query is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"permission.resource_type='METRIC'", "object_grant.object_type='METRIC'", "m.current_published_version_id",
		"metric_version.dataset_version_id=dataset_version.id",
	} {
		if !strings.Contains(authorizedMetricsSQL, expected) {
			t.Fatalf("metric query is missing %q", expected)
		}
	}
	for name, sql := range map[string]string{"datasets": authorizedDatasetsSQL, "drafts": authorizedDraftDatasetsSQL, "metrics": authorizedMetricsSQL} {
		if !strings.Contains(sql, "(d.origin_table_id IS NOT NULL) AS mapped") {
			t.Fatalf("%s query does not derive the mapped dataset marker from origin_table_id", name)
		}
	}
	for _, expected := range []string{"permission.action='MANAGE'", "manage_grant.action='MANAGE'", "AS manageable"} {
		if !strings.Contains(authorizedDatasetsSQL, expected) {
			t.Fatalf("dataset manage capability is missing %q", expected)
		}
	}
	for _, expected := range []string{
		"d.current_draft_version_id", "version.status='DRAFT'", "d.deleted_at IS NULL", "d.status<>'DISABLED'",
		"actor_role.status='ACTIVE'", "object_grant.action='READ'", "manage_grant.action='MANAGE'",
		"data_row_policies", "data_column_policies",
	} {
		if !strings.Contains(authorizedDraftDatasetsSQL, expected) {
			t.Fatalf("draft dataset query is missing %q", expected)
		}
	}
	if strings.Count(authorizedDraftDatasetsSQL, datasetManageExpression) < 2 {
		t.Fatal("draft dataset query does not require MANAGE in addition to returning the capability")
	}
	for _, forbidden := range []string{"platform.data_sources", "platform.table_assets", "sample_rows", "credential"} {
		for name, sql := range map[string]string{"draft dataset": authorizedDraftDatasetsSQL, "atomic facts": authorizedAtomicFactsSQL} {
			if strings.Contains(strings.ToLower(sql), forbidden) {
				t.Fatalf("%s query reaches forbidden source data %q", name, forbidden)
			}
		}
	}
}

func TestPostgresRetrieverDropsAtomicFactThatReferencesHiddenField(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	row := retrievalDatasetRow(prepared)
	datasetRow := datasetSnapshot{
		ID: row[0].(string), VersionID: row[1].(string), VersionNo: row[2].(int),
		SearchName: row[3].(string), SearchDescription: row[4].(string), Status: row[5].(string),
		Manageable: row[6].(bool), Mapped: row[7].(bool), DSLHash: row[8].(string), PlanHash: row[9].(string), DSL: row[10].(json.RawMessage),
	}
	result, err := buildRetrievalContext(nil, []datasetSnapshot{datasetRow}, nil, []atomicFactSnapshot{{
		DatasetID: testDatasetID, DatasetVersionID: testDatasetVersionID,
		SourceFieldIDs: []string{"field_internal_margin"}, Name: "内部毛利", Caliber: "内部毛利求和",
		Aggregation: "SUM", Period: "NONE", Confidence: 0.9,
	}})
	if err != nil {
		t.Fatalf("buildRetrievalContext() error = %v", err)
	}
	if len(result.AtomicFacts) != 0 {
		t.Fatalf("hidden-field atomic fact reached prompt context: %#v", result.AtomicFacts)
	}
}

func TestPostgresRetrieverRejectsDraftSnapshotHashDrift(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	row := retrievalDraftDatasetRow(prepared)
	row[9] = strings.Repeat("f", 64)
	_, err := buildRetrievalContext(nil, nil, []datasetSnapshot{{
		ID: row[0].(string), VersionID: row[1].(string), VersionNo: row[2].(int),
		SearchName: row[3].(string), SearchDescription: row[4].(string), Status: row[5].(string),
		Manageable: row[6].(bool), Mapped: row[7].(bool), DSLHash: row[8].(string), PlanHash: row[9].(string), DSL: row[10].(json.RawMessage),
	}})
	if !errors.Is(err, ErrInvalidRetrievalContext) {
		t.Fatalf("error = %v, want invalid retrieval context", err)
	}
}

func TestPostgresRetrieverSharesDatasetBudgetAcrossPublishedAndDraftSnapshots(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	published := make([]datasetSnapshot, 0, 20)
	drafts := make([]datasetSnapshot, 0, 20)
	for index := 0; index < 20; index++ {
		published = append(published, datasetSnapshot{
			ID:        fmt.Sprintf("%08x-0000-4000-8000-%012x", index+1, index+1),
			VersionID: fmt.Sprintf("%08x-0000-4000-9000-%012x", index+101, index+101),
			VersionNo: 1, Status: "PUBLISHED", Manageable: true,
			DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
		})
		drafts = append(drafts, datasetSnapshot{
			ID:        fmt.Sprintf("%08x-0000-4000-8000-%012x", index+201, index+201),
			VersionID: fmt.Sprintf("%08x-0000-4000-9000-%012x", index+301, index+301),
			VersionNo: 1, Status: "DRAFT", Manageable: true,
			DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
		})
	}

	result, err := buildRetrievalContext(nil, published, drafts)
	if err != nil {
		t.Fatalf("buildRetrievalContext() error = %v", err)
	}
	if len(result.Datasets) != 20 || len(result.ModifiableDraftDatasets) != 20 {
		t.Fatalf("all ordinary datasets were not retained: published=%d draft=%d", len(result.Datasets), len(result.ModifiableDraftDatasets))
	}
	if len(result.Datasets)+len(result.ModifiableDraftDatasets) != 40 {
		t.Fatalf("combined dataset count = %d, want 40", len(result.Datasets)+len(result.ModifiableDraftDatasets))
	}
}

func TestPostgresRetrieverFailsInsteadOfSilentlyTruncatingDatasetSearch(t *testing.T) {
	prepared := publishedRetrieverDataset(t)
	published := make([]datasetSnapshot, 0, maxDatasets+1)
	for index := 0; index <= maxDatasets; index++ {
		published = append(published, datasetSnapshot{
			ID:        fmt.Sprintf("%08x-0000-4000-8000-%012x", index+1, index+1),
			VersionID: fmt.Sprintf("%08x-0000-4000-9000-%012x", index+201, index+201),
			VersionNo: 1, Status: "PUBLISHED", Manageable: true,
			DSLHash: prepared.DSLHash, PlanHash: prepared.PlanHash, DSL: prepared.DSLJSON,
		})
	}

	if _, err := buildRetrievalContext(nil, published, nil); !errors.Is(err, ErrInvalidRetrievalContext) {
		t.Fatalf("error = %v, want ErrInvalidRetrievalContext", err)
	}
}

func TestPostgresRetrieverSharesFieldBudgetAcrossPublishedAndDraftSnapshots(t *testing.T) {
	publishedPrepared := retrieverDatasetWithVisibleFields(t, 300)
	draftPrepared := retrieverDatasetWithVisibleFields(t, maxFields-300)
	published := datasetSnapshot{
		ID: testDatasetID, VersionID: testDatasetVersionID, VersionNo: 3, Status: "PUBLISHED", Manageable: true,
		DSLHash: publishedPrepared.DSLHash, PlanHash: publishedPrepared.PlanHash, DSL: publishedPrepared.DSLJSON,
	}
	draft := datasetSnapshot{
		ID: testDraftDatasetID, VersionID: testDraftVersionID, VersionNo: 1, Status: "DRAFT", Manageable: true,
		DSLHash: draftPrepared.DSLHash, PlanHash: draftPrepared.PlanHash, DSL: draftPrepared.DSLJSON,
	}
	result, err := buildRetrievalContext(nil, []datasetSnapshot{published}, []datasetSnapshot{draft})
	if err != nil {
		t.Fatalf("buildRetrievalContext() at field limit error = %v", err)
	}
	if len(result.Fields)+len(result.ModifiableDraftFields) != maxFields {
		t.Fatalf("combined field count = %d, want %d", len(result.Fields)+len(result.ModifiableDraftFields), maxFields)
	}

	overLimitPrepared := retrieverDatasetWithVisibleFields(t, maxFields-299)
	draft.DSLHash, draft.PlanHash, draft.DSL = overLimitPrepared.DSLHash, overLimitPrepared.PlanHash, overLimitPrepared.DSLJSON
	_, err = buildRetrievalContext(nil, []datasetSnapshot{published}, []datasetSnapshot{draft})
	if !errors.Is(err, ErrInvalidRetrievalContext) {
		t.Fatalf("over-limit error = %v, want invalid retrieval context", err)
	}
}

func TestPostgresRetrieverFailsClosedIfMetricRowDoesNotUseCurrentDatasetVersion(t *testing.T) {
	datasetPrepared := publishedRetrieverDataset(t)
	definition := validDefinition()
	definition.DatasetVersionID = "88888888-8888-4888-8888-888888888888"
	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	metricPrepared, err := metric.Prepare(raw)
	if err != nil {
		t.Fatal(err)
	}
	_, err = buildRetrievalContext([]metricSnapshot{{
		ID: testMetricID, VersionID: testMetricVersionID, Status: "PUBLISHED",
		DefinitionHash: metricPrepared.DefinitionHash, Definition: metricPrepared.DefinitionJSON,
		Dataset: datasetSnapshot{
			ID: testDatasetID, VersionID: testDatasetVersionID, VersionNo: 3, Status: "PUBLISHED", Manageable: true,
			DSLHash: datasetPrepared.DSLHash, PlanHash: datasetPrepared.PlanHash, DSL: datasetPrepared.DSLJSON,
		},
	}}, nil, nil)
	if !errors.Is(err, ErrInvalidRetrievalContext) {
		t.Fatalf("error = %v, want invalid retrieval context", err)
	}
}

func TestDatasetDocumentAggregatedUsesStrictMVPFence(t *testing.T) {
	if !datasetDocumentAggregated(dataset.Document{PreAggregations: []dataset.PreAggregation{{ID: "pre"}}}) {
		t.Fatal("pre-aggregation must be fenced")
	}
	if !datasetDocumentAggregated(dataset.Document{Fields: []dataset.Field{{Expression: dataset.Expression{
		Type: "ADD", Arguments: []dataset.Expression{{Type: "AGGREGATE"}},
	}}}}) {
		t.Fatal("nested aggregate expression must be fenced")
	}
	if datasetDocumentAggregated(dataset.Document{}) {
		t.Fatal("detail dataset was marked aggregated")
	}
}
