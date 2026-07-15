package policy

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

type PostgresStore struct{ pool *pgxpool.Pool }

// NewPostgresStore 创建数据安全策略存储。
func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Load 汇总用户属性以及目标对象生效的行、列级策略。
func (s *PostgresStore) Load(ctx context.Context, tenantID, userID, objectType, objectID string) (UserScope, []RowPolicy, []ColumnPolicy, error) {
	scope := UserScope{TenantID: tenantID, UserID: userID}
	var rowsOut []RowPolicy
	var columnsOut []ColumnPolicy
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT attributes FROM platform.users WHERE id=$1 AND status='ACTIVE' AND deleted_at IS NULL`, userID).Scan(&scope.Attributes); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `WITH subject_roles AS (SELECT role_id FROM platform.user_roles WHERE user_id=$1) SELECT id,effect,combine_mode,priority,version,expression_dsl FROM platform.data_row_policies WHERE object_type=$2 AND object_id=$3 AND enabled AND (cardinality(applicable_user_ids)=0 AND cardinality(applicable_role_ids)=0 OR $1=ANY(applicable_user_ids) OR applicable_role_ids && ARRAY(SELECT role_id FROM subject_roles)) ORDER BY priority,id`, userID, objectType, objectID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p RowPolicy
			var raw []byte
			if err := rows.Scan(&p.ID, &p.Effect, &p.CombineMode, &p.Priority, &p.Version, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &p.Expression); err != nil {
				return err
			}
			rowsOut = append(rowsOut, p)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		columns, err := tx.Query(ctx, `WITH subject_roles AS (SELECT role_id FROM platform.user_roles WHERE user_id=$1) SELECT field_code,policy_type,mask_rule,allowed_aggregations,COALESCE(minimum_group_size,0),deny_detail_export,version FROM platform.data_column_policies WHERE object_type=$2 AND object_id=$3 AND enabled AND (cardinality(applicable_user_ids)=0 AND cardinality(applicable_role_ids)=0 OR $1=ANY(applicable_user_ids) OR applicable_role_ids && ARRAY(SELECT role_id FROM subject_roles)) ORDER BY field_code,version DESC`, userID, objectType, objectID)
		if err != nil {
			return err
		}
		defer columns.Close()
		seen := map[string]bool{}
		for columns.Next() {
			var p ColumnPolicy
			var raw []byte
			if err := columns.Scan(&p.FieldCode, &p.PolicyType, &raw, &p.AllowedAggregations, &p.MinimumGroupSize, &p.DenyDetailExport, &p.Version); err != nil {
				return err
			}
			if seen[p.FieldCode] {
				continue
			}
			seen[p.FieldCode] = true
			if err := json.Unmarshal(raw, &p.MaskRule); err != nil {
				return err
			}
			columnsOut = append(columnsOut, p)
		}
		return columns.Err()
	})
	return scope, rowsOut, columnsOut, err
}
