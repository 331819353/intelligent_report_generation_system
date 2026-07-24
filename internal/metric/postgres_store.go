package metric

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/dataset"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresStore 使用租户事务保存指标草稿、派生索引和不可变发布版本。
type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建指标 PostgreSQL 仓储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Create 原子创建指标主对象、唯一草稿和可由定义重建的维度及依赖索引。
func (s *PostgresStore) Create(ctx context.Context, tenantID, actorID string, prepared Prepared) (Record, error) {
	return s.create(ctx, tenantID, actorID, "", 0, prepared)
}

// CreateFromCandidate 在同一事务内锁定候选版本：同数据集同编码指标已存在时
// 迭代其当前草稿，否则创建新的指标主对象。旧发布版本始终保持不可变。
func (s *PostgresStore) CreateFromCandidate(ctx context.Context, tenantID, actorID, candidateID string, expectedCandidateVersion int64, prepared Prepared) (Record, error) {
	if existing, err := s.GetByOriginCandidate(ctx, tenantID, candidateID); err == nil {
		return existing, nil
	} else if !errors.Is(err, ErrNotFound) {
		return Record{}, err
	}
	record, err := s.create(ctx, tenantID, actorID, candidateID, expectedCandidateVersion, prepared)
	if errors.Is(err, ErrAlreadyExists) || errors.Is(err, ErrOriginCandidateConflict) || errors.Is(err, ErrOriginCandidateUnavailable) {
		if existing, loadErr := s.GetByOriginCandidate(ctx, tenantID, candidateID); loadErr == nil {
			existingPrepared, prepareErr := Prepare(existing.Definition)
			if prepareErr == nil && existingPrepared.DefinitionHash == prepared.DefinitionHash {
				return existing, nil
			}
		}
	}
	return record, err
}

func (s *PostgresStore) create(ctx context.Context, tenantID, actorID, candidateID string, expectedCandidateVersion int64, prepared Prepared) (Record, error) {
	var metricID, versionID string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if candidateID != "" {
			var status string
			var version int64
			var candidateDefinition json.RawMessage
			err := tx.QueryRow(ctx, `SELECT status,version,proposed_definition
				FROM platform.metric_candidates WHERE id::text=$1 FOR UPDATE`, candidateID).
				Scan(&status, &version, &candidateDefinition)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrOriginCandidateUnavailable
			}
			if err != nil {
				return err
			}
			if version != expectedCandidateVersion {
				return ErrOriginCandidateConflict
			}
			if status != "READY" && status != "NEEDS_REVIEW" {
				return ErrOriginCandidateUnavailable
			}
			candidatePrepared, err := Prepare(candidateDefinition)
			if err != nil || !sameCandidateCalculation(candidatePrepared.Definition, prepared.Definition) {
				return ErrOriginCandidateUnavailable
			}
		}
		definition := prepared.Definition
		if candidateID != "" {
			var existingMetricID, existingDatasetID string
			err := tx.QueryRow(ctx, `SELECT id::text,dataset_id::text
				FROM platform.metrics
				WHERE code=$1 AND deleted_at IS NULL
				FOR UPDATE`, definition.Metric.Code).Scan(&existingMetricID, &existingDatasetID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err == nil {
				if existingDatasetID != definition.DatasetID {
					return ErrAlreadyExists
				}
				metricID = existingMetricID
				if err := iterateMetricDraftFromCandidateTx(
					ctx, tx, tenantID, actorID, metricID, candidateID, prepared,
				); err != nil {
					return err
				}
				return acceptMetricCandidateTx(
					ctx, tx, tenantID, actorID, candidateID, metricID, expectedCandidateVersion,
				)
			}
		}
		if err := validatePreparedReferencesTx(ctx, tx, "", prepared); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metrics(
			tenant_id,dataset_id,code,name,description,metric_type,origin_candidate_id,created_by,updated_by
		) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8,$8) RETURNING id::text`,
			tenantID, definition.DatasetID, definition.Metric.Code, definition.Metric.Name,
			definition.Metric.Description, definition.Metric.Type, candidateID, actorID).Scan(&metricID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metric_versions(
			tenant_id,metric_id,dataset_id,dataset_version_id,version_no,definition_version,
			definition_json,definition_hash,created_by,updated_by
		) VALUES($1,$2,$3,$4,1,$5,$6,$7,$8,$8) RETURNING id::text`,
			tenantID, metricID, definition.DatasetID, definition.DatasetVersionID, DefinitionVersion,
			prepared.DefinitionJSON, prepared.DefinitionHash, actorID).Scan(&versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metrics SET current_draft_version_id=$1 WHERE id=$2`, versionID, metricID); err != nil {
			return err
		}
		if err := replaceDerivedTx(ctx, tx, tenantID, metricID, versionID, prepared); err != nil {
			return err
		}
		if candidateID != "" {
			if err := acceptMetricCandidateTx(
				ctx, tx, tenantID, actorID, candidateID, metricID, expectedCandidateVersion,
			); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'CREATE','METRIC',$3,jsonb_strip_nulls(jsonb_build_object(
			'datasetId',$4::text,'datasetVersionId',$5::text,'definitionHash',$6::text,
			'originCandidateId',NULLIF($7,'')::text
		)))`, tenantID, actorID, metricID, definition.DatasetID, definition.DatasetVersionID, prepared.DefinitionHash, candidateID)
		return err
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return Record{}, ErrAlreadyExists
		}
		return Record{}, mapReferenceError(err)
	}
	return s.Get(ctx, tenantID, metricID)
}

func iterateMetricDraftFromCandidateTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, metricID, candidateID string,
	prepared Prepared,
) error {
	var metricVersion, draftRecordVersion int64
	var draftVersionID, currentDatasetVersionID string
	err := tx.QueryRow(ctx, `SELECT
		metric.version,draft.id::text,draft.record_version,draft.dataset_version_id::text
		FROM platform.metrics AS metric
		JOIN platform.metric_versions AS draft
		  ON draft.id=metric.current_draft_version_id
		 AND draft.metric_id=metric.id
		 AND draft.tenant_id=metric.tenant_id
		WHERE metric.id::text=$1 AND metric.deleted_at IS NULL
		FOR UPDATE OF metric,draft`, metricID).Scan(
		&metricVersion, &draftVersionID, &draftRecordVersion, &currentDatasetVersionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := validatePreparedReferencesTx(ctx, tx, metricID, prepared); err != nil {
		return err
	}
	// dataset_version_id 是维度与依赖复合外键的一部分；先清理旧草稿索引，
	// 再切换版本并按候选中的完整维度集合重建。
	if err := clearDerivedTx(ctx, tx, draftVersionID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE platform.metric_versions SET
		dataset_version_id=$1,definition_json=$2,definition_hash=$3,
		record_version=record_version+1,updated_by=$4
		WHERE id=$5 AND status='DRAFT' AND record_version=$6`,
		prepared.Definition.DatasetVersionID, prepared.DefinitionJSON, prepared.DefinitionHash,
		actorID, draftVersionID, draftRecordVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	definition := prepared.Definition
	if _, err := tx.Exec(ctx, `UPDATE platform.metrics SET
		name=$1,description=$2,metric_type=$3,version=version+1,updated_by=$4
		WHERE id::text=$5`, definition.Metric.Name, definition.Metric.Description,
		definition.Metric.Type, actorID, metricID); err != nil {
		return err
	}
	if err := replaceDerivedTx(ctx, tx, tenantID, metricID, draftVersionID, prepared); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES($1,$2,'ITERATE_FROM_CANDIDATE','METRIC',$3,jsonb_build_object(
		'candidateId',$4::text,'fromVersion',$5::bigint,
		'fromDatasetVersionId',$6::text,'toDatasetVersionId',$7::text,
		'draftRecordVersion',$8::bigint,'definitionHash',$9::text
	))`, tenantID, actorID, metricID, candidateID, metricVersion,
		currentDatasetVersionID, definition.DatasetVersionID, draftRecordVersion,
		prepared.DefinitionHash)
	return err
}

func acceptMetricCandidateTx(
	ctx context.Context,
	tx pgx.Tx,
	tenantID, actorID, candidateID, metricID string,
	expectedCandidateVersion int64,
) error {
	tag, err := tx.Exec(ctx, `UPDATE platform.metric_candidates SET
		status='ACCEPTED',accepted_metric_id=$1,decision_reason='',reviewed_by=$2,reviewed_at=now(),
		version=version+1,updated_at=now()
		WHERE id::text=$3 AND version=$4 AND status IN ('READY','NEEDS_REVIEW')`,
		metricID, actorID, candidateID, expectedCandidateVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrOriginCandidateConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
		tenant_id,actor_user_id,action,resource_type,resource_id,detail
	) VALUES($1,$2,'ACCEPT','METRIC_CANDIDATE',$3,
		jsonb_build_object('metricId',$4::text,'fromVersion',$5::bigint))`,
		tenantID, actorID, candidateID, metricID, expectedCandidateVersion)
	return err
}

func sameCandidateCalculation(candidate, accepted Definition) bool {
	// LLM enrichment may improve only the human-facing name and description after
	// deterministic extraction. Every executable and formatting fact, including unit,
	// remains byte-for-byte equivalent after normalization.
	candidate.Metric.Name = accepted.Metric.Name
	candidate.Metric.Description = accepted.Metric.Description
	candidateRaw, candidateErr := json.Marshal(candidate)
	acceptedRaw, acceptedErr := json.Marshal(accepted)
	return candidateErr == nil && acceptedErr == nil && string(candidateRaw) == string(acceptedRaw)
}

// GetByOriginCandidate 按首个来源候选或后续迭代候选读取同一指标草稿。
func (s *PostgresStore) GetByOriginCandidate(ctx context.Context, tenantID, candidateID string) (Record, error) {
	var metricID string
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT metric.id::text
			FROM platform.metric_candidates AS candidate
			JOIN platform.metrics AS metric
			  ON metric.tenant_id=candidate.tenant_id
			 AND (metric.id=candidate.accepted_metric_id OR metric.origin_candidate_id=candidate.id)
			 AND metric.deleted_at IS NULL
			WHERE candidate.id::text=$1
			ORDER BY (metric.id=candidate.accepted_metric_id) DESC
			LIMIT 1`, candidateID).Scan(&metricID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, err
	}
	return s.Get(ctx, tenantID, metricID)
}

