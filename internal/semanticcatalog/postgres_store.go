package semanticcatalog

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (store *PostgresStore) ListTenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrInvalidRequest
	}
	// Outbox and documents are FORCE RLS. The scheduler enumerates active tenants
	// only; all event access then occurs in WithTenantTx.
	rows, err := store.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tenantIDs := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	return tenantIDs, rows.Err()
}

const claimBatchSQL = `WITH picked AS (
	SELECT id
	FROM platform.semantic_change_outbox
	WHERE attempt<max_attempts
	  AND (
	    (status='PENDING' AND next_attempt_at<=now())
	    OR (status='RUNNING' AND lease_expires_at<=now())
	  )
	  AND ($4 OR subject_type<>'SEMANTIC_DOCUMENT')
	ORDER BY updated_at,id
	FOR UPDATE SKIP LOCKED
	LIMIT $1
)
UPDATE platform.semantic_change_outbox AS event
SET status='RUNNING',attempt=attempt+1,error_code='',
	lease_owner=$2,lease_token=gen_random_uuid(),
	lease_expires_at=now()+($3*interval '1 second'),
	completed_at=NULL,updated_at=now()
FROM picked
WHERE event.id=picked.id
RETURNING event.id::text,event.subject_type,event.subject_ref,event.event_kind,
	event.event_version,event.attempt,event.max_attempts,event.lease_token::text`

func (store *PostgresStore) ClaimBatch(
	ctx context.Context,
	tenantID string,
	workerID string,
	lease time.Duration,
	limit int,
	includeEmbedding bool,
) (claims []Claim, err error) {
	if store == nil || store.pool == nil || !validUUID(tenantID) ||
		!validWorkerID(workerID) || lease < time.Second || lease > time.Hour ||
		limit < 1 || limit > MaxBatchSize {
		return nil, ErrInvalidRequest
	}
	claims = []Claim{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE platform.semantic_documents AS document
			SET embedding=NULL,embedding_model='',embedding_input_hash='',
				embedding_status='FAILED',embedding_error_code='LEASE_EXPIRED',
				embedded_at=NULL,updated_at=now()
			FROM platform.semantic_change_outbox AS event
			WHERE event.subject_type='SEMANTIC_DOCUMENT'
			  AND event.subject_ref=document.id::text
			  AND event.status='RUNNING' AND event.lease_expires_at<=now()
			  AND event.attempt>=event.max_attempts
			  AND NOT (
			    document.embedding_status='SUCCEEDED'
			    AND document.embedding_input_hash=document.input_hash
			  )`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.semantic_change_outbox
			SET status='FAILED',error_code='LEASE_EXPIRED',
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=now(),updated_at=now()
			WHERE status='RUNNING' AND lease_expires_at<=now()
			  AND attempt>=max_attempts`); err != nil {
			return err
		}
		rows, err := tx.Query(
			ctx, claimBatchSQL, limit, workerID, int64(lease/time.Second), includeEmbedding,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			claim := Claim{TenantID: tenantID}
			if err := rows.Scan(
				&claim.ID, &claim.SubjectType, &claim.SubjectRef, &claim.EventKind,
				&claim.EventVersion, &claim.Attempt, &claim.MaxAttempts, &claim.LeaseToken,
			); err != nil {
				return err
			}
			claims = append(claims, claim)
		}
		return rows.Err()
	})
	return claims, err
}

const verifyLeaseSQL = `SELECT 1
	FROM platform.semantic_change_outbox
	WHERE id=$1 AND status='RUNNING' AND lease_owner=$2
	  AND lease_token::text=$3 AND event_version=$4
	  AND lease_expires_at>now()`

const verifyLeaseForUpdateSQL = verifyLeaseSQL + `
	FOR UPDATE`

const heartbeatEventSQL = `UPDATE platform.semantic_change_outbox
	SET lease_expires_at=GREATEST(
			lease_expires_at,
			clock_timestamp()+($1*interval '1 millisecond')
		),
		updated_at=now()
	WHERE id=$2 AND status='RUNNING' AND lease_owner=$3
	  AND lease_token::text=$4 AND event_version=$5 AND attempt=$6
	  AND lease_expires_at>clock_timestamp()`

