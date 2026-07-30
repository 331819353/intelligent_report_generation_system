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
    CASE stage_job.stage
      WHEN 'DOMAIN_CLASSIFICATION' THEN 'ODS_DOMAIN_CLASSIFICATION'
      WHEN 'DIMENSION_MODELING' THEN 'DIM_MODELING'
      ELSE 'DWD_FACT_MODELING'
    END,
    stage_job.id::text,
    concat(
      COALESCE(
        NULLIF(btrim(workflow.domain_key),''),
        dataset.name
      ),
      CASE stage_job.stage
        WHEN 'DOMAIN_CLASSIFICATION' THEN '领域结构分析'
        WHEN 'DIMENSION_MODELING' THEN '领域维度设计'
        ELSE '领域事实落地'
      END
    )::text,
    concat(
      '触发源：',dataset.name,' · ',dataset.code::text,
      CASE
        WHEN jsonb_typeof(
          COALESCE(
            stage_job.result_json#>'{classificationSummary,factTableCount}',
            classification.result_json#>'{classificationSummary,factTableCount}'
          )
        )='number'
         AND jsonb_typeof(
          COALESCE(
            stage_job.result_json#>'{classificationSummary,dimensionTableCount}',
            classification.result_json#>'{classificationSummary,dimensionTableCount}'
          )
        )='number'
        THEN concat(
          ' · 规划事实',
          COALESCE(
            stage_job.result_json#>>'{classificationSummary,factTableCount}',
            classification.result_json#>>'{classificationSummary,factTableCount}'
          ),
          '张 / 维度',
          COALESCE(
            stage_job.result_json#>>'{classificationSummary,dimensionTableCount}',
            classification.result_json#>>'{classificationSummary,dimensionTableCount}'
          ),
          '张'
        )
        ELSE ''
      END
    )::text,
    stage_job.status::text,'DATASET',workflow.trigger_dataset_id::text,
    (stage_job.generated_count+stage_job.updated_count+
      stage_job.skipped_count)::bigint,NULL::bigint,
    stage_job.attempt,stage_job.max_attempts,
    stage_job.error_code::text,stage_job.error_message::text,
    stage_job.requested_at,stage_job.started_at,
    stage_job.updated_at,stage_job.completed_at
  FROM platform.dwd_modeling_stage_jobs AS stage_job
  JOIN platform.dwd_modeling_jobs AS workflow
    ON workflow.id=stage_job.workflow_job_id
   AND workflow.tenant_id=stage_job.tenant_id
  LEFT JOIN platform.dwd_modeling_stage_jobs AS classification
    ON classification.workflow_job_id=stage_job.workflow_job_id
   AND classification.tenant_id=stage_job.tenant_id
   AND classification.stage='DOMAIN_CLASSIFICATION'
  JOIN platform.datasets AS dataset
    ON dataset.id=workflow.trigger_dataset_id
   AND dataset.tenant_id=workflow.tenant_id
  WHERE stage_job.manual_enabled

  UNION ALL
  SELECT
    'DWS_MODELING',job.id::text,
    concat(COALESCE(NULLIF(job.source_scope->>'subjectName',''),'综合分析'),'主题建模')::text,
    concat(
      jsonb_array_length(job.source_scope->'dwd'),' 张 DWD · ',
      jsonb_array_length(job.source_scope->'dim'),' 张 DIM 规划上下文 · ',
      job.group_key
    )::text,
    job.status::text,'DATASET',job.source_dwd_dataset_id::text,
    (job.generated_count+job.updated_count+job.skipped_count)::bigint,
    CASE WHEN jsonb_array_length(job.selection_json)=0
      THEN NULL::bigint
      ELSE jsonb_array_length(job.selection_json)::bigint END,
    job.attempt,job.max_attempts,job.error_code::text,job.error_message::text,
    job.requested_at,
    CASE WHEN job.attempt>0 THEN job.requested_at ELSE NULL::timestamptz END,
    job.updated_at,job.completed_at
  FROM platform.dws_modeling_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.source_dwd_dataset_id
   AND dataset.tenant_id=job.tenant_id
  WHERE job.group_key NOT LIKE 'legacy:%'

  UNION ALL
  SELECT
    'ADS_MODELING',job.id::text,
    concat(dataset.name,'应用建模')::text,
    concat(dataset.code::text,' · DWS → ADS 应用数据草稿')::text,
    job.status::text,'DATASET',job.source_dws_dataset_id::text,
    (job.generated_count+job.updated_count+job.skipped_count)::bigint,
    1::bigint,
    job.attempt,job.max_attempts,job.error_code::text,job.error_message::text,
    job.requested_at,
    CASE WHEN job.attempt>0 THEN job.requested_at ELSE NULL::timestamptz END,
    job.updated_at,job.completed_at
  FROM platform.ads_modeling_jobs AS job
  JOIN platform.datasets AS dataset
    ON dataset.id=job.source_dws_dataset_id
   AND dataset.tenant_id=job.tenant_id

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