// Get 读取指标主对象和 current_draft_version_id 指向的规范草稿。
func (s *PostgresStore) Get(ctx context.Context, tenantID, id string) (record Record, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT
			m.id::text,m.code::text,m.name,m.description,m.metric_type,m.status,m.version,
			v.id::text,v.version_no,v.record_version,COALESCE(m.current_published_version_id::text,''),
			m.dataset_id::text,v.dataset_version_id::text,v.definition_hash,v.definition_json,
			to_char(m.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
			to_char(m.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
			FROM platform.metrics AS m
			JOIN platform.metric_versions AS v
			  ON v.id=m.current_draft_version_id AND v.metric_id=m.id AND v.tenant_id=m.tenant_id
			WHERE m.id::text=$1 AND m.deleted_at IS NULL`, id).Scan(
			&record.ID, &record.Code, &record.Name, &record.Description, &record.Type, &record.Status,
			&record.Version, &record.DraftVersionID, &record.DraftVersionNo, &record.DraftRecordVersion,
			&record.CurrentPublishedVersionID, &record.DatasetID, &record.DatasetVersionID,
			&record.DefinitionHash, &record.Definition, &record.CreatedAt, &record.UpdatedAt,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return record, err
}

// List 按更新时间倒序返回指标摘要和租户内总数。
func (s *PostgresStore) List(ctx context.Context, tenantID string, limit, offset int) (items []Summary, total int, err error) {
	items = []Summary{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metrics WHERE deleted_at IS NULL`).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT
			m.id::text,m.code::text,m.name,m.description,m.metric_type,m.status,m.version,
			m.dataset_id::text,v.dataset_version_id::text,
			COALESCE(m.current_published_version_id::text,''),
			to_char(m.updated_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
			FROM platform.metrics AS m
			JOIN platform.metric_versions AS v
			  ON v.id=m.current_draft_version_id AND v.metric_id=m.id AND v.tenant_id=m.tenant_id
			WHERE m.deleted_at IS NULL
			ORDER BY m.updated_at DESC,m.id LIMIT $1 OFFSET $2`, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item Summary
			if err := rows.Scan(
				&item.ID, &item.Code, &item.Name, &item.Description, &item.Type, &item.Status,
				&item.Version, &item.DatasetID, &item.DatasetVersionID,
				&item.CurrentPublishedVersionID, &item.UpdatedAt,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

// Delete 将指标从活动资产目录中软删除，同时保留不可变版本和审计事实。
// 软删除时释放租户内业务编码，使用户可以从头创建同编码的新指标。
func (s *PostgresStore) Delete(ctx context.Context, tenantID, actorID, id string, input DeleteInput) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var version int64
		var code string
		err := tx.QueryRow(ctx, `SELECT version,code::text
			FROM platform.metrics
			WHERE id::text=$1 AND deleted_at IS NULL
			FOR UPDATE`, id).Scan(&version, &code)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if version != input.ExpectedVersion {
			return ErrConflict
		}
		var inUse bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.report_draft_dependencies AS dependency
			JOIN platform.reports AS report
			  ON report.id=dependency.report_id
			 AND report.tenant_id=dependency.tenant_id
			 AND report.deleted_at IS NULL
			WHERE (dependency.dependency_type='METRIC' AND dependency.dependency_id=$1)
			   OR (dependency.dependency_type='METRIC_VERSION' AND dependency.dependency_id IN (
			     SELECT version.id::text FROM platform.metric_versions AS version
			     WHERE version.metric_id::text=$1
			   ))
			UNION ALL
			SELECT 1
			FROM platform.metric_dependencies AS dependency
			JOIN platform.metric_versions AS downstream
			  ON downstream.id=dependency.metric_version_id
			 AND downstream.tenant_id=dependency.tenant_id
			JOIN platform.metrics AS downstream_metric
			  ON downstream_metric.id=downstream.metric_id
			 AND downstream_metric.tenant_id=downstream.tenant_id
			 AND downstream_metric.deleted_at IS NULL
			WHERE dependency.dependency_metric_id::text=$1
			  AND downstream.status IN ('DRAFT','PUBLISHED','STALE')
			UNION ALL
			SELECT 1 FROM platform.query_runs AS run
			WHERE run.metric_id::text=$1 AND run.status='RUNNING'
		)`, id).Scan(&inUse); err != nil {
			return err
		}
		if inUse {
			return ErrInUse
		}
		tombstoneCode := "deleted_" + strings.ReplaceAll(id, "-", "")
		tag, err := tx.Exec(ctx, `UPDATE platform.metrics SET
			code=$1,status='DEPRECATED',current_draft_version_id=NULL,
			current_published_version_id=NULL,deleted_at=now(),
			version=version+1,updated_by=$2
			WHERE id::text=$3 AND version=$4 AND deleted_at IS NULL`,
			tombstoneCode, actorID, id, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'DELETE','METRIC',$3,jsonb_build_object(
			'code',$4::text,'fromVersion',$5::bigint
		))`, tenantID, actorID, id, code, version)
		return err
	})
}

// Update 在主对象和草稿行锁内复核三重并发条件，再重建派生索引。
func (s *PostgresStore) Update(ctx context.Context, tenantID, actorID, id string, input UpdateInput, prepared Prepared) (Record, error) {
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var currentVersion, draftRecordVersion int64
		var draftVersionID, definitionHash, datasetID, metricCode string
		err := tx.QueryRow(ctx, `SELECT
			m.version,m.code::text,m.dataset_id::text,v.id::text,v.record_version,v.definition_hash
			FROM platform.metrics AS m
			JOIN platform.metric_versions AS v
			  ON v.id=m.current_draft_version_id AND v.metric_id=m.id AND v.tenant_id=m.tenant_id
			WHERE m.id::text=$1 AND m.deleted_at IS NULL FOR UPDATE OF m,v`, id).
			Scan(&currentVersion, &metricCode, &datasetID, &draftVersionID, &draftRecordVersion, &definitionHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentVersion != input.ExpectedVersion || draftRecordVersion != input.ExpectedDraftRecordVersion ||
			definitionHash != input.ExpectedDefinitionHash {
			return ErrConflict
		}
		// 编码和所属数据集属于主对象身份，即使绕过服务层也不能写出相互矛盾的草稿快照。
		if metricCode != prepared.Definition.Metric.Code || datasetID != prepared.Definition.DatasetID {
			return ErrInvalidDefinition
		}
		if err := validatePreparedReferencesTx(ctx, tx, id, prepared); err != nil {
			return err
		}
		// 数据集版本属于派生索引复合外键的一部分；先清理当前草稿索引，
		// 才能在同一事务中安全切换精确数据集版本并重建索引。
		if err := clearDerivedTx(ctx, tx, draftVersionID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE platform.metric_versions SET
			dataset_version_id=$1,definition_json=$2,definition_hash=$3,
			record_version=record_version+1,updated_by=$4
			WHERE id=$5 AND status='DRAFT' AND record_version=$6`,
			prepared.Definition.DatasetVersionID, prepared.DefinitionJSON, prepared.DefinitionHash,
			actorID, draftVersionID, draftRecordVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		definition := prepared.Definition
		if _, err := tx.Exec(ctx, `UPDATE platform.metrics SET
			name=$1,description=$2,metric_type=$3,version=version+1,updated_by=$4
			WHERE id::text=$5`, definition.Metric.Name, definition.Metric.Description,
			definition.Metric.Type, actorID, id); err != nil {
			return err
		}
		if err := replaceDerivedTx(ctx, tx, tenantID, id, draftVersionID, prepared); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'UPDATE_DRAFT','METRIC',$3,jsonb_build_object(
			'fromVersion',$4::bigint,'draftRecordVersion',$5::bigint,
			'datasetVersionId',$6::text,'definitionHash',$7::text
		))`, tenantID, actorID, id, currentVersion, draftRecordVersion,
			definition.DatasetVersionID, prepared.DefinitionHash)
		return err
	})
	if err != nil {
		return Record{}, mapReferenceError(err)
	}
	return s.Get(ctx, tenantID, id)
}

// GetDatasetVersion 按父数据集和精确版本 ID 加载非草稿数据集版本。
func (s *PostgresStore) GetDatasetVersion(ctx context.Context, tenantID, datasetID, versionID string) (dataset.VersionRecord, error) {
	return dataset.NewPostgresStore(s.pool).GetVersion(ctx, tenantID, datasetID, versionID)
}

// GetVersionByID 按精确版本 ID 加载非草稿指标版本，不回退到当前发布指针。
func (s *PostgresStore) GetVersionByID(ctx context.Context, tenantID, versionID string) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanVersionByIDTx(ctx, tx, versionID, &record)
	})
	return record, err
}

// ReplayPublication 仅在当前发布权限仍有效时精确重放首次成功响应。
func (s *PostgresStore) ReplayPublication(ctx context.Context, tenantID, actorID, metricID, key, requestHash string) (record VersionRecord, found bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := metricActionAllowedTx(ctx, tx, tenantID, actorID, metricID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return replayPublicationTx(ctx, tx, actorID, metricID, key, requestHash, &record, &found)
	})
	return record, found, err
}

// Publish 在同一指标行锁内分配版本号、复制草稿并移动当前发布指针。
func (s *PostgresStore) Publish(ctx context.Context, tenantID, actorID, metricID string, plan PublishPlan) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := metricActionAllowedTx(ctx, tx, tenantID, actorID, metricID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}

		var metricVersion, draftRecordVersion int64
		var draftVersionNo int
		var draftVersionID, definitionHash, datasetID, datasetVersionID string
		err = tx.QueryRow(ctx, `SELECT
			m.version,m.dataset_id::text,v.id::text,v.version_no,v.record_version,
			v.definition_hash,v.dataset_version_id::text
			FROM platform.metrics AS m
			JOIN platform.metric_versions AS v
			  ON v.id=m.current_draft_version_id AND v.metric_id=m.id AND v.tenant_id=m.tenant_id
			WHERE m.id::text=$1 AND m.deleted_at IS NULL FOR UPDATE OF m,v`, metricID).
			Scan(&metricVersion, &datasetID, &draftVersionID, &draftVersionNo,
				&draftRecordVersion, &definitionHash, &datasetVersionID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		// 行锁后重新查重，串行化相同键重试和不同键并发发布。
		var found bool
		if err := replayPublicationTx(ctx, tx, actorID, metricID, plan.IdempotencyKey, plan.RequestHash, &record, &found); err != nil || found {
			return err
		}
		if metricVersion != plan.ExpectedVersion || draftVersionID != plan.DraftVersionID ||
			draftRecordVersion != plan.ExpectedDraftRecordVersion || definitionHash != plan.ExpectedDefinitionHash ||
			plan.Prepared.DefinitionHash != definitionHash || plan.Prepared.Definition.DatasetID != datasetID ||
			plan.Prepared.Definition.DatasetVersionID != datasetVersionID {
			return ErrConflict
		}
		if err := validatePreparedReferencesTx(ctx, tx, metricID, plan.Prepared); err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `UPDATE platform.metric_versions SET
			version_no=version_no+1,updated_by=$1
			WHERE id=$2 AND status='DRAFT' AND record_version=$3`,
			actorID, draftVersionID, draftRecordVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}

		var publishedVersionID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metric_versions(
			tenant_id,metric_id,dataset_id,dataset_version_id,version_no,status,
			definition_version,definition_json,definition_hash,record_version,
			created_by,updated_by,published_at,published_by,
			source_draft_version_id,source_draft_record_version
		) VALUES($1,$2,$3,$4,$5,'PUBLISHING',$6,$7,$8,1,$9,$9,now(),$9,$10,$11)
		RETURNING id::text`, tenantID, metricID, datasetID, datasetVersionID, draftVersionNo,
			DefinitionVersion, plan.Prepared.DefinitionJSON, plan.Prepared.DefinitionHash,
			actorID, draftVersionID, draftRecordVersion).Scan(&publishedVersionID); err != nil {
			return err
		}
		if err := replaceDerivedTx(ctx, tx, tenantID, metricID, publishedVersionID, plan.Prepared); err != nil {
			return err
		}
		if tag, err := tx.Exec(ctx, `UPDATE platform.metric_versions SET status='PUBLISHED' WHERE id=$1 AND status='PUBLISHING'`, publishedVersionID); err != nil {
			return err
		} else if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metrics SET
			current_published_version_id=$1,status='PUBLISHED',version=version+1,updated_by=$2
			WHERE id=$3`, publishedVersionID, actorID, metricID); err != nil {
			return err
		}
		if err := scanVersionTx(ctx, tx, metricID, publishedVersionID, &record); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,'PUBLISH','METRIC',$3,jsonb_build_object(
			'metricVersionId',$4::text,'versionNo',$5::int,'draftVersionId',$6::text,
			'draftRecordVersion',$7::bigint,'datasetVersionId',$8::text,'definitionHash',$9::text
		))`, tenantID, actorID, metricID, publishedVersionID, draftVersionNo, draftVersionID,
			draftRecordVersion, datasetVersionID, plan.Prepared.DefinitionHash); err != nil {
			return err
		}
		responseJSON, err := json.Marshal(record)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.metric_publish_idempotency(
			tenant_id,metric_id,actor_user_id,idempotency_key,request_hash,published_version_id,response_json
		) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, metricID, actorID,
			plan.IdempotencyKey, plan.RequestHash, publishedVersionID, responseJSON)
		return err
	})
	if err != nil {
		return VersionRecord{}, mapPublicationPostgresError(err)
	}
	return record, nil
}