// Heartbeat extends only the exact claimed event generation. Matching the
// owner, random lease token, event version and attempt prevents a stale worker
// from reviving an expired, reclaimed or superseded event.
func (store *PostgresStore) Heartbeat(
	ctx context.Context,
	claim Claim,
	workerID string,
	lease time.Duration,
) error {
	if store == nil || store.pool == nil || !validClaim(claim) ||
		!validWorkerID(workerID) || lease < time.Second || lease > time.Hour {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(
			ctx, heartbeatEventSQL, lease.Milliseconds(), claim.ID, workerID,
			claim.LeaseToken, claim.EventVersion, claim.Attempt,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func (store *PostgresStore) Prepare(
	ctx context.Context,
	claim Claim,
	workerID string,
	model string,
) (work Work, err error) {
	if store == nil || store.pool == nil || !validClaim(claim) || !validWorkerID(workerID) {
		return Work{}, ErrInvalidRequest
	}
	work.Claim = claim
	subject, subjectErr := subjectFromClaim(claim)
	if subjectErr != nil {
		return Work{}, subjectErr
	}
	work.Subject = subject
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		if err := verifyLease(ctx, tx, claim, workerID, false); err != nil {
			return err
		}
		if claim.SubjectType == SubjectDocument {
			work.DocumentID = claim.SubjectRef
			if claim.EventKind == EventDelete {
				work.Missing = true
				return nil
			}
			var subjectType, status, embeddingModel, embeddingHash string
			queryErr := tx.QueryRow(ctx, `SELECT subject_type,document,input_hash,embedding_status,
					embedding_model,embedding_input_hash
				FROM platform.semantic_documents
				WHERE id=$1`, claim.SubjectRef).Scan(
				&subjectType, &work.Text, &work.InputHash, &status,
				&embeddingModel, &embeddingHash,
			)
			if errors.Is(queryErr, pgx.ErrNoRows) {
				work.Missing = true
				return nil
			}
			if queryErr != nil {
				return queryErr
			}
			if work.Text == "" || work.InputHash == "" || len(work.Text) > maxDocumentBytes {
				return ErrInvalidRequest
			}
			work.EmbeddingSuppressed = subjectType == SubjectDimensionMember
			work.Current = status == "SUCCEEDED" && embeddingModel == model &&
				embeddingHash == work.InputHash
			return nil
		}
		// 维度成员值只参与租户内的受控倒排查询，既不生成语义文档，
		// 也不在此 worker 内拼接包含原值的文本。
		if claim.SubjectType == SubjectDimensionMember {
			work.Missing = true
			return nil
		}
		if claim.EventKind == EventDelete {
			return nil
		}
		facts, loadErr := loadFacts(ctx, tx, subject)
		if errors.Is(loadErr, pgx.ErrNoRows) {
			work.Missing = true
			return nil
		}
		if loadErr != nil {
			return loadErr
		}
		text, inputHash, buildErr := buildDocument(facts)
		if buildErr != nil {
			return buildErr
		}
		work.Subject = facts.Subject
		work.Text = text
		work.InputHash = inputHash
		return nil
	})
	return work, err
}

func (store *PostgresStore) ApplyDocument(ctx context.Context, work Work, workerID string) error {
	if store == nil || store.pool == nil || work.SubjectType == SubjectDocument ||
		!validClaim(work.Claim) || !validWorkerID(workerID) {
		return ErrInvalidRequest
	}
	if work.EventKind == EventRebuild && !work.Missing &&
		(work.Text == "" || work.InputHash == "" || work.Subject.Type == "") {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, work.TenantID, func(tx pgx.Tx) error {
		if err := verifyLease(ctx, tx, work.Claim, workerID, true); err != nil {
			return err
		}
		status := "SUCCEEDED"
		if work.SubjectType == SubjectDimensionMember {
			if err := deleteDocument(ctx, tx, work.Subject); err != nil {
				return err
			}
			status = "SKIPPED"
		} else if work.EventKind == EventDelete || work.Missing {
			if err := deleteDocument(ctx, tx, work.Subject); err != nil {
				return err
			}
			if work.Missing && work.EventKind != EventDelete {
				status = "SKIPPED"
			}
		} else if err := upsertDocument(ctx, tx, work); err != nil {
			return err
		}
		return finishEvent(ctx, tx, work.Claim, workerID, status)
	})
}

func (store *PostgresStore) Acknowledge(ctx context.Context, work Work, workerID string) error {
	if store == nil || store.pool == nil || work.SubjectType != SubjectDocument ||
		!validClaim(work.Claim) || !validWorkerID(workerID) {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, work.TenantID, func(tx pgx.Tx) error {
		if err := verifyLease(ctx, tx, work.Claim, workerID, true); err != nil {
			return err
		}
		if work.EmbeddingSuppressed {
			tag, err := tx.Exec(ctx, `UPDATE platform.semantic_documents
				SET embedding=NULL,embedding_model='',embedding_input_hash='',
					embedding_status='SKIPPED',
					embedding_error_code='MEMBER_EMBEDDING_DISABLED',
					embedded_at=NULL,updated_at=now()
				WHERE id=$1 AND subject_type='DIMENSION_MEMBER'`, work.DocumentID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrSubjectChanged
			}
		}
		status := "SUCCEEDED"
		if work.Missing {
			status = "SKIPPED"
		}
		return finishEvent(ctx, tx, work.Claim, workerID, status)
	})
}

func (store *PostgresStore) CompleteEmbedding(
	ctx context.Context,
	work Work,
	workerID string,
	model string,
	vector []float32,
) error {
	if store == nil || store.pool == nil || work.SubjectType != SubjectDocument ||
		work.Missing || work.Current || work.DocumentID == "" || work.InputHash == "" ||
		!validClaim(work.Claim) || !validWorkerID(workerID) ||
		strings.TrimSpace(model) == "" || !validVector(vector) {
		return ErrInvalidRequest
	}
	literal := formatVector(vector)
	subjectChanged := false
	err := database.WithTenantTx(ctx, store.pool, work.TenantID, func(tx pgx.Tx) error {
		if err := verifyLease(ctx, tx, work.Claim, workerID, true); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_documents
			SET embedding=$1::halfvec,embedding_model=$2,embedding_input_hash=input_hash,
				embedding_status='SUCCEEDED',embedding_error_code='',embedded_at=now(),
				updated_at=now()
			WHERE id=$3 AND input_hash=$4
			  AND subject_type<>'DIMENSION_MEMBER'`,
			literal, model, work.DocumentID, work.InputHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			if finishErr := finishEvent(ctx, tx, work.Claim, workerID, "SKIPPED"); finishErr != nil {
				return finishErr
			}
			subjectChanged = true
			return nil
		}
		return finishEvent(ctx, tx, work.Claim, workerID, "SUCCEEDED")
	})
	if err != nil {
		return err
	}
	if subjectChanged {
		return ErrSubjectChanged
	}
	return nil
}

func (store *PostgresStore) Fail(
	ctx context.Context,
	claim Claim,
	workerID string,
	code string,
) error {
	code = normalizedCode(code)
	if store == nil || store.pool == nil || !validClaim(claim) ||
		!validWorkerID(workerID) || code == "" {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var attempt, maxAttempts int
		err := tx.QueryRow(ctx, `SELECT attempt,max_attempts
			FROM platform.semantic_change_outbox
			WHERE id=$1 AND status='RUNNING' AND lease_owner=$2
			  AND lease_token::text=$3 AND event_version=$4
			  AND lease_expires_at>now()
			FOR UPDATE`,
			claim.ID, workerID, claim.LeaseToken, claim.EventVersion,
		).Scan(&attempt, &maxAttempts)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrLeaseLost
		}
		if err != nil {
			return err
		}
		status := "PENDING"
		if attempt >= maxAttempts {
			status = "FAILED"
		}
		if status == "FAILED" && claim.SubjectType == SubjectDocument {
			if _, err := tx.Exec(ctx, `UPDATE platform.semantic_documents
				SET embedding=NULL,embedding_model='',embedding_input_hash='',
					embedding_status='FAILED',embedding_error_code=$1,
					embedded_at=NULL,updated_at=now()
				WHERE id=$2`, code, claim.SubjectRef); err != nil {
				return err
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.semantic_change_outbox
			SET status=$1,error_code=$2,
				next_attempt_at=CASE
				  WHEN attempt=1 THEN now()+interval '30 seconds'
				  WHEN attempt=2 THEN now()+interval '2 minutes'
				  ELSE now()+interval '10 minutes'
				END,
				lease_owner='',lease_token=NULL,lease_expires_at=NULL,
				completed_at=CASE WHEN $1='FAILED' THEN now() ELSE NULL END,
				updated_at=now()
			WHERE id=$3 AND status='RUNNING' AND lease_owner=$4
			  AND lease_token::text=$5 AND event_version=$6
			  AND lease_expires_at>now()`,
			status, code, claim.ID, workerID, claim.LeaseToken, claim.EventVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrLeaseLost
		}
		return nil
	})
}

