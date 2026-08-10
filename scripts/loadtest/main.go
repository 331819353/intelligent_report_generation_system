package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"intelligent-report-generation-system/internal/askdata/observability"
)

const bearerTokenEnvironment = "ASKDATA_LOADTEST_BEARER_TOKEN"

type loadConfig struct {
	BaseURL       string                      `json:"baseUrl"`
	Seed          int64                       `json:"seed"`
	DatabaseLabel string                      `json:"databaseLabel"`
	GraphLabel    string                      `json:"graphLabel"`
	LLMLabel      string                      `json:"llmLabel"`
	Scale         observability.CapacityScale `json:"scale"`
	Scenarios     []scenarioConfig            `json:"scenarios"`
}

type scenarioConfig struct {
	Scenario    observability.CapacityScenario   `json:"scenario"`
	Method      string                           `json:"method"`
	Path        string                           `json:"path"`
	Headers     map[string]string                `json:"headers,omitempty"`
	Body        json.RawMessage                  `json:"body"`
	Concurrency int                              `json:"concurrency"`
	Requests    int                              `json:"requests"`
	TimeoutMS   int64                            `json:"timeoutMs"`
	Thresholds  observability.CapacityThresholds `json:"thresholds"`
}

type requestObservation struct {
	duration    time.Duration
	succeeded   bool
	rateLimited bool
	degraded    bool
	poolTimeout bool
	failureCode string
	recallAtK   *float64
}

