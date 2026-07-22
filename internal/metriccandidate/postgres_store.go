package metriccandidate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresStore 持久化发布 outbox、worker 租约以及租户隔离的候选审核状态。
type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// EnqueueDatasetMetricExtractionTx 与数据集发布复用同一事务，避免出现“发布成功但
// 未提取”或“未发布版本被提取”的双写裂缝。
func (s *PostgresStore) EnqueueDatasetMetricExtractionTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID string,
	version dataset.VersionRecord,
) error {
	if tenantID == "" || version.Status != "PUBLISHED" || version.DatasetID == "" || version.ID == "" || version.DSLHash == "" {
		return ErrInvalidRequest
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.metric_extraction_jobs(
		tenant_id,dataset_id,dataset_version_id,dsl_hash,requested_by,extractor_version
	) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6)
	ON CONFLICT(tenant_id,dataset_version_id,extractor_version) DO NOTHING`,
		tenantID, version.DatasetID, version.ID, version.DSLHash, actorID, JobVersion)
	return err
}

// JobClaim 是 worker 对一个精确发布版本的短期租约。
type JobClaim struct {
	ID               string
	TenantID         string
	DatasetID        string
	DatasetVersionID string
	DSLHash          string
	RequestedBy      string
}

// ListJobTenantIDs 只读取未启用 RLS 的租户目录；实际 claim 和写入仍逐租户进入 RLS 事务。
func (s *PostgresStore) ListJobTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text FROM platform.tenants WHERE status='ACTIVE' AND deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ClaimJob 以 SKIP LOCKED 和过期租约实现多 worker 安全恢复。
func (s *PostgresStore) ClaimJob(ctx context.Context, tenantID, workerID string, lease time.Duration) (claim *JobClaim, err error) {
	if tenantID == "" || workerID == "" || lease < time.Second {
		return nil, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// An expired third lease means the worker died without reaching FailJob. Close it before
		// claiming more work so crashes cannot bypass the three-attempt budget indefinitely.
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_extraction_jobs SET
			status='FAILED',error_code='LEASE_EXPIRED',
			error_message='worker lease expired after maximum attempts',completed_at=now(),
			heartbeat_at=now(),lease_owner='',lease_expires_at=NULL
			WHERE attempt>=3 AND (
				(status='RUNNING' AND lease_expires_at<=now())
				OR (status='PENDING' AND next_attempt_at<=now())
			)`); err != nil {
			return err
		}
		var item JobClaim
		err := tx.QueryRow(ctx, `WITH candidate AS (
			SELECT id FROM platform.metric_extraction_jobs
			WHERE attempt<3 AND (
				(status='PENDING' AND next_attempt_at<=now())
				OR (status='RUNNING' AND lease_expires_at<=now())
			)
			ORDER BY created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		) UPDATE platform.metric_extraction_jobs AS job SET
			status='RUNNING',lease_owner=$1,
			lease_expires_at=now()+($2 * interval '1 second'),heartbeat_at=now(),
			attempt=attempt+1,started_at=COALESCE(started_at,now()),
			error_code='',error_message=''
		FROM candidate WHERE job.id=candidate.id
		RETURNING job.id::text,job.dataset_id::text,job.dataset_version_id::text,
			job.dsl_hash,COALESCE(job.requested_by::text,'')`, workerID, int64(lease/time.Second)).
			Scan(&item.ID, &item.DatasetID, &item.DatasetVersionID, &item.DSLHash, &item.RequestedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		item.TenantID = tenantID
		claim = &item
		return nil
	})
	return claim, err
}

func (s *PostgresStore) LoadExactDatasetVersion(ctx context.Context, claim JobClaim) (dataset.VersionRecord, error) {
	datasetStore := dataset.NewPostgresStore(s.pool)
	if err := datasetStore.ValidateVersionDependencies(ctx, claim.TenantID, claim.DatasetID, claim.DatasetVersionID); err != nil {
		return dataset.VersionRecord{}, err
	}
	version, err := datasetStore.GetVersion(ctx, claim.TenantID, claim.DatasetID, claim.DatasetVersionID)
	if err != nil {
		return dataset.VersionRecord{}, err
	}
	if version.DSLHash != claim.DSLHash {
		return dataset.VersionRecord{}, fmt.Errorf("metric extraction job dataset hash drift")
	}
	return version, nil
}

