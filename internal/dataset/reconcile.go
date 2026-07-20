package dataset

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

type mappedDatasetCandidate struct {
	tableID string
	actorID string
}

// ReconcileMappedDatasets 为升级前已经完成 LLM 映射的表补建并默认发布数据集。
// 租户目录本身不受 RLS 保护；实际候选读取和数据集创建均逐租户运行在
// WithTenantTx 中，每张表独立提交，避免单个异常回滚同租户此前的补建结果。
// EnsureMappedDatasetTx 负责并发幂等和最终的资格复核。
func (s *PostgresStore) ReconcileMappedDatasets(ctx context.Context) (int, error) {
	tenantIDs, err := s.listActiveTenantIDs(ctx)
	if err != nil {
		return 0, err
	}

	reconciled := 0
	for _, tenantID := range tenantIDs {
		var candidates []mappedDatasetCandidate
		if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			var err error
			candidates, err = listMappedDatasetCandidates(ctx, tx)
			return err
		}); err != nil {
			return reconciled, fmt.Errorf("list mapped datasets for tenant %s: %w", tenantID, err)
		}
		for _, candidate := range candidates {
			changed := false
			err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
				var err error
				changed, err = s.ensureMappedDatasetTx(ctx, tx, tenantID, candidate.actorID, candidate.tableID)
				if err != nil {
					return fmt.Errorf("ensure mapped dataset for table %s: %w", candidate.tableID, err)
				}
				return nil
			})
			if err != nil {
				return reconciled, fmt.Errorf("reconcile mapped datasets for tenant %s: %w", tenantID, err)
			}
			if changed {
				reconciled++
			}
		}
	}
	return reconciled, nil
}

func (s *PostgresStore) listActiveTenantIDs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text
		FROM platform.tenants
		WHERE status='ACTIVE' AND deleted_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list active tenants: %w", err)
	}
	defer rows.Close()

	tenantIDs := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("scan active tenant: %w", err)
		}
		tenantIDs = append(tenantIDs, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active tenants: %w", err)
	}
	return tenantIDs, nil
}

func listMappedDatasetCandidates(ctx context.Context, tx pgx.Tx) ([]mappedDatasetCandidate, error) {
	rows, err := tx.Query(ctx, `SELECT t.id::text,
		COALESCE(
			(SELECT dataset.created_by::text
			 FROM platform.datasets AS dataset
			 JOIN platform.users AS owner
			   ON owner.id=dataset.created_by AND owner.tenant_id=dataset.tenant_id
			  AND owner.status='ACTIVE' AND owner.deleted_at IS NULL
			 WHERE dataset.tenant_id=t.tenant_id AND dataset.origin_table_id=t.id
			 ORDER BY dataset.created_at,dataset.id
			 LIMIT 1),
			(SELECT j.created_by::text
			 FROM platform.ai_metadata_jobs j
			 WHERE j.tenant_id=t.tenant_id AND j.table_id=t.id
			   AND j.status='SUCCEEDED' AND j.created_by IS NOT NULL
			 ORDER BY j.completed_at DESC NULLS LAST,j.created_at DESC,j.id DESC
			 LIMIT 1),
			(SELECT u.id::text
			 FROM platform.users u
			 WHERE u.tenant_id=t.tenant_id AND u.status='ACTIVE' AND u.deleted_at IS NULL
			 ORDER BY u.created_at,u.id
			 LIMIT 1),
			''
		)
		FROM platform.metadata_tables t
		WHERE t.asset_status='ACTIVE'
		  AND t.management_status='ENABLED'
		  AND t.last_enriched_structure_hash<>''
		  AND t.last_enriched_structure_hash=t.structure_hash
		  AND EXISTS (
			SELECT 1 FROM platform.metadata_columns c
			WHERE c.tenant_id=t.tenant_id AND c.table_id=t.id AND c.asset_status='ACTIVE'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM platform.metadata_columns c
			WHERE c.tenant_id=t.tenant_id AND c.table_id=t.id AND c.asset_status='ACTIVE'
			  AND (c.last_enriched_structure_hash='' OR c.last_enriched_structure_hash<>c.structure_hash)
		  )
		  AND (
			NOT EXISTS (
				SELECT 1 FROM platform.datasets AS dataset
				WHERE dataset.tenant_id=t.tenant_id AND dataset.origin_table_id=t.id
			)
			OR EXISTS (
				SELECT 1
				FROM platform.datasets AS dataset
				JOIN platform.dataset_versions AS draft
				  ON draft.id=dataset.current_draft_version_id
				 AND draft.dataset_id=dataset.id AND draft.tenant_id=dataset.tenant_id
				WHERE dataset.tenant_id=t.tenant_id AND dataset.origin_table_id=t.id
				  AND dataset.deleted_at IS NULL AND dataset.status='DRAFT' AND dataset.version=1
				  AND draft.status='DRAFT' AND draft.version_no=1 AND draft.record_version=1
				  AND NOT EXISTS (
					SELECT 1 FROM platform.dataset_versions AS published
					WHERE published.dataset_id=dataset.id AND published.tenant_id=dataset.tenant_id
					  AND published.status IN ('PUBLISHED','STALE','DEPRECATED')
				  )
			)
		  )
		ORDER BY t.id`)
	if err != nil {
		return nil, fmt.Errorf("list mapped dataset candidates: %w", err)
	}

	// 必须在创建数据集之前关闭游标；同一事务内边读边写会让连接长期持有
	// 活跃结果集，也会使未来批量实现难以保证确定的执行顺序。
	candidates := []mappedDatasetCandidate{}
	for rows.Next() {
		var candidate mappedDatasetCandidate
		if err := rows.Scan(&candidate.tableID, &candidate.actorID); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan mapped dataset candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate mapped dataset candidates: %w", err)
	}
	rows.Close()
	return candidates, nil
}