func main() {
	configPath := flag.String("config", "", "path to a governed load-test JSON config")
	outputPath := flag.String("output", "", "path for the capacity JSON report")
	flag.Parse()
	if *configPath == "" || *outputPath == "" {
		fatal(errors.New("both -config and -output are required"))
	}
	raw, err := os.ReadFile(*configPath)
	if err != nil {
		fatal(fmt.Errorf("read config: %w", err))
	}
	config, base, err := parseLoadConfig(raw)
	if err != nil {
		fatal(err)
	}
	startedAt := time.Now().UTC()
	report, err := executeLoad(context.Background(), http.DefaultClient, config, base, raw, startedAt, os.Getenv(bearerTokenEnvironment))
	if err != nil {
		fatal(err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(*outputPath, append(encoded, '\n'), 0o600); err != nil {
		fatal(fmt.Errorf("write report: %w", err))
	}
	alerts, err := observability.EvaluateCapacityAlerts(report)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("capacity report written: scenarios=%d alerts=%d\n", len(report.Scenarios), len(alerts))
}

func parseLoadConfig(raw []byte) (loadConfig, *url.URL, error) {
	var config loadConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return loadConfig{}, nil, fmt.Errorf("decode load config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return loadConfig{}, nil, errors.New("load config must contain one JSON document")
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return loadConfig{}, nil, errors.New("load-test base URL is invalid")
	}
	if config.Seed == 0 || config.DatabaseLabel == "" || config.GraphLabel == "" || config.LLMLabel == "" {
		return loadConfig{}, nil, errors.New("load-test environment metadata is incomplete")
	}
	seen := make(map[observability.CapacityScenario]struct{}, len(config.Scenarios))
	for _, scenario := range config.Scenarios {
		if scenario.Concurrency < 1 || scenario.Concurrency > 512 || scenario.Requests < scenario.Concurrency ||
			scenario.Requests > 10_000_000 || scenario.TimeoutMS < 1 || scenario.TimeoutMS > 120_000 ||
			(scenario.Method != http.MethodGet && scenario.Method != http.MethodPost) ||
			scenario.Thresholds.MaxP95MS <= 0 || scenario.Thresholds.MinSuccessRate <= 0 || scenario.Thresholds.MinSuccessRate > 1 {
			return loadConfig{}, nil, fmt.Errorf("load-test scenario %s is invalid", scenario.Scenario)
		}
		reference, parseErr := url.Parse(scenario.Path)
		if parseErr != nil || reference.IsAbs() || reference.Host != "" || !strings.HasPrefix(reference.Path, "/") || reference.Fragment != "" {
			return loadConfig{}, nil, fmt.Errorf("load-test scenario %s path is invalid", scenario.Scenario)
		}
		if scenario.Method == http.MethodPost && (len(scenario.Body) == 0 || !json.Valid(scenario.Body)) {
			return loadConfig{}, nil, fmt.Errorf("load-test scenario %s body is invalid", scenario.Scenario)
		}
		if err := validateScenarioHeaders(scenario.Headers); err != nil {
			return loadConfig{}, nil, fmt.Errorf("load-test scenario %s headers are invalid: %w", scenario.Scenario, err)
		}
		if _, duplicate := seen[scenario.Scenario]; duplicate {
			return loadConfig{}, nil, fmt.Errorf("load-test scenario %s is duplicated", scenario.Scenario)
		}
		seen[scenario.Scenario] = struct{}{}
	}
	for _, required := range []observability.CapacityScenario{
		observability.CapacityFastPath, observability.CapacityComplexLoop,
		observability.CapacityWarehousePool, observability.CapacityVectorRecall,
		observability.CapacityGraphThreeHops, observability.CapacityLLMDegradation,
		observability.CapacityKPIBundle,
	} {
		if _, exists := seen[required]; !exists {
			return loadConfig{}, nil, fmt.Errorf("load-test scenario %s is missing", required)
		}
	}
	return config, base, nil
}

func executeLoad(
	ctx context.Context,
	client *http.Client,
	config loadConfig,
	base *url.URL,
	rawConfig []byte,
	startedAt time.Time,
	bearerToken string,
) (observability.CapacityReport, error) {
	if ctx == nil || client == nil || base == nil || startedAt.IsZero() {
		return observability.CapacityReport{}, errors.New("load-test dependencies are invalid")
	}
	configDigest := sha256.Sum256(rawConfig)
	report := observability.CapacityReport{
		SchemaVersion: observability.CapacityReportSchemaVersion,
		RunID:         startedAt.Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(configDigest[:6]),
		Seed:          config.Seed, StartedAt: startedAt.UTC(), Scale: config.Scale,
		Environment: observability.CapacityEnvironment{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(),
			GoVersion: runtime.Version(), ConfigHash: hex.EncodeToString(configDigest[:]),
			TargetOrigin:  base.Scheme + "://" + base.Host,
			DatabaseLabel: config.DatabaseLabel, GraphLabel: config.GraphLabel, LLMLabel: config.LLMLabel,
		},
	}
	for _, scenario := range config.Scenarios {
		result, err := executeScenario(ctx, client, base, scenario, config.Seed, bearerToken)
		if err != nil {
			return observability.CapacityReport{}, err
		}
		report.Scenarios = append(report.Scenarios, result)
	}
	report.DurationMS = time.Since(startedAt).Milliseconds()
	if report.DurationMS < 1 {
		report.DurationMS = 1
	}
	if err := observability.ValidateCapacityReport(report); err != nil {
		return observability.CapacityReport{}, err
	}
	return report, nil
}

func executeScenario(
	ctx context.Context,
	client *http.Client,
	base *url.URL,
	config scenarioConfig,
	seed int64,
	bearerToken string,
) (observability.CapacityScenarioResult, error) {
	reference, _ := url.Parse(config.Path)
	target := base.ResolveReference(reference)
	jobs := make(chan int)
	observations := make(chan requestObservation, config.Requests)
	var workers sync.WaitGroup
	for index := 0; index < config.Concurrency; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for requestIndex := range jobs {
				observations <- executeRequest(ctx, client, target.String(), config, seed, requestIndex, bearerToken)
			}
		}()
	}
	go func() {
		for index := 0; index < config.Requests; index++ {
			jobs <- index
		}
		close(jobs)
		workers.Wait()
		close(observations)
	}()
	result := observability.CapacityScenarioResult{
		Scenario: config.Scenario, Requests: int64(config.Requests), Thresholds: config.Thresholds,
	}
	durations := make([]time.Duration, 0, config.Requests)
	failureCodes := make(map[string]int64)
	var recallTotal float64
	var recallCount int64
	for observation := range observations {
		durations = append(durations, observation.duration)
		if observation.succeeded {
			result.Succeeded++
		} else {
			result.Failed++
			failureCodes[observation.failureCode]++
		}
		if observation.rateLimited {
			result.RateLimited++
		}
		if observation.degraded {
			result.Degraded++
		}
		if observation.poolTimeout {
			result.PoolTimeouts++
		}
		if observation.recallAtK != nil {
			recallTotal += *observation.recallAtK
			recallCount++
		}
	}
	var err error
	if result.P50MS, err = observability.PercentileMilliseconds(durations, .50); err != nil {
		return observability.CapacityScenarioResult{}, err
	}
	result.P95MS, _ = observability.PercentileMilliseconds(durations, .95)
	result.P99MS, _ = observability.PercentileMilliseconds(durations, .99)
	result.MaxMS, _ = observability.PercentileMilliseconds(durations, 1)
	if recallCount > 0 {
		value := recallTotal / float64(recallCount)
		result.RecallAtK = &value
	}
	result.FailureCodeHash = hashFailureCodes(failureCodes)
	return result, nil
}

