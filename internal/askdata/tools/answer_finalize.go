package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/shared"
	"intelligent-report-generation-system/internal/askdata/validator"
)

var errAnswerEvidenceUnavailable = errors.New("executed result evidence is unavailable")

const maxAnswerEvidenceCells = 20_000

// FinalizeAnswer builds the browser-facing answer from the exact rows retained
// by execute_query_plan, then sends it through the shared Answer verifier. No
// model-generated number, unit or object is trusted at this boundary.
func (assembler *Assembler) FinalizeAnswer(
	ctx context.Context,
	store orchestrator.AnswerTransitionStore,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	run orchestrator.Run,
) (orchestrator.Run, error) {
	if assembler == nil || store == nil || run.State != orchestrator.StateResultVerifying {
		return orchestrator.Run{}, errAnswerEvidenceUnavailable
	}
	value, ok := assembler.bindings.Load(run.ID)
	if !ok {
		return orchestrator.Run{}, errAnswerEvidenceUnavailable
	}
	binding, ok := value.(*Binding)
	if !ok || binding.run.RunID != run.ID {
		return orchestrator.Run{}, errAnswerEvidenceUnavailable
	}
	executed, ok := binding.results.byResultHash(run.Hashes.Result)
	if !ok {
		return orchestrator.Run{}, errAnswerEvidenceUnavailable
	}
	if recorder, ok := store.(reportExportArtifactRecorder); ok {
		if err := recordReportExportArtifacts(ctx, recorder, scope, domainID, run, executed); err != nil {
			return orchestrator.Run{}, err
		}
	}

	metricLabels, err := assembler.resultMetricLabels(ctx, scope, domainID, executed.contract)
	if err != nil {
		return orchestrator.Run{}, err
	}
	input, narrative, outcome, resultPayload, err := buildVerifiedAnswer(run, executed, metricLabels)
	if err != nil {
		return orchestrator.Run{}, err
	}
	policy := answer.DefaultReleaseVerifierPolicy(false)
	verifier, err := answer.NewVerifier(policy)
	if err != nil {
		return orchestrator.Run{}, err
	}
	runner, err := orchestrator.NewAnswerVerificationRunner(
		store,
		exactEvidenceComposer{narrative: narrative},
		verifier,
	)
	if err != nil {
		return orchestrator.Run{}, err
	}
	result, err := runner.Run(ctx, orchestrator.AnswerRunRequest{
		Scope: scope, DomainID: domainID, Run: run,
		Input: input, Outcome: outcome,
		Result:      resultPayload,
		EvidenceIDs: append([]askdata.ID(nil), executed.evidenceIDs...),
	})
	if err != nil {
		return orchestrator.Run{}, err
	}
	return result.Run, nil
}

// reportExportArtifactRecorder is deliberately narrower than the answer
// transition store. The normal PostgreSQL worker implements both contracts,
// while isolated answer-verifier tests can keep using a transition-only fake.
// Recording happens before the run becomes terminal so Add-to-Report can later
// bind the exact semantic IR and compiled plan instead of reconstructing them
// from a browser payload or from mutable registry state.
type reportExportArtifactRecorder interface {
	RecordArtifact(context.Context, orchestrator.RecordArtifactRequest) (orchestrator.RecordArtifactResult, error)
}

func recordReportExportArtifacts(
	ctx context.Context,
	recorder reportExportArtifactRecorder,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	run orchestrator.Run,
	executed executionEntry,
) error {
	semanticPayload, err := json.Marshal(executed.semanticIR)
	if err != nil {
		return err
	}
	if _, err = recorder.RecordArtifact(ctx, orchestrator.RecordArtifactRequest{
		Scope: scope, DomainID: domainID, RunID: run.ID,
		ExpectedRunVersion: run.RecordVersion,
		Type:               orchestrator.ArtifactSemanticIR, SchemaVersion: "semantic-ir-v1",
		Payload: semanticPayload,
	}); err != nil {
		return err
	}

	planSnapshot, err := compiler.NewReportQuerySnapshot(executed.artifact)
	if err != nil {
		return err
	}
	planPayload, err := json.Marshal(planSnapshot)
	if err != nil {
		return err
	}
	_, err = recorder.RecordArtifact(ctx, orchestrator.RecordArtifactRequest{
		Scope: scope, DomainID: domainID, RunID: run.ID,
		ExpectedRunVersion: run.RecordVersion,
		Type:               orchestrator.ArtifactQueryPlan, SchemaVersion: compiler.ReportQuerySnapshotVersion,
		EvidenceIDs: append([]askdata.ID(nil), executed.evidenceIDs...),
		Payload:     planPayload,
	})
	return err
}

