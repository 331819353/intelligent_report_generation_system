package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

type LearningTask string

const (
	TaskUnresolvedExpression    LearningTask = "UNRESOLVED_EXPRESSION"
	TaskFrequentClarification   LearningTask = "FREQUENT_CLARIFICATION"
	TaskConfusableMetric        LearningTask = "CONFUSABLE_METRIC"
	TaskConfusableMember        LearningTask = "CONFUSABLE_MEMBER"
	TaskRetrievalMiss           LearningTask = "RETRIEVAL_MISS"
	TaskReportMetricCombination LearningTask = "REPORT_METRIC_COMBINATION"
	TaskFeedbackCluster         LearningTask = "FEEDBACK_CLUSTER"
	TaskDataRequestCluster      LearningTask = "DATA_REQUEST_CLUSTER"
)

var LearningTasks = []LearningTask{
	TaskUnresolvedExpression, TaskFrequentClarification, TaskConfusableMetric,
	TaskConfusableMember, TaskRetrievalMiss, TaskReportMetricCombination, TaskFeedbackCluster,
	TaskDataRequestCluster,
}

type CandidateType string

const (
	CandidateBusinessTerm     CandidateType = "BUSINESS_TERM"
	CandidateNegativeContext  CandidateType = "NEGATIVE_CONTEXT"
	CandidateCertifiedExample CandidateType = "CERTIFIED_EXAMPLE"
	CandidateHardNegative     CandidateType = "HARD_NEGATIVE"
	CandidateSearchDocument   CandidateType = "SEARCH_DOCUMENT"
	CandidateKPIBundle        CandidateType = "KPI_BUNDLE"
	CandidateFixPriority      CandidateType = "FIX_PRIORITY"
	CandidateSemanticAsset    CandidateType = "SEMANTIC_ASSET"
)

var candidateTypeForTask = map[LearningTask]CandidateType{
	TaskUnresolvedExpression:    CandidateBusinessTerm,
	TaskFrequentClarification:   CandidateNegativeContext,
	TaskConfusableMetric:        CandidateCertifiedExample,
	TaskConfusableMember:        CandidateHardNegative,
	TaskRetrievalMiss:           CandidateSearchDocument,
	TaskReportMetricCombination: CandidateKPIBundle,
	TaskFeedbackCluster:         CandidateFixPriority,
	TaskDataRequestCluster:      CandidateSemanticAsset,
}

type LearningSignal struct {
	TenantID, DomainID      askdata.ID
	Task                    LearningTask
	KeyHash                 askdata.ContentHash
	Summary                 json.RawMessage
	Evidence                json.RawMessage
	OccurrenceCount         int64
	RepresentativeRunIDs    []askdata.ID
	FirstSeenAt, LastSeenAt time.Time
}

func (signal LearningSignal) Validate() error {
	if signal.TenantID.Validate() != nil || signal.DomainID.Validate() != nil || signal.KeyHash.Validate() != nil ||
		candidateTypeForTask[signal.Task] == "" || signal.OccurrenceCount < 1 || signal.OccurrenceCount > 1_000_000 ||
		signal.FirstSeenAt.IsZero() || signal.LastSeenAt.Before(signal.FirstSeenAt) ||
		len(signal.RepresentativeRunIDs) > 20 || !safeLearningJSON(signal.Summary) || !safeLearningJSON(signal.Evidence) {
		return ErrInvalid
	}
	seen := map[askdata.ID]bool{}
	for _, id := range signal.RepresentativeRunIDs {
		if id.Validate() != nil || seen[id] {
			return ErrInvalid
		}
		seen[id] = true
	}
	return nil
}

