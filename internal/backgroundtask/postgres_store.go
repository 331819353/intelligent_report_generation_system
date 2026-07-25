package backgroundtask

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/platform/database"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

const taskUnionSQL = `
WITH tasks AS (
  SELECT
    'DATA_SOURCE_METADATA'::text AS kind,job.id::text AS id,
    source.name::text AS name,
    concat(source.code::text,' · ',job.kind,' · ',job.refresh_mode,' · ',job.stage)::text AS description,
    job.status::text AS source_status,'DATA_SOURCE'::text AS resource_type,
    job.data_source_id::text AS resource_id,
    progress.processed::bigint AS processed,job.total::bigint AS total,
    job.attempt::integer AS attempt,3::integer AS max_attempts,
    job.error_code::text AS error_code,job.error_message::text AS error_message,
    job.created_at,job.started_at,
    COALESCE(job.completed_at,job.heartbeat_at,job.started_at,job.created_at) AS updated_at,
    job.completed_at
  FROM platform.data_source_metadata_jobs AS job
  JOIN platform.data_sources AS source
    ON source.id=job.data_source_id AND source.tenant_id=job.tenant_id
  LEFT JOIN LATERAL (
    SELECT count(*) FILTER(
      WHERE item.status IN ('SUCCEEDED','SKIPPED','FAILED')
    )::bigint AS processed
    FROM platform.data_source_metadata_job_items AS item
    WHERE item.job_id=job.id AND item.tenant_id=job.tenant_id
  ) AS progress ON true

  UNION ALL
  SELECT
    'DATA_SOURCE_CONNECTION_TEST',job.id::text,source.name::text,
    concat(source.code::text,' · 连接测试')::text,job.status::text,
    'DATA_SOURCE',job.data_source_id::text,NULL::bigint,NULL::bigint,
    job.attempt,job.max_attempts,job.error_code::text,job.error_message::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.data_source_connection_test_jobs AS job
  JOIN platform.data_sources AS source
    ON source.id=job.data_source_id AND source.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'DATASET_BUILD',run.id::text,dataset.name::text,
    concat(dataset.code::text,' · ',run.layer,' · ',run.run_mode)::text,
    run.status::text,'DATASET',run.dataset_id::text,
    progress.processed::bigint,progress.total::bigint,
    run.attempt,run.max_attempts,run.error_code::text,run.error_message::text,
    run.created_at,run.started_at,run.updated_at,run.completed_at
  FROM platform.dataset_build_runs AS run
  JOIN platform.datasets AS dataset
    ON dataset.id=run.dataset_id AND dataset.tenant_id=run.tenant_id
  LEFT JOIN LATERAL (
    SELECT
      count(*) FILTER(
        WHERE node.status IN ('SUCCEEDED','FAILED','SKIPPED')
      )::bigint AS processed,
      count(*)::bigint AS total
    FROM platform.build_node_runs AS node
    WHERE node.build_run_id=run.id AND node.tenant_id=run.tenant_id
  ) AS progress ON true

  UNION ALL
  SELECT
    'DATASET_TAG_SUGGESTION',job.id::text,dataset.name::text,
    concat(dataset.code::text,' · ',job.layer,' · LLM 标签建议')::text,
    job.status::text,'DATASET',job.dataset_id::text,
    CASE WHEN job.status='SUCCEEDED' THEN job.suggestion_count::bigint ELSE NULL::bigint END,
    CASE WHEN job.status='SUCCEEDED' THEN job.suggestion_count::bigint ELSE NULL::bigint END,
    job.attempt,job.max_attempts,job.error_code::text,job.error_message::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.dataset_tag_suggestion_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.dataset_id AND dataset.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'METRIC_EXTRACTION',job.id::text,dataset.name::text,
    concat(dataset.code::text,' · 指标候选提取')::text,
    job.status::text,'DATASET',job.dataset_id::text,
    (job.ready_count+job.review_count+job.blocked_count)::bigint,job.total::bigint,
    job.attempt,3,job.error_code::text,job.error_message::text,
    job.created_at,job.started_at,
    COALESCE(job.completed_at,job.heartbeat_at,job.started_at,job.created_at),
    job.completed_at
  FROM platform.metric_extraction_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.dataset_id AND dataset.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'METRIC_CANDIDATE_PREPARATION',job.id::text,dataset.name::text,
    concat(dataset.code::text,' · 发布前指标候选准备')::text,
    job.status::text,'DATASET',job.dataset_id::text,NULL::bigint,NULL::bigint,
    job.attempt,3,job.error_code::text,job.error_message::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.metric_candidate_preparation_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.dataset_id AND dataset.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'DIMENSION_MEMBER_REFRESH',job.id::text,dimension.name::text,
    concat(dimension.code::text,' · ',job.field_code,' · 维度值刷新')::text,
    job.status::text,'DATASET',job.dataset_id::text,
    CASE WHEN job.member_count IS NULL THEN NULL::bigint ELSE job.member_count::bigint END,
    CASE WHEN job.member_count IS NULL THEN NULL::bigint ELSE job.member_count::bigint END,
    job.attempt,job.max_attempts,job.result_code::text,job.error_message::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.dimension_member_refresh_jobs AS job
  JOIN platform.semantic_dimensions AS dimension
    ON dimension.id=job.dimension_id AND dimension.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'DIMENSION_PROFILE',job.id::text,dataset.name::text,
    concat(dataset.code::text,' · ',job.field_code,' · DWS 维度画像')::text,
    job.status::text,'DATASET',job.dataset_id::text,NULL::bigint,NULL::bigint,
    job.attempt,job.max_attempts,job.result_code::text,''::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.dimension_profile_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.dataset_id AND dataset.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'DWD_MODELING',job.id::text,dataset.name::text,
    concat(
      dataset.code::text,' · ',
      COALESCE(NULLIF(job.domain_key,''),'待识别领域'),' · LLM DWD 建模'
    )::text,
    job.status::text,'DATASET',job.trigger_dataset_id::text,
    (job.generated_count+job.updated_count+job.skipped_count)::bigint,NULL::bigint,
    job.attempt,job.max_attempts,job.error_code::text,job.error_message::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.dwd_modeling_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.trigger_dataset_id AND dataset.tenant_id=job.tenant_id

  UNION ALL
  SELECT
    'DATASET_MATERIALIZATION_CLEANUP',job.id::text,dataset.name::text,
    concat(dataset.code::text,' · ',job.layer,' · 仓库物理表清理')::text,
    job.status::text,'DATASET',job.dataset_id::text,
    job.deleted_count::bigint,job.expected_count::bigint,
    job.attempt,job.max_attempts,job.error_code::text,job.error_message::text,
    job.created_at,job.started_at,job.updated_at,job.completed_at
  FROM platform.dataset_materialization_cleanup_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.dataset_id AND dataset.tenant_id=job.tenant_id
)
`

