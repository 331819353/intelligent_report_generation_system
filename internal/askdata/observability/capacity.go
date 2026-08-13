package observability

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

const CapacityReportSchemaVersion = "askdata-capacity-report-v2"

type CapacityScenario string

const (
	CapacityFastPath       CapacityScenario = "FAST_PATH"
	CapacityComplexLoop    CapacityScenario = "COMPLEX_LOOP"
	CapacityWarehousePool  CapacityScenario = "WAREHOUSE_POOL"
	CapacityVectorRecall   CapacityScenario = "VECTOR_RECALL"
	CapacityGraphThreeHops CapacityScenario = "GRAPH_3_HOPS"
	CapacityLLMDegradation CapacityScenario = "LLM_DEGRADATION"
	CapacityKPIBundle      CapacityScenario = "KPI_BUNDLE"
	// The report surface is half of the phase-one product. A profile that
	// claimed to cover "report and AskData" while measuring only AskData would
	// produce a capacity number for the wrong system.
	CapacityPlatformAccess CapacityScenario = "PLATFORM_ACCESS"
	CapacityReportRuntime  CapacityScenario = "REPORT_RUNTIME"
	CapacityReportPublish  CapacityScenario = "REPORT_PUBLISH"
	CapacityReportExport   CapacityScenario = "REPORT_EXPORT"
	// Full-platform only.
	CapacitySubscriptionDispatch CapacityScenario = "SUBSCRIPTION_DISPATCH"
	CapacityDecisionWorkflow     CapacityScenario = "DECISION_WORKFLOW"
)

var askDataCapacityScenarios = []CapacityScenario{
	CapacityFastPath, CapacityComplexLoop, CapacityWarehousePool,
	CapacityVectorRecall, CapacityGraphThreeHops, CapacityLLMDegradation,
	CapacityKPIBundle,
}

var reportCapacityScenarios = []CapacityScenario{
	CapacityPlatformAccess, CapacityReportRuntime, CapacityReportPublish, CapacityReportExport,
}

var fullPlatformCapacityScenarios = []CapacityScenario{
	CapacitySubscriptionDispatch, CapacityDecisionWorkflow,
}

// RequiredCapacityScenarios returns the scenarios a scope profile must cover.
// The profile is the contract: FULL_PLATFORM is a superset, never a substitute.
func RequiredCapacityScenarios(profile CapacityScopeProfile) []CapacityScenario {
	required := append(append([]CapacityScenario(nil), askDataCapacityScenarios...), reportCapacityScenarios...)
	if profile == CapacityScopeFullPlatform {
		required = append(required, fullPlatformCapacityScenarios...)
	}
	return required
}

func governedCapacityScenario(value CapacityScenario) bool {
	for _, scenario := range RequiredCapacityScenarios(CapacityScopeFullPlatform) {
		if scenario == value {
			return true
		}
	}
	return false
}

type CapacityScale struct {
	Tenants                 int64 `json:"tenants"`
	Domains                 int64 `json:"domains"`
	SearchDocuments         int64 `json:"searchDocuments"`
	MaxFullMembersPerDomain int64 `json:"maxFullMembersPerDomain"`
	MaxOnDemandMembers      int64 `json:"maxOnDemandMembersPerDomain"`
	DailyQuestionRuns       int64 `json:"dailyQuestionRuns"`
	PeakConcurrentQuestions int64 `json:"peakConcurrentQuestions"`
	PublishedReports        int64 `json:"publishedReports"`
	GraphVertices           int64 `json:"graphVertices"`
	GraphEdges              int64 `json:"graphEdges"`
}

type CapacityThresholds struct {
	MaxP95MS       int64   `json:"maxP95Ms"`
	MaxP99MS       int64   `json:"maxP99Ms,omitempty"`
	MinSuccessRate float64 `json:"minSuccessRate"`
	MinRecallAtK   float64 `json:"minRecallAtK,omitempty"`
}

type CapacityScenarioResult struct {
	Scenario        CapacityScenario   `json:"scenario"`
	Requests        int64              `json:"requests"`
	Succeeded       int64              `json:"succeeded"`
	Failed          int64              `json:"failed"`
	RateLimited     int64              `json:"rateLimited"`
	Degraded        int64              `json:"degraded"`
	PoolTimeouts    int64              `json:"poolTimeouts"`
	P50MS           int64              `json:"p50Ms"`
	P95MS           int64              `json:"p95Ms"`
	P99MS           int64              `json:"p99Ms"`
	MaxMS           int64              `json:"maxMs"`
	RecallAtK       *float64           `json:"recallAtK,omitempty"`
	Thresholds      CapacityThresholds `json:"thresholds"`
	FailureCodeHash string             `json:"failureCodeHash"`
}

type CapacityEnvironment struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	LogicalCPUs   int    `json:"logicalCpus"`
	GoVersion     string `json:"goVersion"`
	ConfigHash    string `json:"configHash"`
	TargetOrigin  string `json:"targetOrigin"`
	DatabaseLabel string `json:"databaseLabel"`
	GraphLabel    string `json:"graphLabel"`
	LLMLabel      string `json:"llmLabel"`
}

