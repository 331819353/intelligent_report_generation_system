package semanticasset

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

const parsingRuleColumns = `id::text,rule_type,pattern,match_mode,action,
	output_name,output_code,minimum_length,maximum_length,priority,
	CASE WHEN tenant_id IS NULL THEN 'PLATFORM' ELSE 'TENANT' END,
	status,version,COALESCE(created_by::text,''),COALESCE(updated_by::text,''),
	created_at,updated_at`

func (store *PostgresStore) ListParsingRules(
	ctx context.Context,
	tenantID string,
	filter ParsingRuleFilter,
) (items []ParsingRule, total int, err error) {
	items = []ParsingRule{}
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		rows, queryErr := tx.Query(ctx, `SELECT `+parsingRuleColumns+`,
				count(*) OVER()::int
			FROM platform.semantic_parsing_rules
			WHERE (tenant_id IS NULL OR tenant_id=platform.current_tenant_id())
			  AND (
			    $1='' OR pattern ILIKE '%'||$1||'%'
			    OR output_name ILIKE '%'||$1||'%'
			    OR output_code ILIKE '%'||$1||'%'
			  )
			  AND ($2='' OR rule_type=$2)
			  AND ($3='' OR status=$3)
			ORDER BY rule_type,priority DESC,char_length(pattern) DESC,
				lower(pattern),tenant_id NULLS FIRST,id
			LIMIT $4 OFFSET $5`,
			filter.Query, filter.RuleType, filter.Status,
			filter.Limit, filter.Offset,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var item ParsingRule
			if scanErr := scanParsingRule(rows, &item, &total); scanErr != nil {
				return scanErr
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (store *PostgresStore) CreateParsingRule(
	ctx context.Context,
	tenantID, actorID string,
	input ParsingRuleInput,
) (item ParsingRule, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `INSERT INTO platform.semantic_parsing_rules(
				tenant_id,rule_type,pattern,match_mode,action,
				output_name,output_code,minimum_length,maximum_length,priority,
				created_by,updated_by
			) VALUES(
				platform.current_tenant_id(),$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10
			)
			ON CONFLICT(tenant_id,rule_type,(lower(pattern)))
			  WHERE tenant_id IS NOT NULL
			DO UPDATE SET
				pattern=EXCLUDED.pattern,match_mode=EXCLUDED.match_mode,
				action=EXCLUDED.action,output_name=EXCLUDED.output_name,
				output_code=EXCLUDED.output_code,
				minimum_length=EXCLUDED.minimum_length,
				maximum_length=EXCLUDED.maximum_length,
				priority=EXCLUDED.priority,status='ACTIVE',
				version=platform.semantic_parsing_rules.version+1,
				updated_by=EXCLUDED.updated_by
			RETURNING `+parsingRuleColumns,
			input.RuleType, input.Pattern, input.MatchMode, input.Action,
			input.OutputName, input.OutputCode, input.MinimumLength,
			input.MaximumLength, input.Priority, actorID,
		)
		if scanErr := scanParsingRule(row, &item); scanErr != nil {
			return mapWriteError(scanErr)
		}
		return audit(ctx, tx, actorID, "SEMANTIC_PARSING_RULE_CREATE", item.ID,
			map[string]any{"ruleType": item.RuleType, "pattern": item.Pattern,
				"version": item.Version})
	})
	return item, err
}

func (store *PostgresStore) UpdateParsingRule(
	ctx context.Context,
	tenantID, actorID, id string,
	input ParsingRuleUpdateInput,
) (item ParsingRule, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_parsing_rules SET
				rule_type=$1,pattern=$2,match_mode=$3,action=$4,
				output_name=$5,output_code=$6,minimum_length=$7,
				maximum_length=$8,priority=$9,version=version+1,updated_by=$10
			WHERE id=$11::uuid AND tenant_id=platform.current_tenant_id()
			  AND version=$12 AND status='ACTIVE'
			RETURNING `+parsingRuleColumns,
			input.RuleType, input.Pattern, input.MatchMode, input.Action,
			input.OutputName, input.OutputCode, input.MinimumLength,
			input.MaximumLength, input.Priority, actorID, id,
			input.ExpectedVersion,
		)
		if scanErr := scanParsingRule(row, &item); scanErr != nil {
			if !errors.Is(scanErr, pgx.ErrNoRows) {
				return mapWriteError(scanErr)
			}
			return classifyParsingRuleMissingOrConflict(ctx, tx, id)
		}
		return audit(ctx, tx, actorID, "SEMANTIC_PARSING_RULE_UPDATE", item.ID,
			map[string]any{"previousVersion": input.ExpectedVersion,
				"version": item.Version})
	})
	return item, err
}

func (store *PostgresStore) DeprecateParsingRule(
	ctx context.Context,
	tenantID, actorID, id string,
	expectedVersion int64,
) (item ParsingRule, err error) {
	err = database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `UPDATE platform.semantic_parsing_rules SET
				status='DEPRECATED',version=version+1,updated_by=$1
			WHERE id=$2::uuid AND tenant_id=platform.current_tenant_id()
			  AND version=$3 AND status='ACTIVE'
			RETURNING `+parsingRuleColumns,
			actorID, id, expectedVersion,
		)
		if scanErr := scanParsingRule(row, &item); scanErr != nil {
			if !errors.Is(scanErr, pgx.ErrNoRows) {
				return mapWriteError(scanErr)
			}
			return classifyParsingRuleMissingOrConflict(ctx, tx, id)
		}
		return audit(ctx, tx, actorID, "SEMANTIC_PARSING_RULE_DEPRECATE", item.ID,
			map[string]any{"previousVersion": expectedVersion,
				"version": item.Version})
	})
	return item, err
}

func scanParsingRule(
	row pgx.Row,
	item *ParsingRule,
	total ...*int,
) error {
	destinations := []any{
		&item.ID, &item.RuleType, &item.Pattern, &item.MatchMode,
		&item.Action, &item.OutputName, &item.OutputCode,
		&item.MinimumLength, &item.MaximumLength, &item.Priority,
		&item.Scope, &item.Status, &item.Version, &item.CreatedBy,
		&item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt,
	}
	if len(total) > 0 {
		destinations = append(destinations, total[0])
	}
	return row.Scan(destinations...)
}

func classifyParsingRuleMissingOrConflict(
	ctx context.Context,
	tx pgx.Tx,
	id string,
) error {
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM platform.semantic_parsing_rules
		WHERE id=$1::uuid AND tenant_id=platform.current_tenant_id()
	)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrConflict
	}
	return ErrNotFound
}