const taskVisibilityFilter = `created_at > COALESCE((
  SELECT NULLIF(tenant.settings->>'backgroundTaskCenterClearedAt','')::timestamptz
  FROM platform.tenants AS tenant
  WHERE tenant.id=platform.current_tenant_id()
),'-infinity'::timestamptz)`

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
			filter = `source_status IN (
				'PENDING','QUEUED','WAITING_DEPENDENCY','RUNNING'
			)`
		case ViewRecent:
			filter = `source_status NOT IN (
				'PENDING','QUEUED','WAITING_DEPENDENCY','RUNNING'
			)`
		case ViewAll:
		default:
			return ErrInvalidRequest
		}
		rows, queryErr := tx.Query(ctx, taskUnionSQL+`
			SELECT `+taskSelectColumns+` FROM tasks
			WHERE (`+filter+`) AND `+taskVisibilityFilter+`
			ORDER BY
			  CASE WHEN source_status='RUNNING' THEN 0
			       WHEN source_status IN (
			         'PENDING','QUEUED','WAITING_DEPENDENCY'
			       ) THEN 1 ELSE 2 END,
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
			WHERE source_status IN (
			  'PENDING','QUEUED','WAITING_DEPENDENCY','RUNNING'
			) AND `+taskVisibilityFilter,
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
			WHERE kind=$1 AND id=$2 AND `+taskVisibilityFilter, kind, taskID))
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
		query := `SELECT platform.cancel_background_task($1,$2::uuid,$3::uuid)`
		args := []any{kind, taskID, actorID}
		if warehouseStageKinds[kind] {
			query = `SELECT platform.cancel_dwd_modeling_stage_task(
				$1::uuid,$2::uuid
			)`
			args = []any{taskID, actorID}
		}
		if err := tx.QueryRow(ctx, query, args...).Scan(&cancelled); err != nil {
			return err
		}
		if !cancelled {
			return ErrNotActive
		}
		return nil
	})
}

func (store *PostgresStore) Retry(
	ctx context.Context,
	tenantID, actorID, kind, taskID string,
) error {
	if store == nil || store.pool == nil {
		return ErrInvalidRequest
	}
	return database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		var retried bool
		query := `SELECT platform.retry_background_task($1,$2::uuid,$3::uuid)`
		args := []any{kind, taskID, actorID}
		if warehouseStageKinds[kind] {
			query = `SELECT platform.retry_dwd_modeling_stage_task(
				$1::uuid,$2::uuid
			)`
			args = []any{taskID, actorID}
		}
		if err := tx.QueryRow(ctx, query, args...).Scan(&retried); err != nil {
			return err
		}
		if !retried {
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
	coalesced := task.ErrorCode == "DOMAIN_PLAN_COALESCED"
	task.Status = normalizeStatus(task.SourceStatus, task.ErrorCode)
	active := task.Status == "QUEUED" || task.Status == "RUNNING"
	task.CanCancel = active && cancellableKinds[task.Kind]
	if !active {
		task.CancelDisabledReason = "任务已经结束"
	} else if !task.CanCancel {
		task.CancelDisabledReason = "该任务当前不支持安全中止"
	}
	retryableTerminal := task.Status == "FAILED" || task.Status == "PARTIAL" ||
		task.Status == "CANCELLED" || task.Status == "SKIPPED"
	retryableBuild := task.Kind != "DATASET_BUILD" ||
		task.ErrorCode == "TRUSTED_PLAN_INVALID"
	task.CanRetry = retryableTerminal && retryableKinds[task.Kind] &&
		retryableBuild && !coalesced
	switch {
	case coalesced:
		task.RetryDisabledReason = "同领域方案已由代表任务统一完成，无需重试"
	case active:
		task.RetryDisabledReason = "任务仍在运行"
	case !retryableTerminal:
		task.RetryDisabledReason = "任务已成功结束，无需重试"
	case !retryableBuild:
		task.RetryDisabledReason = "该构建失败无法在原运行上安全重试"
	case !retryableKinds[task.Kind]:
		task.RetryDisabledReason = "该任务当前不支持安全重试"
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
	if coalesced {
		task.ErrorCode = ""
		task.ErrorMessage = ""
		task.ProgressText = "已合并到同领域代表任务"
	}
	if task.ErrorCode == "WAITING_DIM_PUBLICATION" ||
		task.ErrorCode == "WAITING_ACTIVE_DWD_MATERIALIZATION" ||
		task.ErrorCode == "WAITING_ACTIVE_DWS_MATERIALIZATION" {
		task.ProgressText = task.ErrorMessage
		task.ErrorCode = ""
		task.ErrorMessage = ""
	}
	task.Description = strings.TrimSpace(task.Description)
}

func normalizeStatus(sourceStatus, errorCode string) string {
	if errorCode == "USER_CANCELLED" {
		return "CANCELLED"
	}
	switch sourceStatus {
	case "PENDING", "QUEUED", "WAITING_DEPENDENCY":
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
	"ODS_DOMAIN_CLASSIFICATION":       "领域分类",
	"DIM_MODELING":                    "维度建模",
	"DWD_FACT_MODELING":               "事实落地",
	"DWS_MODELING":                    "LLM 市场通用 DWS 建模",
	"ADS_MODELING":                    "ADS 应用数据建模",
	"DATASET_MATERIALIZATION_CLEANUP": "DIM/DWD/DWS/ADS 仓库物理表清理",
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
	"ODS_DOMAIN_CLASSIFICATION":    true,
	"DIM_MODELING":                 true,
	"DWD_FACT_MODELING":            true,
}

var retryableKinds = map[string]bool{
	"ODS_DOMAIN_CLASSIFICATION": true,
	"DIM_MODELING":              true,
	"DWD_FACT_MODELING":         true,
	"DWS_MODELING":              true,
	"ADS_MODELING":              false,
	"DATASET_BUILD":             true,
}

var warehouseStageKinds = map[string]bool{
	"ODS_DOMAIN_CLASSIFICATION": true,
	"DIM_MODELING":              true,
	"DWD_FACT_MODELING":         true,
}

var statusLabels = map[string]string{
	"SUCCEEDED": "执行成功",
	"PARTIAL":   "部分完成",
	"FAILED":    "执行失败",
	"CANCELLED": "已中止",
	"SKIPPED":   "已跳过",
	"STALE":     "已失效",
}
