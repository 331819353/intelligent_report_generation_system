package semanticasset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"intelligent-report-generation-system/internal/platform/database"
)

type RuntimeProjectionClaim struct {
	ProjectionID    string
	ReleaseID       string
	Target          string
	SemanticVersion string
	ContentHash     string
	LeaseToken      string
	Attempt         int
}

type RuntimeProjectionWorker struct{ store *PostgresStore }

func NewRuntimeProjectionWorker(store *PostgresStore) *RuntimeProjectionWorker {
	return &RuntimeProjectionWorker{store: store}
}

func (worker *RuntimeProjectionWorker) TenantIDs(ctx context.Context) ([]string, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil {
		return nil, ErrInvalidRequest
	}
	rows, err := worker.store.pool.Query(ctx,
		`SELECT tenant_id::text FROM platform.list_semantic_runtime_projection_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		result = append(result, tenantID)
	}
	return result, rows.Err()
}

func (worker *RuntimeProjectionWorker) ProcessNext(
	ctx context.Context,
	tenantID, workerID string,
	lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || worker.store.pool == nil ||
		strings.TrimSpace(workerID) == "" || lease < 30*time.Second || lease > 10*time.Minute {
		return false, ErrInvalidRequest
	}
	claim := RuntimeProjectionClaim{}
	err := worker.store.pool.QueryRow(ctx, `SELECT
		projection_id::text,release_id::text,target,semantic_version,
		content_hash,lease_token::text,attempt
		FROM platform.claim_semantic_runtime_projection($1::uuid,$2,$3)`,
		tenantID, workerID, int(lease/time.Second),
	).Scan(
		&claim.ProjectionID, &claim.ReleaseID, &claim.Target,
		&claim.SemanticVersion, &claim.ContentHash, &claim.LeaseToken, &claim.Attempt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	objectCount, detail, projectErr := worker.project(ctx, tenantID, claim)
	if projectErr == nil {
		resourceVersion := strings.ToLower(claim.Target) + ":" + claim.ContentHash
		projectErr = worker.complete(
			ctx, tenantID, workerID, claim, resourceVersion, objectCount, detail,
		)
	}
	if projectErr != nil {
		failErr := worker.fail(ctx, tenantID, workerID, claim, projectErr)
		return true, errors.Join(projectErr, failErr)
	}
	slog.Info("semantic release runtime projection ready",
		"tenant_id", tenantID, "release_id", claim.ReleaseID,
		"semantic_version", claim.SemanticVersion, "target", claim.Target,
		"object_count", objectCount)
	return true, nil
}

func (worker *RuntimeProjectionWorker) project(
	ctx context.Context,
	tenantID string,
	claim RuntimeProjectionClaim,
) (int, map[string]any, error) {
	switch claim.Target {
	case SemanticProjectionRegistry:
		return worker.projectRegistry(ctx, tenantID, claim)
	case SemanticProjectionExecution:
		return worker.projectExecution(ctx, tenantID, claim)
	case SemanticProjectionSearch:
		return worker.projectSearch(ctx, tenantID, claim)
	default:
		return 0, nil, ErrInvalidRequest
	}
}

func (worker *RuntimeProjectionWorker) projectRegistry(
	ctx context.Context,
	tenantID string,
	claim RuntimeProjectionClaim,
) (count int, detail map[string]any, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		var expected int
		if err := tx.QueryRow(ctx, `SELECT object_count
			FROM platform.semantic_releases
			WHERE id=$1::uuid AND content_hash=$2 AND status IN ('PROJECTING','READY')`,
			claim.ReleaseID, claim.ContentHash).Scan(&expected); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*)::int
			FROM platform.semantic_release_objects
			WHERE release_id=$1::uuid AND certification='CERTIFIED'
			  AND content_hash ~ '^[0-9a-f]{64}$'`, claim.ReleaseID).Scan(&count); err != nil {
			return err
		}
		if count != expected || count < 1 {
			return fmt.Errorf("%w: registry object count mismatch", ErrReleaseNotReady)
		}
		return nil
	})
	return count, map[string]any{"verifiedObjects": count, "mode": "IMMUTABLE_REGISTRY"}, err
}