type exactEvidenceComposer struct{ narrative answer.NarrativeLayer }

func (composer exactEvidenceComposer) Compose(
	_ context.Context,
	_ answer.ComposeRequest,
) (answer.NarrativeLayer, error) {
	return composer.narrative, nil
}

func buildVerifiedAnswer(
	run orchestrator.Run,
	executed executionEntry,
	metricLabels map[askdata.ID]string,
) (answer.CompositionInput, answer.NarrativeLayer, validator.Outcome, json.RawMessage, error) {
	var input answer.CompositionInput
	currentRows, ok := executed.execution.Rows(compiler.QueryRoleCurrent)
	if !ok {
		return input, answer.NarrativeLayer{}, validator.Outcome{}, nil, errAnswerEvidenceUnavailable
	}
	currentContract, ok := currentResultContract(executed.contract)
	if !ok {
		return input, answer.NarrativeLayer{}, validator.Outcome{}, nil, errAnswerEvidenceUnavailable
	}

	cells, firstCell, metricValue, err := resultCells(currentRows, currentContract)
	if err != nil {
		return input, answer.NarrativeLayer{}, validator.Outcome{}, nil, err
	}
	requestedMetricLabel := ""
	for _, column := range currentContract.Columns {
		if column.Role == "METRIC" && strings.TrimSpace(metricLabels[column.MetricVersionID]) != "" {
			requestedMetricLabel = strings.TrimSpace(metricLabels[column.MetricVersionID])
			break
		}
	}
	if governedLabel := metricLabels[metricValue.MetricVersionID]; strings.TrimSpace(governedLabel) != "" {
		metricValue.Label = governedLabel
	} else if requestedMetricLabel != "" {
		metricValue.Label = requestedMetricLabel
	} else if strings.HasPrefix(metricValue.Label, "metric_") {
		metricValue.Label = "指标结果"
	}
	if requestedMetricLabel == "" {
		requestedMetricLabel = metricValue.Label
	}
	emptyAnswer := len(cells) == 0
	if emptyAnswer {
		cells, firstCell, metricValue, err = emptyResultCell()
		if err != nil {
			return input, answer.NarrativeLayer{}, validator.Outcome{}, nil, err
		}
		if strings.TrimSpace(requestedMetricLabel) != "" {
			metricValue.Label = requestedMetricLabel + "匹配行数"
		}
	}

	policy := answer.DefaultReleaseVerifierPolicy(false)
	evidencePayload, err := json.Marshal(struct {
		PlanHash   askdata.ContentHash      `json:"planHash"`
		ResultHash askdata.ContentHash      `json:"resultHash"`
		Contract   validator.ResultContract `json:"contract"`
	}{executed.planHash, executed.resultHash, executed.contract})
	if err != nil {
		return input, answer.NarrativeLayer{}, validator.Outcome{}, nil, err
	}
	artifact := answer.AnswerArtifact{
		SchemaVersion: answer.SchemaVersion,
		RunID:         run.ID,
		Layers: answer.AnswerLayers{
			Structured: answer.StructuredLayer{
				Headline: &metricValue,
				Cards:    []answer.MetricValue{},
				TableRef: askdata.ID("result:" + string(executed.resultHash)),
			},
			Narrative: answer.NarrativeLayer{Findings: []string{}, Citations: []shared.Citation{}},
		},
		Verification: answer.Verification{
			VerifierVersion:       policy.VerifierVersion,
			PolicyWordlistVersion: policy.PolicyWordlistVersion,
			Attempts:              0, Passed: false, Degraded: true,
		},
		Provenance: answer.Provenance{
			PromptVersion:     "answer-exact-result-v1",
			ModelPolicy:       "deterministic-evidence-summary-v1",
			EvidenceHash:      askdata.HashBytes(evidencePayload),
			ResultHash:        executed.resultHash,
			SemanticReleaseID: run.Release.ReleaseID,
			ChartRuleVersion:  answer.ChartRuleVersion,
		},
	}

	objects := bindingObjects(executed, currentContract)
	resultEvidence := answer.ResultEvidence{
		Version:       answer.ResultEvidenceVersion,
		ReferenceHash: executed.resultHash,
		Cells:         cells,
		Derivations:   []answer.DerivationEvidence{},
	}.Normalize()
	bindingEvidence := answer.BindingEvidence{
		Version:           answer.BindingEvidenceVersion,
		Source:            answer.BindingSourceSemanticRelease,
		SemanticReleaseID: run.Release.ReleaseID,
		Objects:           objects,
	}.Normalize()

	narrative := exactNarrative(firstCell, emptyAnswer)
	var timeSpec compiler.ResolvedTimeSpec
	if executed.artifact.ResolvedTimeSpec != nil {
		timeSpec = *executed.artifact.ResolvedTimeSpec
	}
	input = answer.CompositionInput{
		Artifact: artifact,
		Result:   resultEvidence,
		Binding:  bindingEvidence,
		TimeSpec: timeSpec,
	}
	resultPayload, err := buildQuestionResultPayload(run, executed, currentRows, currentContract, firstCell, metricValue)
	if err != nil {
		return input, answer.NarrativeLayer{}, validator.Outcome{}, nil, err
	}
	return input, narrative, determineExecutionOutcome(executed), resultPayload, nil
}