func safeLearningJSON(raw json.RawMessage) bool {
	if len(raw) < 2 || len(raw) > 128<<10 {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	forbidden := map[string]bool{"rawmembervalue": true, "memberlabel": true, "samplerows": true, "resultrows": true, "sql": true, "prompt": true, "questiontext": true}
	var walk func(any) bool
	walk = func(node any) bool {
		switch typed := node.(type) {
		case map[string]any:
			for key, item := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
				if forbidden[normalized] || !walk(item) {
					return false
				}
			}
		case []any:
			if len(typed) > 100 {
				return false
			}
			for _, item := range typed {
				if !walk(item) {
					return false
				}
			}
		case string:
			if len(typed) > 2048 || strings.ContainsRune(typed, '\x00') {
				return false
			}
		}
		return true
	}
	return walk(value)
}

type Candidate struct {
	ID                   askdata.ID          `json:"id"`
	TenantID             askdata.ID          `json:"tenantId"`
	DomainID             askdata.ID          `json:"domainId"`
	Task                 LearningTask        `json:"taskType"`
	Type                 CandidateType       `json:"candidateType"`
	State                string              `json:"candidateState"`
	ReviewStatus         string              `json:"reviewStatus"`
	KeyHash              askdata.ContentHash `json:"candidateKeyHash"`
	Summary              json.RawMessage     `json:"normalizedSummary"`
	Evidence             json.RawMessage     `json:"evidence"`
	OccurrenceCount      int64               `json:"occurrenceCount"`
	RepresentativeRunIDs []askdata.ID        `json:"representativeRunIds"`
	FirstSeenAt          time.Time           `json:"firstSeenAt"`
	LastSeenAt           time.Time           `json:"lastSeenAt"`
	Suppressed           bool                `json:"suppressed,omitempty"`
}

type CandidateStore interface {
	UpsertSignal(context.Context, LearningSignal, time.Time) (Candidate, error)
}
type SignalSource interface {
	TenantDomains(context.Context) ([][2]string, error)
	Mine(context.Context, string, string, LearningTask, int) ([]LearningSignal, error)
}

type ActiveLearningWorker struct {
	source SignalSource
	store  CandidateStore
	topN   int
}

func NewActiveLearningWorker(source SignalSource, store CandidateStore, topN int) (*ActiveLearningWorker, error) {
	if source == nil || store == nil || topN < 1 || topN > 1000 {
		return nil, ErrInvalid
	}
	return &ActiveLearningWorker{source: source, store: store, topN: topN}, nil
}
func (worker *ActiveLearningWorker) TenantDomains(ctx context.Context) ([][2]string, error) {
	return worker.source.TenantDomains(ctx)
}
func (worker *ActiveLearningWorker) ProcessDomain(ctx context.Context, tenantID, domainID string, now time.Time) (int, error) {
	processed := 0
	for _, task := range LearningTasks {
		signals, err := worker.source.Mine(ctx, tenantID, domainID, task, worker.topN)
		if err != nil {
			return processed, err
		}
		for _, signal := range signals {
			candidate, err := worker.store.UpsertSignal(ctx, signal, now)
			if err != nil {
				return processed, err
			}
			if !candidate.Suppressed {
				processed++
			}
		}
	}
	return processed, nil
}