func executeRequest(
	ctx context.Context,
	client *http.Client,
	target string,
	config scenarioConfig,
	seed int64,
	requestIndex int,
	bearerToken string,
) requestObservation {
	requestContext, cancel := context.WithTimeout(ctx, time.Duration(config.TimeoutMS)*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, config.Method, target, bytes.NewReader(config.Body))
	if err != nil {
		return requestObservation{failureCode: "REQUEST_BUILD"}
	}
	if config.Method == http.MethodPost {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", loadRequestKey(seed, config.Scenario, requestIndex))
	}
	for name, value := range config.Headers {
		request.Header.Set(name, value)
	}
	if bearerToken != "" {
		request.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	startedAt := time.Now()
	response, err := client.Do(request)
	duration := time.Since(startedAt)
	if err != nil {
		code := "TRANSPORT_ERROR"
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			code = "TIMEOUT"
		}
		return requestObservation{duration: duration, failureCode: code, poolTimeout: code == "TIMEOUT" && config.Scenario == observability.CapacityWarehousePool}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	succeeded := response.StatusCode >= 200 && response.StatusCode < 400
	code := strings.TrimSpace(response.Header.Get("X-AskData-Error-Code"))
	if code == "" && !succeeded {
		code = "HTTP_" + strconv.Itoa(response.StatusCode)
	}
	observation := requestObservation{
		duration: duration, succeeded: succeeded, failureCode: code,
		rateLimited: response.StatusCode == http.StatusTooManyRequests,
		degraded:    strings.EqualFold(response.Header.Get("X-AskData-Degraded"), "true"),
		poolTimeout: strings.EqualFold(response.Header.Get("X-AskData-Pool-Timeout"), "true"),
	}
	if value, parseErr := strconv.ParseFloat(response.Header.Get("X-AskData-Recall-At-K"), 64); parseErr == nil && value >= 0 && value <= 1 {
		observation.recallAtK = &value
	}
	return observation
}

func validateScenarioHeaders(headers map[string]string) error {
	if len(headers) > 16 {
		return errors.New("at most 16 headers are allowed")
	}
	for name, value := range headers {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if canonical == "" || canonical != name || len(name) > 64 || len(value) > 1024 ||
			strings.ContainsAny(name+value, "\r\n\x00") {
			return errors.New("header name or value is unsafe")
		}
		switch canonical {
		case "Authorization", "Cookie", "Proxy-Authorization", "Idempotency-Key",
			"Content-Length", "Transfer-Encoding", "Host":
			return errors.New("authentication, idempotency and transport headers are managed by the runner")
		}
	}
	return nil
}

func loadRequestKey(seed int64, scenario observability.CapacityScenario, requestIndex int) string {
	preimage := fmt.Sprintf("askdata-capacity-v1\x00%d\x00%s\x00%d", seed, scenario, requestIndex)
	digest := sha256.Sum256([]byte(preimage))
	return "capacity-" + hex.EncodeToString(digest[:16])
}

func hashFailureCodes(codes map[string]int64) string {
	keys := make([]string, 0, len(codes))
	for code := range codes {
		keys = append(keys, code)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, code := range keys {
		_, _ = fmt.Fprintf(hash, "%s=%d\n", code, codes[code])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "askdata capacity load test:", err)
	os.Exit(1)
}
