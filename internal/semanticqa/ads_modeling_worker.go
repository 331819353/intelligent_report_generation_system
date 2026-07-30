package semanticqa

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

const adsModelingConcurrency = 4

type adsModelingClaim struct {
	ID, TenantID, SourceDatasetID, SourceVersionID string
	ActorID, DomainID, LeaseToken                  string
	ModelingMode                                   string
	Attempt, MaxAttempts                           int
}

type ADSModelingWorker struct {
	store    *PostgresStore
	datasets dwsModelingDatasetService
}

func NewADSModelingWorker(
	store *PostgresStore,
	datasets dwsModelingDatasetService,
) *ADSModelingWorker {
	return &ADSModelingWorker{store: store, datasets: datasets}
}

func (worker *ADSModelingWorker) TenantIDs(
	ctx context.Context,
) ([]string, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil {
		return nil, ErrInvalidRequest
	}
	rows, err := worker.store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (worker *ADSModelingWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.datasets == nil ||
		uuid.Validate(tenantID) != nil || !validWorkerID(workerID) ||
		lease < time.Second || lease > time.Hour {
		return false, ErrInvalidRequest
	}
	claim, err := worker.claim(ctx, tenantID, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	processCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(max(time.Second, lease/3))
		defer ticker.Stop()
		for {
			select {
			case <-processCtx.Done():
				return
			case <-ticker.C:
				if err := worker.renew(
					processCtx, *claim, workerID, lease,
				); err != nil {
					select {
					case heartbeatErr <- err:
					default:
					}
					cancel()
					return
				}
			}
		}
	}()
	processErr := worker.process(processCtx, *claim, workerID)
	cancel()
	if processErr != nil {
		select {
		case leaseErr := <-heartbeatErr:
			return true, errors.Join(processErr, leaseErr)
		default:
		}
		finishErr := worker.finish(
			ctx, *claim, workerID, "FAILED",
			"ADS_MODELING_FAILED", "", "SKIPPED",
		)
		return true, errors.Join(processErr, finishErr)
	}
	return true, nil
}

func (worker *ADSModelingWorker) claim(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (claim *adsModelingClaim, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.ads_modeling_jobs
			SET status='FAILED',error_code='LEASE_EXPIRED',
				error_message='任务租约已过期且达到最大尝试次数，请重新触发应用建模',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts`); err != nil {
			return err
		}
		item := adsModelingClaim{TenantID: tenantID}
		err := tx.QueryRow(ctx, `WITH candidate AS (
				SELECT job.id
				FROM platform.ads_modeling_jobs AS job
				WHERE (
				    job.status IN ('PENDING','WAITING_DEPENDENCY')
				    AND job.next_attempt_at<=now()
				  ) OR (
				    job.status='RUNNING'
				    AND job.lease_expires_at<=now()
				    AND job.attempt<job.max_attempts
				  )
				ORDER BY job.next_attempt_at,job.created_at,job.id
				FOR UPDATE SKIP LOCKED LIMIT 1
			)
			UPDATE platform.ads_modeling_jobs AS job
			SET status='RUNNING',attempt=attempt+1,
				error_code='',error_message='',
				lease_owner=$1,lease_token=public.gen_random_uuid(),
				lease_expires_at=now()+($2*interval '1 second'),
				updated_at=now()
			FROM candidate,platform.datasets AS source
			WHERE job.id=candidate.id
			  AND source.id=job.source_dws_dataset_id
			  AND source.tenant_id=job.tenant_id
			RETURNING job.id::text,job.source_dws_dataset_id::text,
				job.source_dws_version_id::text,job.requested_by::text,
				source.domain_id::text,job.lease_token::text,job.modeling_mode,
				job.attempt,job.max_attempts`,
			workerID, int64(lease/time.Second),
		).Scan(
			&item.ID, &item.SourceDatasetID, &item.SourceVersionID,
			&item.ActorID, &item.DomainID, &item.LeaseToken,
			&item.ModelingMode,
			&item.Attempt, &item.MaxAttempts,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		claim = &item
		return nil
	})
	return claim, err
}

func (worker *ADSModelingWorker) renew(
	ctx context.Context,
	claim adsModelingClaim,
	workerID string,
	lease time.Duration,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.ads_modeling_jobs
			SET lease_expires_at=now()+($1*interval '1 second'),
				updated_at=now()
			WHERE id=$2::uuid AND status='RUNNING'
			  AND lease_owner=$3 AND lease_token=$4::uuid`,
			int64(lease/time.Second), claim.ID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

func (worker *ADSModelingWorker) process(
	ctx context.Context,
	claim adsModelingClaim,
	workerID string,
) error {
	if uuid.Validate(claim.DomainID) != nil {
		return ErrInvalidRequest
	}
	ctx = database.WithAccessContext(ctx, claim.ActorID, claim.DomainID)
	source, err := worker.datasets.Get(
		ctx, claim.TenantID, claim.SourceDatasetID,
	)
	if err != nil {
		return err
	}
	version, err := worker.datasets.GetVersion(
		ctx, claim.TenantID, claim.SourceDatasetID, claim.SourceVersionID,
	)
	if err != nil {
		return err
	}
	if source.Status != "PUBLISHED" ||
		source.CurrentPublishedVersionID != claim.SourceVersionID ||
		version.Status != "PUBLISHED" || version.Layer != dataset.LayerDWS {
		return worker.finish(
			ctx, claim, workerID, "SKIPPED",
			"SOURCE_CHANGED", "", "SKIPPED",
		)
	}
	ready, terminal, err := worker.sourceReady(ctx, claim)
	if err != nil {
		return err
	}
	if terminal {
		return worker.finish(
			ctx, claim, workerID, "FAILED",
			"DWS_MATERIALIZATION_FAILED", "", "SKIPPED",
		)
	}
	if !ready {
		return worker.waitForDependency(ctx, claim, workerID)
	}
	source.DSL = version.DSL
	prepared := dataset.Prepared{}
	targetDatasetID, targetVersionID := "", ""
	if claim.ModelingMode == "DEFAULT" {
		var target *adsDWSAsset
		target, err = worker.findADSContainmentTarget(
			ctx, claim, source,
		)
		if err != nil {
			return err
		}
		if target != nil {
			targetReady, targetTerminal, readyErr :=
				worker.dwsVersionReady(
					ctx, claim, target.Record.ID, target.VersionID,
				)
			if readyErr != nil {
				return readyErr
			}
			if targetTerminal {
				return worker.finish(
					ctx, claim, workerID, "FAILED",
					"DWS_MATERIALIZATION_FAILED", "", "SKIPPED",
				)
			}
			if !targetReady {
				return worker.waitForDependency(
					ctx, claim, workerID,
				)
			}
			prepared, err = buildContainedDWSADSCandidate(
				source, claim.SourceVersionID, *target,
			)
			if errors.Is(err, ErrUnprovenPath) {
				err = nil
			} else if err == nil {
				targetDatasetID = target.Record.ID
				targetVersionID = target.VersionID
			}
		}
	}
	if len(prepared.DSLJSON) == 0 && err == nil {
		prepared, err = buildADSCandidate(
			source, claim.SourceVersionID,
		)
	}
	if err != nil {
		return err
	}
	datasetID, action, err := worker.upsertADS(
		ctx, claim, prepared, targetDatasetID, targetVersionID,
	)
	if err != nil {
		return err
	}
	return worker.finish(
		ctx, claim, workerID, "SUCCEEDED", "",
		datasetID, action,
	)
}

func (worker *ADSModelingWorker) sourceReady(
	ctx context.Context,
	claim adsModelingClaim,
) (ready, terminal bool, err error) {
	return worker.dwsVersionReady(
		ctx, claim, claim.SourceDatasetID, claim.SourceVersionID,
	)
}

func (worker *ADSModelingWorker) dwsVersionReady(
	ctx context.Context,
	claim adsModelingClaim,
	datasetID, versionID string,
) (ready, terminal bool, err error) {
	var latestBuildStatus string
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
				EXISTS(
					SELECT 1
					FROM platform.dataset_materializations AS materialization
					WHERE materialization.dataset_id=$1::uuid
					  AND materialization.dataset_version_id=$2::uuid
					  AND materialization.status='ACTIVE'
				),
				COALESCE((
					SELECT run.status
					FROM platform.dataset_build_runs AS run
					WHERE run.dataset_id=$1::uuid
					  AND run.dataset_version_id=$2::uuid
					ORDER BY run.created_at DESC,run.id DESC LIMIT 1
				),'')`,
			datasetID, versionID,
		).Scan(&ready, &latestBuildStatus)
	})
	if err != nil {
		return false, false, err
	}
	terminal = !ready &&
		(latestBuildStatus == "FAILED" || latestBuildStatus == "CANCELLED")
	return ready, terminal, nil
}

func (worker *ADSModelingWorker) waitForDependency(
	ctx context.Context,
	claim adsModelingClaim,
	workerID string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.ads_modeling_jobs
			SET status='WAITING_DEPENDENCY',
				attempt=GREATEST(attempt-1,0),
				error_code='WAITING_ACTIVE_DWS_MATERIALIZATION',
				error_message='等待 DWS 发布版本完成物化；可用后应用建模会自动继续',
				next_attempt_at=now()+interval '1 minute',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				updated_at=now()
			WHERE id=$1::uuid AND status='RUNNING'
			  AND lease_owner=$2 AND lease_token=$3::uuid`,
			claim.ID, workerID, claim.LeaseToken)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

type adsDWSAsset struct {
	Record    dataset.Record
	VersionID string
	Document  dataset.Document
}

func (worker *ADSModelingWorker) findADSContainmentTarget(
	ctx context.Context,
	claim adsModelingClaim,
	source dataset.Record,
) (*adsDWSAsset, error) {
	candidates := []adsDWSAsset{}
	err := database.WithTenantTx(
		ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT
					item.id::text,item.code,item.name,item.description,
					version.id::text,version.dsl_json
				FROM platform.datasets AS item
				JOIN platform.dataset_versions AS version
				  ON version.id=item.current_published_version_id
				 AND version.dataset_id=item.id
				 AND version.tenant_id=item.tenant_id
				 AND version.status='PUBLISHED'
				 AND version.layer='DWS'
				WHERE item.domain_id=$1::uuid
				  AND item.status='PUBLISHED'
				  AND item.deleted_at IS NULL
				  AND item.id<>$2::uuid
				ORDER BY item.code,item.id`,
				claim.DomainID, claim.SourceDatasetID,
			)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var candidate adsDWSAsset
				var raw json.RawMessage
				if err := rows.Scan(
					&candidate.Record.ID, &candidate.Record.Code,
					&candidate.Record.Name, &candidate.Record.Description,
					&candidate.VersionID, &raw,
				); err != nil {
					return err
				}
				document, err := dataset.DecodeAndNormalize(raw)
				if err != nil {
					return err
				}
				candidate.Record.DSL = raw
				candidate.Record.Layer = dataset.LayerDWS
				candidate.Document = document
				candidates = append(candidates, candidate)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return nil, err
	}
	sourceDocument, err := dataset.DecodeAndNormalize(source.DSL)
	if err != nil {
		return nil, err
	}
	return selectADSContainmentTarget(sourceDocument, candidates), nil
}

func selectADSContainmentTarget(
	source dataset.Document,
	candidates []adsDWSAsset,
) *adsDWSAsset {
	if !singleGrainDWS(source) ||
		!hasADSReaggregatableMeasure(source) {
		return nil
	}
	sourceDimensions := dwsDimensionCodes(source)
	if len(sourceDimensions) == 0 {
		return nil
	}
	sourceSet := make(map[string]bool, len(sourceDimensions))
	for _, code := range sourceDimensions {
		sourceSet[strings.ToLower(code)] = true
	}
	var selected *adsDWSAsset
	selectedDimensionCount := -1
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.Document.Dataset.Layer != dataset.LayerDWS {
			continue
		}
		if !singleGrainDWS(candidate.Document) {
			continue
		}
		dimensions := dwsDimensionCodes(candidate.Document)
		// Equal grains do not need aggregation. A strict subset is the safe,
		// useful containment direction: source can roll up to candidate.
		if len(dimensions) == 0 ||
			len(dimensions) >= len(sourceDimensions) {
			continue
		}
		if !compatibleDWSContainment(source, candidate.Document) {
			continue
		}
		contained := true
		for _, code := range dimensions {
			if !sourceSet[strings.ToLower(code)] {
				contained = false
				break
			}
		}
		if !contained {
			continue
		}
		if len(dimensions) > selectedDimensionCount ||
			(len(dimensions) == selectedDimensionCount &&
				(selected == nil ||
					candidate.Record.Code < selected.Record.Code)) {
			selected = candidate
			selectedDimensionCount = len(dimensions)
		}
	}
	return selected
}

func singleGrainDWS(document dataset.Document) bool {
	return document.GroupByMode == "" ||
		document.GroupByMode == dataset.GroupByModeStandard
}

func hasADSReaggregatableMeasure(document dataset.Document) bool {
	if document.AnalysisContract == nil {
		return false
	}
	fields := make(map[string]dataset.Field, len(document.Fields))
	for _, field := range document.Fields {
		fields[strings.ToLower(field.Code)] = field
	}
	for _, measure := range document.AnalysisContract.Measures {
		field, exists := fields[strings.ToLower(measure.Field)]
		if exists && adsMeasureReaggregatable(measure, field) {
			return true
		}
	}
	return false
}

func adsMeasureReaggregatable(
	measure dataset.AnalysisMeasureContract,
	field dataset.Field,
) bool {
	if measure.Additivity != "ADDITIVE" {
		return false
	}
	behavior := strings.ToUpper(strings.TrimSpace(measure.ValueBehavior))
	if inferred := inferredDWSMeasureValueBehavior(field); inferred != "" {
		behavior = inferred
	}
	return behavior == "" || behavior == "FLOW"
}

func compatibleDWSContainment(
	source, target dataset.Document,
) bool {
	sourceFields := make(map[string]dataset.Field, len(source.Fields))
	for _, field := range source.Fields {
		sourceFields[strings.ToLower(field.Code)] = field
	}
	for _, code := range dwsDimensionCodes(target) {
		targetField := dataset.Field{}
		for _, field := range target.Fields {
			if strings.EqualFold(field.Code, code) {
				targetField = field
				break
			}
		}
		sourceField, exists := sourceFields[strings.ToLower(code)]
		if !exists ||
			!strings.EqualFold(
				sourceField.CanonicalType, targetField.CanonicalType,
			) ||
			(sourceField.SemanticType != "" &&
				targetField.SemanticType != "" &&
				!strings.EqualFold(
					sourceField.SemanticType, targetField.SemanticType,
				)) {
			return false
		}
	}
	if target.AnalysisContract == nil {
		return false
	}
	for _, measure := range target.AnalysisContract.Measures {
		for _, field := range target.Fields {
			if strings.EqualFold(field.Code, measure.Field) &&
				field.Role == "MEASURE" {
				return true
			}
		}
	}
	return false
}

func dwsDimensionCodes(document dataset.Document) []string {
	fieldByCode := make(map[string]dataset.Field, len(document.Fields))
	for _, field := range document.Fields {
		fieldByCode[strings.ToLower(field.Code)] = field
	}
	candidates := []string{}
	if document.AnalysisContract != nil {
		candidates = append(
			candidates,
			document.AnalysisContract.ConformedDimensions...,
		)
	}
	if len(candidates) == 0 {
		for _, field := range document.Fields {
			if field.Role != "MEASURE" {
				candidates = append(candidates, field.Code)
			}
		}
	}
	result := []string{}
	seen := map[string]bool{}
	for _, code := range candidates {
		key := strings.ToLower(strings.TrimSpace(code))
		field, exists := fieldByCode[key]
		if !exists || field.Role == "MEASURE" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, field.Code)
	}
	return result
}

func buildContainedDWSADSCandidate(
	source dataset.Record,
	sourceVersionID string,
	target adsDWSAsset,
) (dataset.Prepared, error) {
	sourceDocument, err := dataset.DecodeAndNormalize(source.DSL)
	if err != nil || sourceDocument.Dataset.Layer != dataset.LayerDWS ||
		target.Document.Dataset.Layer != dataset.LayerDWS ||
		uuid.Validate(sourceVersionID) != nil {
		return dataset.Prepared{}, ErrInvalidRequest
	}
	selected := selectADSContainmentTarget(
		sourceDocument, []adsDWSAsset{target},
	)
	if selected == nil {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	sourceFields := make(
		map[string]dataset.Field, len(sourceDocument.Fields),
	)
	for _, field := range sourceDocument.Fields {
		sourceFields[strings.ToLower(field.Code)] = field
	}
	targetFields := make(
		map[string]dataset.Field, len(target.Document.Fields),
	)
	for _, field := range target.Document.Fields {
		targetFields[strings.ToLower(field.Code)] = field
	}
	targetDimensions := dwsDimensionCodes(target.Document)
	sourceProjection := []string{}
	targetProjection := []string{}
	fields := []dataset.Field{}
	grainFields := []string{}
	preAggregationGroups := []dataset.PreAggregationGroup{}
	joinConditions := []dataset.JoinCondition{}
	visible := true
	for _, code := range targetDimensions {
		sourceField, exists := sourceFields[strings.ToLower(code)]
		targetField, targetExists := targetFields[strings.ToLower(code)]
		if !exists || !targetExists || sourceField.Role == "MEASURE" ||
			targetField.Role == "MEASURE" {
			return dataset.Prepared{}, ErrUnprovenPath
		}
		sourceProjection = append(sourceProjection, sourceField.Code)
		targetProjection = append(targetProjection, targetField.Code)
		preAggregationGroups = append(
			preAggregationGroups,
			dataset.PreAggregationGroup{Field: sourceField.Code},
		)
		joinConditions = append(joinConditions, dataset.JoinCondition{
			LeftExpression: dataset.Expression{
				Type: "FIELD_REF", NodeID: "source",
				Field: sourceField.Code,
			},
			Operator: "EQUALS",
			RightExpression: dataset.Expression{
				Type: "FIELD_REF", NodeID: "target",
				Field: targetField.Code,
			},
		})
		output := targetField
		output.ID = "field_" + safeDWSIdentifier(
			targetField.Code, "dimension",
		)
		output.Role = "DIMENSION"
		output.Visible = &visible
		output.Expression = dataset.Expression{
			Type: "FIELD_REF", NodeID: "target", Field: targetField.Code,
		}
		fields = append(fields, output)
		grainFields = append(grainFields, output.Code)
	}
	if sourceDocument.AnalysisContract == nil {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	preAggregationMetrics := []dataset.PreAggregationMetric{}
	sourceMetricCount := 0
	for _, measure := range sourceDocument.AnalysisContract.Measures {
		sourceField, exists :=
			sourceFields[strings.ToLower(measure.Field)]
		if !exists || sourceField.Role != "MEASURE" ||
			!adsMeasureReaggregatable(measure, sourceField) {
			continue
		}
		sourceProjection = append(sourceProjection, sourceField.Code)
		alias := boundedDWSFieldCode(
			"source_" + safeDWSIdentifier(sourceField.Code, "measure"),
		)
		expression := dataset.Expression{
			Type: "FIELD_REF", NodeID: "source", Field: sourceField.Code,
		}
		preAggregationMetrics = append(
			preAggregationMetrics, dataset.PreAggregationMetric{
				Field: alias, Function: "SUM", Expression: &expression,
			},
		)
		output := sourceField
		output.ID = "field_" + alias
		output.Code = alias
		output.Name = source.Name + " · " + sourceField.Name
		output.Visible = &visible
		output.Expression = dataset.Expression{
			Type: "FIELD_REF", NodeID: "source", Field: alias,
		}
		output.Aggregation = "SUM"
		fields = append(fields, output)
		sourceMetricCount++
		if sourceMetricCount == 3 {
			break
		}
	}
	if len(grainFields) == 0 || len(preAggregationMetrics) == 0 {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	targetMetricCount := 0
	if target.Document.AnalysisContract != nil {
		for _, measure := range target.Document.AnalysisContract.Measures {
			targetField, exists :=
				targetFields[strings.ToLower(measure.Field)]
			if !exists || targetField.Role != "MEASURE" {
				continue
			}
			targetProjection = append(targetProjection, targetField.Code)
			code := boundedDWSFieldCode(
				"target_" + safeDWSIdentifier(
					targetField.Code, "measure",
				),
			)
			output := targetField
			output.ID = "field_" + code
			output.Code = code
			output.Name = target.Record.Name + " · " + targetField.Name
			output.Visible = &visible
			output.Aggregation = ""
			output.Expression = dataset.Expression{
				Type: "FIELD_REF", NodeID: "target",
				Field: targetField.Code,
			}
			fields = append(fields, output)
			targetMetricCount++
			if targetMetricCount == 3 {
				break
			}
		}
	}
	if targetMetricCount == 0 {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	sourceCode := strings.TrimPrefix(
		safeDWSIdentifier(source.Code, "application"), "dws_",
	)
	targetCode := strings.TrimPrefix(
		safeDWSIdentifier(target.Record.Code, "rollup"), "dws_",
	)
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code: boundedDWSCode(
				"ads_" + sourceCode + "_by_" + targetCode,
			),
			Name: source.Name + "按" + target.Record.Name + "汇总",
			Description: "依据 DWS 维度包含关系，将较细粒度主题安全汇总到" +
				target.Record.Name + "的维度粒度并关联两侧指标",
			Domain:  sourceDocument.Dataset.Domain,
			Subject: sourceDocument.Dataset.Subject,
			Type:    "CROSS_SOURCE", Layer: dataset.LayerADS,
		},
		Nodes: []dataset.Node{
			{
				ID: "source", Type: "DATASET",
				DatasetVersionID: sourceVersionID,
				Alias:            "source", Projection: sourceProjection,
				SourceFilters: []dataset.SourceFilter{},
			},
			{
				ID: "target", Type: "DATASET",
				DatasetVersionID: target.VersionID,
				Alias:            "target", Projection: targetProjection,
				SourceFilters: []dataset.SourceFilter{},
			},
		},
		Joins: []dataset.Join{{
			ID: "join_target", LeftNodeID: "source",
			RightNodeID: "target", JoinType: "LEFT",
			Cardinality: "ONE_TO_ONE", RelationshipType: "DIRECT",
			FanoutPolicy: "SAFE", ManualConfirmed: true,
			Conditions: joinConditions,
		}},
		PreAggregations: []dataset.PreAggregation{{
			ID: "preagg_source", NodeID: "source",
			JoinID: "join_target", JoinSide: "LEFT",
			GroupBy: preAggregationGroups, Metrics: preAggregationMetrics,
		}},
		Fields:     fields,
		Filters:    []dataset.Filter{},
		GroupBy:    []string{},
		Having:     []dataset.Filter{},
		Sorts:      []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description: "每行代表 " + strings.Join(grainFields, " + "),
			KeyFields:   grainFields,
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

func buildADSCandidate(
	source dataset.Record,
	sourceVersionID string,
) (dataset.Prepared, error) {
	sourceDocument, err := dataset.DecodeAndNormalize(source.DSL)
	if err != nil || sourceDocument.Dataset.Layer != dataset.LayerDWS ||
		uuid.Validate(sourceVersionID) != nil {
		return dataset.Prepared{}, ErrInvalidRequest
	}
	projection := make([]string, 0, len(sourceDocument.Fields))
	fields := make([]dataset.Field, 0, len(sourceDocument.Fields))
	for _, sourceField := range sourceDocument.Fields {
		projection = append(projection, sourceField.Code)
		output := sourceField
		output.Expression = dataset.Expression{
			Type: "FIELD_REF", NodeID: "source", Field: sourceField.Code,
		}
		fields = append(fields, output)
	}
	if len(fields) == 0 || len(sourceDocument.OutputGrain.KeyFields) == 0 {
		return dataset.Prepared{}, ErrUnprovenPath
	}
	sourceCode := safeDWSIdentifier(source.Code, "application")
	if strings.HasPrefix(sourceCode, "dws_") {
		sourceCode = strings.TrimPrefix(sourceCode, "dws_")
	}
	document := dataset.Document{
		DSLVersion: dataset.DSLVersion,
		Dataset: dataset.Descriptor{
			Code:        boundedDWSCode("ads_" + sourceCode),
			Name:        source.Name + "应用数据",
			Description: "基于精确 DWS 发布版本生成的可评审应用数据草稿",
			Domain:      sourceDocument.Dataset.Domain,
			Subject:     sourceDocument.Dataset.Subject,
			Type:        sourceDocument.Dataset.Type,
			Layer:       dataset.LayerADS,
		},
		Nodes: []dataset.Node{{
			ID: "source", Type: "DATASET",
			DatasetVersionID: sourceVersionID,
			Alias:            "source",
			Projection:       projection,
			SourceFilters:    []dataset.SourceFilter{},
		}},
		Joins:      []dataset.Join{},
		Fields:     fields,
		Filters:    []dataset.Filter{},
		GroupBy:    []string{},
		Having:     []dataset.Filter{},
		Sorts:      []dataset.Sort{},
		Parameters: []dataset.Parameter{},
		OutputGrain: dataset.OutputGrain{
			Description:      sourceDocument.OutputGrain.Description,
			KeyFields:        append([]string(nil), sourceDocument.OutputGrain.KeyFields...),
			TimeField:        sourceDocument.OutputGrain.TimeField,
			DefaultTimeGrain: sourceDocument.OutputGrain.DefaultTimeGrain,
		},
		ExecutionPolicy: dataset.ExecutionPolicy{
			Mode: "MATERIALIZED_PREFERRED", TimeoutMS: 5000,
			PreviewLimit: 100, ResultLimit: 10000, CacheTTLSeconds: 300,
			Materialization: dataset.MaterializationPolicy{
				Enabled: true, RefreshMode: "MANUAL",
			},
		},
	}
	raw, err := json.Marshal(document)
	if err != nil {
		return dataset.Prepared{}, err
	}
	return dataset.Prepare(raw)
}

func (worker *ADSModelingWorker) upsertADS(
	ctx context.Context,
	claim adsModelingClaim,
	prepared dataset.Prepared,
	targetDatasetID, targetVersionID string,
) (datasetID, action string, err error) {
	var existingDatasetID, lastHash string
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT
				ads_dataset_id::text,last_generated_dsl_hash
			FROM platform.ads_modeling_outputs
			WHERE source_dws_dataset_id=$1::uuid`,
			claim.SourceDatasetID,
		).Scan(&existingDatasetID, &lastHash)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", "", err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		recoveredID, recovered, recoverErr := worker.recoverADS(
			ctx, claim, prepared,
		)
		if recoverErr != nil {
			return "", "", recoverErr
		}
		if recovered {
			if saveErr := worker.saveADSOutput(
				ctx, claim, recoveredID, prepared.DSLHash, "UNCHANGED",
				targetDatasetID, targetVersionID,
			); saveErr != nil {
				return "", "", saveErr
			}
			return recoveredID, "UNCHANGED", nil
		}
		record, createErr := worker.datasets.Create(
			ctx, claim.TenantID, claim.ActorID, dataset.CreateInput{
				Code:        prepared.Document.Dataset.Code,
				Name:        prepared.Document.Dataset.Name,
				Description: prepared.Document.Dataset.Description,
				Type:        prepared.Document.Dataset.Type,
				Layer:       dataset.LayerADS,
				DSL:         prepared.DSLJSON,
			},
		)
		if createErr != nil {
			return "", "", createErr
		}
		if saveErr := worker.saveADSOutput(
			ctx, claim, record.ID, prepared.DSLHash, "CREATED",
			targetDatasetID, targetVersionID,
		); saveErr != nil {
			return "", "", saveErr
		}
		return record.ID, "CREATED", nil
	}
	current, err := worker.datasets.Get(
		ctx, claim.TenantID, existingDatasetID,
	)
	if err != nil {
		return "", "", err
	}
	if current.DSLHash != lastHash {
		_ = worker.saveADSOutput(
			ctx, claim, current.ID, lastHash, "MANUAL_OWNED",
			targetDatasetID, targetVersionID,
		)
		return current.ID, "MANUAL_OWNED", nil
	}
	action = "UNCHANGED"
	if current.DSLHash != prepared.DSLHash {
		current, err = worker.datasets.Update(
			ctx, claim.TenantID, claim.ActorID, current.ID,
			dataset.UpdateInput{
				Name:            prepared.Document.Dataset.Name,
				Description:     prepared.Document.Dataset.Description,
				ExpectedVersion: current.Version,
				DSL:             prepared.DSLJSON,
			},
		)
		if err != nil {
			return "", "", err
		}
		action = "UPDATED"
	}
	if err := worker.saveADSOutput(
		ctx, claim, current.ID, prepared.DSLHash, action,
		targetDatasetID, targetVersionID,
	); err != nil {
		return "", "", err
	}
	return current.ID, action, nil
}

func (worker *ADSModelingWorker) recoverADS(
	ctx context.Context,
	claim adsModelingClaim,
	prepared dataset.Prepared,
) (datasetID string, recovered bool, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(ctx, `SELECT dataset.id::text
			FROM platform.datasets AS dataset
			JOIN platform.dataset_versions AS draft
			  ON draft.id=dataset.current_draft_version_id
			 AND draft.dataset_id=dataset.id
			 AND draft.tenant_id=dataset.tenant_id
			 AND draft.status='DRAFT'
			WHERE dataset.code=$1
			  AND dataset.layer='ADS'
			  AND dataset.created_by=$2::uuid
			  AND dataset.deleted_at IS NULL
			  AND draft.schema_hash=$3
			ORDER BY dataset.created_at DESC,dataset.id
			LIMIT 1`,
			prepared.Document.Dataset.Code, claim.ActorID, prepared.DSLHash,
		).Scan(&datasetID)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil
		}
		return scanErr
	})
	return datasetID, datasetID != "", err
}