type persistedDraft struct {
	draft       CandidateDraft
	definition  json.RawMessage
	evidence    json.RawMessage
	lineage     json.RawMessage
	document    string
	confidence  float64
	assumptions []string
	method      string
}

// FinishJob 在一个事务内保存全部候选并收口任务；worker 崩溃不会留下部分候选批次。
func (s *PostgresStore) FinishJob(ctx context.Context, claim JobClaim, workerID string, result ExtractionResult) error {
	if result.DatasetID != claim.DatasetID || result.DatasetVersionID != claim.DatasetVersionID || result.DSLHash != claim.DSLHash ||
		(result.Status != TaskStatusSucceeded && result.Status != TaskStatusPartial) {
		return ErrInvalidRequest
	}
	persisted := make([]persistedDraft, 0, len(result.Candidates))
	ready, review, blocked := 0, 0, 0
	for _, draft := range result.Candidates {
		draft.Semantic = normalizeSemanticForPersistence(draft, claim)
		definition, err := json.Marshal(draft.Definition)
		if err != nil {
			return err
		}
		evidenceItems := make([]CandidateEvidence, 0, len(draft.Evidence))
		for _, item := range draft.Evidence {
			evidenceItems = append(evidenceItems, CandidateEvidence{Property: item.Code, Source: item.Path, Detail: item.Value})
		}
		evidence, err := json.Marshal(evidenceItems)
		if err != nil {
			return err
		}
		lineage, err := json.Marshal(draft.Semantic.Lineage)
		if err != nil {
			return err
		}
		method := "RULE"
		if draft.Semantic.Source == "HYBRID" {
			method = "HYBRID"
		}
		assumptions := []string{}
		switch draft.Status {
		case CandidateStatusReady:
			ready++
		case CandidateStatusNeedsReview:
			review++
			assumptions = append(assumptions, "源字段没有可直接采用的显式度量聚合，当前聚合方式仅为待确认建议。")
		case CandidateStatusBlocked:
			blocked++
		default:
			return ErrInvalidRequest
		}
		persisted = append(persisted, persistedDraft{
			draft: draft, definition: definition, evidence: evidence, lineage: lineage,
			document: EmbeddingDocument(draft.Semantic), method: method,
			confidence: confidenceScore(draft.Confidence), assumptions: assumptions,
		})
	}

	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		var owned bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.metric_extraction_jobs
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2 AND lease_expires_at>now()
			  AND dataset_id=$3 AND dataset_version_id=$4 AND dsl_hash=$5
		)`, claim.ID, workerID, claim.DatasetID, claim.DatasetVersionID, claim.DSLHash).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return errors.New("metric extraction job lease was lost")
		}
		// The source can be disabled or made stale after extraction begins. Revalidate under
		// row locks in the persistence transaction so an unavailable version never enters review.
		if err := dataset.ValidateVersionDependenciesInTx(ctx, tx, claim.DatasetID, claim.DatasetVersionID); err != nil {
			return err
		}
		for _, item := range persisted {
			definition := item.draft.Definition
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_candidates(
				tenant_id,job_id,dataset_id,dataset_version_id,dsl_hash,name,code,description,
				status,method,confidence,proposed_definition,source_field_ids,evidence,
				assumptions,warnings,block_reasons,fingerprint
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
			ON CONFLICT(tenant_id,fingerprint) DO NOTHING`,
				claim.TenantID, claim.ID, claim.DatasetID, claim.DatasetVersionID, claim.DSLHash,
				definition.Metric.Name, definition.Metric.Code, definition.Metric.Description,
				item.draft.Status, item.method, item.confidence, item.definition, []string{item.draft.SourceFieldID},
				item.evidence, item.assumptions, item.draft.Warnings, item.draft.BlockReasons,
				item.draft.Fingerprint); err != nil {
				return err
			}
			var candidateID string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM platform.metric_candidates
				WHERE tenant_id=$1 AND fingerprint=$2`, claim.TenantID, item.draft.Fingerprint).Scan(&candidateID); err != nil {
				return err
			}
			semantic := item.draft.Semantic
			if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_semantic_documents(
				tenant_id,subject_type,candidate_id,dataset_id,dataset_version_id,name,description,
				caliber,dimensions,period,period_description,lineage,lineage_summary,tags,document,
				semantic_source,llm_model,prompt_version,semantic_input_hash,ai_request_id,enrichment_error_code
			) VALUES($1,'CANDIDATE',$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,'')::uuid,$20)
			ON CONFLICT(tenant_id,candidate_id) WHERE subject_type='CANDIDATE' DO UPDATE SET
				name=EXCLUDED.name,description=EXCLUDED.description,caliber=EXCLUDED.caliber,
				dimensions=EXCLUDED.dimensions,period=EXCLUDED.period,
				period_description=EXCLUDED.period_description,lineage=EXCLUDED.lineage,
				lineage_summary=EXCLUDED.lineage_summary,tags=EXCLUDED.tags,document=EXCLUDED.document,
				semantic_source=EXCLUDED.semantic_source,llm_model=EXCLUDED.llm_model,
				prompt_version=EXCLUDED.prompt_version,semantic_input_hash=EXCLUDED.semantic_input_hash,
				ai_request_id=EXCLUDED.ai_request_id,enrichment_error_code=EXCLUDED.enrichment_error_code,
				embedding=NULL,embedding_model='',embedding_input_hash='',embedding_status='PENDING',
				embedding_attempt=0,embedding_error_code='',next_attempt_at=now(),lease_owner='',
				lease_expires_at=NULL,embedded_at=NULL,updated_at=now()
			WHERE platform.metric_semantic_documents.semantic_source<>'HYBRID'`,
				claim.TenantID, candidateID, claim.DatasetID, claim.DatasetVersionID,
				semantic.Name, semantic.Description, semantic.Caliber, semantic.Dimensions,
				semantic.Period, semantic.PeriodDescription, item.lineage, semantic.LineageSummary,
				semantic.Tags, item.document, semantic.Source, semantic.Model, semantic.PromptVersion,
				semantic.InputHash, semantic.RequestID, semantic.ErrorCode); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_extraction_jobs SET
			status=$1,total=$2,ready_count=$3,review_count=$4,blocked_count=$5,
			completed_at=now(),heartbeat_at=now(),lease_owner='',lease_expires_at=NULL
			WHERE id=$6 AND status='RUNNING' AND lease_owner=$7 AND lease_expires_at>now()`,
			result.Status, len(persisted), ready, review, blocked, claim.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metric extraction job finalization lost its lease")
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,NULLIF($2,'')::uuid,'EXTRACT_METRIC_CANDIDATES','DATASET',$3,
			jsonb_build_object('jobId',$4::text,'datasetVersionId',$5::text,'dslHash',$6::text,
			'total',$7::int,'ready',$8::int,'needsReview',$9::int,'blocked',$10::int))`,
			claim.TenantID, claim.RequestedBy, claim.DatasetID, claim.ID, claim.DatasetVersionID,
			claim.DSLHash, len(persisted), ready, review, blocked)
		return err
	})
}