// GetVersion 按父指标和精确版本 ID 加载不可变发布快照。
func (s *PostgresStore) GetVersion(ctx context.Context, tenantID, metricID, versionID string) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		return scanVersionTx(ctx, tx, metricID, versionID, &record)
	})
	return record, err
}

// ListVersions 仅返回不可变发布版本，不暴露草稿或事务内构建态。
func (s *PostgresStore) ListVersions(ctx context.Context, tenantID, metricID string, limit, offset int) (items []VersionSummary, total int, err error) {
	items = []VersionSummary{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1 FROM platform.metrics WHERE id::text=$1 AND deleted_at IS NULL
		)`, metricID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metric_versions
			WHERE metric_id::text=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')`, metricID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT
			id::text,metric_id::text,version_no,status,dataset_id::text,dataset_version_id::text,
			definition_hash,source_draft_record_version,
			to_char(published_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),published_by::text
			FROM platform.metric_versions
			WHERE metric_id::text=$1 AND status IN ('PUBLISHED','STALE','DEPRECATED')
			ORDER BY version_no DESC,id LIMIT $2 OFFSET $3`, metricID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item VersionSummary
			if err := rows.Scan(
				&item.ID, &item.MetricID, &item.VersionNo, &item.Status, &item.DatasetID,
				&item.DatasetVersionID, &item.DefinitionHash, &item.DraftRecordVersion,
				&item.PublishedAt, &item.PublishedBy,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

// GetVersionUsage 汇总报告草稿、下游指标版本和仍在运行的查询引用。
func (s *PostgresStore) GetVersionUsage(ctx context.Context, tenantID, metricID, versionID string) (usage VersionUsage, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `WITH target AS (
			SELECT version.id
			FROM platform.metric_versions AS version
			JOIN platform.metrics AS metric
			  ON metric.id=version.metric_id AND metric.tenant_id=version.tenant_id
			WHERE metric.id::text=$1 AND version.id::text=$2 AND metric.deleted_at IS NULL
			  AND version.status IN ('PUBLISHED','STALE','DEPRECATED')
		)
		SELECT
			(SELECT count(DISTINCT dependency.report_id)::int
			 FROM platform.report_draft_dependencies AS dependency
			 JOIN platform.reports AS report
			   ON report.id=dependency.report_id AND report.tenant_id=dependency.tenant_id
			  AND report.deleted_at IS NULL
			 CROSS JOIN target
			 WHERE (dependency.dependency_type='METRIC_VERSION'
			   AND dependency.dependency_id=target.id::text)
			    OR (dependency.dependency_type='METRIC'
			   AND dependency.dependency_id=$1)),
			(SELECT count(DISTINCT dependency.metric_version_id)::int
			 FROM platform.metric_dependencies AS dependency
			 JOIN platform.metric_versions AS downstream
			   ON downstream.id=dependency.metric_version_id AND downstream.tenant_id=dependency.tenant_id
			 JOIN platform.metrics AS downstream_metric
			   ON downstream_metric.id=downstream.metric_id AND downstream_metric.tenant_id=downstream.tenant_id
			  AND downstream_metric.deleted_at IS NULL
			 CROSS JOIN target
			 WHERE dependency.dependency_metric_version_id=target.id AND downstream.status='DRAFT'),
			(SELECT count(DISTINCT dependency.metric_version_id)::int
			 FROM platform.metric_dependencies AS dependency
			 JOIN platform.metric_versions AS downstream
			   ON downstream.id=dependency.metric_version_id AND downstream.tenant_id=dependency.tenant_id
			 JOIN platform.metrics AS downstream_metric
			   ON downstream_metric.id=downstream.metric_id AND downstream_metric.tenant_id=downstream.tenant_id
			  AND downstream_metric.deleted_at IS NULL
			 CROSS JOIN target
			 WHERE dependency.dependency_metric_version_id=target.id
			   AND downstream.status IN ('PUBLISHED','STALE','DEPRECATED')),
			(SELECT count(*)::int FROM platform.query_runs AS run,target
			 WHERE run.metric_version_id=target.id AND run.status='RUNNING')
		FROM target`, metricID, versionID).Scan(
			&usage.ReportDraftReferences, &usage.DownstreamDraftReferences,
			&usage.DownstreamPublishedReferences, &usage.ActiveQueryRuns,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		return err
	})
	return usage, err
}

// TransitionVersion 在指标行锁内执行发布版本的单向生命周期迁移。
func (s *PostgresStore) TransitionVersion(ctx context.Context, tenantID, actorID, metricID, versionID string, input VersionTransitionInput) (record VersionRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := metricActionAllowedTx(ctx, tx, tenantID, actorID, metricID, "PUBLISH")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		validTransition := input.ExpectedStatus == "PUBLISHED" && (input.TargetStatus == "STALE" || input.TargetStatus == "DEPRECATED") ||
			input.ExpectedStatus == "STALE" && input.TargetStatus == "DEPRECATED"
		if !validTransition {
			return ErrInvalidTransition
		}
		var metricVersion int64
		var currentPublishedID, currentStatus string
		err = tx.QueryRow(ctx, `SELECT
			m.version,COALESCE(m.current_published_version_id::text,''),v.status
			FROM platform.metrics AS m
			JOIN platform.metric_versions AS v
			  ON v.id::text=$2 AND v.metric_id=m.id AND v.tenant_id=m.tenant_id
			WHERE m.id::text=$1 AND m.deleted_at IS NULL FOR UPDATE OF m,v`, metricID, versionID).
			Scan(&metricVersion, &currentPublishedID, &currentStatus)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		if metricVersion != input.ExpectedVersion || currentStatus != input.ExpectedStatus {
			return ErrConflict
		}
		if input.TargetStatus == "DEPRECATED" {
			// 目标版本行已被锁定；依赖插入触发器会对同一行加共享锁，
			// 因而并发发布下游指标不能越过这次占用复核。
			var activeDownstream bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(
					SELECT 1
					FROM platform.metric_dependencies AS dependency
					JOIN platform.metric_versions AS downstream
					  ON downstream.id=dependency.metric_version_id
					 AND downstream.tenant_id=dependency.tenant_id
					WHERE dependency.dependency_metric_version_id::text=$1
					  AND downstream.status IN ('PUBLISHED','STALE')
				)`, versionID).Scan(&activeDownstream); err != nil {
				return err
			}
			if activeDownstream {
				return ErrVersionInUse
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.metric_versions SET status=$1,updated_by=$2 WHERE id::text=$3`, input.TargetStatus, actorID, versionID); err != nil {
			return err
		}
		if currentPublishedID == versionID {
			if _, err := tx.Exec(ctx, `UPDATE platform.metrics SET
				current_published_version_id=NULL,status=$1,version=version+1,updated_by=$2
				WHERE id::text=$3`, input.TargetStatus, actorID, metricID); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `UPDATE platform.metrics SET
			version=version+1,updated_by=$1 WHERE id::text=$2`, actorID, metricID); err != nil {
			return err
		}
		if err := scanVersionTx(ctx, tx, metricID, versionID, &record); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO platform.audit_logs(
			tenant_id,actor_user_id,action,resource_type,resource_id,detail
		) VALUES($1,$2,$3,'METRIC',$4,jsonb_build_object(
			'metricVersionId',$5::text,'fromStatus',$6::text,'toStatus',$7::text
		))`, tenantID, actorID, "VERSION_"+input.TargetStatus, metricID,
			versionID, currentStatus, input.TargetStatus)
		return err
	})
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23514" {
			return VersionRecord{}, ErrInvalidTransition
		}
		return VersionRecord{}, err
	}
	return record, nil
}

