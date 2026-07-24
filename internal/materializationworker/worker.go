package materializationworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"intelligent-report-generation-system/internal/materialization"
	"intelligent-report-generation-system/internal/warehouse"
)

type Worker struct {
	store             Store
	resolver          Resolver
	builder           Builder
	heartbeatInterval func(time.Duration) time.Duration
}

func NewWorker(store Store, resolver Resolver, builder Builder) *Worker {
	return &Worker{
		store: store, resolver: resolver, builder: builder,
		heartbeatInterval: defaultHeartbeatInterval,
	}
}

func (worker *Worker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil {
		return nil, fmt.Errorf("materialization worker is not configured")
	}
	return worker.store.ListTenantIDs(ctx)
}

// ProcessNext claims and fully closes at most one registered build. A
// deterministic contract/execution failure is durably recorded and therefore
// returns processed=true with nil error; infrastructure and lease failures are
// returned so polling can surface them and the lease can be retried safely.
func (worker *Worker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil {
		return false, fmt.Errorf("materialization worker is not configured")
	}
	if err := validateDependencies(worker.store, worker.resolver, worker.builder); err != nil {
		return false, err
	}
	claim, err := worker.store.Claim(ctx, tenantID, workerID, lease)
	if err != nil {
		return false, err
	}
	if claim == nil {
		return false, nil
	}

	workCtx, cancelWork := context.WithCancel(ctx)
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan error, 1)
	go worker.heartbeat(heartbeatCtx, cancelWork, *claim, lease, heartbeatDone)

	resolved, err := worker.resolver.Resolve(workCtx, *claim)
	var activation materialization.Activation
	var failureQuality []materialization.QualityResult
	if err == nil {
		activation, failureQuality, err = worker.execute(workCtx, *claim, resolved)
	}
	if err != nil {
		if completed, heartbeatErr := pollHeartbeatResult(heartbeatDone); completed && heartbeatErr != nil {
			stopHeartbeat()
			cancelWork()
			return true, heartbeatErr
		} else if completed {
			stopHeartbeat()
			cancelWork()
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			return true, err
		}
		if ctx.Err() != nil {
			stopHeartbeat()
			cancelWork()
			<-heartbeatDone
			return true, ctx.Err()
		}
		var execution *ExecutionError
		if !errors.As(err, &execution) {
			stopHeartbeat()
			cancelWork()
			heartbeatErr := <-heartbeatDone
			if heartbeatErr != nil {
				return true, heartbeatErr
			}
			return true, err
		}
		failErr := worker.store.Fail(
			ctx, *claim, execution.Code, execution.Message, failureQuality,
		)
		stopHeartbeat()
		cancelWork()
		heartbeatErr := <-heartbeatDone
		if failErr == nil {
			// A successful terminal write necessarily invalidates any heartbeat
			// racing behind it; that ErrLeaseLost is expected and ignored.
			return true, nil
		}
		if errors.Is(failErr, materialization.ErrLeaseLost) && heartbeatErr != nil {
			return true, heartbeatErr
		}
		return true, failErr
	}

	_, activateErr := worker.store.Activate(ctx, *claim, activation)
	stopHeartbeat()
	cancelWork()
	heartbeatErr := <-heartbeatDone
	if activateErr == nil || errors.Is(activateErr, materialization.ErrQualityGateFailed) {
		return true, nil
	}
	if errors.Is(activateErr, materialization.ErrLeaseLost) {
		if heartbeatErr != nil {
			return true, heartbeatErr
		}
		return true, activateErr
	}
	// Activation is transactional. Domain conflicts are deterministic and can
	// be closed immediately; transport/database errors keep the run leased until
	// expiry so another attempt can retry the same immutable shadow safely.
	if errors.Is(activateErr, materialization.ErrConflict) ||
		errors.Is(activateErr, materialization.ErrInvalidTransition) ||
		errors.Is(activateErr, materialization.ErrInvalidRequest) {
		failErr := worker.store.Fail(
			ctx, *claim,
			CodeMaterializationActivationFailed,
			"the completed warehouse build could not be activated",
			activation.Quality,
		)
		if failErr == nil {
			return true, nil
		}
		return true, failErr
	}
	if heartbeatErr != nil {
		return true, heartbeatErr
	}
	return true, activateErr
}