func (s *PostgresStore) FailJob(ctx context.Context, claim JobClaim, workerID, code, message string) error {
	message = strings.ToValidUTF8(message, "�")
	if runes := []rune(message); len(runes) > 2000 {
		message = string(runes[:2000])
	}
	return database.WithTenantTx(ctx, s.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_extraction_jobs SET
			status=CASE WHEN attempt>=3 THEN 'FAILED' ELSE 'PENDING' END,
			error_code=$1,error_message=$2,
			next_attempt_at=CASE WHEN attempt=1 THEN now()+interval '30 seconds'
				WHEN attempt=2 THEN now()+interval '2 minutes' ELSE next_attempt_at END,
			completed_at=CASE WHEN attempt>=3 THEN now() ELSE NULL END,heartbeat_at=now(),
			lease_owner='',lease_expires_at=NULL
			WHERE id=$3 AND status='RUNNING' AND lease_owner=$4`,
			code, message, claim.ID, workerID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("metric extraction job failure update lost its lease")
		}
		return nil
	})
}

const candidateSelect = `candidate.id::text,candidate.dataset_id::text,candidate.dataset_version_id::text,
	candidate.dsl_hash,candidate.name,candidate.code::text,candidate.description,candidate.status,
	candidate.method,candidate.confidence::float8,candidate.proposed_definition,candidate.source_field_ids,
	candidate.evidence,candidate.assumptions,candidate.warnings,candidate.block_reasons,candidate.fingerprint,
	candidate.version,COALESCE(candidate.accepted_metric_id::text,''),candidate.decision_reason,
	to_char(candidate.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	to_char(candidate.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	COALESCE(semantic.name,candidate.name),COALESCE(semantic.description,candidate.description),
	COALESCE(semantic.caliber,''),COALESCE(semantic.dimensions,'{}'::text[]),
	COALESCE(semantic.period,'NONE'),COALESCE(semantic.period_description,'无固定统计周期'),
	COALESCE(semantic.lineage,'{}'::jsonb),COALESCE(semantic.lineage_summary,''),
	COALESCE(semantic.tags,'{}'::text[]),COALESCE(semantic.semantic_source,'RULE'),
	COALESCE(semantic.llm_model,''),COALESCE(semantic.prompt_version,''),
	COALESCE(semantic.semantic_input_hash,''),COALESCE(semantic.ai_request_id::text,''),
	COALESCE(semantic.enrichment_error_code,'')`

const candidateSemanticJoin = ` LEFT JOIN platform.metric_semantic_documents AS semantic
	ON semantic.tenant_id=candidate.tenant_id AND semantic.subject_type='CANDIDATE'
	AND semantic.candidate_id=candidate.id `

func (s *PostgresStore) List(ctx context.Context, tenantID string, filter ListFilter) (items []Candidate, total int, err error) {
	items = []Candidate{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_candidates
			WHERE ($1='' OR status=$1) AND ($2='' OR dataset_id::text=$2)`, filter.Status, filter.DatasetID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`
			WHERE ($1='' OR candidate.status=$1) AND ($2='' OR candidate.dataset_id::text=$2)
			ORDER BY candidate.updated_at DESC,candidate.id LIMIT $3 OFFSET $4`,
			filter.Status, filter.DatasetID, filter.Limit, filter.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Candidate
			if err := scanCandidate(rows, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (candidate Candidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`WHERE candidate.id::text=$1`, id), &candidate)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, ErrNotFound
	}
	return candidate, err
}

func (s *PostgresStore) Reject(ctx context.Context, tenantID, actorID, id string, input RejectInput) (candidate Candidate, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var status string
		var version int64
		var priorReason string
		err := tx.QueryRow(ctx, `SELECT status,version,decision_reason FROM platform.metric_candidates WHERE id::text=$1 FOR UPDATE`, id).
			Scan(&status, &version, &priorReason)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if status == string(CandidateStatusRejected) && priorReason == input.Reason {
			return scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`WHERE candidate.id::text=$1`, id), &candidate)
		}
		if status == string(CandidateStatusAccepted) || status == string(CandidateStatusRejected) {
			return ErrNotReviewable
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_candidates SET
			status='REJECTED',decision_reason=$1,reviewed_by=$2,reviewed_at=now(),
			version=version+1,updated_at=now() WHERE id::text=$3`, input.Reason, actorID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'REJECT','METRIC_CANDIDATE',$3,jsonb_build_object('reason',$4::text,'fromVersion',$5::bigint))`,
			tenantID, actorID, id, input.Reason, version); err != nil {
			return err
		}
		return scanCandidate(tx.QueryRow(ctx, `SELECT `+candidateSelect+` FROM platform.metric_candidates AS candidate`+candidateSemanticJoin+`WHERE candidate.id::text=$1`, id), &candidate)
	})
	return candidate, err
}

