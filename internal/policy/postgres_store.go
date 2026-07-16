package policy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// ValidateDefinitions 校验目标对象全部启用策略的结构和字段引用，不依赖某一个发布操作者。
func (s *PostgresStore) ValidateDefinitions(ctx context.Context, tenantID, objectType, objectID string, fieldCodes []string) error {
	fields := make(map[string]bool, len(fieldCodes))
	for _, code := range fieldCodes {
		fields[code] = true
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,expression_dsl FROM platform.data_row_policies
			WHERE object_type=$1 AND object_id::text=$2 AND enabled ORDER BY id`, objectType, objectID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var raw []byte
			if err := rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			var expression Expression
			if err := json.Unmarshal(raw, &expression); err != nil {
				rows.Close()
				return fmt.Errorf("row policy %s: invalid expression", id)
			}
			if err := validatePolicyExpression(expression, fields, 0); err != nil {
				rows.Close()
				return fmt.Errorf("row policy %s: %w", id, err)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		columns, err := tx.Query(ctx, `SELECT id::text,field_code,policy_type,mask_rule,allowed_aggregations,
			COALESCE(minimum_group_size,0),deny_detail_export FROM platform.data_column_policies
			WHERE object_type=$1 AND object_id::text=$2 AND enabled ORDER BY id`, objectType, objectID)
		if err != nil {
			return err
		}
		defer columns.Close()
		for columns.Next() {
			var id, fieldCode, policyType string
			var maskRaw []byte
			var aggregations []string
			var minimumGroupSize int
			var denyDetailExport bool
			if err := columns.Scan(&id, &fieldCode, &policyType, &maskRaw, &aggregations, &minimumGroupSize, &denyDetailExport); err != nil {
				return err
			}
			if !fields[fieldCode] {
				return fmt.Errorf("column policy %s: unknown dataset field", id)
			}
			policy := ColumnPolicy{FieldCode: fieldCode, PolicyType: policyType, AllowedAggregations: aggregations, MinimumGroupSize: minimumGroupSize, DenyDetailExport: denyDetailExport}
			if err := json.Unmarshal(maskRaw, &policy.MaskRule); err != nil {
				return fmt.Errorf("column policy %s: invalid mask rule", id)
			}
			if err := validateColumnPolicyDefinition(policy); err != nil {
				return fmt.Errorf("column policy %s: %w", id, err)
			}
		}
		return columns.Err()
	})
}

func validatePolicyExpression(expression Expression, fields map[string]bool, depth int) error {
	if depth > 64 {
		return errors.New("expression is too deep")
	}
	switch expression.Type {
	case "FIELD_REF":
		if !fields[expression.FieldCode] {
			return errors.New("unknown dataset field")
		}
	case "USER_ATTRIBUTE_REF":
		if !identifier.MatchString(expression.Attribute) {
			return errors.New("invalid user attribute")
		}
	case "LITERAL":
		return nil
	case "EQUALS", "NOT_EQUALS", "IN":
		if expression.Left == nil || expression.Right == nil {
			return errors.New("binary expression requires left and right")
		}
		if err := validatePolicyExpression(*expression.Left, fields, depth+1); err != nil {
			return err
		}
		return validatePolicyExpression(*expression.Right, fields, depth+1)
	case "AND", "OR":
		if len(expression.Children) < 2 {
			return errors.New("logical expression requires at least two children")
		}
		for _, child := range expression.Children {
			if err := validatePolicyExpression(child, fields, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported expression type")
	}
	return nil
}

func validateColumnPolicyDefinition(value ColumnPolicy) error {
	switch value.PolicyType {
	case "ALLOW", "DENY", "NULLIFY", "HASH":
		return nil
	case "MASK":
		_, err := CompileColumnPlan(value.FieldCode, &value, QueryContext{Detail: true})
		return err
	case "AGGREGATE_ONLY":
		if len(value.AllowedAggregations) == 0 {
			return errors.New("aggregate-only policy requires allowed aggregations")
		}
		for _, aggregation := range value.AllowedAggregations {
			aggregation = strings.ToUpper(strings.TrimSpace(aggregation))
			if _, err := CompileColumnPlan(value.FieldCode, &value, QueryContext{Aggregation: aggregation}); err != nil {
				return err
			}
		}
		return nil
	default:
		return errors.New("unsupported column policy")
	}
}
