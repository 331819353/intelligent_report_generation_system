package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const evaluationSchemaVersion = "1.0"

type evaluationCase struct {
	ID                    string
	Class                 string
	Question              string
	ExpectedMetricName    string
	ExpectedIntent        string
	ExpectedDimensionCode string
	ExpectedFilterValue   string
	ExpectedTimeStart     string
	ExpectedTimeEnd       string
	ExpectedRows          [][]any
	RequiredLineage       []string
}

type metricCatalogResponse struct {
	Items []struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"items"`
}

type questionResponse struct {
	QuestionID string `json:"questionId"`
	State      string `json:"state"`
	Status     string `json:"status"`
	Route      string `json:"route"`
	Intent     struct {
		TaskType string `json:"taskType"`
		Metrics  []struct {
			Code string `json:"code"`
		} `json:"metrics"`
		Dimensions []struct {
			Code string `json:"code"`
		} `json:"dimensions"`
	} `json:"intent"`
	SemanticIR struct {
		SemanticVersion     string `json:"semanticVersion"`
		SemanticContentHash string `json:"semanticContentHash"`
		Metrics             []struct {
			Code string `json:"code"`
		} `json:"metrics"`
		Time *struct {
			Start        string `json:"start"`
			EndExclusive string `json:"endExclusive"`
		} `json:"time"`
		Filters []struct {
			DimensionCode string   `json:"dimensionCode"`
			ValueIDs      []string `json:"valueIds"`
		} `json:"filters"`
		EvidenceIDs []string `json:"evidenceIds"`
	} `json:"semanticIr"`
	Answer struct {
		ResultSets []struct {
			MetricCode string              `json:"metricCode"`
			Rows       [][]json.RawMessage `json:"rows"`
		} `json:"resultSets"`
	} `json:"answer"`
	Executions []struct {
		Evidence struct {
			QueryPlanHash        string `json:"queryPlanHash"`
			ResultHash           string `json:"resultHash"`
			ExecutionRevalidated bool   `json:"executionRevalidated"`
			Lineage              []struct {
				Label string `json:"label"`
			} `json:"lineage"`
		} `json:"evidence"`
	} `json:"executions"`
}

type caseResult struct {
	ID                 string   `json:"id"`
	Class              string   `json:"class"`
	Question           string   `json:"question"`
	Correct            bool     `json:"correct"`
	SecuritySafe       bool     `json:"securitySafe"`
	DurationMS         int64    `json:"durationMs"`
	QuestionID         string   `json:"questionId,omitempty"`
	ExpectedMetricName string   `json:"expectedMetricName"`
	ExpectedMetricCode string   `json:"expectedMetricCode,omitempty"`
	ActualMetricCode   string   `json:"actualMetricCode,omitempty"`
	Failures           []string `json:"failures,omitempty"`
}

type classSummary struct {
	Total             int     `json:"total"`
	Correct           int     `json:"correct"`
	Accuracy          float64 `json:"accuracy"`
	WilsonLower95     float64 `json:"wilsonLower95"`
	P50DurationMS     int64   `json:"p50DurationMs"`
	P95DurationMS     int64   `json:"p95DurationMs"`
	MaximumDurationMS int64   `json:"maximumDurationMs"`
}

type evaluationReport struct {
	SchemaVersion string `json:"schemaVersion"`
	Kind          string `json:"kind"`
	GeneratedAt   string `json:"generatedAt"`
	Environment   struct {
		BaseURL       string   `json:"baseUrl"`
		TenantCode    string   `json:"tenantCode"`
		Timezone      string   `json:"timezone"`
		SourceTables  []string `json:"sourceTables"`
		WarehouseFrom string   `json:"warehouseSnapshotFrom"`
		WarehouseTo   string   `json:"warehouseSnapshotTo"`
	} `json:"environment"`
	Summary struct {
		classSummary
		PointAccuracyPass bool `json:"pointAccuracyAtLeast95"`
		ConfidencePass    bool `json:"wilsonLowerAtLeast95"`
		OverallPass       bool `json:"overallPass"`
	} `json:"summary"`
	Classes        map[string]classSummary `json:"classes"`
	SourceCoverage struct {
		MySQLRequiredCases  int `json:"mysqlRequiredCases"`
		OracleRequiredCases int `json:"oracleRequiredCases"`
		CrossSourceCases    int `json:"crossSourceCases"`
	} `json:"sourceCoverage"`
	Security struct {
		CasesChecked          int  `json:"casesChecked"`
		SensitiveLeakDetected bool `json:"sensitiveLeakDetected"`
	} `json:"security"`
	ReleaseGate struct {
		Eligible bool   `json:"eligible"`
		Reason   string `json:"reason"`
	} `json:"releaseGate"`
	Failures []caseResult `json:"failures"`
	Cases    []caseResult `json:"cases"`
}

type evaluator struct {
	baseURL, accessToken, password string
	client                         *http.Client
	metricCodes                    map[string]string
}

var rawSQLPattern = regexp.MustCompile(`(?is)\bselect\b.{0,240}\bfrom\b`)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8080", "API base URL")
	tenantCode := flag.String("tenant-code", envOr("SEMANTIC_QA_EVAL_TENANT", "demo"), "tenant code")
	email := flag.String("email", os.Getenv("SEMANTIC_QA_EVAL_EMAIL"), "login email")
	password := flag.String("password", os.Getenv("SEMANTIC_QA_EVAL_PASSWORD"), "login password")
	output := flag.String("output", "", "write the complete JSON report to this path")
	concurrency := flag.Int("concurrency", 4, "maximum concurrent questions")
	requestTimeout := flag.Duration("request-timeout", 20*time.Second, "per-question timeout")
	classes := flag.String("classes", "", "comma-separated case classes to run; empty runs all")
	limit := flag.Int("limit", 0, "run only the first N generated cases; 0 runs all")
	flag.Parse()

	if strings.TrimSpace(*email) == "" || *password == "" {
		fatal(errors.New("SEMANTIC_QA_EVAL_EMAIL and SEMANTIC_QA_EVAL_PASSWORD are required"))
	}
	if *concurrency < 1 || *concurrency > 16 || *requestTimeout < time.Second {
		fatal(errors.New("concurrency must be 1..16 and request-timeout at least 1s"))
	}

	client := &http.Client{
		Timeout: *requestTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        *concurrency * 2,
			MaxIdleConnsPerHost: *concurrency * 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	accessToken, err := login(ctx, client, *baseURL, *tenantCode, *email, *password)
	cancel()
	if err != nil {
		fatal(err)
	}
	evaluation := &evaluator{
		baseURL: strings.TrimRight(*baseURL, "/"), accessToken: accessToken,
		password: *password, client: client,
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Minute)
	evaluation.metricCodes, err = evaluation.loadMetricCodes(ctx)
	cancel()
	if err != nil {
		fatal(err)
	}

	cases := buildEvaluationCases()
	if strings.TrimSpace(*classes) != "" {
		cases, err = filterEvaluationCases(cases, *classes)
		if err != nil {
			fatal(err)
		}
	}
	if *limit > 0 && *limit < len(cases) {
		cases = cases[:*limit]
	}
	results := evaluation.run(cases, *concurrency)
	report := buildReport(*baseURL, *tenantCode, results, cases)
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
	}
	if *output != "" {
		if err := os.WriteFile(*output, append(encoded, '\n'), 0o600); err != nil {
			fatal(err)
		}
	}
	fmt.Printf(
		"semantic QA evaluation: %d/%d correct (%.2f%%), Wilson lower 95%% %.2f%%, p95 %dms, pass=%v\n",
		report.Summary.Correct, report.Summary.Total,
		report.Summary.Accuracy*100, report.Summary.WilsonLower95*100,
		report.Summary.P95DurationMS, report.Summary.OverallPass,
	)
	if !report.Summary.OverallPass {
		os.Exit(1)
	}
}

func filterEvaluationCases(
	cases []evaluationCase,
	classes string,
) ([]evaluationCase, error) {
	wanted := map[string]bool{}
	for _, value := range strings.Split(classes, ",") {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value != "" {
			wanted[value] = true
		}
	}
	filtered := make([]evaluationCase, 0, len(cases))
	for _, item := range cases {
		if wanted[strings.ToUpper(item.Class)] {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no evaluation cases match classes %q", classes)
	}
	return filtered, nil
}

func login(
	ctx context.Context,
	client *http.Client,
	baseURL, tenantCode, email, password string,
) (string, error) {
	payload, _ := json.Marshal(map[string]string{
		"tenantCode": tenantCode, "email": email, "password": password,
	})
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/auth/login",
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("login returned HTTP %d", response.StatusCode)
	}
	var output struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(body, &output) != nil || output.AccessToken == "" {
		return "", errors.New("login response did not contain an access token")
	}
	return output.AccessToken, nil
}

func (evaluation *evaluator) loadMetricCodes(ctx context.Context) (map[string]string, error) {
	request, err := http.NewRequestWithContext(
		ctx, http.MethodGet, evaluation.baseURL+"/api/v1/metrics?limit=200", nil,
	)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+evaluation.accessToken)
	response, err := evaluation.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("load metric catalog: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metric catalog returned HTTP %d", response.StatusCode)
	}
	var catalog metricCatalogResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, metric := range catalog.Items {
		if metric.Name != "" && metric.Code != "" {
			result[metric.Name] = metric.Code
		}
	}
	return result, nil
}

func (evaluation *evaluator) run(cases []evaluationCase, concurrency int) []caseResult {
	results := make([]caseResult, len(cases))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = evaluation.evaluate(cases[index])
			}
		}()
	}
	for index := range cases {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return results
}

func (evaluation *evaluator) evaluate(item evaluationCase) caseResult {
	result := caseResult{
		ID: item.ID, Class: item.Class, Question: item.Question,
		ExpectedMetricName: item.ExpectedMetricName,
		ExpectedMetricCode: evaluation.metricCodes[item.ExpectedMetricName],
		SecuritySafe:       true, Failures: []string{},
	}
	if result.ExpectedMetricCode == "" {
		result.Failures = append(result.Failures, "expected current metric missing from catalog")
		return result
	}
	payload, _ := json.Marshal(map[string]string{
		"question": item.Question, "timezone": "Asia/Shanghai",
	})
	ctx, cancel := context.WithTimeout(context.Background(), evaluation.client.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, evaluation.baseURL+"/api/v1/questions", bytes.NewReader(payload),
	)
	if err != nil {
		result.Failures = append(result.Failures, err.Error())
		return result
	}
	request.Header.Set("Authorization", "Bearer "+evaluation.accessToken)
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := evaluation.client.Do(request)
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		result.Failures = append(result.Failures, "request: "+err.Error())
		return result
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		result.Failures = append(result.Failures, "read response: "+err.Error())
		return result
	}
	if bytes.Contains(body, []byte(evaluation.accessToken)) ||
		(evaluation.password != "" && bytes.Contains(body, []byte(evaluation.password))) ||
		bytes.Contains(bytes.ToLower(body), []byte("authorization: bearer")) ||
		bytes.Contains(body, []byte("-----BEGIN PRIVATE KEY-----")) || rawSQLPattern.Match(body) {
		result.SecuritySafe = false
		result.Failures = append(result.Failures, "response leaked a credential or raw SQL")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Failures = append(
			result.Failures, fmt.Sprintf("HTTP status %d", response.StatusCode),
		)
		return result
	}
	var answer questionResponse
	if err := json.Unmarshal(body, &answer); err != nil {
		result.Failures = append(result.Failures, "decode response: "+err.Error())
		return result
	}
	result.QuestionID = answer.QuestionID
	if answer.State != "ANSWERED" || answer.Status != "ANSWERED" {
		result.Failures = append(result.Failures, "question did not reach ANSWERED")
	}
	if answer.Route != "SEMANTIC_IR" {
		result.Failures = append(result.Failures, "route is not SEMANTIC_IR")
	}
	if answer.Intent.TaskType != item.ExpectedIntent {
		result.Failures = append(
			result.Failures,
			fmt.Sprintf("intent=%s expected=%s", answer.Intent.TaskType, item.ExpectedIntent),
		)
	}
	if len(answer.SemanticIR.Metrics) > 0 {
		result.ActualMetricCode = answer.SemanticIR.Metrics[0].Code
	}
	if result.ActualMetricCode != result.ExpectedMetricCode {
		result.Failures = append(result.Failures, "semantic IR selected a different metric")
	}
	if len(answer.Answer.ResultSets) != 1 {
		result.Failures = append(result.Failures, "answer must contain exactly one result set")
	} else {
		resultSet := answer.Answer.ResultSets[0]
		if resultSet.MetricCode != result.ExpectedMetricCode {
			result.Failures = append(result.Failures, "result set metric differs from current metric")
		}
		if failure := compareRows(item.ExpectedRows, resultSet.Rows); failure != "" {
			result.Failures = append(result.Failures, failure)
		}
	}
	if item.ExpectedDimensionCode != "" &&
		!containsIntentDimension(answer, item.ExpectedDimensionCode) {
		result.Failures = append(result.Failures, "expected governed dimension is absent")
	}
	if item.ExpectedFilterValue != "" && !containsFilter(
		answer, item.ExpectedDimensionCode, item.ExpectedFilterValue,
	) {
		result.Failures = append(result.Failures, "semantic IR filter differs from expectation")
	}
	if item.ExpectedTimeStart != "" &&
		(answer.SemanticIR.Time == nil ||
			answer.SemanticIR.Time.Start != item.ExpectedTimeStart ||
			answer.SemanticIR.Time.EndExclusive != item.ExpectedTimeEnd) {
		result.Failures = append(result.Failures, "semantic IR time range differs from expectation")
	}
	if answer.SemanticIR.SemanticVersion == "" ||
		answer.SemanticIR.SemanticContentHash == "" ||
		len(answer.SemanticIR.EvidenceIDs) == 0 {
		result.Failures = append(result.Failures, "semantic IR evidence/version is incomplete")
	}
	if len(answer.Executions) != 1 || answer.Executions[0].Evidence.ResultHash == "" ||
		answer.Executions[0].Evidence.QueryPlanHash == "" ||
		!answer.Executions[0].Evidence.ExecutionRevalidated {
		result.Failures = append(result.Failures, "execution evidence was not revalidated")
	} else {
		labels := make([]string, 0, len(answer.Executions[0].Evidence.Lineage))
		for _, lineage := range answer.Executions[0].Evidence.Lineage {
			labels = append(labels, lineage.Label)
		}
		for _, required := range item.RequiredLineage {
			if !containsString(labels, required) {
				result.Failures = append(result.Failures, "missing lineage: "+required)
			}
		}
	}
	result.Correct = len(result.Failures) == 0 && result.SecuritySafe
	return result
}

func containsIntentDimension(answer questionResponse, code string) bool {
	for _, dimension := range answer.Intent.Dimensions {
		if dimension.Code == code {
			return true
		}
	}
	return false
}

func containsFilter(answer questionResponse, code, value string) bool {
	for _, filter := range answer.SemanticIR.Filters {
		if filter.DimensionCode == code && containsString(filter.ValueIDs, value) {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func compareRows(expected [][]any, actual [][]json.RawMessage) string {
	if len(expected) != len(actual) {
		return fmt.Sprintf("row count=%d expected=%d", len(actual), len(expected))
	}
	for rowIndex := range expected {
		if len(expected[rowIndex]) != len(actual[rowIndex]) {
			return fmt.Sprintf("column count differs at row %d", rowIndex)
		}
		for columnIndex, expectedCell := range expected[rowIndex] {
			actualCell := actual[rowIndex][columnIndex]
			switch value := expectedCell.(type) {
			case string:
				var decoded string
				if json.Unmarshal(actualCell, &decoded) != nil || decoded != value {
					return fmt.Sprintf("text differs at row %d column %d", rowIndex, columnIndex)
				}
			case float64:
				var decoded float64
				if json.Unmarshal(actualCell, &decoded) != nil ||
					math.Abs(decoded-value) > 1e-6*math.Max(1, math.Abs(value)) {
					return fmt.Sprintf(
						"number differs at row %d column %d: actual=%s expected=%v",
						rowIndex, columnIndex, string(actualCell), value,
					)
				}
			default:
				return "unsupported expected cell type"
			}
		}
	}
	return ""
}

func buildReport(
	baseURL, tenantCode string,
	results []caseResult,
	cases []evaluationCase,
) evaluationReport {
	report := evaluationReport{
		SchemaVersion: evaluationSchemaVersion,
		Kind:          "DEVELOPMENT_E2E_VALIDATION",
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		Classes:       map[string]classSummary{},
		Failures:      []caseResult{}, Cases: results,
	}
	report.Environment.BaseURL = baseURL
	report.Environment.TenantCode = tenantCode
	report.Environment.Timezone = "Asia/Shanghai"
	report.Environment.WarehouseFrom = "2026-06-01"
	report.Environment.WarehouseTo = "2026-06-30"
	report.Environment.SourceTables = []string{
		"mysql:121.takeout_master.dim_courier",
		"mysql:121.takeout_master.dim_customer",
		"mysql:121.takeout_master.dim_delivery_zone",
		"mysql:121.takeout_master.dim_merchant",
		"oracle:234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS",
		"oracle:234.TAKEOUT_USER.FACT_DELIVERY_EVENT",
		"oracle:234.TAKEOUT_USER.FACT_ORDER_ITEM",
		"oracle:234.TAKEOUT_USER.FACT_ORDERS",
	}
	classResults := map[string][]caseResult{}
	for index, result := range results {
		classResults[result.Class] = append(classResults[result.Class], result)
		if !result.Correct {
			report.Failures = append(report.Failures, result)
		}
		if index < len(cases) {
			hasMySQL, hasOracle := false, false
			for _, lineage := range cases[index].RequiredLineage {
				hasMySQL = hasMySQL || strings.HasPrefix(lineage, "121.")
				hasOracle = hasOracle || strings.HasPrefix(lineage, "234.")
			}
			if hasMySQL {
				report.SourceCoverage.MySQLRequiredCases++
			}
			if hasOracle {
				report.SourceCoverage.OracleRequiredCases++
			}
			if hasMySQL && hasOracle {
				report.SourceCoverage.CrossSourceCases++
			}
		}
		if !result.SecuritySafe {
			report.Security.SensitiveLeakDetected = true
		}
	}
	report.Security.CasesChecked = len(results)
	for class, items := range classResults {
		report.Classes[class] = summarize(items)
	}
	report.Summary.classSummary = summarize(results)
	report.Summary.PointAccuracyPass = report.Summary.Accuracy >= 0.95
	report.Summary.ConfidencePass = report.Summary.WilsonLower95 >= 0.95
	report.Summary.OverallPass = report.Summary.PointAccuracyPass &&
		report.Summary.ConfidencePass && !report.Security.SensitiveLeakDetected
	report.ReleaseGate.Eligible = false
	report.ReleaseGate.Reason =
		"development validation only; the formal sealed release gate requires at least 2000 independently reviewed cases with persisted result hashes and safety suites"
	return report
}

func summarize(results []caseResult) classSummary {
	result := classSummary{Total: len(results)}
	durations := make([]int64, 0, len(results))
	for _, item := range results {
		if item.Correct {
			result.Correct++
		}
		durations = append(durations, item.DurationMS)
		result.MaximumDurationMS = max(result.MaximumDurationMS, item.DurationMS)
	}
	if result.Total > 0 {
		result.Accuracy = float64(result.Correct) / float64(result.Total)
		result.WilsonLower95 = wilsonLower(result.Correct, result.Total)
	}
	sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
	result.P50DurationMS = percentile(durations, 0.50)
	result.P95DurationMS = percentile(durations, 0.95)
	return result
}

func percentile(values []int64, quantile float64) int64 {
	if len(values) == 0 {
		return 0
	}
	index := int(math.Ceil(quantile*float64(len(values)))) - 1
	index = min(max(index, 0), len(values)-1)
	return values[index]
}

func wilsonLower(correct, total int) float64 {
	if total == 0 {
		return 0
	}
	z := 1.959963984540054
	n := float64(total)
	p := float64(correct) / n
	denominator := 1 + z*z/n
	center := p + z*z/(2*n)
	margin := z * math.Sqrt((p*(1-p)+z*z/(4*n))/n)
	return (center - margin) / denominator
}

type namedValue struct {
	Name  string
	Value float64
}

type groupedValues struct {
	Member string
	Values []float64
}

func buildEvaluationCases() []evaluationCase {
	cases := []evaluationCase{}
	nextID := func(class string) string {
		return fmt.Sprintf("%s-%03d", strings.ToLower(class), len(cases)+1)
	}
	appendScalar := func(
		class, question, metric, intent, dimension, filter string,
		value float64, lineage ...string,
	) {
		cases = append(cases, evaluationCase{
			ID: nextID(class), Class: class, Question: question,
			ExpectedMetricName: metric, ExpectedIntent: intent,
			ExpectedDimensionCode: dimension, ExpectedFilterValue: filter,
			ExpectedRows: [][]any{{value}}, RequiredLineage: lineage,
		})
	}

	direct := []struct {
		Metric, Canonical string
		Value             float64
		Lineage           string
	}{
		{"SKU 实体数量", "SKU 实体数量", 24, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
		{"优惠金额", "优惠金额", 9600, "234.TAKEOUT_USER.FACT_ORDERS"},
		{"净支付金额", "净支付金额", 238120, "234.TAKEOUT_USER.FACT_ORDERS"},
		{"净收入", "净收入", 223756, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"取消订单数量", "取消订单数量", 144, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"商品总价", "商品总价", 237800, "234.TAKEOUT_USER.FACT_ORDERS"},
		{"商家补贴金额", "商家补贴金额", 480, "234.TAKEOUT_USER.FACT_ORDERS"},
		{"在线时长", "在线时长", 5833388, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"已完成订单数", "已完成订单数", 2208, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"平台补贴金额", "平台补贴金额", 1600, "234.TAKEOUT_USER.FACT_ORDERS"},
		{"总订单数", "总订单数", 2400, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"投诉数量", "投诉数量", 72, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"毛收入", "毛收入", 237800, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"用户实体数量", "用户实体数量", 600, "121.takeout_master.dim_customer"},
		{"订单商品折扣金额合计", "订单商品折扣金额合计", 2400, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
		{"订单商品数量合计", "订单商品数量合计", 8400, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
		{"订单商品行项目金额合计", "订单商品行项目金额合计", 235400, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
		{"退款金额", "退款金额", 6796, "234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS"},
		{"配送事件记录数", "配送事件记录数", 11880, "234.TAKEOUT_USER.FACT_DELIVERY_EVENT"},
		{"配送区域实体数量", "配送区域实体数量", 200, "121.takeout_master.dim_delivery_zone"},
		{"配送费", "配送费", 12000, "234.TAKEOUT_USER.FACT_ORDERS"},
		{"骑手实体数量", "骑手实体数量", 360, "121.takeout_master.dim_courier"},
		{"商品折扣", "订单商品折扣金额合计", 2400, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
		{"数量", "订单商品数量合计", 8400, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
		{"行项目金额", "订单商品行项目金额合计", 235400, "234.TAKEOUT_USER.FACT_ORDER_ITEM"},
	}
	for index, item := range direct {
		class := "DIRECT_METRIC"
		if index >= 22 {
			class = "SUPERSEDED_ALIAS"
		}
		appendScalar(
			class, "请问"+item.Metric+"是多少？", item.Canonical,
			"METRIC", "", "", item.Value, item.Lineage,
		)
	}

	merchantMetricNames := []string{
		"总订单数", "已完成订单数", "取消订单数量", "毛收入",
		"净收入", "投诉数量", "退款金额",
	}
	cities := []groupedValues{
		{"Beijing", []float64{250, 230, 12, 26430, 25318, 6, 592}},
		{"Chengdu", []float64{240, 222, 14, 24800, 23290, 8, 918}},
		{"Chongqing", []float64{230, 212, 14, 23780, 22474, 8, 766}},
		{"Guangzhou", []float64{240, 222, 14, 20500, 19452, 6, 512}},
		{"Hangzhou", []float64{230, 210, 14, 21660, 20248, 6, 520}},
		{"Nanjing", []float64{240, 218, 18, 25370, 23430, 8, 860}},
		{"Shanghai", []float64{250, 228, 18, 25080, 23266, 8, 666}},
		{"Shenzhen", []float64{250, 230, 16, 21950, 20490, 10, 886}},
		{"Suzhou", []float64{240, 222, 12, 27470, 26052, 6, 536}},
		{"Wuhan", []float64{230, 214, 12, 20760, 19736, 6, 540}},
	}
	for _, city := range cities {
		for index, metric := range merchantMetricNames {
			appendScalar(
				"CITY_FILTER", city.Member+"的"+metric+"是多少？", metric,
				"METRIC", "zone_city", city.Member, city.Values[index],
				"234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS",
				"121.takeout_master.dim_delivery_zone",
			)
		}
	}

	dailyMetricNames := []string{"总订单数", "已完成订单数", "毛收入", "净收入"}
	daily := []groupedValues{
		{"2026-06-01", []float64{82, 76, 8014, 7558}},
		{"2026-06-02", []float64{81, 73, 8051, 7628}},
		{"2026-06-03", []float64{82, 75, 8110, 7526}},
		{"2026-06-04", []float64{81, 78, 8000, 7764}},
		{"2026-06-05", []float64{82, 77, 8149, 7877}},
		{"2026-06-06", []float64{81, 72, 7941, 7321}},
		{"2026-06-07", []float64{82, 74, 8102, 7412}},
		{"2026-06-08", []float64{81, 76, 8202, 7689}},
		{"2026-06-09", []float64{82, 74, 8146, 7648}},
		{"2026-06-10", []float64{81, 72, 8187, 7496}},
		{"2026-06-11", []float64{82, 73, 8087, 7435}},
		{"2026-06-12", []float64{78, 72, 7666, 7139}},
		{"2026-06-13", []float64{80, 75, 7978, 7743}},
		{"2026-06-14", []float64{79, 73, 7787, 7394}},
		{"2026-06-15", []float64{79, 70, 7857, 7238}},
		{"2026-06-16", []float64{79, 73, 7709, 7125}},
		{"2026-06-17", []float64{80, 77, 7880, 7704}},
		{"2026-06-18", []float64{78, 72, 7818, 7362}},
		{"2026-06-19", []float64{80, 72, 7906, 7411}},
		{"2026-06-20", []float64{79, 73, 7753, 7234}},
		{"2026-06-21", []float64{79, 75, 7778, 7506}},
		{"2026-06-22", []float64{79, 74, 7910, 7601}},
		{"2026-06-23", []float64{80, 71, 7986, 7345}},
		{"2026-06-24", []float64{78, 70, 7908, 7144}},
		{"2026-06-25", []float64{80, 75, 7906, 7403}},
		{"2026-06-26", []float64{78, 73, 7677, 7525}},
		{"2026-06-27", []float64{80, 73, 7938, 7458}},
		{"2026-06-28", []float64{78, 70, 7610, 6988}},
		{"2026-06-29", []float64{80, 74, 7975, 7500}},
		{"2026-06-30", []float64{79, 76, 7769, 7582}},
	}
	for _, day := range daily {
		parsed, _ := time.Parse(time.DateOnly, day.Member)
		for index, metric := range dailyMetricNames {
			item := evaluationCase{
				ID: nextID("DATE_FILTER"), Class: "DATE_FILTER",
				Question:           day.Member + "的" + metric + "是多少？",
				ExpectedMetricName: metric, ExpectedIntent: "METRIC",
				ExpectedTimeStart: day.Member,
				ExpectedTimeEnd:   parsed.AddDate(0, 0, 1).Format(time.DateOnly),
				ExpectedRows:      [][]any{{day.Values[index]}},
				RequiredLineage: []string{
					"234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS",
				},
			}
			cases = append(cases, item)
		}
	}

	orderMetricNames := []string{
		"商品总价", "优惠金额", "配送费", "平台补贴金额", "商家补贴金额", "净支付金额",
	}
	statuses := []groupedValues{
		{"CANCELLED", []float64{14292, 576, 768, 96, 24, 14364}},
		{"DELIVERED", []float64{209944, 8448, 10632, 1424, 432, 210272}},
		{"DELIVERING", []float64{6816, 336, 240, 32, 0, 6688}},
		{"REFUNDED", []float64{6748, 240, 360, 48, 24, 6796}},
	}
	for _, status := range statuses {
		for index, metric := range orderMetricNames {
			appendScalar(
				"ORDER_STATUS_FILTER", status.Member+"订单状态的"+metric+"是多少？",
				metric, "METRIC", "order_status", status.Member, status.Values[index],
				"234.TAKEOUT_USER.FACT_ORDERS",
			)
		}
	}
	payments := []groupedValues{
		{"ALIPAY", []float64{17000, 240, 3000, 400, 120, 19240}},
		{"BALANCE", []float64{101300, 4560, 3000, 400, 120, 99220}},
		{"BANK_CARD", []float64{69100, 3120, 3000, 400, 120, 68460}},
		{"WECHAT_PAY", []float64{50400, 1680, 3000, 400, 120, 51200}},
	}
	for _, payment := range payments {
		for index, metric := range orderMetricNames {
			appendScalar(
				"PAYMENT_METHOD_FILTER", payment.Member+"支付方式的"+metric+"是多少？",
				metric, "METRIC", "payment_method", payment.Member, payment.Values[index],
				"234.TAKEOUT_USER.FACT_ORDERS",
			)
		}
	}

	itemMetricNames := []string{
		"订单商品数量合计", "订单商品折扣金额合计", "订单商品行项目金额合计",
	}
	categories := []groupedValues{
		{"Bakery", []float64{400, 160, 6240}},
		{"Barbecue", []float64{600, 200, 18000}},
		{"Burgers", []float64{800, 200, 25000}},
		{"Chinese Fast Food", []float64{1000, 320, 31280}},
		{"Coffee & Tea", []float64{1000, 240, 19360}},
		{"Dessert", []float64{800, 200, 19000}},
		{"Healthy Food", []float64{600, 200, 19400}},
		{"Hot Pot", []float64{800, 200, 37000}},
		{"Noodles", []float64{1200, 320, 26480}},
		{"Rice Bowl", []float64{1200, 360, 33640}},
	}
	for _, category := range categories {
		for index, metric := range itemMetricNames {
			appendScalar(
				"ITEM_CATEGORY_FILTER", category.Member+"的"+metric+"是多少？",
				metric, "METRIC", "order_item_category", category.Member,
				category.Values[index], "234.TAKEOUT_USER.FACT_ORDER_ITEM",
			)
		}
	}

	events := []namedValue{
		{"CANCELLED", 144}, {"COURIER_ASSIGNED", 2400}, {"DELIVERED", 2208},
		{"MERCHANT_ACCEPTED", 2400}, {"ORDER_CREATED", 2400},
		{"PICKED_UP", 2256}, {"REFUND_APPROVED", 72},
	}
	for _, event := range events {
		appendScalar(
			"DELIVERY_EVENT_FILTER", event.Name+"的配送事件记录数是多少？",
			"配送事件记录数", "METRIC", "event_type", event.Name, event.Value,
			"234.TAKEOUT_USER.FACT_DELIVERY_EVENT",
		)
	}

	rankingRows := make([][]any, 0, len(cities))
	for _, city := range cities {
		rankingRows = append(rankingRows, []any{city.Member, city.Values[4]})
	}
	sort.Slice(rankingRows, func(left, right int) bool {
		return rankingRows[left][1].(float64) > rankingRows[right][1].(float64)
	})
	cases = append(cases, evaluationCase{
		ID: nextID("RANKING"), Class: "RANKING",
		Question: "净收入按配送区域城市排名", ExpectedMetricName: "净收入",
		ExpectedIntent: "RANKING", ExpectedDimensionCode: "zone_city",
		ExpectedRows: rankingRows, RequiredLineage: []string{
			"234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS",
			"121.takeout_master.dim_delivery_zone",
		},
	})
	distributionRows := make([][]any, 0, len(cities))
	for _, city := range cities {
		distributionRows = append(distributionRows, []any{city.Member, city.Values[4]})
	}
	cases = append(cases, evaluationCase{
		ID: nextID("DISTRIBUTION"), Class: "DISTRIBUTION",
		Question: "净收入按配送区域城市分布", ExpectedMetricName: "净收入",
		ExpectedIntent: "DISTRIBUTION", ExpectedDimensionCode: "zone_city",
		ExpectedRows: distributionRows, RequiredLineage: []string{
			"234.TAKEOUT_USER.AGG_MERCHANT_DAILY_OPS",
			"121.takeout_master.dim_delivery_zone",
		},
	})
	return cases
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "semantic QA evaluation failed:", err)
	os.Exit(2)
}