func (worker *Worker) heartbeat(
	ctx context.Context,
	cancelWork context.CancelFunc,
	claim materialization.Claim,
	lease time.Duration,
	done chan<- error,
) {
	interval := worker.heartbeatInterval(lease)
	if interval <= 0 {
		interval = defaultHeartbeatInterval(lease)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			done <- nil
			return
		case <-timer.C:
			updated, err := worker.store.Heartbeat(ctx, claim, lease)
			if err != nil {
				cancelWork()
				done <- err
				return
			}
			claim = updated
			timer.Reset(interval)
		}
	}
}

func pollHeartbeatResult(done <-chan error) (bool, error) {
	select {
	case err := <-done:
		return true, err
	default:
		return false, nil
	}
}

func defaultHeartbeatInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < 250*time.Millisecond {
		return 250 * time.Millisecond
	}
	if interval > time.Minute {
		return time.Minute
	}
	return interval
}

func (worker *Worker) execute(
	ctx context.Context,
	claim materialization.Claim,
	resolved ResolvedBuild,
) (materialization.Activation, []materialization.QualityResult, error) {
	nodes, err := topologicalNodes(claim.Plan)
	if err != nil {
		return materialization.Activation{}, nil, executionError(
			CodeTrustedPlanInvalid,
			"the registered build topology is invalid",
			err,
		)
	}
	var buildResult warehouse.BuildResult
	materializeNodeID := ""
	nodeOutputs := map[string]*int64{}
	for _, node := range nodes {
		if err := worker.store.StartNode(ctx, claim, node.ID); err != nil {
			return materialization.Activation{}, nil, err
		}
		inputRows := nodeInputRowCount(node, resolved.InputRowCount, nodeOutputs)
		if node.Kind != materialization.NodeMaterialize {
			outputRows := cloneCount(inputRows)
			if node.Kind == materialization.NodeFilter ||
				node.Kind == materialization.NodeJoin ||
				node.Kind == materialization.NodeAggregate {
				outputRows = nil
			}
			if err := worker.store.FinishNode(ctx, claim, node.ID, materialization.NodeResult{
				Status:        materialization.NodeSucceeded,
				InputRowCount: inputRows, OutputRowCount: outputRows,
			}); err != nil {
				return materialization.Activation{}, nil, err
			}
			nodeOutputs[node.ID] = outputRows
			continue
		}

		materializeNodeID = node.ID
		buildCtx, cancelBuild := context.WithTimeout(
			ctx,
			time.Duration(resolved.Document.ExecutionPolicy.TimeoutMS)*time.Millisecond,
		)
		buildResult, err = worker.builder.Build(buildCtx, warehouse.BuildInput{
			TenantID: claim.TenantID, RunID: claim.ID,
			DatasetID: claim.DatasetID, DatasetVersionID: claim.DatasetVersionID,
			Layer: string(claim.Layer), Document: resolved.Document,
			Tables: resolved.Tables, Parameters: nil, RequireRows: false,
			BusinessKeyCode: append([]string(nil), resolved.Document.OutputGrain.KeyFields...),
		})
		cancelBuild()
		if err != nil {
			if ctx.Err() != nil {
				return materialization.Activation{}, nil, err
			}
			if errors.Is(err, context.DeadlineExceeded) {
				return materialization.Activation{}, nil, executionError(
					CodeWarehouseBuildTimeout,
					"the warehouse build exceeded the published execution timeout",
					err,
				)
			}
			if errors.Is(err, context.Canceled) {
				return materialization.Activation{}, nil, err
			}
			if errors.Is(err, warehouse.ErrQualityFailed) {
				quality := failedGrainQuality(node.ID)
				return materialization.Activation{}, quality, executionError(
					CodeQualityGateFailed,
					"the warehouse output failed its declared grain quality gate",
					err,
				)
			}
			code := CodeWarehouseBuildFailed
			message := "the trusted warehouse build failed"
			if errors.Is(err, warehouse.ErrInvalidBuild) {
				code = CodeTrustedPlanInvalid
				message = "the published dataset could not be compiled as a trusted warehouse build"
			}
			return materialization.Activation{}, nil, executionError(code, message, err)
		}
		expected, identifierErr := materialization.GeneratePhysicalIdentifier(
			claim.TenantID, claim.DatasetID, claim.ID, claim.Layer,
		)
		if identifierErr != nil ||
			buildResult.Schema != expected.Schema ||
			buildResult.Table != expected.Name ||
			buildResult.RowCount < 0 ||
			buildResult.SizeBytes < 0 {
			return materialization.Activation{}, nil, executionError(
				CodeWarehouseBuildFailed,
				"the warehouse executor returned an invalid build result",
				identifierErr,
			)
		}
		outputRows := buildResult.RowCount
		outputSize := buildResult.SizeBytes
		if err := worker.store.FinishNode(ctx, claim, node.ID, materialization.NodeResult{
			Status:        materialization.NodeSucceeded,
			InputRowCount: inputRows, OutputRowCount: &outputRows,
			OutputSizeBytes: &outputSize,
		}); err != nil {
			return materialization.Activation{}, nil, err
		}
		nodeOutputs[node.ID] = &outputRows
	}
	if materializeNodeID == "" {
		return materialization.Activation{}, nil, executionError(
			CodeTrustedPlanInvalid,
			"the registered build has no materialization node",
			nil,
		)
	}

	physical, err := materialization.GeneratePhysicalIdentifier(
		claim.TenantID, claim.DatasetID, claim.ID, claim.Layer,
	)
	if err != nil {
		return materialization.Activation{}, nil, executionError(
			CodeWarehouseBuildFailed,
			"the warehouse target identity is invalid",
			err,
		)
	}
	quality := successfulQuality(
		materializeNodeID,
		buildResult.RowCount,
		resolved.Document.OutputGrain.KeyFields,
	)
	snapshotHash := outputSnapshotHash(
		claim, resolved.SchemaHash, buildResult.RowCount, buildResult.SizeBytes,
	)
	watermark, _ := json.Marshal(map[string]string{
		"buildRunId":        claim.ID,
		"inputSnapshotHash": claim.InputSnapshotHash,
		"planHash":          claim.PlanHash,
	})
	return materialization.Activation{
		Physical: physical, RelationKind: claim.Plan.Target.RelationKind,
		SchemaHash: resolved.SchemaHash, SnapshotHash: snapshotHash,
		RowCount: buildResult.RowCount, SizeBytes: buildResult.SizeBytes,
		Watermark: watermark, Quality: quality,
	}, nil, nil
}