func (worker *RuntimeProjectionWorker) projectExecution(
	ctx context.Context,
	tenantID string,
	claim RuntimeProjectionClaim,
) (count int, detail map[string]any, err error) {
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.semantic_execution_registry
			WHERE release_id=$1::uuid`, claim.ReleaseID); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `INSERT INTO platform.semantic_execution_registry(
				tenant_id,release_id,object_type,object_id,object_version,
				content_hash,native_object_id,native_version_id,contract_json
			)
			SELECT platform.current_tenant_id(),object.release_id,
				object.object_type,object.object_id,object.object_version,
				object.content_hash,
				COALESCE(
				  object.contract_json->>'nativeMetricId',
				  object.contract_json->>'nativeDimensionId',
				  object.contract_json->>'nativeDatasetId',''
				),
				COALESCE(
				  object.contract_json->>'nativeMetricVersionId',
				  object.contract_json->>'nativeDimensionVersionId',
				  object.contract_json->>'nativeDatasetVersionId',''
				),object.contract_json
			FROM platform.semantic_release_objects AS object
			WHERE object.release_id=$1::uuid AND object.certification='CERTIFIED'`,
			claim.ReleaseID)
		if err != nil {
			return err
		}
		count = int(command.RowsAffected())
		var expected int
		if err := tx.QueryRow(ctx, `SELECT object_count
			FROM platform.semantic_releases
			WHERE id=$1::uuid AND content_hash=$2`,
			claim.ReleaseID, claim.ContentHash).Scan(&expected); err != nil {
			return err
		}
		if count != expected || count < 1 {
			return fmt.Errorf("%w: execution registry count mismatch", ErrReleaseNotReady)
		}
		return nil
	})
	return count, map[string]any{
		"registeredObjects": count, "adapter": "GO_NATIVE_SEMANTIC_V1",
	}, err
}

func (worker *RuntimeProjectionWorker) projectSearch(
	ctx context.Context,
	tenantID string,
	claim RuntimeProjectionClaim,
) (objectCount int, detail map[string]any, err error) {
	documentCount := 0
	err = database.WithTenantTx(ctx, worker.store.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT object_type,object_id,object_version,
			domain_id,sensitivity,content_hash,contract_json
			FROM platform.semantic_release_objects
			WHERE release_id=$1::uuid AND certification='CERTIFIED'
			ORDER BY object_type,object_id,object_version`, claim.ReleaseID)
		if err != nil {
			return err
		}
		type object struct {
			objectType, objectID, objectVersion, domainID, sensitivity, contentHash string
			contract                                                                map[string]any
		}
		objects := []object{}
		for rows.Next() {
			var item object
			var contractJSON []byte
			if err := rows.Scan(
				&item.objectType, &item.objectID, &item.objectVersion,
				&item.domainID, &item.sensitivity, &item.contentHash, &contractJSON,
			); err != nil {
				rows.Close()
				return err
			}
			if err := json.Unmarshal(contractJSON, &item.contract); err != nil || item.contract == nil {
				rows.Close()
				return ErrInvalidRequest
			}
			objects = append(objects, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if _, err := tx.Exec(ctx, `DELETE FROM platform.semantic_release_search_documents
			WHERE release_id=$1::uuid`, claim.ReleaseID); err != nil {
			return err
		}
		for _, item := range objects {
			views := semanticSearchViews(item.objectID, item.objectType, item.contract)
			for viewType, document := range views {
				metadata, _ := json.Marshal(map[string]any{
					"domainId": item.domainID, "sensitivity": item.sensitivity,
					"contentHash": item.contentHash,
				})
				if _, err := tx.Exec(ctx, `INSERT INTO platform.semantic_release_search_documents(
						tenant_id,release_id,object_type,object_id,object_version,
						view_type,document,metadata
					) VALUES(
						platform.current_tenant_id(),$1::uuid,$2,$3,$4,$5,$6,$7::jsonb
					)`, claim.ReleaseID, item.objectType, item.objectID,
					item.objectVersion, viewType, document, metadata); err != nil {
					return err
				}
				documentCount++
			}
		}
		objectCount = len(objects)
		if objectCount < 1 || documentCount < objectCount {
			return fmt.Errorf("%w: search projection incomplete", ErrReleaseNotReady)
		}
		return nil
	})
	return objectCount, map[string]any{
		"indexedObjects": objectCount, "documentCount": documentCount,
		"views":  []string{"NAME_ALIAS", "DEFINITION_QUESTION", "EXAMPLE_INTENT"},
		"engine": "POSTGRES_FTS_RELEASE_PINNED",
	}, err
}