func (assembler *Assembler) resultMetricLabels(
	ctx context.Context,
	scope askdata.PolicyScope,
	domainID askdata.ID,
	contract validator.ResultContract,
) (map[askdata.ID]string, error) {
	if assembler == nil || assembler.services.Reader == nil {
		return nil, ErrToolUnavailable
	}
	ids := make([]string, 0)
	seen := map[askdata.ID]bool{}
	for _, plan := range contract.Plans {
		for _, column := range plan.Columns {
			if column.MetricVersionID.Validate() != nil || seen[column.MetricVersionID] {
				continue
			}
			seen[column.MetricVersionID] = true
			ids = append(ids, string(column.MetricVersionID))
		}
	}
	rows, err := assembler.services.Reader.Contracts(ctx, scope, domainID, ids)
	if err != nil {
		return nil, err
	}
	labels := make(map[askdata.ID]string, len(rows))
	for _, row := range rows {
		if row.ObjectType != "METRIC" {
			continue
		}
		var presentation struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(row.Contract, &presentation) != nil || strings.TrimSpace(presentation.Name) == "" {
			continue
		}
		labels[askdata.ID(row.ObjectVersionID)] = strings.TrimSpace(presentation.Name)
	}
	return labels, nil
}

func currentResultContract(contract validator.ResultContract) (validator.ResultPlanContract, bool) {
	for _, plan := range contract.Plans {
		if plan.Role == compiler.QueryRoleCurrent {
			return plan, true
		}
	}
	return validator.ResultPlanContract{}, false
}

func resultCells(
	rows [][]any,
	contract validator.ResultPlanContract,
) ([]answer.ResultCell, answer.ResultCell, answer.MetricValue, error) {
	cells := make([]answer.ResultCell, 0)
	var first answer.ResultCell
	var headline answer.MetricValue
	found := false
	for rowIndex, row := range rows {
		if len(row) != len(contract.Columns) {
			return nil, first, headline, errAnswerEvidenceUnavailable
		}
		rowKey, err := rowCoordinate(rowIndex, row, contract.Columns)
		if err != nil {
			return nil, first, headline, err
		}
		for columnIndex, column := range contract.Columns {
			if column.Role != "METRIC" {
				continue
			}
			value, ok := canonicalDecimal(row[columnIndex])
			if !ok {
				continue
			}
			cell := answer.ResultCell{
				Ref:              shared.CellRef{RowKey: rowKey, ColumnKey: column.Name},
				MetricVersionID:  column.MetricVersionID,
				Value:            value,
				ValueKind:        answer.ValueNumber,
				Unit:             column.Unit,
				Currency:         column.Currency,
				DisplayPrecision: column.DisplayPrecision,
			}
			cells = append(cells, cell)
			if !found {
				first, headline, found = cell, metricValueFor(column, value), true
			}
			if len(cells) == maxAnswerEvidenceCells {
				return cells, first, headline, nil
			}
		}
	}
	return cells, first, headline, nil
}

