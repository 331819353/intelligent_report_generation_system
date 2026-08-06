package validator

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/testfixture"
	"intelligent-report-generation-system/internal/platform/database"
)

type validatorContractStore struct{ snapshot compiler.ContractSnapshot }

func (store validatorContractStore) LoadContractSnapshot(
	context.Context,
	compiler.ContractLookup,
) (compiler.ContractSnapshot, error) {
	return store.snapshot, nil
}

type recordingExplainer struct {
	requests []ExplainRequest
	raw      json.RawMessage
	err      error
}

func (explainer *recordingExplainer) Explain(
	_ context.Context,
	request ExplainRequest,
) (json.RawMessage, error) {
	explainer.requests = append(explainer.requests, request)
	return append(json.RawMessage(nil), explainer.raw...), explainer.err
}

func TestValidatorAcceptsLivePlanAndPersistsOnlyExplainSummary(t *testing.T) {
	artifact, ctx := liveQueryArtifact(t)
	explainer := &recordingExplainer{raw: safeExplainJSON()}
	limits := DefaultLimits()
	limits.StatementTimeoutMS = 10000
	validator, err := NewValidator(explainer, limits)
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validator.Validate(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := validated.Validate(); err != nil {
		t.Fatalf("validation artifact failed replay: %v", err)
	}
	if len(explainer.requests) != 1 || len(validated.Plans) != 1 ||
		validated.Plans[0].Explain.RootNodeType != "Limit" ||
		validated.Plans[0].Explain.SequentialScans != 1 ||
		validated.Plans[0].Explain.PlanNodes != 3 {
		t.Fatalf("unexpected validation result: %#v", validated)
	}
	request := explainer.requests[0]
	if !strings.HasPrefix(request.SQL, "SELECT ") || !reflect.DeepEqual(request.Args, []any{500}) ||
		request.StatementTimeoutMS != 10000 || request.LockTimeoutMS != 1000 || request.Timezone != "UTC" ||
		request.QueryPlanHash != artifact.Plans[0].PlanHash ||
		request.CompiledPlanHash != artifact.Plans[0].CompiledPlanHash {
		t.Fatalf("unexpected EXPLAIN request: %#v", request)
	}
	raw, err := json.Marshal(validated)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, request.SQL) || strings.Contains(serialized, `"args"`) ||
		strings.Contains(serialized, "dws_sales_orders") || strings.Contains(serialized, "net_sales") {
		t.Fatalf("validation artifact leaked SQL/args/physical identifiers: %s", serialized)
	}
	var replayed ValidationArtifact
	if err := askdata.DecodeStrictJSON(raw, &replayed); err != nil || replayed.Validate() != nil {
		t.Fatalf("serialized validation artifact is not replayable: %v", err)
	}

	var withoutLive compiler.QueryArtifact
	queryRaw, _ := json.Marshal(artifact)
	if err := askdata.DecodeStrictJSON(queryRaw, &withoutLive); err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate(ctx, withoutLive); !errors.Is(err, ErrPlanNotExecutable) {
		t.Fatalf("serialized plan without live parameters error = %v", err)
	}
}