func replayPublicationTx(ctx context.Context, tx pgx.Tx, actorID, metricID, key, requestHash string, record *VersionRecord, found *bool) error {
	var storedActorID, storedHash string
	var responseJSON []byte
	err := tx.QueryRow(ctx, `SELECT actor_user_id::text,request_hash,response_json
		FROM platform.metric_publish_idempotency
		WHERE metric_id::text=$1 AND idempotency_key=$2`, metricID, key).
		Scan(&storedActorID, &storedHash, &responseJSON)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if storedActorID != actorID || storedHash != requestHash {
		return ErrIdempotencyConflict
	}
	if err := json.Unmarshal(responseJSON, record); err != nil {
		return err
	}
	*found = true
	return nil
}

func scanVersionTx(ctx context.Context, tx pgx.Tx, metricID, versionID string, record *VersionRecord) error {
	err := tx.QueryRow(ctx, `SELECT
		v.id::text,v.metric_id::text,m.version,v.source_draft_version_id::text,
		v.source_draft_record_version,v.version_no,v.status,v.dataset_id::text,
		v.dataset_version_id::text,v.definition_hash,v.definition_json,
		to_char(v.published_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),v.published_by::text
		FROM platform.metric_versions AS v
		JOIN platform.metrics AS m
		  ON m.id=v.metric_id AND m.tenant_id=v.tenant_id AND m.deleted_at IS NULL
		WHERE m.id::text=$1 AND v.id::text=$2
		  AND v.status IN ('PUBLISHED','STALE','DEPRECATED')`, metricID, versionID).Scan(
		&record.ID, &record.MetricID, &record.MetricRecordVersion, &record.DraftVersionID,
		&record.DraftRecordVersion, &record.VersionNo, &record.Status, &record.DatasetID,
		&record.DatasetVersionID, &record.DefinitionHash, &record.Definition,
		&record.PublishedAt, &record.PublishedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	}
	return err
}