func verifyLease(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	workerID string,
	forUpdate bool,
) error {
	query := verifyLeaseSQL
	if forUpdate {
		query = verifyLeaseForUpdateSQL
	}
	var one int
	err := tx.QueryRow(
		ctx, query, claim.ID, workerID, claim.LeaseToken, claim.EventVersion,
	).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}
	return err
}

func finishEvent(
	ctx context.Context,
	tx pgx.Tx,
	claim Claim,
	workerID string,
	status string,
) error {
	if status != "SUCCEEDED" && status != "SKIPPED" {
		return ErrInvalidRequest
	}
	tag, err := tx.Exec(ctx, `UPDATE platform.semantic_change_outbox
		SET status=$1,error_code='',lease_owner='',lease_token=NULL,
			lease_expires_at=NULL,completed_at=now(),updated_at=now()
		WHERE id=$2 AND status='RUNNING' AND lease_owner=$3
		  AND lease_token::text=$4 AND event_version=$5
		  AND lease_expires_at>now()`,
		status, claim.ID, workerID, claim.LeaseToken, claim.EventVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func loadFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var facts documentFacts
	var err error
	switch subject.Type {
	case SubjectTag:
		facts, err = loadTagFacts(ctx, tx, subject)
	case SubjectDatasetVersion:
		facts, err = loadDatasetFacts(ctx, tx, subject)
	case SubjectDatasetField:
		facts, err = loadDatasetFieldFacts(ctx, tx, subject)
	case SubjectDimension:
		facts, err = loadDimensionFacts(ctx, tx, subject)
	case SubjectDimensionMember:
		facts, err = loadMemberFacts(ctx, tx, subject)
	case SubjectMetricVersion:
		facts, err = loadMetricFacts(ctx, tx, subject)
	default:
		return documentFacts{}, ErrInvalidRequest
	}
	if err != nil {
		return documentFacts{}, err
	}
	if subject.Type != SubjectTag {
		facts.Tags, err = loadApprovedTags(ctx, tx, facts.Subject)
		if err != nil {
			return documentFacts{}, err
		}
	}
	return facts, nil
}

func loadTagFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var tagID, code, name, description, category, governance, status, parent string
	var version int64
	var aliases []string
	err := tx.QueryRow(ctx, `SELECT tag.id::text,tag.code::text,tag.name,tag.description,
			tag.category,tag.governance,tag.status,tag.version,
			COALESCE(parent.code::text||' '||parent.name,''),
			COALESCE(array_agg(alias.alias::text ORDER BY lower(alias.alias::text),alias.id)
			  FILTER(WHERE alias.id IS NOT NULL),'{}'::text[])
		FROM platform.semantic_tags AS tag
		LEFT JOIN platform.semantic_tags AS parent
		  ON parent.tenant_id=tag.tenant_id AND parent.id=tag.parent_tag_id
		LEFT JOIN platform.semantic_tag_aliases AS alias
		  ON alias.tenant_id=tag.tenant_id AND alias.tag_id=tag.id
		WHERE tag.id=$1
		GROUP BY tag.id,tag.code,tag.name,tag.description,tag.category,tag.governance,
			tag.status,tag.version,parent.code,parent.name`, subject.TagID).Scan(
		&tagID, &code, &name, &description, &category, &governance, &status,
		&version, &parent, &aliases,
	)
	if err != nil {
		return documentFacts{}, err
	}
	return documentFacts{
		Subject: Subject{Type: SubjectTag, TagID: tagID},
		Lines: []documentLine{
			{Label: "标签编码", Value: code},
			{Label: "标签名称", Value: name},
			{Label: "标签说明", Value: description},
			{Label: "标签分类", Value: category},
			{Label: "治理方式", Value: governance},
			{Label: "状态", Value: status},
			{Label: "版本", Value: integerText(version)},
			{Label: "父标签", Value: parent},
			{Label: "别名", Value: strings.Join(aliases, "、")},
		},
		Tags: append([]string{code, name, category}, aliases...),
	}, nil
}

func loadDatasetFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var datasetID, versionID, code, name, description, datasetType, layer, status, schemaHash, planHash string
	var versionNo, recordVersion int64
	err := tx.QueryRow(ctx, `SELECT dataset.id::text,version.id::text,dataset.code::text,
			dataset.name,dataset.description,dataset.dataset_type,version.layer,
			version.version_no,version.status,version.record_version,
			version.schema_hash,version.plan_hash
		FROM platform.dataset_versions AS version
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
		WHERE version.id=$1 AND dataset.deleted_at IS NULL`, subject.DatasetVersionID).Scan(
		&datasetID, &versionID, &code, &name, &description, &datasetType, &layer,
		&versionNo, &status, &recordVersion, &schemaHash, &planHash,
	)
	if err != nil {
		return documentFacts{}, err
	}
	facts := documentFacts{
		Subject: Subject{
			Type: SubjectDatasetVersion, DatasetID: datasetID, DatasetVersionID: versionID,
		},
		Lines: []documentLine{
			{Label: "数据集编码", Value: code},
			{Label: "数据集名称", Value: name},
			{Label: "数据集说明", Value: description},
			{Label: "数仓层级", Value: layer},
			{Label: "数据集类型", Value: datasetType},
			{Label: "版本号", Value: integerText(versionNo)},
			{Label: "修订号", Value: integerText(recordVersion)},
			{Label: "状态", Value: status},
			{Label: "结构摘要", Value: schemaHash},
			{Label: "计划摘要", Value: planHash},
		},
	}
	rows, err := tx.Query(ctx, `SELECT field_code::text,field_name,description,canonical_type,
			semantic_type,field_role,aggregation,nullable,ordinal_position
		FROM platform.dataset_fields
		WHERE dataset_version_id=$1
		ORDER BY ordinal_position,id`, versionID)
	if err != nil {
		return documentFacts{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var fieldCode, fieldName, description, canonicalType, semanticType, role, aggregation string
		var nullable bool
		var ordinal int
		if err := rows.Scan(
			&fieldCode, &fieldName, &description, &canonicalType, &semanticType, &role,
			&aggregation, &nullable, &ordinal,
		); err != nil {
			return documentFacts{}, err
		}
		facts.Lines = append(facts.Lines, documentLine{
			Label: "字段" + strconv.Itoa(ordinal),
			Value: fieldSummary(
				fieldCode, fieldName, canonicalType, semanticType, role, aggregation, nullable,
			),
		})
		if strings.TrimSpace(description) != "" {
			facts.Lines = append(facts.Lines, documentLine{
				Label: "字段" + strconv.Itoa(ordinal) + "说明",
				Value: description,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return documentFacts{}, err
	}
	return facts, nil
}

func loadDatasetFieldFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var datasetID, versionID, fieldID, datasetCode, datasetName, layer string
	var fieldCode, fieldName, description, canonicalType, semanticType, role, aggregation string
	var nullable, visible bool
	var versionNo, ordinal int64
	err := tx.QueryRow(ctx, `SELECT dataset.id::text,version.id::text,field.field_id,
			dataset.code::text,dataset.name,version.layer,version.version_no,
			field.field_code::text,field.field_name,field.description,field.canonical_type,
			field.semantic_type,field.field_role,field.aggregation,
			field.nullable,field.visible,field.ordinal_position
		FROM platform.dataset_fields AS field
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=field.tenant_id AND version.id=field.dataset_version_id
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
		WHERE field.dataset_version_id=$1 AND field.field_id=$2
		  AND dataset.deleted_at IS NULL`, subject.DatasetVersionID, subject.DatasetFieldID).Scan(
		&datasetID, &versionID, &fieldID, &datasetCode, &datasetName, &layer, &versionNo,
		&fieldCode, &fieldName, &description, &canonicalType, &semanticType, &role, &aggregation,
		&nullable, &visible, &ordinal,
	)
	if err != nil {
		return documentFacts{}, err
	}
	return documentFacts{
		Subject: Subject{
			Type: SubjectDatasetField, DatasetID: datasetID, DatasetVersionID: versionID,
			DatasetFieldID: fieldID,
		},
		Lines: []documentLine{
			{Label: "所属数据集", Value: datasetCode + " " + datasetName},
			{Label: "数据集版本", Value: integerText(versionNo)},
			{Label: "数仓层级", Value: layer},
			{Label: "字段编码", Value: fieldCode},
			{Label: "字段名称", Value: fieldName},
			{Label: "字段说明", Value: description},
			{Label: "规范类型", Value: canonicalType},
			{Label: "语义类型", Value: semanticType},
			{Label: "字段角色", Value: role},
			{Label: "聚合方式", Value: aggregation},
			{Label: "可空", Value: boolText(nullable)},
			{Label: "可见", Value: boolText(visible)},
			{Label: "字段顺序", Value: integerText(ordinal)},
		},
	}, nil
}

func loadDimensionFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var dimensionID, datasetID, versionID, fieldID, code, name, description string
	var dimensionType, policy, status, definitionHash, datasetCode, datasetName, layer string
	var fieldCode, fieldName, canonicalType, semanticType, role string
	var highCardinality, sensitive bool
	var version int64
	err := tx.QueryRow(ctx, `SELECT dimension.id::text,dimension.dataset_id::text,
			dimension.dataset_version_id::text,dimension.field_id,
			dimension.code::text,dimension.name,dimension.description,
			dimension.dimension_type,dimension.member_index_policy,
			dimension.high_cardinality,dimension.sensitive,dimension.status,
			dimension.definition_hash,dimension.version,
			dataset.code::text,dataset.name,version.layer,
			field.field_code::text,field.field_name,field.canonical_type,
			field.semantic_type,field.field_role
		FROM platform.semantic_dimensions AS dimension
		JOIN platform.dataset_versions AS version
		  ON version.tenant_id=dimension.tenant_id AND version.id=dimension.dataset_version_id
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=dimension.tenant_id AND dataset.id=dimension.dataset_id
		JOIN platform.dataset_fields AS field
		  ON field.tenant_id=dimension.tenant_id
		  AND field.dataset_version_id=dimension.dataset_version_id
		  AND field.field_id=dimension.field_id
		WHERE dimension.id=$1`, subject.DimensionID).Scan(
		&dimensionID, &datasetID, &versionID, &fieldID, &code, &name, &description,
		&dimensionType, &policy, &highCardinality, &sensitive, &status,
		&definitionHash, &version, &datasetCode, &datasetName, &layer,
		&fieldCode, &fieldName, &canonicalType, &semanticType, &role,
	)
	if err != nil {
		return documentFacts{}, err
	}
	return documentFacts{
		Subject: Subject{
			Type: SubjectDimension, DimensionID: dimensionID,
		},
		Lines: []documentLine{
			{Label: "维度编码", Value: code},
			{Label: "维度名称", Value: name},
			{Label: "维度说明", Value: description},
			{Label: "维度类型", Value: dimensionType},
			{Label: "成员索引策略", Value: policy},
			{Label: "高基数", Value: boolText(highCardinality)},
			{Label: "敏感维度", Value: boolText(sensitive)},
			{Label: "状态", Value: status},
			{Label: "版本", Value: integerText(version)},
			{Label: "定义摘要", Value: definitionHash},
			{Label: "所属数据集", Value: datasetCode + " " + datasetName},
			{Label: "数仓层级", Value: layer},
			{Label: "维度字段", Value: fieldSummary(
				fieldCode, fieldName, canonicalType, semanticType, role, "", false,
			)},
		},
	}, nil
}

func loadMemberFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var memberID, dimensionID, key, label, normalizedValue, status string
	var dimensionCode, dimensionName, dimensionType string
	var aliases []string
	err := tx.QueryRow(ctx, `SELECT member.id::text,member.dimension_id::text,
			member.member_key,member.canonical_label,member.normalized_value,member.status,
			dimension.code::text,dimension.name,dimension.dimension_type,
			COALESCE(array_agg(alias.alias ORDER BY lower(alias.normalized_alias),alias.id)
			  FILTER(WHERE alias.id IS NOT NULL),'{}'::text[])
		FROM platform.dimension_members AS member
		JOIN platform.semantic_dimensions AS dimension
		  ON dimension.tenant_id=member.tenant_id AND dimension.id=member.dimension_id
		LEFT JOIN platform.dimension_member_aliases AS alias
		  ON alias.tenant_id=member.tenant_id AND alias.dimension_member_id=member.id
		WHERE member.id=$1
		GROUP BY member.id,member.dimension_id,member.member_key,member.canonical_label,
			member.normalized_value,member.status,dimension.code,dimension.name,
			dimension.dimension_type`, subject.DimensionMemberID).Scan(
		&memberID, &dimensionID, &key, &label, &normalizedValue, &status,
		&dimensionCode, &dimensionName, &dimensionType, &aliases,
	)
	if err != nil {
		return documentFacts{}, err
	}
	return documentFacts{
		Subject: Subject{
			Type: SubjectDimensionMember, DimensionID: dimensionID, DimensionMemberID: memberID,
		},
		Lines: []documentLine{
			{Label: "维度", Value: dimensionCode + " " + dimensionName},
			{Label: "维度类型", Value: dimensionType},
			{Label: "成员键", Value: key},
			{Label: "规范名称", Value: label},
			{Label: "规范值", Value: normalizedValue},
			{Label: "别名", Value: strings.Join(aliases, "、")},
			{Label: "状态", Value: status},
		},
		Tags: append([]string{dimensionCode, dimensionName, key, label}, aliases...),
	}, nil
}