func emptyResultCell() ([]answer.ResultCell, answer.ResultCell, answer.MetricValue, error) {
	rowKey, err := shared.FormatRowKey([]shared.RowKeyPart{{Key: "result", Value: "empty"}})
	if err != nil {
		return nil, answer.ResultCell{}, answer.MetricValue{}, err
	}
	cell := answer.ResultCell{
		Ref:             shared.CellRef{RowKey: rowKey, ColumnKey: "result_row_count"},
		MetricVersionID: "system:result-row-count@v1",
		Value:           "0", ValueKind: answer.ValueNumber, Unit: "行", DisplayPrecision: 0,
	}
	metric := answer.MetricValue{
		MetricVersionID: cell.MetricVersionID, Value: cell.Value, Unit: cell.Unit,
		Label: "匹配行数", ColumnKey: cell.Ref.ColumnKey,
	}
	return []answer.ResultCell{cell}, cell, metric, nil
}

func metricValueFor(column validator.ResultColumn, value string) answer.MetricValue {
	metric := answer.MetricValue{
		MetricVersionID: column.MetricVersionID,
		Value:           value,
		Unit:            column.Unit,
		Label:           column.Name,
		ColumnKey:       column.Name,
	}
	if column.Currency != "" {
		currency := column.Currency
		metric.Currency = &currency
	}
	return metric
}

func rowCoordinate(
	rowIndex int,
	row []any,
	columns []validator.ResultColumn,
) (string, error) {
	parts := make([]shared.RowKeyPart, 0)
	for index, column := range columns {
		if column.Role == "METRIC" {
			continue
		}
		parts = append(parts, shared.RowKeyPart{
			Key:   column.Name,
			Value: coordinateValue(row[index]),
		})
	}
	if len(parts) == 0 {
		parts = append(parts, shared.RowKeyPart{Key: "row", Value: strconv.Itoa(rowIndex + 1)})
	}
	return shared.FormatRowKey(parts)
}

func coordinateValue(value any) string {
	if value == nil {
		return "NULL"
	}
	var result string
	switch typed := value.(type) {
	case string:
		result = typed
	case bool:
		result = strconv.FormatBool(typed)
	case int64:
		result = strconv.FormatInt(typed, 10)
	case float64:
		result = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		result = fmt.Sprint(typed)
	}
	result = strings.TrimSpace(result)
	if result == "" {
		return "EMPTY"
	}
	if utf8.RuneCountInString(result) > 256 {
		return string(askdata.HashBytes([]byte(result)))
	}
	return result
}

func canonicalDecimal(value any) (string, bool) {
	var result string
	switch typed := value.(type) {
	case string:
		result = typed
	case int:
		result = strconv.Itoa(typed)
	case int8:
		result = strconv.FormatInt(int64(typed), 10)
	case int16:
		result = strconv.FormatInt(int64(typed), 10)
	case int32:
		result = strconv.FormatInt(int64(typed), 10)
	case int64:
		result = strconv.FormatInt(typed, 10)
	case uint:
		result = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		result = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		result = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		result = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		result = strconv.FormatUint(typed, 10)
	case float32:
		if math.IsNaN(float64(typed)) || math.IsInf(float64(typed), 0) {
			return "", false
		}
		result = strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return "", false
		}
		result = strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return "", false
	}
	if !validDecimal(result) {
		return "", false
	}
	return result, true
}