func topologicalNodes(plan materialization.BuildPlan) ([]materialization.PlanNode, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	emitted := make(map[string]bool, len(plan.Nodes))
	result := make([]materialization.PlanNode, 0, len(plan.Nodes))
	for len(result) < len(plan.Nodes) {
		progress := false
		for _, node := range plan.Nodes {
			if emitted[node.ID] {
				continue
			}
			ready := true
			for _, dependency := range node.DependsOn {
				if !emitted[dependency] {
					ready = false
					break
				}
			}
			if ready {
				result = append(result, node)
				emitted[node.ID] = true
				progress = true
			}
		}
		if !progress {
			return nil, fmt.Errorf("build plan contains a dependency cycle")
		}
	}
	return result, nil
}

func nodeInputRowCount(
	node materialization.PlanNode,
	inputs map[int]int64,
	nodeOutputs map[string]*int64,
) *int64 {
	var total int64
	hasInput := false
	for _, ordinal := range node.InputOrdinals {
		value, ok := inputs[ordinal]
		if !ok || value > math.MaxInt64-total {
			return nil
		}
		total += value
		hasInput = true
	}
	for _, dependency := range node.DependsOn {
		value := nodeOutputs[dependency]
		if value == nil || *value > math.MaxInt64-total {
			return nil
		}
		total += *value
		hasInput = true
	}
	if !hasInput {
		return nil
	}
	return &total
}

