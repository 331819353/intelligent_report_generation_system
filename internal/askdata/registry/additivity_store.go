package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

type AdditivityCandidate struct {
	MetricVersionID     string               `json:"metricVersionId"`
	MetricID            string               `json:"metricId"`
	MetricCode          string               `json:"metricCode"`
	MetricName          string               `json:"metricName"`
	ModelVersionID      string               `json:"modelVersionId"`
	Suggestion          AdditivitySuggestion `json:"suggestion"`
	PersistedSuggestion Additivity           `json:"persistedSuggestion,omitempty"`
	PersistedRuleID     string               `json:"persistedRuleId,omitempty"`
	UpdatedAt           time.Time            `json:"updatedAt"`
	metric              MetricVersion
	model               SemanticModel
	defaultAggregation  Aggregation
}

type AdditivityCandidatePage struct {
	Items      []AdditivityCandidate `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type AdditivityReadiness struct {
	DomainID         string  `json:"domainId"`
	MetricCount      int     `json:"metricCount"`
	ConfirmedCount   int     `json:"confirmedCount"`
	UnconfirmedCount int     `json:"unconfirmedCount"`
	ConfirmationRate float64 `json:"confirmationRate"`
}

type BulkAdditivityConfirmation struct {
	MetricVersionIDs []string   `json:"metricVersionIds"`
	Suggestion       Additivity `json:"suggestion"`
}

type BulkAdditivityConfirmationResult struct {
	MetricVersionIDs []string `json:"metricVersionIds"`
	ConfirmedCount   int      `json:"confirmedCount"`
	Replayed         bool     `json:"replayed"`
}

type AdditivityAdminBackend interface {
	ListUnconfirmedAdditivity(context.Context, AdminScope, Additivity, string, int) (AdditivityCandidatePage, error)
	GetAdditivityReadiness(context.Context, AdminScope) (AdditivityReadiness, error)
	BulkConfirmAdditivity(context.Context, AdminScope, BulkAdditivityConfirmation, AdminCommand) (BulkAdditivityConfirmationResult, error)
}

func (store *PostgresStore) ListUnconfirmedAdditivity(
	ctx context.Context,
	scope AdminScope,
	group Additivity,
	cursor string,
	limit int,
) (AdditivityCandidatePage, error) {
	if store == nil || store.pool == nil {
		return AdditivityCandidatePage{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return AdditivityCandidatePage{}, err
	}
	if group != "" && !validAdditivity(group) {
		return AdditivityCandidatePage{}, fmt.Errorf("%w: invalid additivity suggestion group", ErrRegistryInvalidRequest)
	}
	if limit < 1 || limit > 200 {
		return AdditivityCandidatePage{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrRegistryInvalidRequest)
	}
	position, err := decodeMetricCursor(cursor)
	if err != nil {
		return AdditivityCandidatePage{}, fmt.Errorf("%w: cursor is invalid", ErrRegistryInvalidRequest)
	}
	var page AdditivityCandidatePage
	err = database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		var cursorID any
		if position.ID != "" {
			cursorID = position.ID
		}
		rows, err := tx.Query(ctx, `SELECT
			version.id::text,version.metric_id::text,metric.code::text,metric.name,
			version.semantic_model_version_id::text,version.formula_ast,
			version.unit,model.grain_contract,
			COALESCE(version.additivity_suggestion,''),
			COALESCE(version.additivity_suggestion_rule_id,''),version.updated_at,
			COALESCE((SELECT CASE
				WHEN bool_or(measure.aggregation='COUNT_DISTINCT') THEN 'COUNT_DISTINCT'
				WHEN bool_and(measure.aggregation='SUM') THEN 'SUM'
				ELSE '' END
			 FROM askdata.metric_version_measures AS link
			 JOIN askdata.measures AS measure ON measure.id=link.measure_version_id
			 WHERE link.metric_version_id=version.id),'')
		FROM askdata.metric_versions AS version
		JOIN askdata.metrics AS metric ON metric.id=version.metric_id
		JOIN askdata.semantic_models AS model ON model.id=version.semantic_model_version_id
		WHERE version.domain_id=$1 AND version.status='DRAFT'
		  AND version.additivity IS NULL
		  AND ($2='' OR version.additivity_suggestion=$2)
		  AND ($3::timestamptz IS NULL OR (version.updated_at,version.id)<($3,$4::uuid))
		ORDER BY version.updated_at DESC,version.id DESC LIMIT $5`,
			scope.DomainID, group, position.UpdatedAt, cursorID, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item AdditivityCandidate
			var formula, grain json.RawMessage
			var aggregation string
			if err := rows.Scan(
				&item.MetricVersionID, &item.MetricID, &item.MetricCode, &item.MetricName,
				&item.ModelVersionID, &formula, &item.metric.Unit, &grain,
				&item.PersistedSuggestion, &item.PersistedRuleID, &item.UpdatedAt, &aggregation,
			); err != nil {
				return err
			}
			item.metric.ID, item.metric.FormulaAST = item.MetricVersionID, formula
			item.model.ID, item.model.GrainContract = item.ModelVersionID, grain
			item.defaultAggregation = Aggregation(aggregation)
			item.Suggestion = SuggestAdditivityWithContext(AdditivitySuggestionInput{
				Metric: item.metric, Model: item.model, MetricName: item.MetricName,
				MetricCode: item.MetricCode, DefaultAggregation: item.defaultAggregation,
			}, DefaultAdditivityLexicon())
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return AdditivityCandidatePage{}, normalizeAdminStoreError(err)
	}
	if len(page.Items) > limit {
		last := page.Items[limit-1]
		page.NextCursor, err = encodeMetricCursor(metricCursor{UpdatedAt: &last.UpdatedAt, ID: last.MetricVersionID})
		if err != nil {
			return AdditivityCandidatePage{}, err
		}
		page.Items = page.Items[:limit]
	}
	return page, nil
}

func (store *PostgresStore) GetAdditivityReadiness(
	ctx context.Context,
	scope AdminScope,
) (AdditivityReadiness, error) {
	if store == nil || store.pool == nil {
		return AdditivityReadiness{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return AdditivityReadiness{}, err
	}
	result := AdditivityReadiness{DomainID: scope.DomainID}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `WITH current_versions AS(
			SELECT DISTINCT ON(metric_id) additivity_confirmed_at
			FROM askdata.metric_versions
			WHERE domain_id=$1 AND status<>'DEPRECATED'
			ORDER BY metric_id,version_no DESC,id DESC
		) SELECT count(*)::int,
			count(*) FILTER(WHERE additivity_confirmed_at IS NOT NULL)::int
		FROM current_versions`, scope.DomainID).Scan(&result.MetricCount, &result.ConfirmedCount)
	})
	if err != nil {
		return AdditivityReadiness{}, normalizeAdminStoreError(err)
	}
	result.UnconfirmedCount = result.MetricCount - result.ConfirmedCount
	if result.MetricCount > 0 {
		result.ConfirmationRate = float64(result.ConfirmedCount) / float64(result.MetricCount)
	}
	return result, nil
}

func (store *PostgresStore) BulkConfirmAdditivity(
	ctx context.Context,
	scope AdminScope,
	input BulkAdditivityConfirmation,
	command AdminCommand,
) (BulkAdditivityConfirmationResult, error) {
	if store == nil || store.pool == nil {
		return BulkAdditivityConfirmationResult{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return BulkAdditivityConfirmationResult{}, err
	}
	if err := command.Validate(); err != nil {
		return BulkAdditivityConfirmationResult{}, err
	}
	ids, err := normalizeConfirmationIDs(input.MetricVersionIDs)
	if err != nil || !validAdditivity(input.Suggestion) {
		return BulkAdditivityConfirmationResult{}, fmt.Errorf("%w: invalid bulk additivity confirmation", ErrRegistryInvalidRequest)
	}
	result := BulkAdditivityConfirmationResult{MetricVersionIDs: ids}
	err = database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionEditDraft, ""); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
			"askdata-additivity-confirm:"+scope.TenantID+":"+command.RequestID); err != nil {
			return err
		}
		var existingHash string
		var detail json.RawMessage
		err := tx.QueryRow(ctx, `SELECT action_hash,detail FROM askdata.audit_events
			WHERE request_id=$1 AND actor_id=$2 AND domain_id=$3
			  AND event_type='ADDITIVITY_CONFIRMED'
			ORDER BY created_at,id LIMIT 1`, command.RequestID, scope.ActorID, scope.DomainID).
			Scan(&existingHash, &detail)
		if err == nil {
			if existingHash != string(command.ActionHash) {
				return ErrRegistryIdempotencyConflict
			}
			var replay struct {
				Result BulkAdditivityConfirmationResult `json:"result"`
			}
			if json.Unmarshal(detail, &replay) != nil || replay.Result.ConfirmedCount < 1 {
				return errors.New("additivity confirmation audit is corrupt")
			}
			result = replay.Result
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		for _, id := range ids {
			value, err := getDraftTx(ctx, tx, scope.DomainID, AdminResourceMetricVersion, id, true)
			if err != nil {
				return err
			}
			metric, ok := value.(MetricVersion)
			if !ok || metric.Status != VersionStatusDraft || metric.Additivity != "" {
				return ErrRegistryVersionConflict
			}
			var persisted Additivity
			var ruleID string
			if err := tx.QueryRow(ctx, `SELECT COALESCE(additivity_suggestion,''),
				COALESCE(additivity_suggestion_rule_id,'') FROM askdata.metric_versions
				WHERE id=$1 AND domain_id=$2 FOR UPDATE`, id, scope.DomainID).
				Scan(&persisted, &ruleID); err != nil {
				return err
			}
			if persisted != input.Suggestion || ruleID == "" {
				return fmt.Errorf("%w: metric suggestion changed", ErrRegistryVersionConflict)
			}
			metric.Additivity = persisted
			switch persisted {
			case NonAdditive:
				metric.AggregationRestriction = PostAggregate
			case SemiAdditive:
				metric.SemiAdditiveTimeAggregation = SemiAdditivePeriodEnd
			}
			confirmMetricAdditivity(&metric, scope.ActorID, nil)
			if err := ValidateAdditivity(metric); err != nil {
				return err
			}
			metric.ContentHash = metricVersionContentHash(metric)
			tag, err := tx.Exec(ctx, `UPDATE askdata.metric_versions SET
				additivity=$1,semi_additive_time_aggregation=$2,
				aggregation_restriction=$3,additivity_confirmed_by=$4,
				additivity_confirmed_at=$5,content_hash=$6,updated_at=now()
				WHERE id=$7 AND domain_id=$8 AND status='DRAFT' AND additivity IS NULL`,
				emptyStringToNil(string(metric.Additivity)),
				emptyStringToNil(string(metric.SemiAdditiveTimeAggregation)),
				emptyStringToNil(string(metric.AggregationRestriction)), scope.ActorID,
				metric.AdditivityConfirmedAt, metric.ContentHash, id, scope.DomainID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrRegistryVersionConflict
			}
		}
		result.ConfirmedCount = len(ids)
		result.Replayed = false
		detail, err = json.Marshal(struct {
			Result BulkAdditivityConfirmationResult `json:"result"`
		}{Result: result})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.audit_events(
				id,tenant_id,domain_id,actor_id,event_type,resource_type,
				resource_id,request_id,action_hash,detail
			) VALUES($1,$2,$3,$4,'ADDITIVITY_CONFIRMED','METRIC_VERSION',$5,$6,$7,$8)`,
				uuid.NewString(), scope.TenantID, scope.DomainID, scope.ActorID,
				id, command.RequestID, command.ActionHash, detail); err != nil {
				return err
			}
		}
		return nil
	})
	return result, normalizeAdminStoreError(err)
}