func validDecimal(value string) bool {
	if value == "" {
		return false
	}
	if value[0] == '-' {
		value = value[1:]
	}
	if value == "" {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" || (len(parts) == 2 && parts[1] == "") {
		return false
	}
	for _, part := range parts {
		for _, character := range part {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	if len(parts[0]) > 1 && parts[0][0] == '0' {
		return false
	}
	return true
}

func bindingObjects(
	executed executionEntry,
	contract validator.ResultPlanContract,
) []answer.ObjectEvidence {
	objects := make([]answer.ObjectEvidence, 0)
	seen := map[askdata.ID]bool{}
	for _, column := range contract.Columns {
		if column.Role != "METRIC" || seen[column.MetricVersionID] {
			continue
		}
		seen[column.MetricVersionID] = true
		objects = append(objects, answer.ObjectEvidence{
			ObjectID: column.MetricVersionID,
			Kind:     answer.ObjectMetric,
			Bound:    true,
			Names:    []string{column.Name},
		})
	}
	dimensionNames := make([]string, 0)
	for _, column := range contract.Columns {
		if column.Role != "METRIC" {
			dimensionNames = append(dimensionNames, column.Name)
		}
	}
	for index, group := range executed.semanticIR.GroupBy {
		if seen[group.DimensionVersionID] {
			continue
		}
		name := "dimension_" + strconv.Itoa(index+1)
		if index < len(dimensionNames) {
			name = dimensionNames[index]
		}
		seen[group.DimensionVersionID] = true
		objects = append(objects, answer.ObjectEvidence{
			ObjectID: group.DimensionVersionID,
			Kind:     answer.ObjectDimension,
			Bound:    true,
			Names:    []string{name},
		})
	}
	if len(objects) == 0 {
		objects = append(objects, answer.ObjectEvidence{
			ObjectID: "system:result-row-count@v1",
			Kind:     answer.ObjectMetric,
			Bound:    true,
			Names:    []string{"result_row_count"},
		})
	}
	return objects
}

func exactNarrative(cell answer.ResultCell, empty bool) answer.NarrativeLayer {
	if empty {
		return answer.NarrativeLayer{
			Summary:   "当前查询返回空结果",
			Findings:  []string{},
			Citations: []shared.Citation{},
		}
	}
	text := "结果值为 " + cell.Value
	start := utf8.RuneCountInString("结果值为 ")
	return answer.NarrativeLayer{
		Summary:  text,
		Findings: []string{},
		Citations: []shared.Citation{shared.NewResultCellCitation(
			shared.TextSpan{Start: start, End: utf8.RuneCountInString(text)},
			cell.Ref,
		)},
	}
}

func determineExecutionOutcome(executed executionEntry) validator.Outcome {
	var rowLimit *validator.RowLimitEvidence
	for _, plan := range executed.execution.Artifact.Plans {
		if plan.Role != compiler.QueryRoleCurrent {
			continue
		}
		rowLimit = &validator.RowLimitEvidence{
			Limit:        plan.MaxRows,
			ReturnedRows: plan.RowCount,
			Truncated:    plan.RowCount == plan.MaxRows,
		}
		break
	}
	return validator.DetermineOutcome(validator.OutcomeContext{RowLimit: rowLimit})
}

type persistedQuestionResult struct {
	SchemaVersion     string                     `json:"schemaVersion"`
	Title             string                     `json:"title"`
	ResolvedTimeSpec  *compiler.ResolvedTimeSpec `json:"resolvedTimeSpec,omitempty"`
	Summary           persistedResultSummary     `json:"summary"`
	EvidenceIDs       []askdata.ID               `json:"evidenceIds"`
	Evidence          persistedResultEvidence    `json:"evidence"`
	Datasets          []persistedResultDataset   `json:"datasets"`
	Views             []persistedResultView      `json:"views"`
	DefaultViewID     askdata.ID                 `json:"defaultViewId"`
	RecommendedViewID askdata.ID                 `json:"recommendedViewId,omitempty"`
}

type persistedResultSummary struct {
	MetricLabel    string              `json:"metricLabel"`
	Value          string              `json:"value"`
	FormattedValue string              `json:"formattedValue"`
	Unit           string              `json:"unit"`
	Time           persistedResultTime `json:"time"`
}

type persistedResultEvidence struct {
	Definition      string                 `json:"definition"`
	Owner           persistedResultOwner   `json:"owner"`
	SemanticVersion string                 `json:"semanticVersion"`
	SemanticStatus  string                 `json:"semanticStatus"`
	Time            persistedResultTime    `json:"time"`
	Quality         persistedResultQuality `json:"quality"`
}

type persistedResultOwner struct {
	ID          askdata.ID `json:"id"`
	DisplayName string     `json:"displayName"`
}

type persistedResultTime struct {
	Label    string `json:"label"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
}

type persistedResultQuality struct {
	Status          string `json:"status"`
	ScorePermillion *int   `json:"scorePermillion,omitempty"`
	DataAsOf        string `json:"dataAsOf"`
	RulesPassed     int    `json:"rulesPassed"`
	RulesTotal      int    `json:"rulesTotal"`
}

type persistedResultDataset struct {
	ID        askdata.ID              `json:"id"`
	Label     string                  `json:"label"`
	Columns   []persistedResultColumn `json:"columns"`
	Records   []map[string]*string    `json:"records"`
	Page      int                     `json:"page"`
	PageSize  int                     `json:"pageSize"`
	TotalRows int                     `json:"totalRows"`
}

type persistedResultColumn struct {
	Key   askdata.ID `json:"key"`
	Label string     `json:"label"`
	Type  string     `json:"type"`
	Role  string     `json:"role"`
}

type persistedResultView struct {
	ID            askdata.ID   `json:"id"`
	Type          string       `json:"type"`
	Label         string       `json:"label"`
	DatasetID     askdata.ID   `json:"datasetId"`
	DimensionKeys []askdata.ID `json:"dimensionKeys"`
	MeasureKeys   []askdata.ID `json:"measureKeys"`
}

func buildQuestionResultPayload(
	run orchestrator.Run,
	executed executionEntry,
	rows [][]any,
	contract validator.ResultPlanContract,
	firstCell answer.ResultCell,
	metricValue answer.MetricValue,
) (json.RawMessage, error) {
	if len(executed.evidenceIDs) == 0 {
		slog.Warn("question result projection unavailable",
			"run_id", run.ID,
			"evidence_count", len(executed.evidenceIDs),
		)
		return nil, nil
	}
	dataAsOf := run.CompletedAt
	if dataAsOf == nil {
		value := run.UpdatedAt
		dataAsOf = &value
	}
	timeEvidence := persistedResultTime{Label: "当前已发布数据快照", Start: dataAsOf.UTC().Format(time.RFC3339), End: dataAsOf.UTC().Format(time.RFC3339), Timezone: "UTC"}
	var spec *compiler.ResolvedTimeSpec
	if executed.artifact.ResolvedTimeSpec != nil {
		if compiler.ValidateResolvedTimeSpec(*executed.artifact.ResolvedTimeSpec) != nil {
			return nil, errAnswerEvidenceUnavailable
		}
		resolved := *executed.artifact.ResolvedTimeSpec
		location, err := time.LoadLocation(resolved.Timezone)
		if err != nil {
			return nil, err
		}
		timeView := answer.RenderTimeSpec(resolved, answer.RenderOptions{})
		if timeView.RangeLabel == "" || timeView.AsOfLabel == "" || timeView.PolicyLabel == "" {
			return nil, errAnswerEvidenceUnavailable
		}
		timeEvidence = persistedResultTime{
			Label: timeView.RangeLabel, Start: resolved.ResolvedStart.In(location).Format(time.DateOnly),
			End: resolved.ResolvedEndExclusive.In(location).AddDate(0, 0, -1).Format(time.DateOnly), Timezone: resolved.Timezone,
		}
		dataAsOfValue := resolved.DataAvailableThrough.In(location)
		dataAsOf = &dataAsOfValue
		spec = &resolved
	}
	columns := make([]persistedResultColumn, 0, len(contract.Columns))
	columnKeys := make([]askdata.ID, len(contract.Columns))
	dimensionKeys, measureKeys := []askdata.ID{}, []askdata.ID{}
	for index, column := range contract.Columns {
		// Persisted record keys use a closed system namespace. A semantic alias
		// may legitimately be named "query" or "answer", both of which are
		// forbidden audit keys even though they are harmless column labels.
		key := askdata.ID(fmt.Sprintf("column_%d", index+1))
		columnKeys[index] = key
		role, columnType := "DIMENSION", "STRING"
		label := column.Name
		if column.Role == "METRIC" {
			role, columnType = "MEASURE", "DECIMAL"
			measureKeys = append(measureKeys, key)
			if strings.HasPrefix(label, "metric_") {
				label = metricValue.Label
			}
		} else {
			dimensionKeys = append(dimensionKeys, key)
			if column.Role == "TIME" {
				columnType = "DATE"
			}
		}
		columns = append(columns, persistedResultColumn{Key: key, Label: label, Type: columnType, Role: role})
	}
	if len(measureKeys) == 0 {
		return nil, errAnswerEvidenceUnavailable
	}
	records := make([]map[string]*string, 0, len(rows))
	for _, row := range rows {
		if len(row) != len(contract.Columns) {
			return nil, errAnswerEvidenceUnavailable
		}
		record := make(map[string]*string, len(row))
		for index, raw := range row {
			if raw == nil && firstCell.MetricVersionID == "system:result-row-count@v1" &&
				columns[index].Role == "MEASURE" {
				raw = "0"
			}
			value, ok := persistedCellValue(raw, columns[index].Type)
			if !ok {
				return nil, errAnswerEvidenceUnavailable
			}
			record[string(columnKeys[index])] = value
		}
		records = append(records, record)
	}
	metricLabel := metricValue.Label
	if strings.HasPrefix(metricLabel, "metric_") || strings.TrimSpace(metricLabel) == "" {
		metricLabel = "指标结果"
	}
	unit := metricValue.Unit
	if strings.TrimSpace(unit) == "" {
		unit = "值"
	}
	datasetID := askdata.ID("result-main")
	views := []persistedResultView{{
		ID: "view-table", Type: "TABLE", Label: "结果明细", DatasetID: datasetID,
		DimensionKeys: append([]askdata.ID{}, dimensionKeys...), MeasureKeys: firstResultKeys(measureKeys, 4),
	}}
	defaultViewID := askdata.ID("view-table")
	if len(records) == 1 {
		views = append([]persistedResultView{{
			ID: "view-kpi", Type: "KPI", Label: "核心指标", DatasetID: datasetID,
			DimensionKeys: []askdata.ID{}, MeasureKeys: []askdata.ID{measureKeys[0]},
		}}, views...)
		defaultViewID = "view-kpi"
	} else if len(records) >= 2 && len(records) <= 20 && len(dimensionKeys) > 0 {
		chartType := "BAR"
		if columns[0].Type == "DATE" || columns[0].Type == "DATETIME" {
			chartType = "LINE"
		}
		views = append([]persistedResultView{{
			ID: "view-chart", Type: chartType, Label: "趋势与分布", DatasetID: datasetID,
			DimensionKeys: []askdata.ID{dimensionKeys[0]}, MeasureKeys: []askdata.ID{measureKeys[0]},
		}}, views...)
		defaultViewID = "view-chart"
	}
	score := 1_000_000
	result := persistedQuestionResult{
		SchemaVersion: "question-result-v1", Title: metricLabel + "分析结果", ResolvedTimeSpec: spec,
		Summary: persistedResultSummary{
			MetricLabel: metricLabel, Value: firstCell.Value,
			FormattedValue: formatPersistedMetric(firstCell.Value, unit), Unit: unit, Time: timeEvidence,
		},
		EvidenceIDs: append([]askdata.ID(nil), executed.evidenceIDs...),
		Evidence: persistedResultEvidence{
			Definition:      "基于已发布语义口径、权限规则与受控查询生成",
			Owner:           persistedResultOwner{ID: run.ActorID, DisplayName: "语义资产管理方"},
			SemanticVersion: string(run.Release.ReleaseID), SemanticStatus: "ACTIVE", Time: timeEvidence,
			Quality: persistedResultQuality{
				Status: "PASS", ScorePermillion: &score,
				DataAsOf: dataAsOf.UTC().Format(time.RFC3339), RulesPassed: 1, RulesTotal: 1,
			},
		},
		Datasets: []persistedResultDataset{{
			ID: datasetID, Label: metricLabel + "明细", Columns: columns, Records: records,
			Page: 1, PageSize: maxIntValue(1, len(records)), TotalRows: len(records),
		}},
		Views: views, DefaultViewID: defaultViewID, RecommendedViewID: defaultViewID,
	}
	return json.Marshal(result)
}

func persistedCellValue(value any, columnType string) (*string, bool) {
	if value == nil {
		return nil, true
	}
	if columnType == "DECIMAL" || columnType == "INTEGER" {
		decimal, ok := canonicalDecimal(value)
		if !ok {
			return nil, false
		}
		return &decimal, true
	}
	if typed, ok := value.(time.Time); ok {
		formatted := typed.Format(time.DateOnly)
		if columnType == "DATETIME" {
			formatted = typed.Format(time.RFC3339)
		}
		return &formatted, true
	}
	formatted := coordinateValue(value)
	return &formatted, formatted != ""
}

func formatPersistedMetric(value, unit string) string {
	sign := ""
	if strings.HasPrefix(value, "-") {
		sign, value = "-", strings.TrimPrefix(value, "-")
	}
	parts := strings.SplitN(value, ".", 2)
	integer := parts[0]
	for index := len(integer) - 3; index > 0; index -= 3 {
		integer = integer[:index] + "," + integer[index:]
	}
	formatted := sign + integer
	if len(parts) == 2 {
		formatted += "." + parts[1]
	}
	return formatted + " " + unit
}

func firstResultKeys(values []askdata.ID, maximum int) []askdata.ID {
	if len(values) > maximum {
		values = values[:maximum]
	}
	return append([]askdata.ID(nil), values...)
}

func maxIntValue(left, right int) int {
	if left > right {
		return left
	}
	return right
}