func semanticSearchViews(
	objectID, objectType string,
	contract map[string]any,
) map[string]string {
	join := func(fields ...string) string {
		parts := []string{objectID}
		for _, field := range fields {
			value, exists := contract[field]
			if !exists {
				continue
			}
			switch typed := value.(type) {
			case string:
				parts = append(parts, typed)
			case []any, map[string]any:
				encoded, _ := json.Marshal(typed)
				parts = append(parts, string(encoded))
			}
		}
		return boundedSemanticSearchText(strings.Join(parts, " "))
	}
	views := map[string]string{
		"NAME_ALIAS": join(
			"title", "name", "code", "aliases", "positiveAliases",
			"synonyms", "abbreviations", "shortNames",
		),
		"DEFINITION_QUESTION": join(
			"title", "description", "definition", "formula", "grain", "entityId",
			"defaultTimeDimensionId", "dimensionId", "canonicalCode", "usages",
		),
	}
	if objectType == "CERTIFIED_EXAMPLE" {
		views["EXAMPLE_INTENT"] = join("question", "intent", "objectIds")
	}
	return views
}

func boundedSemanticSearchText(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	characters := []rune(value)
	if len(characters) > 32768 {
		characters = characters[:32768]
	}
	return string(characters)
}

func (worker *RuntimeProjectionWorker) complete(
	ctx context.Context,
	tenantID, workerID string,
	claim RuntimeProjectionClaim,
	resourceVersion string,
	objectCount int,
	detail map[string]any,
) error {
	detailJSON, _ := json.Marshal(detail)
	var completed bool
	err := worker.store.pool.QueryRow(ctx, `SELECT platform.complete_semantic_runtime_projection(
		$1::uuid,$2::uuid,$3,$4::uuid,$5,$6,$7,$8::jsonb
	)`, tenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		claim.ContentHash, resourceVersion, objectCount, detailJSON).Scan(&completed)
	if err != nil {
		return err
	}
	if !completed {
		return ErrReleaseNotReady
	}
	return nil
}

func (worker *RuntimeProjectionWorker) fail(
	ctx context.Context,
	tenantID, workerID string,
	claim RuntimeProjectionClaim,
	cause error,
) error {
	code := "SEMANTIC_RUNTIME_PROJECTION_FAILED"
	if errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, context.Canceled) {
		code = "SEMANTIC_RUNTIME_PROJECTION_TIMEOUT"
	} else if errors.Is(cause, ErrInvalidRequest) || errors.Is(cause, ErrReleaseNotReady) {
		code = "SEMANTIC_RUNTIME_PROJECTION_CONTRACT_INVALID"
	}
	detail, _ := json.Marshal(map[string]any{
		"target": claim.Target, "attempt": claim.Attempt,
	})
	var failed bool
	err := worker.store.pool.QueryRow(ctx, `SELECT platform.fail_semantic_runtime_projection(
		$1::uuid,$2::uuid,$3,$4::uuid,$5,$6::jsonb
	)`, tenantID, claim.ProjectionID, workerID, claim.LeaseToken, code, detail).Scan(&failed)
	if err != nil {
		return err
	}
	if !failed {
		return ErrReleaseNotReady
	}
	return nil
}

func RunRuntimeProjectionWorker(
	ctx context.Context,
	logger *slog.Logger,
	worker *RuntimeProjectionWorker,
	workerID string,
	pollInterval time.Duration,
) {
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		tenantIDs, err := worker.TenantIDs(ctx)
		if err != nil {
			logger.Error("list semantic runtime projection tenants", "error", err)
		} else {
			sort.Strings(tenantIDs)
			for _, tenantID := range tenantIDs {
				processed, processErr := worker.ProcessNext(
					ctx, tenantID, workerID+"-semantic-runtime", 3*time.Minute,
				)
				if processErr != nil {
					logger.Error("project semantic release runtime",
						"tenant_id", tenantID, "error", processErr)
				}
				if processed {
					continue
				}
			}
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