const taskSelectColumns = `kind,id,name,description,source_status,resource_type,
  resource_id,processed,total,attempt,max_attempts,error_code,error_message,
  created_at,started_at,updated_at,completed_at`

func (store *PostgresStore) List(
	ctx context.Context,
	tenantID, view string,
	limit int,
) (page Page, err error) {
	if store == nil || store.pool == nil {
		return Page{}, ErrInvalidRequest
	}
	page.Items = make([]Task, 0)
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		filter := `true`
		switch view {
		case ViewActive:
			filter = `source_status IN ('PENDING','QUEUED','RUNNING')`
		case ViewRecent:
			filter = `source_status NOT IN ('PENDING','QUEUED','RUNNING')`
		case ViewAll:
		default:
			return ErrInvalidRequest
		}
		rows, queryErr := tx.Query(ctx, taskUnionSQL+`
			SELECT `+taskSelectColumns+` FROM tasks
			WHERE `+filter+`
			ORDER BY
			  CASE WHEN source_status='RUNNING' THEN 0
			       WHEN source_status IN ('PENDING','QUEUED') THEN 1 ELSE 2 END,
			  updated_at DESC,id DESC
			LIMIT $1`, limit)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			task, scanErr := scanTask(rows)
			if scanErr != nil {
				return scanErr
			}
			page.Items = append(page.Items, task)
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		return tx.QueryRow(ctx, taskUnionSQL+`
			SELECT count(*) FROM tasks
			WHERE source_status IN ('PENDING','QUEUED','RUNNING')`,
		).Scan(&page.ActiveCount)
	})
	page.GeneratedAt = time.Now().UTC()
	return page, err
}

func (store *PostgresStore) Find(
	ctx context.Context,
	tenantID, kind, taskID string,
) (task Task, err error) {
	if store == nil || store.pool == nil {
		return Task{}, ErrInvalidRequest
	}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var scanErr error
		task, scanErr = scanTask(tx.QueryRow(ctx, taskUnionSQL+`
			SELECT `+taskSelectColumns+` FROM tasks
			WHERE kind=$1 AND id=$2`, kind, taskID))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return scanErr
	})
	return task, err
}

func (store *PostgresStore) Cancel(
	ctx context.Context,
	tenantID, actorID, kind, taskID string,
) error {
	if store == nil || store.pool == nil {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var cancelled bool
		if err := tx.QueryRow(ctx, `
			SELECT platform.cancel_background_task($1,$2::uuid,$3::uuid)
		`, kind, taskID, actorID).Scan(&cancelled); err != nil {
			return err
		}
		if !cancelled {
			return ErrNotActive
		}
		return nil
	})
}