func scanCandidate(row interface{ Scan(...any) error }, candidate *Candidate) error {
	var status string
	var evidenceRaw json.RawMessage
	var lineageRaw json.RawMessage
	if err := row.Scan(
		&candidate.ID, &candidate.DatasetID, &candidate.DatasetVersionID, &candidate.DSLHash,
		&candidate.Name, &candidate.Code, &candidate.Description, &status, &candidate.Method,
		&candidate.Confidence, &candidate.ProposedDefinition, &candidate.SourceFieldIDs,
		&evidenceRaw, &candidate.Assumptions, &candidate.Warnings, &candidate.BlockReasons,
		&candidate.Fingerprint, &candidate.Version, &candidate.AcceptedMetricID,
		&candidate.DecisionReason, &candidate.CreatedAt, &candidate.UpdatedAt,
		&candidate.Semantic.Name, &candidate.Semantic.Description, &candidate.Semantic.Caliber,
		&candidate.Semantic.Dimensions, &candidate.Semantic.Period, &candidate.Semantic.PeriodDescription,
		&lineageRaw, &candidate.Semantic.LineageSummary, &candidate.Semantic.Tags,
		&candidate.Semantic.Source, &candidate.Semantic.Model, &candidate.Semantic.PromptVersion,
		&candidate.Semantic.InputHash, &candidate.Semantic.RequestID, &candidate.Semantic.ErrorCode,
	); err != nil {
		return err
	}
	candidate.Status = CandidateStatus(status)
	if err := json.Unmarshal(evidenceRaw, &candidate.Evidence); err != nil {
		return err
	}
	if err := json.Unmarshal(lineageRaw, &candidate.Semantic.Lineage); err != nil {
		return err
	}
	if candidate.SourceFieldIDs == nil {
		candidate.SourceFieldIDs = []string{}
	}
	if candidate.Evidence == nil {
		candidate.Evidence = []CandidateEvidence{}
	}
	if candidate.Assumptions == nil {
		candidate.Assumptions = []string{}
	}
	if candidate.Warnings == nil {
		candidate.Warnings = []string{}
	}
	if candidate.BlockReasons == nil {
		candidate.BlockReasons = []string{}
	}
	if candidate.Semantic.Dimensions == nil {
		candidate.Semantic.Dimensions = []string{}
	}
	if candidate.Semantic.Tags == nil {
		candidate.Semantic.Tags = []string{}
	}
	return nil
}

