package observability

import (
	"fmt"
	"sort"
	"strings"
)

// The capacity contract in 04 §6.1 has two halves. The first is a load test,
// which any machine can run. The second is a signature, which only the target
// environment can earn. Keeping them in one report and one verdict is the whole
// point: a green load test on a laptop is not a capacity conclusion, and the
// only safe way to say so is to make the report say it itself.

// CapacityScopeProfile names which product surface a run covers. A run must
// declare it, because "P95 is fine" means nothing without knowing what was
// under load.
type CapacityScopeProfile string

const (
	// CapacityScopeP1ReportAskData is the phase-one surface: platform access,
	// report design/publish/run/export and AskData.
	CapacityScopeP1ReportAskData CapacityScopeProfile = "P1_REPORT_ASKDATA"
	// CapacityScopeFullPlatform adds scheduled dispatch and the decision loop.
	// It does not gate phase one.
	CapacityScopeFullPlatform CapacityScopeProfile = "FULL_PLATFORM"
)

// SignabilityNotSigned is the fixed verdict a run reports when any input the
// signature depends on is absent. It is deliberately a single stable code:
// a partially-signed capacity conclusion is not a weaker conclusion, it is
// not a conclusion.
const (
	SignabilityNotSigned = "POC_NOT_SIGNED"
	SignabilitySignable  = "SIGNABLE"
)

// CapacityFault enumerates the failure modes 04 §6.1 requires a signed run to
// have injected. The list is closed: adding a fault is a contract change.
type CapacityFault string

const (
	CapacityFaultAPIInstanceLoss         CapacityFault = "API_INSTANCE_LOSS"
	CapacityFaultWorkerRestart           CapacityFault = "WORKER_RESTART"
	CapacityFaultControlDatabaseFailover CapacityFault = "CONTROL_DATABASE_FAILOVER"
	CapacityFaultWarehouseExhaustion     CapacityFault = "WAREHOUSE_TIMEOUT_OR_POOL_EXHAUSTION"
	CapacityFaultObjectStorageDown       CapacityFault = "OBJECT_STORAGE_UNAVAILABLE"
	CapacityFaultGraphDown               CapacityFault = "GRAPH_UNAVAILABLE"
	CapacityFaultProviderRateLimited     CapacityFault = "PROVIDER_RATE_LIMITED"
)

var requiredCapacityFaults = []CapacityFault{
	CapacityFaultAPIInstanceLoss, CapacityFaultWorkerRestart, CapacityFaultControlDatabaseFailover,
	CapacityFaultWarehouseExhaustion, CapacityFaultObjectStorageDown, CapacityFaultGraphDown,
	CapacityFaultProviderRateLimited,
}

// CapacityConnectionPool names the independent connection budgets. They are
// separate on purpose: one exhausted pool must not be able to starve another,
// and a run that cannot show four budgets cannot show that it did not.
type CapacityConnectionPool string

const (
	CapacityPoolAPI           CapacityConnectionPool = "API"
	CapacityPoolAskData       CapacityConnectionPool = "ASKDATA"
	CapacityPoolReportRuntime CapacityConnectionPool = "REPORT_RUNTIME"
	CapacityPoolExport        CapacityConnectionPool = "EXPORT"
)

var requiredCapacityPools = []CapacityConnectionPool{
	CapacityPoolAPI, CapacityPoolAskData, CapacityPoolReportRuntime, CapacityPoolExport,
}

// CapacityFaultResult records one injection. DegradedGracefully means the
// platform returned a stable, visible failure or degradation code; it must
// never mean "the task disappeared".
type CapacityFaultResult struct {
	Fault              CapacityFault `json:"fault"`
	Injected           bool          `json:"injected"`
	DegradedGracefully bool          `json:"degradedGracefully"`
	StableFailureCode  string        `json:"stableFailureCode"`
	RecoverySeconds    int64         `json:"recoverySeconds"`
	DroppedTasks       int64         `json:"droppedTasks"`
}

type CapacityConnectionBudget struct {
	Pool            CapacityConnectionPool `json:"pool"`
	MaxConnections  int64                  `json:"maxConnections"`
	AdmissionLimit  int64                  `json:"admissionLimit"`
	PeakInUse       int64                  `json:"peakInUse"`
	AdmissionDenied int64                  `json:"admissionDenied"`
}

type CapacityResourceWatermark struct {
	Resource   string  `json:"resource"`
	PeakUsage  float64 `json:"peakUsage"`
	Limit      float64 `json:"limit"`
	SampleSize int64   `json:"sampleSize"`
}

// CapacitySignoff is the human half. It is a declaration by a named owner about
// a named environment, not something the runner can compute.
type CapacitySignoff struct {
	TargetEnvironment string `json:"targetEnvironment"`
	OwnerActorID      string `json:"ownerActorId"`
	SignedAt          string `json:"signedAt"`
	EvidenceReference string `json:"evidenceReference"`
	AlertDisposition  string `json:"alertDisposition"`
}

// CapacitySignability is the verdict. MissingInputs is ordered and stable so
// two runs of the same incomplete environment produce the same list.
type CapacitySignability struct {
	Verdict       string   `json:"verdict"`
	ScopeProfile  string   `json:"scopeProfile"`
	MissingInputs []string `json:"missingInputs"`
}