func (repository *PostgresRepository) UpsertSignal(ctx context.Context, signal LearningSignal, now time.Time) (Candidate, error) {
	if repository == nil || repository.pool == nil || signal.Validate() != nil || now.IsZero() {
		return Candidate{}, ErrInvalid
	}
	var result Candidate
	err := database.WithTenantTx(ctx, repository.pool, string(signal.TenantID), func(tx pgx.Tx) error {
		runIDs := make([]string, len(signal.RepresentativeRunIDs))
		for index, id := range signal.RepresentativeRunIDs {
			runIDs[index] = string(id)
		}
		row := tx.QueryRow(ctx, `INSERT INTO askdata.active_learning_candidates(
			id,tenant_id,domain_id,task_type,candidate_key_hash,candidate_type,normalized_summary_json,
			evidence_json,occurrence_count,representative_run_ids,first_seen_at,last_seen_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::uuid[],$11,$12)
		ON CONFLICT(tenant_id,domain_id,task_type,candidate_key_hash) DO UPDATE SET
			occurrence_count=CASE WHEN EXCLUDED.task_type='DATA_REQUEST_CLUSTER'
				THEN GREATEST(askdata.active_learning_candidates.occurrence_count,EXCLUDED.occurrence_count)
				ELSE askdata.active_learning_candidates.occurrence_count+EXCLUDED.occurrence_count END,
			evidence_json=CASE WHEN EXCLUDED.task_type='DATA_REQUEST_CLUSTER'
				THEN EXCLUDED.evidence_json
				ELSE askdata.active_learning_candidates.evidence_json||EXCLUDED.evidence_json END,
			representative_run_ids=(SELECT ARRAY(SELECT DISTINCT item FROM unnest(
				askdata.active_learning_candidates.representative_run_ids||EXCLUDED.representative_run_ids) item LIMIT 20)),
			last_seen_at=GREATEST(askdata.active_learning_candidates.last_seen_at,EXCLUDED.last_seen_at),
			review_status=CASE WHEN askdata.active_learning_candidates.review_status='REJECTED' THEN 'PENDING'
				ELSE askdata.active_learning_candidates.review_status END,
			rejected_at=CASE WHEN askdata.active_learning_candidates.review_status='REJECTED' THEN NULL
				ELSE askdata.active_learning_candidates.rejected_at END,
			reviewed_by=CASE WHEN askdata.active_learning_candidates.review_status='REJECTED' THEN NULL
				ELSE askdata.active_learning_candidates.reviewed_by END,
			reviewed_at=CASE WHEN askdata.active_learning_candidates.review_status='REJECTED' THEN NULL
				ELSE askdata.active_learning_candidates.reviewed_at END,updated_at=$13
		WHERE askdata.active_learning_candidates.review_status<>'REJECTED'
			OR askdata.active_learning_candidates.rejected_at<=$13-interval '90 days'
		RETURNING id::text,tenant_id::text,domain_id::text,task_type,candidate_type,candidate_state,
			review_status,candidate_key_hash,normalized_summary_json,evidence_json,occurrence_count,
			representative_run_ids::text[],first_seen_at,last_seen_at`,
			uuid.NewString(), signal.TenantID, signal.DomainID, signal.Task, signal.KeyHash, candidateTypeForTask[signal.Task],
			signal.Summary, signal.Evidence, signal.OccurrenceCount, runIDs, signal.FirstSeenAt, signal.LastSeenAt, now)
		var ids []string
		err := row.Scan(&result.ID, &result.TenantID, &result.DomainID, &result.Task, &result.Type, &result.State, &result.ReviewStatus, &result.KeyHash, &result.Summary, &result.Evidence, &result.OccurrenceCount, &ids, &result.FirstSeenAt, &result.LastSeenAt)
		if errors.Is(err, pgx.ErrNoRows) {
			result = Candidate{TenantID: signal.TenantID, DomainID: signal.DomainID, Task: signal.Task, Type: candidateTypeForTask[signal.Task], KeyHash: signal.KeyHash, Suppressed: true}
			return nil
		}
		if err != nil {
			return err
		}
		for _, id := range ids {
			result.RepresentativeRunIDs = append(result.RepresentativeRunIDs, askdata.ID(id))
		}
		return nil
	})
	return result, err
}

