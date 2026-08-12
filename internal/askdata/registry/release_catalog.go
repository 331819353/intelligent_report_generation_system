package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

// ReleaseCatalogItem is the governed, domain-scoped release summary used by
// administration surfaces. Evaluation case bodies are deliberately absent.
type ReleaseCatalogItem struct {
	ID                   string     `json:"id"`
	SemanticVersion      string     `json:"semanticVersion"`
	ContentHash          string     `json:"contentHash"`
	Status               string     `json:"status"`
	ObjectCount          int        `json:"objectCount"`
	Version              int64      `json:"version"`
	ReadyProjectionCount int        `json:"readyProjectionCount"`
	ApprovalCount        int        `json:"approvalCount"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	ReadyAt              *time.Time `json:"readyAt,omitempty"`
	ActivatedAt          *time.Time `json:"activatedAt,omitempty"`
}

type ReleaseCatalogPage struct {
	Items      []ReleaseCatalogItem `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

type ReleaseCatalogBackend interface {
	ListReleaseCatalog(context.Context, AdminScope, string, int) (ReleaseCatalogPage, error)
}

// EvaluationSetCatalogItem exposes only governed set metadata. It never
// exposes evaluation case bodies, especially sealed prompts.
type EvaluationSetCatalogItem struct {
	ID                string    `json:"id"`
	Code              string    `json:"code"`
	VersionNo         int       `json:"versionNo"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	DatasetSplit      string    `json:"datasetSplit"`
	EvaluationMode    string    `json:"evaluationMode"`
	Status            string    `json:"status"`
	TargetReleaseID   string    `json:"targetReleaseId,omitempty"`
	SealedCaseCount   int       `json:"sealedCaseCount"`
	SealedReviewCount int       `json:"sealedReviewCount"`
	RecordVersion     int64     `json:"recordVersion"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type EvaluationSetCatalogPage struct {
	Items []EvaluationSetCatalogItem `json:"items"`
}

type EvaluationSetCatalogBackend interface {
	ListEvaluationSetCatalog(context.Context, AdminScope, int) (EvaluationSetCatalogPage, error)
}

type TimeContractCatalogItem struct {
	ID                     string    `json:"id"`
	TimeContractID         string    `json:"timeContractId"`
	Code                   string    `json:"code"`
	Name                   string    `json:"name"`
	VersionNo              int       `json:"versionNo"`
	Status                 string    `json:"status"`
	Timezone               string    `json:"timezone"`
	IncompletePeriodPolicy string    `json:"incompletePeriodPolicy,omitempty"`
	ExpectedLagHours       int       `json:"expectedLagHours"`
	ContentHash            string    `json:"contentHash"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

type TimeContractCatalogBackend interface {
	ListTimeContractCatalog(context.Context, AdminScope, int) ([]TimeContractCatalogItem, error)
}

type releaseCatalogCursor struct {
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

func encodeReleaseCatalogCursor(value releaseCatalogCursor) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeReleaseCatalogCursor(raw string) (releaseCatalogCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return releaseCatalogCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return releaseCatalogCursor{}, ErrRegistryInvalidRequest
	}
	var value releaseCatalogCursor
	if err := json.Unmarshal(payload, &value); err != nil || value.CreatedAt.IsZero() || !canonicalAdminUUID(value.ID) {
		return releaseCatalogCursor{}, ErrRegistryInvalidRequest
	}
	return value, nil
}

func (store *PostgresStore) ListReleaseCatalog(
	ctx context.Context, scope AdminScope, cursor string, limit int,
) (ReleaseCatalogPage, error) {
	if store == nil || store.pool == nil {
		return ReleaseCatalogPage{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return ReleaseCatalogPage{}, err
	}
	if limit < 1 || limit > 200 {
		return ReleaseCatalogPage{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrRegistryInvalidRequest)
	}
	position, err := decodeReleaseCatalogCursor(cursor)
	if err != nil {
		return ReleaseCatalogPage{}, fmt.Errorf("%w: cursor is invalid", ErrRegistryInvalidRequest)
	}
	page := ReleaseCatalogPage{Items: []ReleaseCatalogItem{}}
	err = database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		var createdBefore any
		var idBefore any
		if !position.CreatedAt.IsZero() {
			createdBefore = position.CreatedAt
			idBefore = position.ID
		}
		rows, err := tx.Query(ctx, `SELECT release.id::text,release.semantic_version,
			release.content_hash,release.status,release.object_count,release.version,
			(SELECT count(*) FROM askdata.release_projections AS projection
			 WHERE projection.release_id=release.id AND projection.status='READY'
			   AND projection.expected_content_hash=release.content_hash
			   AND projection.applied_content_hash=release.content_hash),
			(SELECT count(*) FROM askdata.release_approvals AS approval
			 WHERE approval.release_id=release.id),
			release.created_at,release.updated_at,release.ready_at,release.activated_at
		FROM askdata.releases AS release
		WHERE release.domain_id=$1
		  AND ($2::timestamptz IS NULL OR (release.created_at,release.id)<($2,$3::uuid))
		ORDER BY release.created_at DESC,release.id DESC LIMIT $4`,
			scope.DomainID, createdBefore, idBefore, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ReleaseCatalogItem
			if err := rows.Scan(
				&item.ID, &item.SemanticVersion, &item.ContentHash, &item.Status,
				&item.ObjectCount, &item.Version, &item.ReadyProjectionCount,
				&item.ApprovalCount, &item.CreatedAt, &item.UpdatedAt,
				&item.ReadyAt, &item.ActivatedAt,
			); err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			last := page.Items[len(page.Items)-1]
			page.NextCursor, err = encodeReleaseCatalogCursor(releaseCatalogCursor{CreatedAt: last.CreatedAt, ID: last.ID})
			return err
		}
		return nil
	})
	return page, err
}

func (store *PostgresStore) ListEvaluationSetCatalog(
	ctx context.Context, scope AdminScope, limit int,
) (EvaluationSetCatalogPage, error) {
	if store == nil || store.pool == nil {
		return EvaluationSetCatalogPage{}, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return EvaluationSetCatalogPage{}, err
	}
	if limit < 1 || limit > 200 {
		return EvaluationSetCatalogPage{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrRegistryInvalidRequest)
	}
	page := EvaluationSetCatalogPage{Items: []EvaluationSetCatalogItem{}}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,code::text,version_no,name,description,
			dataset_split,evaluation_mode,status,sealed_case_count,sealed_review_count,
			COALESCE(target_release_id::text,''),record_version,updated_at
		FROM askdata.evaluation_sets WHERE domain_id=$1
		ORDER BY updated_at DESC,id DESC LIMIT $2`, scope.DomainID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item EvaluationSetCatalogItem
			if err := rows.Scan(
				&item.ID, &item.Code, &item.VersionNo, &item.Name, &item.Description,
				&item.DatasetSplit, &item.EvaluationMode, &item.Status,
				&item.SealedCaseCount, &item.SealedReviewCount, &item.TargetReleaseID,
				&item.RecordVersion, &item.UpdatedAt,
			); err != nil {
				return err
			}
			page.Items = append(page.Items, item)
		}
		return rows.Err()
	})
	return page, err
}