func (worker *ADSModelingWorker) saveADSOutput(
	ctx context.Context,
	claim adsModelingClaim,
	datasetID, dslHash, action string,
	targetDatasetID, targetVersionID string,
) error {
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.ads_modeling_outputs(
				tenant_id,source_dws_dataset_id,ads_dataset_id,
				last_source_dws_version_id,last_job_id,
				last_generated_dsl_hash,last_action,
				target_dws_dataset_id,target_dws_version_id
			) VALUES(
			  $1,$2,$3,$4,$5,$6,$7,
			  NULLIF($8,'')::uuid,NULLIF($9,'')::uuid
			)
			ON CONFLICT(tenant_id,source_dws_dataset_id) DO UPDATE
			SET ads_dataset_id=EXCLUDED.ads_dataset_id,
				last_source_dws_version_id=EXCLUDED.last_source_dws_version_id,
				last_job_id=EXCLUDED.last_job_id,
				last_generated_dsl_hash=EXCLUDED.last_generated_dsl_hash,
				last_action=EXCLUDED.last_action,
				target_dws_dataset_id=EXCLUDED.target_dws_dataset_id,
				target_dws_version_id=EXCLUDED.target_dws_version_id,
				updated_at=now()`,
			claim.TenantID, claim.SourceDatasetID, datasetID,
			claim.SourceVersionID, claim.ID, dslHash, action,
			targetDatasetID, targetVersionID,
		)
		return err
	})
}

func (worker *ADSModelingWorker) finish(
	ctx context.Context,
	claim adsModelingClaim,
	workerID, status, errorCode, datasetID, action string,
) error {
	result, err := json.Marshal(map[string]string{
		"datasetId": datasetID, "action": action,
	})
	if err != nil {
		return err
	}
	generated, updated, skipped := 0, 0, 0
	switch action {
	case "CREATED":
		generated = 1
	case "UPDATED":
		updated = 1
	case "MANUAL_OWNED", "SKIPPED":
		skipped = 1
	}
	return database.WithTenantTx(ctx, worker.store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.ads_modeling_jobs
			SET status=$1,error_code=$2,
				error_message=CASE
				  WHEN $2='' THEN ''
				  ELSE '应用建模未完成，请查看规则校验或重试'
				END,
				generated_count=$3,updated_count=$4,skipped_count=$5,
				result_json=$6,lease_owner='',lease_token=NULL,
				lease_expires_at=NULL,completed_at=now(),updated_at=now()
			WHERE id=$7::uuid AND status='RUNNING'
			  AND lease_owner=$8 AND lease_token=$9::uuid`,
			status, errorCode, generated, updated, skipped,
			json.RawMessage(result), claim.ID, workerID, claim.LeaseToken,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrProjectionLease
		}
		return nil
	})
}

func RunADSModelingWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *ADSModelingWorker,
	workerID string,
	pollInterval time.Duration,
) {
	var group sync.WaitGroup
	group.Add(adsModelingConcurrency)
	for index := 1; index <= adsModelingConcurrency; index++ {
		index := index
		go func() {
			defer group.Done()
			runADSModelingLoop(
				ctx, logger, worker,
				workerID+"-ads-"+strconv.Itoa(index), pollInterval,
			)
		}()
	}
	group.Wait()
}

func runADSModelingLoop(
	ctx context.Context,
	logger *slog.Logger,
	worker *ADSModelingWorker,
	workerID string,
	pollInterval time.Duration,
) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		processed := false
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list ADS modeling tenants", "error", err)
		} else {
			for _, tenantID := range tenantIDs {
				didProcess, processErr := worker.ProcessNext(
					ctx, tenantID, workerID, 2*time.Minute,
				)
				if processErr != nil {
					logger.Error(
						"process ADS modeling",
						"tenant_id", tenantID, "error", processErr,
					)
				}
				processed = processed || didProcess
			}
		}
		if processed {
			timer.Reset(10 * time.Millisecond)
		} else {
			timer.Reset(pollInterval)
		}
	}
}