type CapacityReport struct {
	SchemaVersion string                   `json:"schemaVersion"`
	RunID         string                   `json:"runId"`
	Seed          int64                    `json:"seed"`
	StartedAt     time.Time                `json:"startedAt"`
	DurationMS    int64                    `json:"durationMs"`
	ScopeProfile  CapacityScopeProfile     `json:"scopeProfile"`
	Environment   CapacityEnvironment      `json:"environment"`
	Scale         CapacityScale            `json:"scale"`
	Scenarios     []CapacityScenarioResult `json:"scenarios"`
	// The three blocks below are the signature material from 04 §6.1. They are
	// optional in a load test and required for a signable capacity conclusion,
	// which is exactly the difference EvaluateCapacitySignability reports.
	ConnectionBudgets  []CapacityConnectionBudget  `json:"connectionBudgets,omitempty"`
	FaultInjection     []CapacityFaultResult       `json:"faultInjection,omitempty"`
	ResourceWatermarks []CapacityResourceWatermark `json:"resourceWatermarks,omitempty"`
	Signoff            *CapacitySignoff            `json:"signoff,omitempty"`
	// Signability is recomputed on validation and never read from the file, so
	// an unsigned run cannot be edited into a signed one.
	Signability CapacitySignability `json:"signability"`
}

type CapacityAlert struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Actual   string `json:"actual"`
	Limit    string `json:"limit"`
}

func ValidateCapacityReport(report CapacityReport) error {
	if report.SchemaVersion != CapacityReportSchemaVersion || report.RunID == "" || report.StartedAt.IsZero() || report.DurationMS <= 0 {
		return errors.New("capacity report identity is incomplete")
	}
	if !validCapacityScopeProfile(report.ScopeProfile) {
		return fmt.Errorf("capacity scope profile %q is not governed", report.ScopeProfile)
	}
	environment := report.Environment
	if environment.GOOS == "" || environment.GOARCH == "" || environment.LogicalCPUs <= 0 ||
		environment.GoVersion == "" || len(environment.ConfigHash) != 64 || environment.TargetOrigin == "" ||
		environment.DatabaseLabel == "" || environment.GraphLabel == "" || environment.LLMLabel == "" {
		return errors.New("capacity environment snapshot is incomplete")
	}
	seen := make(map[CapacityScenario]struct{}, len(report.Scenarios))
	for _, result := range report.Scenarios {
		if err := validateCapacityScenarioResult(result); err != nil {
			return err
		}
		if _, duplicate := seen[result.Scenario]; duplicate {
			return fmt.Errorf("capacity scenario %s is duplicated", result.Scenario)
		}
		seen[result.Scenario] = struct{}{}
	}
	for _, scenario := range RequiredCapacityScenarios(report.ScopeProfile) {
		if _, exists := seen[scenario]; !exists {
			return fmt.Errorf("capacity scenario %s is missing from profile %s", scenario, report.ScopeProfile)
		}
	}
	if err := validateCapacityConnectionBudgets(report.ConnectionBudgets); err != nil {
		return err
	}
	if err := validateCapacityFaultResults(report.FaultInjection); err != nil {
		return err
	}
	if err := validateCapacityResourceWatermarks(report.ResourceWatermarks); err != nil {
		return err
	}
	// The verdict is recomputed rather than compared, so a stored report can
	// never carry a signature its inputs do not support.
	expected := EvaluateCapacitySignability(report)
	if report.Signability.Verdict != expected.Verdict ||
		report.Signability.ScopeProfile != expected.ScopeProfile ||
		!equalStrings(report.Signability.MissingInputs, expected.MissingInputs) {
		return fmt.Errorf("capacity signability must be %s for the declared inputs", expected.Verdict)
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func EvaluateCapacityAlerts(report CapacityReport) ([]CapacityAlert, error) {
	if err := ValidateCapacityReport(report); err != nil {
		return nil, err
	}
	alerts := architectureScaleAlerts(report.Scale)
	// An unsigned run is surfaced at the same severity as a blown threshold.
	// The most expensive capacity mistake is not a slow P95; it is presenting a
	// development-machine run as a target-environment conclusion.
	if report.Signability.Verdict != SignabilitySignable {
		alerts = append(alerts, CapacityAlert{
			Code: "CAPACITY_" + SignabilityNotSigned, Severity: "CRITICAL",
			Actual: fmt.Sprintf("%s (%d missing inputs)", report.Signability.Verdict, len(report.Signability.MissingInputs)),
			Limit:  SignabilitySignable,
		})
	}
	for _, result := range report.Scenarios {
		successRate := float64(result.Succeeded) / float64(result.Requests)
		if successRate < result.Thresholds.MinSuccessRate {
			alerts = append(alerts, CapacityAlert{
				Code: "CAPACITY_SUCCESS_RATE", Severity: "CRITICAL",
				Actual: fmt.Sprintf("%.6f", successRate), Limit: fmt.Sprintf(">=%.6f", result.Thresholds.MinSuccessRate),
			})
		}
		if result.P95MS > result.Thresholds.MaxP95MS {
			alerts = append(alerts, CapacityAlert{
				Code: "CAPACITY_" + string(result.Scenario) + "_P95", Severity: "CRITICAL",
				Actual: fmt.Sprintf("%dms", result.P95MS), Limit: fmt.Sprintf("<=%dms", result.Thresholds.MaxP95MS),
			})
		}
		if result.Thresholds.MaxP99MS > 0 && result.P99MS > result.Thresholds.MaxP99MS {
			alerts = append(alerts, CapacityAlert{
				Code: "CAPACITY_" + string(result.Scenario) + "_P99", Severity: "WARNING",
				Actual: fmt.Sprintf("%dms", result.P99MS), Limit: fmt.Sprintf("<=%dms", result.Thresholds.MaxP99MS),
			})
		}
		if result.Thresholds.MinRecallAtK > 0 && (result.RecallAtK == nil || *result.RecallAtK < result.Thresholds.MinRecallAtK) {
			actual := "missing"
			if result.RecallAtK != nil {
				actual = fmt.Sprintf("%.6f", *result.RecallAtK)
			}
			alerts = append(alerts, CapacityAlert{
				Code: "CAPACITY_" + string(result.Scenario) + "_RECALL", Severity: "CRITICAL",
				Actual: actual, Limit: fmt.Sprintf(">=%.6f", result.Thresholds.MinRecallAtK),
			})
		}
	}
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Severity != alerts[j].Severity {
			return alerts[i].Severity < alerts[j].Severity
		}
		return alerts[i].Code < alerts[j].Code
	})
	return alerts, nil
}