func (store *PostgresStore) ListTimeContractCatalog(
	ctx context.Context, scope AdminScope, limit int,
) ([]TimeContractCatalogItem, error) {
	if store == nil || store.pool == nil {
		return nil, errors.New("semantic registry PostgreSQL store is not configured")
	}
	if err := scope.Validate(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("%w: limit must be between 1 and 200", ErrRegistryInvalidRequest)
	}
	items := []TimeContractCatalogItem{}
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT version.id::text,contract.id::text,contract.code::text,
			contract.name,version.version_no,version.status,version.timezone,
			COALESCE(version.incomplete_period_policy,''),version.expected_lag_hours,
			version.content_hash,version.updated_at
		FROM askdata.time_contract_versions AS version
		JOIN askdata.time_contracts AS contract
		  ON contract.id=version.time_contract_id AND contract.tenant_id=version.tenant_id
		WHERE version.domain_id=$1 AND version.status='CERTIFIED'
		ORDER BY version.updated_at DESC,version.id DESC LIMIT $2`, scope.DomainID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item TimeContractCatalogItem
			if err := rows.Scan(
				&item.ID, &item.TimeContractID, &item.Code, &item.Name, &item.VersionNo,
				&item.Status, &item.Timezone, &item.IncompletePeriodPolicy,
				&item.ExpectedLagHours, &item.ContentHash, &item.UpdatedAt,
			); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}