func (repository *PostgresRepository) ListCandidates(ctx context.Context, identity Identity) ([]Candidate, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil {
		return nil, ErrInvalid
	}
	result := []Candidate{}
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,tenant_id::text,domain_id::text,task_type,candidate_type,candidate_state,
			review_status,candidate_key_hash,normalized_summary_json,evidence_json,occurrence_count,
			representative_run_ids::text[],first_seen_at,last_seen_at FROM askdata.active_learning_candidates
			WHERE tenant_id=$1 AND domain_id=$2 ORDER BY review_status,occurrence_count DESC,last_seen_at DESC,id`, identity.TenantID, identity.DomainID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			candidate, scanErr := scanCandidate(rows)
			if scanErr != nil {
				return scanErr
			}
			result = append(result, candidate)
		}
		return rows.Err()
	})
	return result, err
}

func (repository *PostgresRepository) ReviewCandidate(ctx context.Context, identity Identity, id askdata.ID, decision string, now time.Time) (Candidate, error) {
	if repository == nil || repository.pool == nil || identity.Validate() != nil || id.Validate() != nil ||
		(decision != "APPROVED" && decision != "REJECTED") || now.IsZero() {
		return Candidate{}, ErrInvalid
	}
	var result Candidate
	err := database.WithTenantTx(ctx, repository.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE askdata.active_learning_candidates SET review_status=$1,
			rejected_at=CASE WHEN $1='REJECTED' THEN $2 ELSE NULL END,reviewed_by=$3,reviewed_at=$2,updated_at=$2
			WHERE id=$4 AND tenant_id=$5 AND domain_id=$6 AND review_status='PENDING'
			  AND (platform.user_is_domain_administrator(domain_id) OR platform.user_is_platform_administrator())
			RETURNING id::text,tenant_id::text,domain_id::text,task_type,candidate_type,candidate_state,
			review_status,candidate_key_hash,normalized_summary_json,evidence_json,occurrence_count,
			representative_run_ids::text[],first_seen_at,last_seen_at`, decision, now, identity.ActorID, id, identity.TenantID, identity.DomainID)
		var err error
		result, err = scanCandidate(row)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Candidate{}, ErrNotFound
	}
	return result, err
}

type candidateScanner interface{ Scan(...any) error }

func scanCandidate(row candidateScanner) (Candidate, error) {
	var result Candidate
	var ids []string
	err := row.Scan(&result.ID, &result.TenantID, &result.DomainID, &result.Task, &result.Type,
		&result.State, &result.ReviewStatus, &result.KeyHash, &result.Summary, &result.Evidence,
		&result.OccurrenceCount, &ids, &result.FirstSeenAt, &result.LastSeenAt)
	for _, id := range ids {
		result.RepresentativeRunIDs = append(result.RepresentativeRunIDs, askdata.ID(id))
	}
	return result, err
}

type PostgresSignalSource struct{ pool *pgxpool.Pool }