func TestValidatorRejectsHighCostBeforeExecution(t *testing.T) {
	artifact, ctx := liveQueryArtifact(t)
	explainer := &recordingExplainer{raw: json.RawMessage(`[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":10000001}}]`)}
	validator, err := NewValidator(explainer, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = validator.Validate(ctx, artifact)
	assertRejectionCode(t, err, CodePlanCostExceeded)
	if len(explainer.requests) != 1 {
		t.Fatalf("EXPLAIN call count = %d", len(explainer.requests))
	}
}

func TestStaticSQLGateAllowsSelectAndCTEOnly(t *testing.T) {
	source := compiler.PhysicalSource{
		PublishedSchema: "warehouse_published", PublishedName: "dws_sales_orders",
	}
	valid := []string{
		`SELECT "secure_base"."net_sales" FROM (SELECT SUM("s"."net_sales") AS "net_sales" FROM "warehouse_published"."dws_sales_orders" "s") "secure_base" LIMIT $1`,
		`WITH "base" AS (SELECT "s"."net_sales" FROM "warehouse_published"."dws_sales_orders" "s") SELECT SUM("base"."net_sales") FROM "base"`,
		`SELECT CAST(DATE_TRUNC('month', "s"."order_date") AS DATE), SUM(CASE WHEN "s"."status" = $1 THEN CAST("s"."net_sales" AS NUMERIC(38,10)) ELSE $2 END) FROM "warehouse_published"."dws_sales_orders" "s" GROUP BY DATE_TRUNC('month', "s"."order_date")`,
	}
	for _, sql := range valid {
		if err := validateSQL(sql, source); err != nil {
			t.Fatalf("valid SELECT rejected: %v\n%s", err, sql)
		}
	}
	cases := []struct {
		sql  string
		code string
	}{
		{`DELETE FROM "warehouse_published"."dws_sales_orders"`, CodeStatementNotSelect},
		{`WITH changed AS (DELETE FROM "warehouse_published"."dws_sales_orders" RETURNING *) SELECT * FROM changed`, CodeForbiddenSQLToken},
		{`SELECT pg_sleep(1) FROM "warehouse_published"."dws_sales_orders"`, CodeUnsupportedFunction},
		{`SELECT * FROM "other"."secret"`, CodeUntrustedRelation},
		{`SELECT * FROM "warehouse_published"."dws_sales_orders" FOR UPDATE`, CodeForbiddenSQLToken},
		{`EXPLAIN ANALYZE SELECT * FROM "warehouse_published"."dws_sales_orders"`, CodeStatementNotSelect},
		{`SELECT * FROM "warehouse_published"."dws_sales_orders"; DROP TABLE x`, CodeForbiddenSQLToken},
		{`SELECT * FROM "warehouse_published"."dws_sales_orders" -- hidden`, CodeForbiddenSQLToken},
		{`WITH RECURSIVE x AS (SELECT * FROM "warehouse_published"."dws_sales_orders") SELECT * FROM x`, CodeForbiddenSQLToken},
	}
	for _, test := range cases {
		err := validateSQL(test.sql, source)
		assertRejectionCode(t, err, test.code)
	}
}

func TestExplainRiskGatesRowsScansJoinsAndFanout(t *testing.T) {
	limits := DefaultLimits()
	cases := []struct {
		name string
		raw  string
		code string
	}{
		{
			name: "root rows", code: CodeRootRowsExceeded,
			raw: `[{"Plan":{"Node Type":"Limit","Plan Rows":501,"Total Cost":10}}]`,
		},
		{
			name: "sequential scan", code: CodeSequentialScanExceeded,
			raw: `[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":10,"Plans":[{"Node Type":"Seq Scan","Plan Rows":1000001,"Total Cost":9}]}}]`,
		},
		{
			name: "join rows", code: CodeJoinRowsExceeded,
			raw: `[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":10,"Plans":[{"Node Type":"Hash Join","Plan Rows":1000001,"Total Cost":9,"Plans":[{"Node Type":"Index Scan","Plan Rows":100,"Total Cost":2},{"Node Type":"Index Scan","Plan Rows":100,"Total Cost":2}]}]}}]`,
		},
		{
			name: "join fanout", code: CodeJoinFanoutExceeded,
			raw: `[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":10,"Plans":[{"Node Type":"Nested Loop","Plan Rows":1000,"Total Cost":9,"Plans":[{"Node Type":"Index Scan","Plan Rows":10,"Total Cost":2},{"Node Type":"Index Scan","Plan Rows":20,"Total Cost":2}]}]}}]`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := analyzeExplain(json.RawMessage(test.raw), limits, 500)
			assertRejectionCode(t, err, test.code)
		})
	}
}

func TestLimitsCannotDisableSafetyCeilings(t *testing.T) {
	limits := DefaultLimits()
	limits.StatementTimeoutMS = 25001
	if limits.Validate() == nil {
		t.Fatal("statement timeout above platform ceiling was accepted")
	}
	limits = DefaultLimits()
	limits.LockTimeoutMS = limits.StatementTimeoutMS + 1
	if limits.Validate() == nil {
		t.Fatal("lock timeout above statement timeout was accepted")
	}
	limits = DefaultLimits()
	limits.MaxRows = 10001
	if limits.Validate() == nil {
		t.Fatal("row limit above Semantic IR ceiling was accepted")
	}
}