func cloneCount(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func successfulQuality(
	nodeID string,
	rowCount int64,
	keys []string,
) []materialization.QualityResult {
	rowObserved, _ := json.Marshal(map[string]int64{"rowCount": rowCount})
	keyExpectation, _ := json.Marshal(map[string]int{"keyFieldCount": len(keys)})
	return []materialization.QualityResult{
		{
			NodeID:   nodeID,
			RuleCode: "ROW_COUNT_NONNEGATIVE", RuleVersion: "1",
			RuleDefinitionHash: ruleHash("ROW_COUNT_NONNEGATIVE", "row_count >= 0"),
			Scope:              "DATASET", Severity: materialization.QualityError,
			Status:      materialization.QualityPassed,
			Expectation: json.RawMessage(`{"minimum":0}`),
			Observed:    rowObserved,
			Message:     "warehouse output row count is valid",
		},
		{
			NodeID:   nodeID,
			RuleCode: "OUTPUT_GRAIN_UNIQUE_NOT_NULL", RuleVersion: "1",
			RuleDefinitionHash: ruleHash(
				"OUTPUT_GRAIN_UNIQUE_NOT_NULL",
				"declared output grain keys are non-null and unique",
			),
			Scope: "DATASET", Severity: materialization.QualityError,
			Status:      materialization.QualityPassed,
			Expectation: keyExpectation,
			Observed:    json.RawMessage(`{"duplicateRows":0,"nullRows":0}`),
			Message:     "declared output grain passed uniqueness and null checks",
		},
	}
}

func failedGrainQuality(nodeID string) []materialization.QualityResult {
	return []materialization.QualityResult{{
		NodeID:   nodeID,
		RuleCode: "OUTPUT_GRAIN_UNIQUE_NOT_NULL", RuleVersion: "1",
		RuleDefinitionHash: ruleHash(
			"OUTPUT_GRAIN_UNIQUE_NOT_NULL",
			"declared output grain keys are non-null and unique",
		),
		Scope: "DATASET", Severity: materialization.QualityError,
		Status:      materialization.QualityFailed,
		Expectation: json.RawMessage(`{"duplicateRows":0,"nullRows":0}`),
		Observed:    json.RawMessage(`{"passed":false}`),
		Message:     "declared output grain failed uniqueness or null checks",
	}}
}

func ruleHash(code, definition string) string {
	sum := sha256.Sum256([]byte("materialization-quality-rule-v1\x00" + code + "\x00" + definition))
	return hex.EncodeToString(sum[:])
}

func outputSnapshotHash(
	claim materialization.Claim,
	schemaHash string,
	rowCount, sizeBytes int64,
) string {
	document, _ := json.Marshal(struct {
		Version           string `json:"version"`
		RunID             string `json:"runId"`
		DatasetVersionID  string `json:"datasetVersionId"`
		PlanHash          string `json:"planHash"`
		InputSnapshotHash string `json:"inputSnapshotHash"`
		SchemaHash        string `json:"schemaHash"`
		RowCount          int64  `json:"rowCount"`
		SizeBytes         int64  `json:"sizeBytes"`
	}{
		Version: "1", RunID: claim.ID, DatasetVersionID: claim.DatasetVersionID,
		PlanHash: claim.PlanHash, InputSnapshotHash: claim.InputSnapshotHash,
		SchemaHash: schemaHash, RowCount: rowCount, SizeBytes: sizeBytes,
	})
	sum := sha256.Sum256(document)
	return hex.EncodeToString(sum[:])
}