func normalizeConfirmationIDs(raw []string) ([]string, error) {
	if len(raw) < 1 || len(raw) > 200 {
		return nil, ErrRegistryInvalidRequest
	}
	seen := make(map[string]struct{}, len(raw))
	ids := make([]string, 0, len(raw))
	for _, id := range raw {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != strings.ToLower(id) {
			return nil, ErrRegistryInvalidRequest
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrRegistryInvalidRequest
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// PersistAdditivitySuggestions updates only advisory fields. It is used by the
// inventory command; authority facts and confirmation audit fields are never
// touched.
func (store *PostgresStore) PersistAdditivitySuggestions(
	ctx context.Context,
	scope AdminScope,
	items []AdditivityCandidate,
) (int, error) {
	if store == nil || store.pool == nil {
		return 0, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return 0, err
	}
	updated := 0
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionEditDraft, ""); err != nil {
			return err
		}
		for _, item := range items {
			if item.Suggestion.Value == "" {
				continue
			}
			tag, err := tx.Exec(ctx, `UPDATE askdata.metric_versions SET
				additivity_suggestion=$1,additivity_suggestion_rule_id=$2,updated_at=now()
				WHERE id=$3 AND domain_id=$4 AND status='DRAFT' AND additivity IS NULL`,
				item.Suggestion.Value, item.Suggestion.RuleID, item.MetricVersionID, scope.DomainID)
			if err != nil {
				return err
			}
			updated += int(tag.RowsAffected())
		}
		return nil
	})
	return updated, normalizeAdminStoreError(err)
}

var _ AdditivityAdminBackend = (*PostgresStore)(nil)