func NewPostgresSignalSource(pool *pgxpool.Pool) *PostgresSignalSource {
	return &PostgresSignalSource{pool: pool}
}
func (source *PostgresSignalSource) TenantDomains(ctx context.Context) ([][2]string, error) {
	rows, err := source.pool.Query(ctx, `SELECT tenant_id::text,id::text FROM askdata.domains WHERE status='ACTIVE' ORDER BY tenant_id,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := [][2]string{}
	for rows.Next() {
		var pair [2]string
		if err := rows.Scan(&pair[0], &pair[1]); err != nil {
			return nil, err
		}
		result = append(result, pair)
	}
	return result, rows.Err()
}

// Mine uses only hashes, stable IDs and governed counters. Even member-pair
// mining never selects canonical_label or alias text.
func (source *PostgresSignalSource) Mine(ctx context.Context, tenantID, domainID string, task LearningTask, limit int) ([]LearningSignal, error) {
	if source == nil || source.pool == nil || uuid.Validate(tenantID) != nil || uuid.Validate(domainID) != nil || candidateTypeForTask[task] == "" || limit < 1 {
		return nil, ErrInvalid
	}
	query := learningQuery(task)
	if query == "" {
		return nil, ErrInvalid
	}
	result := []LearningSignal{}
	err := database.WithTenantTx(ctx, source.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, domainID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var signal LearningSignal
			var runID string
			signal.TenantID, signal.DomainID, signal.Task = askdata.ID(tenantID), askdata.ID(domainID), task
			if err := rows.Scan(&signal.KeyHash, &signal.Summary, &signal.Evidence, &signal.OccurrenceCount, &runID, &signal.FirstSeenAt, &signal.LastSeenAt); err != nil {
				return err
			}
			if runID != "" {
				signal.RepresentativeRunIDs = []askdata.ID{askdata.ID(runID)}
			}
			if signal.Validate() != nil {
				return ErrInvalid
			}
			result = append(result, signal)
		}
		return rows.Err()
	})
	return result, err
}

func learningQuery(task LearningTask) string {
	switch task {
	case TaskUnresolvedExpression:
		return `SELECT encode(digest('unresolved:'||artifact.artifact_hash,'sha256'),'hex'),jsonb_build_object('artifactHash',artifact.artifact_hash),jsonb_build_object('source','UNDERSTANDING_ARTIFACT'),1,artifact.question_run_id::text,artifact.created_at,artifact.created_at FROM askdata.question_artifacts artifact WHERE artifact.domain_id=$1 AND artifact.artifact_type='UNDERSTANDING' AND artifact.payload_json ? 'unresolvedSpans' ORDER BY artifact.created_at DESC LIMIT $2`
	case TaskFrequentClarification:
		return `SELECT encode(digest('clarification:'||artifact.artifact_hash,'sha256'),'hex'),jsonb_build_object('artifactHash',artifact.artifact_hash),jsonb_build_object('source','CLARIFICATION_ARTIFACT'),1,artifact.question_run_id::text,artifact.created_at,artifact.created_at FROM askdata.question_artifacts artifact WHERE artifact.domain_id=$1 AND artifact.artifact_type='CLARIFICATION' ORDER BY artifact.created_at DESC LIMIT $2`
	case TaskConfusableMetric:
		return `SELECT encode(digest('metric-pair:'||artifact.artifact_hash,'sha256'),'hex'),jsonb_build_object('candidateSetHash',artifact.artifact_hash),jsonb_build_object('source','CANDIDATE_SET'),1,artifact.question_run_id::text,artifact.created_at,artifact.created_at FROM askdata.question_artifacts artifact WHERE artifact.domain_id=$1 AND artifact.artifact_type='CANDIDATE_SET' ORDER BY artifact.created_at DESC LIMIT $2`
	case TaskConfusableMember:
		return `SELECT encode(digest('member-pair:'||left_member.member_key_hash||':'||right_member.member_key_hash,'sha256'),'hex'),jsonb_build_object('leftMemberVersionId',left_member.id,'rightMemberVersionId',right_member.id,'memberKeyHash',left_member.member_key_hash),jsonb_build_object('dimensions',jsonb_build_array(left_member.dimension_version_id,right_member.dimension_version_id)),1,''::text,GREATEST(left_member.created_at,right_member.created_at),GREATEST(left_member.updated_at,right_member.updated_at) FROM askdata.dimension_members left_member JOIN askdata.dimension_members right_member ON right_member.tenant_id=left_member.tenant_id AND right_member.domain_id=left_member.domain_id AND right_member.member_key_hash=left_member.member_key_hash AND right_member.dimension_version_id<>left_member.dimension_version_id AND right_member.id>left_member.id WHERE left_member.domain_id=$1 AND left_member.status='CERTIFIED' AND right_member.status='CERTIFIED' ORDER BY left_member.member_key_hash LIMIT $2`
	case TaskRetrievalMiss:
		return `SELECT encode(digest('retrieval-miss:'||ticket.id::text,'sha256'),'hex'),jsonb_build_object('ticketId',ticket.id,'issueType',ticket.issue_type),jsonb_build_object('suggestedStage',ticket.suggested_stage),1,ticket.question_run_id::text,ticket.created_at,ticket.updated_at FROM askdata.feedback_tickets ticket WHERE ticket.domain_id=$1 AND ticket.issue_type IN('METRIC','DIMENSION','MEMBER') AND ticket.suggested_stage IN('RETRIEVAL','BINDING') ORDER BY ticket.updated_at DESC LIMIT $2`
	case TaskReportMetricCombination:
		return `SELECT encode(digest('report-combination:'||array_to_string(asset.metric_version_ids,','),'sha256'),'hex'),jsonb_build_object('metricVersionIds',asset.metric_version_ids,'semanticReleaseId',asset.semantic_release_id),jsonb_build_object('reportSemanticAssetId',asset.id),1,''::text,asset.created_at,asset.updated_at FROM askdata.report_semantic_assets asset WHERE asset.domain_id=$1 AND asset.state='CERTIFIED' ORDER BY cardinality(asset.metric_version_ids) DESC,asset.updated_at DESC LIMIT $2`
	case TaskFeedbackCluster:
		return `SELECT encode(digest('feedback-cluster:'||ticket.issue_type||':'||COALESCE(ticket.attributed_stage,ticket.suggested_stage),'sha256'),'hex'),jsonb_build_object('issueType',ticket.issue_type,'stage',COALESCE(ticket.attributed_stage,ticket.suggested_stage)),jsonb_build_object('ticketCount',count(*)),count(*),(array_agg(ticket.question_run_id ORDER BY ticket.created_at))[1]::text,min(ticket.created_at),max(ticket.updated_at) FROM askdata.feedback_tickets ticket WHERE ticket.domain_id=$1 GROUP BY ticket.issue_type,COALESCE(ticket.attributed_stage,ticket.suggested_stage) ORDER BY count(*) DESC LIMIT $2`
	case TaskDataRequestCluster:
		return `WITH recent AS (
			SELECT request.id,request.requester_user_id,request.business_purpose,request.created_at,request.updated_at,
			  ARRAY(SELECT value FROM jsonb_array_elements_text(COALESCE(request.parsed_context_json->'metricIds','[]'::jsonb)) value ORDER BY value) AS metric_ids,
			  ARRAY(SELECT value FROM jsonb_array_elements_text(COALESCE(request.parsed_context_json->'dimensionIds','[]'::jsonb)) value ORDER BY value) AS dimension_ids,
			  upper(COALESCE(request.parsed_context_json#>>'{timeRange,grain}','')) AS grain
			FROM platform.data_requests AS request
			WHERE request.domain_id=$1 AND request.created_at>=now()-interval '30 days'
		),purpose_counts AS (
			SELECT metric_ids,dimension_ids,grain,business_purpose,count(*) AS purpose_count
			FROM recent GROUP BY metric_ids,dimension_ids,grain,business_purpose
		),ranked_purposes AS (
			SELECT metric_ids,dimension_ids,grain,business_purpose,
			  row_number() OVER(PARTITION BY metric_ids,dimension_ids,grain ORDER BY purpose_count DESC,business_purpose) AS purpose_rank
			FROM purpose_counts
		),purposes AS (
			SELECT metric_ids,dimension_ids,grain,jsonb_agg(business_purpose ORDER BY purpose_rank) AS typical_purposes
			FROM ranked_purposes WHERE purpose_rank<=5 GROUP BY metric_ids,dimension_ids,grain
		),clusters AS (
			SELECT metric_ids,dimension_ids,grain,count(*) AS request_count,
			  count(DISTINCT requester_user_id) AS requester_count,min(created_at) AS first_seen,max(updated_at) AS last_seen
			FROM recent GROUP BY metric_ids,dimension_ids,grain HAVING count(*)>=3
		)
		SELECT encode(digest('data-request-cluster:'||$1::text||':'||to_jsonb(cluster.metric_ids)::text||':'||to_jsonb(cluster.dimension_ids)::text||':'||cluster.grain,'sha256'),'hex'),
		  jsonb_build_object('metricIds',to_jsonb(cluster.metric_ids),'dimensionIds',to_jsonb(cluster.dimension_ids),'grain',cluster.grain),
		  jsonb_build_object('requestCount',cluster.request_count,'requesterCount',cluster.requester_count,'typicalBusinessPurposes',purpose.typical_purposes),
		  cluster.request_count,''::text,cluster.first_seen,cluster.last_seen
		FROM clusters AS cluster JOIN purposes AS purpose USING(metric_ids,dimension_ids,grain)
		ORDER BY cluster.request_count DESC,cluster.last_seen DESC LIMIT $2`
	default:
		return ""
	}
}
