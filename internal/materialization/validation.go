package materialization

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

var (
	nodeIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	partitionPattern   = regexp.MustCompile(`^[A-Za-z0-9_.:/=-]*$`)
	hashPattern        = regexp.MustCompile(`^[0-9a-f]{64}$`)
	ruleCodePattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
	forbiddenJSONField = map[string]struct{}{
		"sql": {}, "rawsql": {}, "query": {}, "statement": {},
		"password": {}, "secret": {}, "credentials": {},
		"rows": {}, "samplerows": {}, "rawdata": {},
	}
)

func validLayer(layer Layer) bool {
	return layer == LayerODS || layer == LayerDWD || layer == LayerDWS
}

func validMode(mode RunMode) bool {
	return mode == RunModeFull || mode == RunModeIncremental || mode == RunModeBackfill
}

func validNodeKind(kind NodeKind) bool {
	switch kind {
	case NodeExtract, NodeStage, NodeProject, NodeFilter, NodeJoin, NodeAggregate, NodeMaterialize:
		return true
	default:
		return false
	}
}

func validEngine(engine ExecutionEngine) bool {
	return engine == EngineSourceDB || engine == EnginePostgres
}

// CanTransition reports the only legal build lifecycle edges. Reclaiming an
// expired RUNNING lease is a fenced RUNNING -> RUNNING transition.
func CanTransition(from, to RunStatus) bool {
	switch from {
	case RunQueued:
		return to == RunRunning || to == RunCancelled
	case RunRunning:
		return to == RunRunning || to == RunSucceeded || to == RunFailed || to == RunCancelled
	default:
		return false
	}
}

func CanTransitionNode(from, to NodeStatus) bool {
	switch from {
	case NodePending:
		return to == NodeRunning
	case NodeRunning:
		return to == NodeSucceeded || to == NodeFailed || to == NodeSkipped
	default:
		return false
	}
}