func TestValidationArtifactReplayCannotBypassExplainLimits(t *testing.T) {
	artifact, ctx := liveQueryArtifact(t)
	validator, err := NewValidator(&recordingExplainer{raw: safeExplainJSON()}, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	validated, err := validator.Validate(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	validated.Plans[0].Explain.RootPlanRows = int64(validated.Plans[0].MaxRows + 1)
	validated.Plans[0].Explain.MaxNodeRows = validated.Plans[0].Explain.RootPlanRows
	validated.Plans[0].Explain.SummaryHash, err = explainSummaryHash(validated.Plans[0].Explain)
	if err != nil {
		t.Fatal(err)
	}
	validated.ValidationHash, err = validationArtifactHash(validated)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(validated.Validate(), ErrPlanNotExecutable) {
		t.Fatal("rehashed artifact bypassed the validated root-row ceiling")
	}
}

func assertRejectionCode(t *testing.T, err error, code string) {
	t.Helper()
	var rejection *Rejection
	if !errors.As(err, &rejection) || rejection.Code != code {
		t.Fatalf("rejection = %v, want %s", err, code)
	}
}

func liveQueryArtifact(t *testing.T) (compiler.QueryArtifact, context.Context) {
	return liveQueryArtifactForSource(t, "dws_sales_orders")
}

func liveQueryArtifactForSource(t *testing.T, publishedName string) (compiler.QueryArtifact, context.Context) {
	return liveQueryArtifactForSourceAndMetricFormula(
		t, publishedName,
		json.RawMessage(`{"measureVersionId":"measure-sales-v1","type":"MEASURE_REF"}`),
	)
}

func liveQueryArtifactForSourceAndMetricFormula(
	t *testing.T,
	publishedName string,
	metricFormula json.RawMessage,
) (compiler.QueryArtifact, context.Context) {
	t.Helper()
	request, err := testfixture.SemanticMetricBuildRequest()
	if err != nil {
		t.Fatal(err)
	}
	buildArtifact, err := ir.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	field := compiler.FieldContract{
		FieldID: "net_sales", Code: "net_sales", Role: "MEASURE", CanonicalType: "DECIMAL",
		Visible: true,
	}
	field.ContractHash = validatorFieldHash(t, field)
	snapshot := compiler.ContractSnapshot{
		Release: buildArtifact.Scope.Release, ReleaseStatus: "READY", ReleaseObjectCount: 3,
		Model: compiler.ModelContract{
			ModelVersionID: "model-sales-v1", ContentHash: validatorHash("model-sales-v1"),
			DatasetSchemaHash: validatorHash("dataset-schema-v1"),
			GrainContract:     json.RawMessage(`{"keys":["order_id"],"type":"ENTITY"}`),
			Fields:            []compiler.FieldContract{field},
			Materialization: compiler.MaterializationContract{
				MaterializationID: "materialization-sales-v1", DatasetID: "dataset-sales",
				DatasetVersionID: "dataset-sales-v1", Layer: "DWS", Status: "ACTIVE",
				PublishedSchema: "warehouse_published", PublishedName: publishedName,
				SchemaHash: validatorHash("dataset-schema-v1"), SnapshotHash: validatorHash("snapshot-v1"), RowCount: 10,
			},
		},
		Metrics: []compiler.MetricContract{{
			MetricVersionID: "metric-sales-v1", ModelVersionID: "model-sales-v1",
			ContentHash:      validatorHash("metric-sales-v1"),
			FormulaAST:       append(json.RawMessage(nil), metricFormula...),
			DefaultFilterAST: json.RawMessage(`{"type":"TRUE"}`), TimeGrain: "NONE",
			Additivity: registry.Additive, NullPolicy: "PRESERVE",
			Measures: []compiler.MeasureContract{{
				MeasureID: "measure-sales", MeasureVersionID: "measure-sales-v1",
				ModelVersionID: "model-sales-v1", ContentHash: validatorHash("measure-sales-v1"),
				FormulaAST:  json.RawMessage(`{"fieldId":"net_sales","type":"FIELD_REF"}`),
				Aggregation: registry.AggregationSum, Additivity: registry.Additive,
				DataType: registry.NumericDecimal, Unit: "CNY",
			}},
		}},
		Dimensions: []compiler.DimensionContract{}, Members: []compiler.MemberContract{},
		Relationships: []compiler.RelationshipContract{},
	}
	resolver, err := compiler.NewResolver(validatorContractStore{snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	ctx := database.WithAccessContext(context.Background(), string(buildArtifact.Scope.ActorID), "sales")
	resolution, err := resolver.Resolve(ctx, compiler.ResolveRequest{BuildRequest: request, BuildArtifact: buildArtifact})
	if err != nil {
		t.Fatal(err)
	}
	queryArtifact, err := compiler.Adapt(compiler.AdaptRequest{
		ResolveRequest: compiler.ResolveRequest{BuildRequest: request, BuildArtifact: buildArtifact},
		Resolution:     resolution,
	})
	if err != nil {
		t.Fatal(err)
	}
	return queryArtifact, ctx
}

func validatorFieldHash(t *testing.T, field compiler.FieldContract) askdata.ContentHash {
	t.Helper()
	payload, err := registry.CanonicalValue(struct {
		FieldID       askdata.ID `json:"fieldId"`
		Code          string     `json:"code"`
		Role          string     `json:"role"`
		CanonicalType string     `json:"canonicalType"`
		SemanticType  string     `json:"semanticType,omitempty"`
		Nullable      bool       `json:"nullable"`
		Visible       bool       `json:"visible"`
	}{field.FieldID, field.Code, field.Role, field.CanonicalType, field.SemanticType, field.Nullable, field.Visible})
	if err != nil {
		t.Fatal(err)
	}
	return askdata.HashBytes(payload)
}

func safeExplainJSON() json.RawMessage {
	return json.RawMessage(`[{"Plan":{"Node Type":"Limit","Plan Rows":1,"Total Cost":10,"Plans":[{"Node Type":"Aggregate","Plan Rows":1,"Total Cost":9,"Plans":[{"Node Type":"Seq Scan","Plan Rows":10,"Total Cost":8}]}]}}]`)
}

func validatorHash(value string) askdata.ContentHash { return askdata.HashBytes([]byte(value)) }