func loadMetricFacts(ctx context.Context, tx pgx.Tx, subject Subject) (documentFacts, error) {
	var metricID, versionID, datasetID, datasetVersionID, code, name, description string
	var metricType, status, definitionHash, aggregation, unit, timeGrain, additivity, nullHandling string
	var datasetCode, datasetName, datasetLayer string
	var versionNo int64
	err := tx.QueryRow(ctx, `SELECT metric.id::text,version.id::text,
			version.dataset_id::text,version.dataset_version_id::text,
			metric.code::text,
			COALESCE(NULLIF(version.definition_json#>>'{metric,name}',''),metric.name),
			COALESCE(version.definition_json#>>'{metric,description}',metric.description,''),
			metric.metric_type,version.status,version.version_no,version.definition_hash,
			COALESCE(version.definition_json->>'aggregation',''),
			COALESCE(version.definition_json->>'unit',''),
			COALESCE(version.definition_json->>'timeGrain',''),
			COALESCE(version.definition_json->>'additivity',''),
			COALESCE(version.definition_json->>'nullHandling',''),
			dataset.code::text,dataset.name,dataset_version.layer
		FROM platform.metric_versions AS version
		JOIN platform.metrics AS metric
		  ON metric.tenant_id=version.tenant_id AND metric.id=version.metric_id
		JOIN platform.datasets AS dataset
		  ON dataset.tenant_id=version.tenant_id AND dataset.id=version.dataset_id
		JOIN platform.dataset_versions AS dataset_version
		  ON dataset_version.tenant_id=version.tenant_id
		  AND dataset_version.id=version.dataset_version_id
		WHERE version.id=$1 AND metric.deleted_at IS NULL`, subject.MetricVersionID).Scan(
		&metricID, &versionID, &datasetID, &datasetVersionID, &code, &name,
		&description, &metricType, &status, &versionNo, &definitionHash,
		&aggregation, &unit, &timeGrain, &additivity, &nullHandling,
		&datasetCode, &datasetName, &datasetLayer,
	)
	if err != nil {
		return documentFacts{}, err
	}
	dimensions := []string{}
	rows, err := tx.Query(ctx, `SELECT dimension_name
		FROM platform.metric_dimensions
		WHERE metric_version_id=$1
		ORDER BY ordinal_position,field_id`, versionID)
	if err != nil {
		return documentFacts{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var dimension string
		if err := rows.Scan(&dimension); err != nil {
			return documentFacts{}, err
		}
		dimensions = append(dimensions, dimension)
	}
	if err := rows.Err(); err != nil {
		return documentFacts{}, err
	}
	return documentFacts{
		Subject: Subject{
			Type: SubjectMetricVersion, MetricID: metricID, MetricVersionID: versionID,
			MetricDatasetVersionID: datasetVersionID,
		},
		Lines: []documentLine{
			{Label: "指标编码", Value: code},
			{Label: "指标名称", Value: name},
			{Label: "指标说明", Value: description},
			{Label: "指标类型", Value: metricType},
			{Label: "聚合方式", Value: aggregation},
			{Label: "单位", Value: unit},
			{Label: "时间粒度", Value: timeGrain},
			{Label: "可加性", Value: additivity},
			{Label: "空值处理", Value: nullHandling},
			{Label: "适用维度", Value: strings.Join(dimensions, "、")},
			{Label: "所属数据集", Value: datasetCode + " " + datasetName},
			{Label: "数仓层级", Value: datasetLayer},
			{Label: "指标版本", Value: integerText(versionNo)},
			{Label: "版本状态", Value: status},
			{Label: "定义摘要", Value: definitionHash},
		},
		Tags: append([]string{code, name, metricType, aggregation, unit}, dimensions...),
	}, nil
}

func loadApprovedTags(ctx context.Context, tx pgx.Tx, subject Subject) ([]string, error) {
	query := `SELECT tag.code::text,tag.name,tag.description,tag.category,
			tag.governance,tag.status,tag.version,
			COALESCE(parent.code::text||' '||parent.name,''),
			COALESCE(array_agg(alias.alias::text ORDER BY lower(alias.alias::text),alias.id)
			  FILTER(WHERE alias.id IS NOT NULL),'{}'::text[])
		FROM platform.asset_tag_bindings AS binding
		JOIN platform.semantic_tags AS tag
		  ON tag.tenant_id=binding.tenant_id AND tag.id=binding.tag_id
		LEFT JOIN platform.semantic_tags AS parent
		  ON parent.tenant_id=tag.tenant_id AND parent.id=tag.parent_tag_id
		LEFT JOIN platform.semantic_tag_aliases AS alias
		  ON alias.tenant_id=tag.tenant_id AND alias.tag_id=tag.id
		WHERE binding.status='APPROVED' AND tag.status<>'DEPRECATED' AND `
	args := []any{}
	switch subject.Type {
	case SubjectDatasetVersion:
		query += `binding.asset_type='DATASET_VERSION' AND binding.dataset_version_id=$1`
		args = append(args, subject.DatasetVersionID)
	case SubjectDatasetField:
		query += `binding.asset_type='DATASET_FIELD' AND binding.dataset_version_id=$1
			AND binding.dataset_field_id=$2`
		args = append(args, subject.DatasetVersionID, subject.DatasetFieldID)
	case SubjectDimension:
		query += `binding.asset_type='DIMENSION' AND binding.dimension_id=$1`
		args = append(args, subject.DimensionID)
	case SubjectDimensionMember:
		query += `binding.asset_type='DIMENSION_MEMBER' AND binding.dimension_member_id=$1`
		args = append(args, subject.DimensionMemberID)
	case SubjectMetricVersion:
		query += `binding.asset_type='METRIC_VERSION' AND binding.metric_version_id=$1`
		args = append(args, subject.MetricVersionID)
	default:
		return nil, ErrInvalidRequest
	}
	query += ` GROUP BY tag.id,tag.code,tag.name,tag.description,tag.category,
		tag.governance,tag.status,tag.version,parent.code,parent.name
		ORDER BY lower(tag.code::text),tag.id`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var code, name, description, category, governance, status, parent string
		var version int64
		var aliases []string
		if err := rows.Scan(
			&code, &name, &description, &category, &governance, &status,
			&version, &parent, &aliases,
		); err != nil {
			return nil, err
		}
		result = append(
			result, code, name, description, category, governance, status,
			"标签版本 "+integerText(version), parent,
		)
		result = append(result, aliases...)
	}
	return result, rows.Err()
}