func PercentileMilliseconds(samples []time.Duration, percentile float64) (int64, error) {
	if len(samples) == 0 || percentile < 0 || percentile > 1 {
		return 0, errors.New("capacity percentile input is invalid")
	}
	values := append([]time.Duration(nil), samples...)
	for _, value := range values {
		if value < 0 {
			return 0, errors.New("capacity duration is invalid")
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values))*percentile+0.999999999) - 1
	if index < 0 {
		index = 0
	}
	return values[index].Milliseconds(), nil
}

func validateCapacityScenarioResult(result CapacityScenarioResult) error {
	if !governedCapacityScenario(result.Scenario) || result.Requests <= 0 || result.Succeeded < 0 || result.Failed < 0 ||
		result.Succeeded+result.Failed != result.Requests || result.RateLimited < 0 || result.Degraded < 0 ||
		result.PoolTimeouts < 0 || result.P50MS < 0 || result.P95MS < result.P50MS ||
		result.P99MS < result.P95MS || result.MaxMS < result.P99MS || len(result.FailureCodeHash) != 64 ||
		result.Thresholds.MaxP95MS <= 0 || result.Thresholds.MaxP99MS < 0 ||
		result.Thresholds.MinSuccessRate <= 0 || result.Thresholds.MinSuccessRate > 1 ||
		result.Thresholds.MinRecallAtK < 0 || result.Thresholds.MinRecallAtK > 1 {
		return fmt.Errorf("capacity scenario %s is invalid", result.Scenario)
	}
	if result.Thresholds.MinRecallAtK > 0 && result.Scenario != CapacityVectorRecall {
		return errors.New("recall threshold is only valid for vector recall")
	}
	return nil
}

func architectureScaleAlerts(scale CapacityScale) []CapacityAlert {
	checks := []struct {
		code  string
		value int64
		limit int64
	}{
		{"CAPACITY_TENANT_PARTITION_REVIEW", scale.Tenants, 50},
		{"CAPACITY_GRAPH_DOMAIN_SPACE_REVIEW", scale.Domains, 10},
		{"CAPACITY_VECTOR_SERVICE_REVIEW", scale.SearchDocuments, 2_000_000},
		{"CAPACITY_FULL_MEMBER_POLICY_REVIEW", scale.MaxFullMembersPerDomain, 200_000},
		{"CAPACITY_LOOKUP_PARTITION_REVIEW", scale.MaxOnDemandMembers, 50_000_000},
		{"CAPACITY_REDIS_LAYER_REVIEW", scale.DailyQuestionRuns, 50_000},
		{"CAPACITY_HORIZONTAL_SCALE_REVIEW", scale.PeakConcurrentQuestions, 200},
		{"CAPACITY_REPORT_CDN_REVIEW", scale.PublishedReports, 20_000},
		{"CAPACITY_GRAPH_REPLICA_REVIEW", scale.GraphEdges, 10_000_000},
	}
	alerts := make([]CapacityAlert, 0, len(checks))
	for _, check := range checks {
		if check.value > check.limit {
			alerts = append(alerts, CapacityAlert{
				Code: check.code, Severity: "WARNING",
				Actual: fmt.Sprintf("%d", check.value), Limit: fmt.Sprintf("<=%d", check.limit),
			})
		}
	}
	return alerts
}