type rowScanner interface{ Scan(...any) error }

func scanTask(row rowScanner) (Task, error) {
	var (
		task                   Task
		processed, total       pgtype.Int8
		startedAt, completedAt pgtype.Timestamptz
	)
	err := row.Scan(
		&task.Kind, &task.ID, &task.Name, &task.Description, &task.SourceStatus,
		&task.ResourceType, &task.ResourceID, &processed, &total,
		&task.Attempt, &task.MaxAttempts, &task.ErrorCode, &task.ErrorMessage,
		&task.CreatedAt, &startedAt, &task.UpdatedAt, &completedAt,
	)
	if err != nil {
		return Task{}, err
	}
	if processed.Valid {
		value := processed.Int64
		task.Processed = &value
	}
	if total.Valid {
		value := total.Int64
		task.Total = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		task.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		task.CompletedAt = &value
	}
	decorate(&task)
	return task, nil
}

func decorate(task *Task) {
	task.KindLabel = kindLabels[task.Kind]
	if task.KindLabel == "" {
		task.KindLabel = task.Kind
	}
	task.Status = normalizeStatus(task.SourceStatus, task.ErrorCode)
	active := task.Status == "QUEUED" || task.Status == "RUNNING"
	task.CanCancel = active && cancellableKinds[task.Kind]
	if !active {
		task.CancelDisabledReason = "任务已经结束"
	} else if !task.CanCancel {
		task.CancelDisabledReason = "该任务当前不支持安全中止"
	}
	if task.Total != nil && *task.Total > 0 && task.Processed != nil {
		percent := int((*task.Processed * 100) / *task.Total)
		if percent > 100 {
			percent = 100
		}
		task.ProgressPercent = &percent
		task.ProgressText = fmt.Sprintf("已处理 %d / %d", *task.Processed, *task.Total)
	} else if task.Status == "QUEUED" {
		percent := 0
		task.ProgressPercent = &percent
		task.ProgressText = "等待 worker 领取"
	} else if active {
		task.ProgressText = "正在执行，暂时无法估算剩余时间"
	} else {
		percent := 100
		task.ProgressPercent = &percent
		task.ProgressText = statusLabels[task.Status]
	}
	if task.ProgressText == "" {
		task.ProgressText = task.Status
	}
	task.Description = strings.TrimSpace(task.Description)
}

func normalizeStatus(sourceStatus, errorCode string) string {
	if errorCode == "USER_CANCELLED" {
		return "CANCELLED"
	}
	switch sourceStatus {
	case "PENDING", "QUEUED":
		return "QUEUED"
	case "RUNNING":
		return "RUNNING"
	case "SUCCEEDED":
		return "SUCCEEDED"
	case "PARTIAL":
		return "PARTIAL"
	case "FAILED":
		return "FAILED"
	case "CANCELLED":
		return "CANCELLED"
	case "SKIPPED", "SKIPPED_POLICY":
		return "SKIPPED"
	case "STALE":
		return "STALE"
	default:
		return sourceStatus
	}
}

var kindLabels = map[string]string{
	"DATA_SOURCE_METADATA":            "数据表元数据完善",
	"DATA_SOURCE_CONNECTION_TEST":     "数据源连接测试",
	"DATASET_BUILD":                   "数据集物化构建",
	"DATASET_TAG_SUGGESTION":          "数据集标签建议",
	"METRIC_EXTRACTION":               "指标候选提取",
	"METRIC_CANDIDATE_PREPARATION":    "历史发布前指标准备",
	"DIMENSION_MEMBER_REFRESH":        "维度值刷新",
	"DIMENSION_PROFILE":               "DWS 维度画像",
	"DWD_MODELING":                    "LLM DWD 建模",
	"DATASET_MATERIALIZATION_CLEANUP": "DWD/DWS 仓库物理表清理",
}

var cancellableKinds = map[string]bool{
	"DATA_SOURCE_METADATA":         true,
	"DATA_SOURCE_CONNECTION_TEST":  true,
	"DATASET_BUILD":                true,
	"DATASET_TAG_SUGGESTION":       true,
	"METRIC_EXTRACTION":            true,
	"METRIC_CANDIDATE_PREPARATION": true,
	"DIMENSION_MEMBER_REFRESH":     true,
	"DIMENSION_PROFILE":            true,
	"DWD_MODELING":                 true,
}

var statusLabels = map[string]string{
	"SUCCEEDED": "执行成功",
	"PARTIAL":   "部分完成",
	"FAILED":    "执行失败",
	"CANCELLED": "已中止",
	"SKIPPED":   "已跳过",
	"STALE":     "已失效",
}