func (plan BuildPlan) Validate() error {
	if plan.Version != PlanVersion || !validUUID(plan.DatasetID) || !validUUID(plan.DatasetVersionID) ||
		!validLayer(plan.Layer) || !validMode(plan.Mode) || len(plan.Nodes) == 0 || len(plan.Nodes) > 256 {
		return ErrInvalidRequest
	}
	if plan.Target.Storage != "POSTGRES" || !plan.Target.AtomicPublish ||
		(plan.Target.RelationKind != "TABLE" && plan.Target.RelationKind != "PARTITIONED_TABLE") ||
		plan.Target.RefreshMode != string(plan.Mode) || !plan.Target.StableViewName {
		return ErrInvalidRequest
	}

	nodes := make(map[string]PlanNode, len(plan.Nodes))
	materializeID := ""
	hasAggregate := false
	for _, node := range plan.Nodes {
		if !nodeIDPattern.MatchString(node.ID) || !validNodeKind(node.Kind) || !validEngine(node.Engine) {
			return ErrInvalidRequest
		}
		if _, exists := nodes[node.ID]; exists {
			return ErrInvalidRequest
		}
		nodes[node.ID] = node
		if node.Kind == NodeMaterialize {
			if materializeID != "" || node.Engine != EnginePostgres {
				return ErrInvalidRequest
			}
			materializeID = node.ID
		}
		if node.Kind == NodeAggregate {
			hasAggregate = true
		}
		if node.Kind == NodeExtract {
			if len(node.DependsOn) != 0 || len(node.InputOrdinals) == 0 {
				return ErrInvalidRequest
			}
		} else if len(node.DependsOn) == 0 || len(node.InputOrdinals) != 0 {
			return ErrInvalidRequest
		}
		if plan.Layer == LayerDWS && node.Engine != EnginePostgres {
			return ErrInvalidRequest
		}
		if (plan.Layer == LayerODS || plan.Layer == LayerDWD) && node.Kind == NodeAggregate {
			return ErrInvalidRequest
		}
		if plan.Layer == LayerODS && node.Kind == NodeJoin {
			return ErrInvalidRequest
		}
	}
	if materializeID == "" || (plan.Layer == LayerDWS && !hasAggregate) {
		return ErrInvalidRequest
	}

	visiting := make(map[string]bool, len(nodes))
	visited := make(map[string]bool, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return ErrInvalidRequest
		}
		if visited[id] {
			return nil
		}
		node, ok := nodes[id]
		if !ok {
			return ErrInvalidRequest
		}
		visiting[id] = true
		for _, dependency := range node.DependsOn {
			if dependency == id {
				return ErrInvalidRequest
			}
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	if err := visit(materializeID); err != nil || len(visited) != len(nodes) {
		return ErrInvalidRequest
	}
	return nil
}

func (request RegisterRequest) Validate() error {
	if err := request.Plan.Validate(); err != nil {
		return err
	}
	if len(request.Inputs) == 0 || len(request.Inputs) > 128 ||
		request.MaxAttempts < 1 || request.MaxAttempts > 10 ||
		len(request.PartitionKey) > 256 || request.PartitionKey != strings.TrimSpace(request.PartitionKey) ||
		!partitionPattern.MatchString(request.PartitionKey) {
		return ErrInvalidRequest
	}
	inputs := append([]InputSnapshot(nil), request.Inputs...)
	sort.Slice(inputs, func(i, j int) bool { return inputs[i].Ordinal < inputs[j].Ordinal })
	for index, input := range inputs {
		if input.Ordinal != index+1 {
			return ErrInvalidRequest
		}
		if err := validateInput(input, request.Plan.Layer); err != nil {
			return err
		}
	}
	if request.Plan.Layer == LayerODS && len(inputs) != 1 {
		return ErrInvalidRequest
	}
	for _, node := range request.Plan.Nodes {
		for _, ordinal := range node.InputOrdinals {
			if ordinal < 1 || ordinal > len(inputs) {
				return ErrInvalidRequest
			}
		}
	}
	return nil
}

func validateInput(input InputSnapshot, target Layer) error {
	if input.Ordinal < 1 || len(input.SourceVersion) == 0 || len(input.SourceVersion) > 256 ||
		input.SourceVersion != strings.TrimSpace(input.SourceVersion) ||
		!hashPattern.MatchString(input.SchemaHash) || !hashPattern.MatchString(input.SnapshotHash) ||
		(input.RowCount != nil && *input.RowCount < 0) {
		return ErrInvalidRequest
	}
	if err := validateBoundedObject(input.SnapshotJSON, 64<<10); err != nil {
		return err
	}
	empty := func(values ...string) bool {
		for _, value := range values {
			if value != "" {
				return false
			}
		}
		return true
	}
	switch input.Type {
	case InputSourceTable:
		if input.Layer != "SOURCE" ||
			!validUUID(input.DataSourceID) ||
			!validUUID(input.DataSourceVersionID) ||
			!validUUID(input.MetadataTableID) ||
			!empty(input.FileVersionID, input.DatasetID, input.DatasetVersionID, input.MaterializationID) {
			return ErrInvalidRequest
		}
	case InputFileVersion:
		if input.Layer != "SOURCE" ||
			!validUUID(input.DataSourceID) ||
			!validUUID(input.DataSourceVersionID) ||
			!validUUID(input.FileVersionID) ||
			!empty(input.MetadataTableID, input.DatasetID, input.DatasetVersionID, input.MaterializationID) {
			return ErrInvalidRequest
		}
	case InputDatasetVersion:
		if !validUUID(input.DatasetID) || !validUUID(input.DatasetVersionID) ||
			!empty(
				input.DataSourceID, input.DataSourceVersionID,
				input.MetadataTableID, input.FileVersionID, input.MaterializationID,
			) {
			return ErrInvalidRequest
		}
	case InputMaterialization:
		if !validUUID(input.MaterializationID) ||
			!empty(
				input.DataSourceID, input.DataSourceVersionID,
				input.MetadataTableID, input.FileVersionID,
				input.DatasetID, input.DatasetVersionID,
			) {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	switch target {
	case LayerODS:
		if input.Type != InputSourceTable && input.Type != InputFileVersion {
			return ErrInvalidRequest
		}
	case LayerDWD:
		if (input.Type != InputDatasetVersion && input.Type != InputMaterialization) || input.Layer != string(LayerODS) {
			return ErrInvalidRequest
		}
	case LayerDWS:
		if (input.Type != InputDatasetVersion && input.Type != InputMaterialization) || input.Layer != string(LayerDWD) {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

func Prepare(request RegisterRequest) (PreparedRequest, error) {
	if request.MaxAttempts == 0 {
		request.MaxAttempts = 3
	}
	if err := request.Validate(); err != nil {
		return PreparedRequest{}, err
	}
	request.Inputs = append([]InputSnapshot(nil), request.Inputs...)
	sort.Slice(request.Inputs, func(i, j int) bool {
		return request.Inputs[i].Ordinal < request.Inputs[j].Ordinal
	})
	for index := range request.Inputs {
		if len(request.Inputs[index].SnapshotJSON) == 0 {
			request.Inputs[index].SnapshotJSON = json.RawMessage(`{}`)
			continue
		}
		canonical, err := canonicalJSONObject(request.Inputs[index].SnapshotJSON)
		if err != nil {
			return PreparedRequest{}, err
		}
		request.Inputs[index].SnapshotJSON = canonical
	}
	planJSON, err := json.Marshal(request.Plan)
	if err != nil {
		return PreparedRequest{}, ErrInvalidRequest
	}
	inputJSON, err := json.Marshal(request.Inputs)
	if err != nil {
		return PreparedRequest{}, ErrInvalidRequest
	}
	planHash := sha256Hex(planJSON)
	inputHash := sha256Hex(inputJSON)
	idempotencyMaterial := strings.Join([]string{
		"dataset-build-idempotency-v1",
		request.Plan.DatasetID,
		request.Plan.DatasetVersionID,
		string(request.Plan.Layer),
		string(request.Plan.Mode),
		request.PartitionKey,
		planHash,
		inputHash,
	}, "\x00")
	idempotencyKey := sha256Hex([]byte(idempotencyMaterial))
	requestMaterial, _ := json.Marshal(struct {
		PlanHash          string `json:"planHash"`
		InputSnapshotHash string `json:"inputSnapshotHash"`
		IdempotencyKey    string `json:"idempotencyKey"`
		MaxAttempts       int    `json:"maxAttempts"`
	}{
		PlanHash: planHash, InputSnapshotHash: inputHash,
		IdempotencyKey: idempotencyKey, MaxAttempts: request.MaxAttempts,
	})
	return PreparedRequest{
		RegisterRequest:   request,
		PlanJSON:          planJSON,
		PlanHash:          planHash,
		InputSnapshotHash: inputHash,
		RequestHash:       sha256Hex(requestMaterial),
		IdempotencyKey:    idempotencyKey,
	}, nil
}

func decodePlan(raw []byte, expectedHash string) (BuildPlan, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan BuildPlan
	if err := decoder.Decode(&plan); err != nil {
		return BuildPlan{}, ErrCorruptPlan
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return BuildPlan{}, ErrCorruptPlan
	}
	if err := plan.Validate(); err != nil {
		return BuildPlan{}, ErrCorruptPlan
	}
	canonical, err := json.Marshal(plan)
	if err != nil || sha256Hex(canonical) != expectedHash {
		return BuildPlan{}, ErrCorruptPlan
	}
	return plan, nil
}

func validateBoundedObject(raw json.RawMessage, limit int) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > limit {
		return ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return ErrInvalidRequest
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ErrInvalidRequest
	}
	object, ok := value.(map[string]any)
	if !ok || hasForbiddenField(object) {
		return ErrInvalidRequest
	}
	return nil
}

func canonicalJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, ErrInvalidRequest
	}
	if err := ensureJSONEOF(decoder); err != nil || hasForbiddenField(value) {
		return nil, ErrInvalidRequest
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, ErrInvalidRequest
	}
	return canonical, nil
}

func hasForbiddenField(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(key))
			if _, forbidden := forbiddenJSONField[normalized]; forbidden {
				return true
			}
			if hasForbiddenField(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if hasForbiddenField(nested) {
				return true
			}
		}
	}
	return false
}

func validateQuality(results []QualityResult) error {
	if len(results) > 1024 {
		return ErrInvalidRequest
	}
	seen := make(map[string]struct{}, len(results))
	for _, result := range results {
		if !ruleCodePattern.MatchString(result.RuleCode) ||
			len(result.RuleVersion) == 0 || len(result.RuleVersion) > 64 ||
			!hashPattern.MatchString(result.RuleDefinitionHash) ||
			(result.Scope != "DATASET" && result.Scope != "FIELD" && result.Scope != "RELATIONSHIP") ||
			(result.Scope == "FIELD") != (strings.TrimSpace(result.FieldID) != "") ||
			len(result.FieldID) > 256 || len(result.Message) > 4096 {
			return ErrInvalidRequest
		}
		if result.Severity != QualityInfo && result.Severity != QualityWarning && result.Severity != QualityError {
			return ErrInvalidRequest
		}
		if result.Status != QualityPassed && result.Status != QualityFailed && result.Status != QualitySkipped {
			return ErrInvalidRequest
		}
		if err := validateBoundedObject(result.Expectation, 64<<10); err != nil {
			return err
		}
		if err := validateBoundedObject(result.Observed, 64<<10); err != nil {
			return err
		}
		key := strings.Join([]string{result.RuleCode, result.RuleVersion, result.Scope, result.FieldID}, "\x00")
		if _, exists := seen[key]; exists {
			return ErrInvalidRequest
		}
		seen[key] = struct{}{}
	}
	return nil
}

func qualityGateFailed(results []QualityResult) bool {
	for _, result := range results {
		if result.Severity == QualityError && result.Status == QualityFailed {
			return true
		}
	}
	return false
}

func validateNodeResult(result NodeResult) error {
	if result.Status != NodeSucceeded && result.Status != NodeFailed && result.Status != NodeSkipped {
		return ErrInvalidTransition
	}
	for _, value := range []*int64{result.InputRowCount, result.OutputRowCount, result.OutputSizeBytes} {
		if value != nil && *value < 0 {
			return ErrInvalidRequest
		}
	}
	if result.Status == NodeFailed {
		if strings.TrimSpace(result.ErrorCode) == "" || len(result.ErrorCode) > 128 {
			return ErrInvalidRequest
		}
	} else if result.ErrorCode != "" {
		return ErrInvalidRequest
	}
	if len(result.ErrorMessage) > 4096 {
		return ErrInvalidRequest
	}
	return nil
}

func sha256Hex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func validUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errorsIsEOF(err) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return err
}

func errorsIsEOF(err error) bool {
	return err == io.EOF
}