func scanVersionByIDTx(ctx context.Context, tx pgx.Tx, versionID string, record *VersionRecord) error {
	err := tx.QueryRow(ctx, `SELECT
		v.id::text,v.metric_id::text,m.version,v.source_draft_version_id::text,
		v.source_draft_record_version,v.version_no,v.status,v.dataset_id::text,
		v.dataset_version_id::text,v.definition_hash,v.definition_json,
		to_char(v.published_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),v.published_by::text
		FROM platform.metric_versions AS v
		JOIN platform.metrics AS m
		  ON m.id=v.metric_id AND m.tenant_id=v.tenant_id AND m.deleted_at IS NULL
		WHERE v.id::text=$1 AND v.status IN ('PUBLISHED','STALE','DEPRECATED')`, versionID).Scan(
		&record.ID, &record.MetricID, &record.MetricRecordVersion, &record.DraftVersionID,
		&record.DraftRecordVersion, &record.VersionNo, &record.Status, &record.DatasetID,
		&record.DatasetVersionID, &record.DefinitionHash, &record.Definition,
		&record.PublishedAt, &record.PublishedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrVersionNotFound
	}
	return err
}

func metricActionAllowedTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, metricID, action string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.user_roles AS user_role
		JOIN platform.roles AS role
		  ON role.id=user_role.role_id AND role.tenant_id=user_role.tenant_id
		 AND role.status='ACTIVE' AND role.deleted_at IS NULL
		JOIN platform.role_permissions AS role_permission
		  ON role_permission.role_id=user_role.role_id AND role_permission.tenant_id=user_role.tenant_id
		JOIN platform.permissions AS permission
		  ON permission.id=role_permission.permission_id AND permission.tenant_id=role_permission.tenant_id
		WHERE user_role.tenant_id=$1 AND user_role.user_id=$2
		  AND permission.resource_type='METRIC' AND permission.action=$3
		UNION ALL
		SELECT 1 FROM platform.object_permissions AS object_permission
		WHERE object_permission.tenant_id=$1 AND object_permission.object_type='METRIC'
		  AND object_permission.object_id::text=$4 AND object_permission.action=$3 AND (
			object_permission.subject_type='USER' AND object_permission.subject_id=$2
			OR object_permission.subject_type='ROLE' AND EXISTS(
				SELECT 1 FROM platform.user_roles AS user_role
				JOIN platform.roles AS role
				  ON role.id=user_role.role_id AND role.tenant_id=user_role.tenant_id
				 AND role.status='ACTIVE' AND role.deleted_at IS NULL
				WHERE user_role.tenant_id=$1 AND user_role.user_id=$2
				  AND user_role.role_id=object_permission.subject_id
			)
		  )
	)`, tenantID, actorID, action, metricID).Scan(&allowed)
	return allowed, err
}

// validatePreparedReferencesTx 在写入前锁定数据集版本，并复核全部逻辑字段和指标依赖。
func validatePreparedReferencesTx(ctx context.Context, tx pgx.Tx, metricID string, prepared Prepared) error {
	definition := prepared.Definition
	if err := dataset.ValidateVersionDependenciesInTx(ctx, tx, definition.DatasetID, definition.DatasetVersionID); err != nil {
		return ErrVersionUnavailable
	}
	fieldIDs := definitionFieldIDs(definition)
	if len(fieldIDs) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(DISTINCT field_id)::int
			FROM platform.dataset_fields
			WHERE dataset_version_id::text=$1 AND field_id=ANY($2::text[])`,
			definition.DatasetVersionID, fieldIDs).Scan(&count); err != nil {
			return err
		}
		if count != len(fieldIDs) {
			return ErrInvalidDefinition
		}
	}
	if len(prepared.DependencyVersionIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT version.id::text,version.metric_id::text
		FROM platform.metric_versions AS version
		JOIN platform.metrics AS owner
		  ON owner.id=version.metric_id AND owner.tenant_id=version.tenant_id
		 AND owner.deleted_at IS NULL
		WHERE version.id=ANY($1::uuid[]) AND version.status='PUBLISHED'
		  AND version.dataset_version_id::text=$2
		ORDER BY version.id`, prepared.DependencyVersionIDs, definition.DatasetVersionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	loaded := map[string]string{}
	for rows.Next() {
		var versionID, ownerMetricID string
		if err := rows.Scan(&versionID, &ownerMetricID); err != nil {
			return err
		}
		if metricID != "" && ownerMetricID == metricID {
			return ErrInvalidDefinition
		}
		loaded[versionID] = ownerMetricID
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(loaded) != len(prepared.DependencyVersionIDs) {
		return ErrVersionUnavailable
	}
	if metricID != "" {
		var cycle bool
		if err := tx.QueryRow(ctx, `WITH RECURSIVE graph(version_id,metric_id) AS (
			SELECT version.id,version.metric_id
			FROM platform.metric_versions AS version
			WHERE version.id=ANY($1::uuid[])
			UNION
			SELECT target.id,target.metric_id
			FROM graph
			JOIN platform.metric_dependencies AS dependency
			  ON dependency.metric_version_id=graph.version_id
			JOIN platform.metric_versions AS target
			  ON target.id=dependency.dependency_metric_version_id
		)
		SELECT EXISTS(SELECT 1 FROM graph WHERE metric_id::text=$2)`,
			prepared.DependencyVersionIDs, metricID).Scan(&cycle); err != nil {
			return err
		}
		if cycle {
			return ErrInvalidDefinition
		}
	}
	return nil
}

func replaceDerivedTx(ctx context.Context, tx pgx.Tx, tenantID, metricID, versionID string, prepared Prepared) error {
	if err := clearDerivedTx(ctx, tx, versionID); err != nil {
		return err
	}
	definition := prepared.Definition
	for index, dimension := range definition.AllowedDimensions {
		tag, err := tx.Exec(ctx, `INSERT INTO platform.metric_dimensions(
			tenant_id,metric_version_id,metric_id,dataset_version_id,field_id,
			dimension_name,hierarchy_field_ids,sort_direction,null_label,ordinal_position
		) SELECT $1,$2,$3,$4,field.field_id,$5,$6::text[],$7,$8,$9
		FROM platform.dataset_fields AS field
		WHERE field.dataset_version_id=$4::uuid AND field.field_id=$10`,
			tenantID, versionID, metricID, definition.DatasetVersionID, dimension.Name,
			dimension.HierarchyFieldIDs, dimension.SortDirection, dimension.NullLabel, index+1, dimension.FieldID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrInvalidDefinition
		}
	}
	for _, dependencyVersionID := range prepared.DependencyVersionIDs {
		var dependencyMetricID string
		if err := tx.QueryRow(ctx, `SELECT metric_id::text FROM platform.metric_versions
			WHERE id::text=$1 AND status='PUBLISHED' AND dataset_version_id::text=$2`,
			dependencyVersionID, definition.DatasetVersionID).Scan(&dependencyMetricID); errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionUnavailable
		} else if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metric_dependencies(
			tenant_id,metric_version_id,metric_id,dataset_version_id,
			dependency_metric_version_id,dependency_metric_id
		) VALUES($1,$2,$3,$4,$5,$6)`, tenantID, versionID, metricID,
			definition.DatasetVersionID, dependencyVersionID, dependencyMetricID); err != nil {
			return err
		}
	}
	return nil
}