// EvaluateCapacitySignability decides whether a report may be presented as a
// capacity conclusion for a target environment. It never upgrades a verdict on
// the strength of good latency numbers: latency is what the run measured, not
// what the signature attests.
func EvaluateCapacitySignability(report CapacityReport) CapacitySignability {
	missing := make([]string, 0, 8)
	if report.ScopeProfile == "" {
		missing = append(missing, "scopeProfile")
	}
	if report.Signoff == nil {
		missing = append(missing, "signoff")
	} else {
		signoff := report.Signoff
		if strings.TrimSpace(signoff.TargetEnvironment) == "" {
			missing = append(missing, "signoff.targetEnvironment")
		}
		if strings.TrimSpace(signoff.OwnerActorID) == "" {
			missing = append(missing, "signoff.ownerActorId")
		}
		if strings.TrimSpace(signoff.SignedAt) == "" {
			missing = append(missing, "signoff.signedAt")
		}
		if strings.TrimSpace(signoff.EvidenceReference) == "" {
			missing = append(missing, "signoff.evidenceReference")
		}
		if strings.TrimSpace(signoff.AlertDisposition) == "" {
			missing = append(missing, "signoff.alertDisposition")
		}
	}
	budgets := make(map[CapacityConnectionPool]struct{}, len(report.ConnectionBudgets))
	for _, budget := range report.ConnectionBudgets {
		budgets[budget.Pool] = struct{}{}
	}
	for _, pool := range requiredCapacityPools {
		if _, present := budgets[pool]; !present {
			missing = append(missing, "connectionBudgets."+string(pool))
		}
	}
	faults := make(map[CapacityFault]CapacityFaultResult, len(report.FaultInjection))
	for _, result := range report.FaultInjection {
		faults[result.Fault] = result
	}
	for _, fault := range requiredCapacityFaults {
		result, present := faults[fault]
		switch {
		case !present || !result.Injected:
			missing = append(missing, "faultInjection."+string(fault))
		case !result.DegradedGracefully || result.DroppedTasks > 0 || result.StableFailureCode == "":
			// A fault that silently dropped work is not a passed drill. It is
			// reported as missing evidence rather than as a latency alert,
			// because no amount of throughput compensates for a lost task.
			missing = append(missing, "faultInjection."+string(fault)+".degradation")
		}
	}
	if len(report.ResourceWatermarks) == 0 {
		missing = append(missing, "resourceWatermarks")
	}
	sort.Strings(missing)
	verdict := SignabilitySignable
	if len(missing) != 0 {
		verdict = SignabilityNotSigned
	}
	return CapacitySignability{
		Verdict: verdict, ScopeProfile: string(report.ScopeProfile), MissingInputs: missing,
	}
}

func validCapacityScopeProfile(value CapacityScopeProfile) bool {
	return value == CapacityScopeP1ReportAskData || value == CapacityScopeFullPlatform
}

func validateCapacityFaultResults(results []CapacityFaultResult) error {
	seen := make(map[CapacityFault]struct{}, len(results))
	governed := make(map[CapacityFault]struct{}, len(requiredCapacityFaults))
	for _, fault := range requiredCapacityFaults {
		governed[fault] = struct{}{}
	}
	for _, result := range results {
		if _, known := governed[result.Fault]; !known {
			return fmt.Errorf("capacity fault %s is not governed", result.Fault)
		}
		if _, duplicate := seen[result.Fault]; duplicate {
			return fmt.Errorf("capacity fault %s is duplicated", result.Fault)
		}
		seen[result.Fault] = struct{}{}
		if result.RecoverySeconds < 0 || result.DroppedTasks < 0 || len(result.StableFailureCode) > 64 {
			return fmt.Errorf("capacity fault %s result is invalid", result.Fault)
		}
	}
	return nil
}

func validateCapacityConnectionBudgets(budgets []CapacityConnectionBudget) error {
	seen := make(map[CapacityConnectionPool]struct{}, len(budgets))
	governed := make(map[CapacityConnectionPool]struct{}, len(requiredCapacityPools))
	for _, pool := range requiredCapacityPools {
		governed[pool] = struct{}{}
	}
	for _, budget := range budgets {
		if _, known := governed[budget.Pool]; !known {
			return fmt.Errorf("capacity connection pool %s is not governed", budget.Pool)
		}
		if _, duplicate := seen[budget.Pool]; duplicate {
			return fmt.Errorf("capacity connection pool %s is duplicated", budget.Pool)
		}
		seen[budget.Pool] = struct{}{}
		if budget.MaxConnections < 1 || budget.AdmissionLimit < 1 || budget.AdmissionLimit > budget.MaxConnections ||
			budget.PeakInUse < 0 || budget.PeakInUse > budget.MaxConnections || budget.AdmissionDenied < 0 {
			return fmt.Errorf("capacity connection budget %s is invalid", budget.Pool)
		}
	}
	return nil
}

func validateCapacityResourceWatermarks(watermarks []CapacityResourceWatermark) error {
	seen := make(map[string]struct{}, len(watermarks))
	for _, watermark := range watermarks {
		resource := strings.TrimSpace(watermark.Resource)
		if resource == "" || len(resource) > 64 || watermark.PeakUsage < 0 ||
			watermark.Limit <= 0 || watermark.SampleSize < 1 {
			return fmt.Errorf("capacity resource watermark %q is invalid", watermark.Resource)
		}
		if _, duplicate := seen[resource]; duplicate {
			return fmt.Errorf("capacity resource watermark %q is duplicated", resource)
		}
		seen[resource] = struct{}{}
	}
	return nil
}