func upsertDocument(ctx context.Context, tx pgx.Tx, work Work) error {
	var documentID, inputHash, version, text, source string
	err := queryDocument(ctx, tx, work.Subject).Scan(
		&documentID, &inputHash, &version, &text, &source,
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && inputHash == work.InputHash && version == DocumentVersion &&
		text == work.Text && source == "RULE" {
		return nil
	}
	if err == nil {
		tag, updateErr := tx.Exec(ctx, `UPDATE platform.semantic_documents
			SET document_version=$1,document=$2,input_hash=$3,semantic_source='RULE',
				embedding=NULL,embedding_model='',embedding_input_hash='',
				embedding_status='PENDING',embedding_error_code='',embedded_at=NULL,
				updated_at=now()
			WHERE id=$4`, DocumentVersion, work.Text, work.InputHash, documentID)
		if updateErr != nil {
			return updateErr
		}
		if tag.RowsAffected() != 1 {
			return ErrSubjectChanged
		}
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.semantic_documents(
			tenant_id,subject_type,tag_id,dataset_id,dataset_version_id,dataset_field_id,
			dimension_id,dimension_member_id,metric_id,metric_version_id,
			metric_dataset_version_id,document_version,document,input_hash,semantic_source
		) VALUES(
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'RULE'
		)`,
		work.TenantID, work.Subject.Type, nullString(work.TagID), nullString(work.DatasetID),
		nullString(work.DatasetVersionID), nullString(work.DatasetFieldID),
		nullString(work.DimensionID), nullString(work.DimensionMemberID),
		nullString(work.MetricID), nullString(work.MetricVersionID),
		nullString(work.MetricDatasetVersionID), DocumentVersion, work.Text, work.InputHash,
	)
	return err
}

func queryDocument(ctx context.Context, tx pgx.Tx, subject Subject) pgx.Row {
	query, args := subjectDocumentQuery(subject, `SELECT id::text,input_hash,document_version,
		document,semantic_source FROM platform.semantic_documents WHERE `)
	return tx.QueryRow(ctx, query, args...)
}

func deleteDocument(ctx context.Context, tx pgx.Tx, subject Subject) error {
	query, args := subjectDocumentQuery(subject, `DELETE FROM platform.semantic_documents WHERE `)
	_, err := tx.Exec(ctx, query, args...)
	return err
}

func subjectDocumentQuery(subject Subject, prefix string) (string, []any) {
	switch subject.Type {
	case SubjectTag:
		return prefix + `subject_type='TAG' AND tag_id=$1`, []any{subject.TagID}
	case SubjectDatasetVersion:
		return prefix + `subject_type='DATASET_VERSION' AND dataset_version_id=$1`,
			[]any{subject.DatasetVersionID}
	case SubjectDatasetField:
		return prefix + `subject_type='DATASET_FIELD' AND dataset_version_id=$1
			AND dataset_field_id=$2`, []any{subject.DatasetVersionID, subject.DatasetFieldID}
	case SubjectDimension:
		return prefix + `subject_type='DIMENSION' AND dimension_id=$1`, []any{subject.DimensionID}
	case SubjectDimensionMember:
		return prefix + `subject_type='DIMENSION_MEMBER' AND dimension_member_id=$1`,
			[]any{subject.DimensionMemberID}
	case SubjectMetricVersion:
		return prefix + `subject_type='METRIC_VERSION' AND metric_version_id=$1`,
			[]any{subject.MetricVersionID}
	default:
		// Callers validate subjects before reaching this helper. FALSE still fails
		// closed if a future caller violates that contract.
		return prefix + `FALSE`, nil
	}
}

func subjectFromClaim(claim Claim) (Subject, error) {
	subject := Subject{Type: claim.SubjectType}
	switch claim.SubjectType {
	case SubjectTag:
		subject.TagID = normalizedUUID(claim.SubjectRef)
	case SubjectDatasetVersion:
		subject.DatasetVersionID = normalizedUUID(claim.SubjectRef)
	case SubjectDatasetField:
		versionID, fieldID, found := strings.Cut(claim.SubjectRef, ":")
		if !found || !validFieldID(fieldID) {
			return Subject{}, ErrInvalidRequest
		}
		subject.DatasetVersionID = normalizedUUID(versionID)
		subject.DatasetFieldID = fieldID
	case SubjectDimension:
		subject.DimensionID = normalizedUUID(claim.SubjectRef)
	case SubjectDimensionMember:
		subject.DimensionMemberID = normalizedUUID(claim.SubjectRef)
	case SubjectMetricVersion:
		subject.MetricVersionID = normalizedUUID(claim.SubjectRef)
	case SubjectDocument:
		if normalizedUUID(claim.SubjectRef) == "" {
			return Subject{}, ErrInvalidRequest
		}
		return subject, nil
	default:
		return Subject{}, ErrInvalidRequest
	}
	if subjectIdentity(subject) == "" {
		return Subject{}, ErrInvalidRequest
	}
	return subject, nil
}

func subjectIdentity(subject Subject) string {
	switch subject.Type {
	case SubjectTag:
		return subject.TagID
	case SubjectDatasetVersion:
		return subject.DatasetVersionID
	case SubjectDatasetField:
		if subject.DatasetVersionID != "" && subject.DatasetFieldID != "" {
			return subject.DatasetVersionID + ":" + subject.DatasetFieldID
		}
	case SubjectDimension:
		return subject.DimensionID
	case SubjectDimensionMember:
		return subject.DimensionMemberID
	case SubjectMetricVersion:
		return subject.MetricVersionID
	}
	return ""
}

func validClaim(claim Claim) bool {
	if !validUUID(claim.ID) || !validUUID(claim.TenantID) || !validUUID(claim.LeaseToken) ||
		claim.EventVersion < 1 || claim.Attempt < 1 || claim.MaxAttempts < 1 ||
		claim.Attempt > claim.MaxAttempts || claim.SubjectRef == "" ||
		(claim.EventKind != EventRebuild && claim.EventKind != EventDelete) {
		return false
	}
	_, err := subjectFromClaim(claim)
	return err == nil
}

func validUUID(value string) bool {
	_, err := uuid.Parse(strings.TrimSpace(value))
	return err == nil
}

func normalizedUUID(value string) string {
	parsed, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.String()
}

func validFieldID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func normalizedCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' {
			return ""
		}
	}
	return value
}

func validVector(vector []float32) bool {
	if len(vector) != VectorDimensions {
		return false
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return false
		}
	}
	return true
}

func formatVector(vector []float32) string {
	var builder strings.Builder
	builder.Grow(len(vector) * 8)
	builder.WriteByte('[')
	for index, value := range vector {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(strconv.FormatFloat(float64(value), 'g', -1, 32))
	}
	builder.WriteByte(']')
	return builder.String()
}

var _ Store = (*PostgresStore)(nil)