func clearDerivedTx(ctx context.Context, tx pgx.Tx, versionID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM platform.metric_dimensions WHERE metric_version_id=$1`, versionID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.metric_dependencies WHERE metric_version_id=$1`, versionID); err != nil {
		return err
	}
	return nil
}

func definitionFieldIDs(definition Definition) []string {
	seen := map[string]bool{}
	result := []string{}
	add := func(value string) {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	var visit func(Expression)
	visit = func(expression Expression) {
		if expression.Type == "FIELD_REF" {
			add(expression.FieldID)
		}
		for _, argument := range expression.Arguments {
			visit(argument)
		}
	}
	visit(definition.Expression)
	add(definition.TimeFieldID)
	for _, dimension := range definition.AllowedDimensions {
		add(dimension.FieldID)
		for _, hierarchyFieldID := range dimension.HierarchyFieldIDs {
			add(hierarchyFieldID)
		}
	}
	for _, fieldID := range definition.NonAdditiveDimensionFieldIDs {
		add(fieldID)
	}
	return result
}

func mapReferenceError(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidDefinition) ||
		errors.Is(err, ErrVersionUnavailable) {
		return err
	}
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) && (pgError.Code == "23503" || pgError.Code == "23514") {
		return ErrInvalidDefinition
	}
	return err
}

func mapPublicationPostgresError(err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrForbidden) ||
		errors.Is(err, ErrInvalidDefinition) || errors.Is(err, ErrVersionUnavailable) ||
		errors.Is(err, ErrVersionInUse) || errors.Is(err, ErrIdempotencyConflict) {
		return err
	}
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) {
		return err
	}
	if pgError.Code == "23505" {
		if pgError.ConstraintName == "metric_publish_idempotency_key" ||
			pgError.ConstraintName == "metric_publish_idempotency_pkey" {
			return ErrIdempotencyConflict
		}
		return ErrConflict
	}
	if pgError.Code == "23503" || pgError.Code == "23514" {
		return ErrInvalidDefinition
	}
	return fmt.Errorf("persist metric publication: %w", err)
}