func normalizeSemanticForPersistence(draft CandidateDraft, claim JobClaim) SemanticMetadata {
	semantic := draft.Semantic
	if strings.TrimSpace(semantic.Name) == "" {
		semantic.Name = draft.Definition.Metric.Name
	}
	if strings.TrimSpace(semantic.Description) == "" {
		semantic.Description = draft.Definition.Metric.Description
	}
	if strings.TrimSpace(semantic.Caliber) == "" {
		semantic.Caliber = fmt.Sprintf("基于字段 %s，按 %s 聚合；空值处理为 %s", draft.SourceFieldCode, draft.Definition.Aggregation, draft.Definition.NullHandling)
	}
	if semantic.Dimensions == nil {
		semantic.Dimensions = []string{}
		for _, dimension := range draft.Definition.AllowedDimensions {
			semantic.Dimensions = append(semantic.Dimensions, dimension.Name)
		}
	}
	if semantic.Period == "" {
		semantic.Period = draft.Definition.TimeGrain
		if semantic.Period == "" {
			semantic.Period = "NONE"
		}
	}
	if semantic.PeriodDescription == "" {
		semantic.PeriodDescription = periodDescription(semantic.Period)
	}
	if semantic.Lineage.DatasetID == "" {
		semantic.Lineage = LineageMetadata{
			DatasetID: claim.DatasetID, DatasetVersionID: claim.DatasetVersionID,
			SourceFieldID: draft.SourceFieldID, Aggregation: draft.Definition.Aggregation,
			DimensionFieldIDs: []string{}, DependencyMetricVersionIDs: []string{},
		}
		for _, dimension := range draft.Definition.AllowedDimensions {
			semantic.Lineage.DimensionFieldIDs = append(semantic.Lineage.DimensionFieldIDs, dimension.FieldID)
		}
	}
	if semantic.LineageSummary == "" {
		semantic.LineageSummary = fmt.Sprintf("来自发布数据集版本 %s 的字段 %s，按 %s 聚合", claim.DatasetVersionID, draft.SourceFieldCode, draft.Definition.Aggregation)
	}
	semantic.Tags = nonEmptyUnique(append(semantic.Tags, semantic.Name, draft.Definition.Metric.Code, draft.Definition.Aggregation), 16, 32)
	if semantic.Source == "" {
		semantic.Source = "RULE"
	}
	if semantic.PromptVersion == "" {
		semantic.PromptVersion = MetricEnrichmentPromptVersion
	}
	if semantic.InputHash == "" {
		document := EmbeddingDocument(semantic)
		sum := sha256.Sum256([]byte(document))
		semantic.InputHash = hex.EncodeToString(sum[:])
	}
	return semantic
}

func confidenceScore(confidence Confidence) float64 {
	switch confidence {
	case ConfidenceHigh:
		return 0.95
	case ConfidenceMedium:
		return 0.75
	default:
		return 0.45
	}
}
