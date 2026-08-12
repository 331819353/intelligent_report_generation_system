package projection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/platform/database"
)

type Target string

const (
	TargetRegistry  Target = "POSTGRES_REGISTRY"
	TargetSearch    Target = "SEARCH_INDEX"
	TargetExecution Target = "EXECUTION_SEMANTIC_LAYER"

	DefaultLease = 2 * time.Minute
)

var RuntimeTargets = []Target{TargetRegistry, TargetSearch, TargetExecution}

var (
	ErrInvalidWork = errors.New("semantic runtime projection work is invalid")
	ErrLeaseLost   = errors.New("semantic runtime projection lease was lost")
	ErrContract    = errors.New("semantic runtime projection contract is invalid")
)

type Claim struct {
	ProjectionID, TenantID, DomainID, ReleaseID string
	Target                                      Target
	SemanticVersion, ContentHash, LeaseToken    string
	Attempt                                     int
}

type Proof struct {
	ContentHash     string
	ResourceVersion string
	ObjectCount     int
	Detail          map[string]any
}

type Store interface {
	TenantIDs(context.Context, Target) ([]string, error)
	Claim(context.Context, string, Target, string, time.Duration) (*Claim, error)
	Project(context.Context, Claim, string) (Proof, error)
	Complete(context.Context, Claim, string, Proof) error
	Fail(context.Context, Claim, string, string, bool) error
}

type Worker struct{ store Store }

func NewWorker(store Store) (*Worker, error) {
	if store == nil {
		return nil, ErrInvalidWork
	}
	return &Worker{store: store}, nil
}

func (worker *Worker) TenantIDs(ctx context.Context, target Target) ([]string, error) {
	if worker == nil || worker.store == nil || !validTarget(target) {
		return nil, ErrInvalidWork
	}
	return worker.store.TenantIDs(ctx, target)
}

func (worker *Worker) ProcessNext(
	ctx context.Context, tenantID string, target Target, workerID string, lease time.Duration,
) (bool, error) {
	if worker == nil || worker.store == nil || uuid.Validate(tenantID) != nil ||
		!validTarget(target) || strings.TrimSpace(workerID) == "" || len(workerID) > 128 ||
		lease < 30*time.Second || lease > 10*time.Minute {
		return false, ErrInvalidWork
	}
	claim, err := worker.store.Claim(ctx, tenantID, target, workerID, lease)
	if err != nil || claim == nil {
		return false, err
	}
	proof, err := worker.store.Project(ctx, *claim, workerID)
	if err == nil {
		err = worker.store.Complete(ctx, *claim, workerID, proof)
		if err == nil {
			return true, nil
		}
	}
	if errors.Is(err, ErrLeaseLost) {
		return true, err
	}
	code, retryable := "RUNTIME_PROJECTION_FAILED", true
	if errors.Is(err, ErrContract) || errors.Is(err, ErrInvalidWork) {
		code, retryable = "RUNTIME_PROJECTION_CONTRACT_INVALID", false
	}
	if failErr := worker.store.Fail(ctx, *claim, workerID, code, retryable); failErr != nil {
		return true, errors.Join(err, failErr)
	}
	return true, err
}

func validTarget(target Target) bool {
	return target == TargetRegistry || target == TargetSearch || target == TargetExecution
}

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

func (store *PostgresStore) TenantIDs(ctx context.Context, target Target) ([]string, error) {
	if store == nil || store.pool == nil || !validTarget(target) {
		return nil, ErrInvalidWork
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text FROM askdata.list_release_projection_tenants($1)`, target)
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

func (store *PostgresStore) Claim(
	ctx context.Context, tenantID string, target Target, workerID string, lease time.Duration,
) (*Claim, error) {
	if store == nil || store.pool == nil || uuid.Validate(tenantID) != nil || !validTarget(target) ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 || lease < 30*time.Second || lease > 10*time.Minute {
		return nil, ErrInvalidWork
	}
	claim := Claim{TenantID: tenantID, Target: target}
	err := store.pool.QueryRow(ctx, `SELECT projection_id::text,domain_id::text,release_id::text,
		target,semantic_version,content_hash,lease_token::text,attempt
		FROM askdata.claim_release_projection($1,$2,$3,$4)`,
		tenantID, target, workerID, int64(lease/time.Second),
	).Scan(&claim.ProjectionID, &claim.DomainID, &claim.ReleaseID, &claim.Target,
		&claim.SemanticVersion, &claim.ContentHash, &claim.LeaseToken, &claim.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if validateClaim(claim) != nil {
		return &claim, ErrContract
	}
	return &claim, nil
}

type releaseObject struct {
	ObjectType, ObjectID, ObjectVersionID, ContentHash, Sensitivity string
	Contract                                                        json.RawMessage
}

func (store *PostgresStore) Project(ctx context.Context, claim Claim, workerID string) (proof Proof, err error) {
	if store == nil || store.pool == nil || validateClaim(claim) != nil ||
		strings.TrimSpace(workerID) == "" || len(workerID) > 128 {
		return Proof{}, ErrInvalidWork
	}
	err = database.WithTenantTx(ctx, store.pool, claim.TenantID, func(tx pgx.Tx) error {
		var releaseHash, semanticVersion, manifestHash string
		var releaseObjectCount, manifestCount int
		if err := tx.QueryRow(ctx, `SELECT release.content_hash,release.semantic_version,release.object_count,
			(SELECT count(*) FROM askdata.release_objects object WHERE object.tenant_id=release.tenant_id AND object.release_id=release.id),
			askdata.release_manifest_hash(release.id)
		FROM askdata.releases release
		JOIN askdata.release_projections projection ON projection.release_id=release.id
		 AND projection.domain_id=release.domain_id AND projection.tenant_id=release.tenant_id
		WHERE release.id=$1 AND release.domain_id=$2 AND projection.id=$3 AND projection.target=$4
		 AND projection.status='RUNNING' AND projection.lease_owner=$5 AND projection.lease_token=$6
		 AND projection.lease_expires_at>now() AND release.status IN ('PROJECTING','BLOCKED')`,
			claim.ReleaseID, claim.DomainID, claim.ProjectionID, claim.Target, workerID, claim.LeaseToken,
		).Scan(&releaseHash, &semanticVersion, &releaseObjectCount, &manifestCount, &manifestHash); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrLeaseLost
			}
			return err
		}
		if releaseHash != claim.ContentHash || semanticVersion != claim.SemanticVersion ||
			releaseObjectCount != manifestCount || manifestCount < 1 || manifestHash != releaseHash {
			return fmt.Errorf("%w: release manifest count or hash is inconsistent", ErrContract)
		}

		rows, err := tx.Query(ctx, `SELECT object_type,object_id::text,object_version_id::text,
			content_hash,sensitivity,contract_json FROM askdata.release_objects
			WHERE release_id=$1 AND domain_id=$2 ORDER BY object_type,object_id,object_version_id`, claim.ReleaseID, claim.DomainID)
		if err != nil {
			return err
		}
		objects := make([]releaseObject, 0, manifestCount)
		for rows.Next() {
			var object releaseObject
			if err := rows.Scan(&object.ObjectType, &object.ObjectID, &object.ObjectVersionID,
				&object.ContentHash, &object.Sensitivity, &object.Contract); err != nil {
				rows.Close()
				return err
			}
			objects = append(objects, object)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(objects) != manifestCount {
			return fmt.Errorf("%w: release objects changed during projection", ErrContract)
		}

		if _, err := tx.Exec(ctx, `DELETE FROM askdata.release_projection_artifacts
			WHERE release_id=$1 AND domain_id=$2 AND target=$3`, claim.ReleaseID, claim.DomainID, claim.Target); err != nil {
			return err
		}
		searchDocumentCount := 0
		for _, object := range objects {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_projection_artifacts(
				tenant_id,domain_id,release_id,target,artifact_type,artifact_id,
				object_version_id,content_hash,contract_json
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
				claim.TenantID, claim.DomainID, claim.ReleaseID, claim.Target,
				object.ObjectType, object.ObjectVersionID, object.ObjectVersionID,
				object.ContentHash, object.Contract); err != nil {
				return err
			}
			if claim.Target == TargetSearch {
				created, err := upsertSearchDocument(ctx, tx, claim, object)
				if err != nil {
					return err
				}
				if created {
					searchDocumentCount++
				}
			}
		}
		proof = Proof{
			ContentHash:     claim.ContentHash,
			ResourceVersion: fmt.Sprintf("askdata-runtime-projection-v1:%s:%s", claim.Target, claim.ContentHash[:12]),
			ObjectCount:     len(objects),
			Detail: map[string]any{
				"schemaVersion":       "askdata-runtime-projection-v1",
				"artifactCount":       len(objects),
				"searchDocumentCount": searchDocumentCount,
			},
		}
		return nil
	})
	return proof, err
}

func upsertSearchDocument(ctx context.Context, tx pgx.Tx, claim Claim, object releaseObject) (bool, error) {
	objectType, viewType, supported := searchDocumentShape(object.ObjectType)
	if !supported || object.Sensitivity == "RESTRICTED" {
		return false, nil
	}
	var contract map[string]any
	if err := json.Unmarshal(object.Contract, &contract); err != nil {
		return false, fmt.Errorf("%w: search contract is not an object", ErrContract)
	}
	document := searchDocumentText(contract)
	if document == "" {
		// Some executable release contracts intentionally contain only IDs and
		// formula ASTs (metric versions are the common example). They remain part
		// of the immutable runtime artifact, but are not independently searchable;
		// their governed identity is still recalled through measures and business
		// terms. An empty text projection is therefore an ineligible document, not
		// a corrupt release that should permanently block every other target.
		return false, nil
	}
	metadata := map[string]any{
		"releaseId": claim.ReleaseID, "objectId": object.ObjectID,
		"objectVersionId": object.ObjectVersionID, "contentHash": object.ContentHash,
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return false, err
	}
	sensitivity := object.Sensitivity
	if sensitivity != "PUBLIC" && sensitivity != "INTERNAL" && sensitivity != "CONFIDENTIAL" {
		sensitivity = "INTERNAL"
	}
	documentID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(
		claim.TenantID+":"+objectType+":"+object.ObjectVersionID+":"+viewType,
	)).String()
	_, err = tx.Exec(ctx, `INSERT INTO askdata.search_documents(
		id,tenant_id,domain_id,object_type,object_version_id,view_type,
		sensitivity,index_policy,document,metadata,input_hash
	) VALUES($1,$2,$3,$4,$5,$6,$7,'HYBRID',$8,$9,$10)
	ON CONFLICT(tenant_id,object_type,object_version_id,view_type) DO UPDATE SET
		sensitivity=EXCLUDED.sensitivity,index_policy=EXCLUDED.index_policy,
		document=EXCLUDED.document,metadata=EXCLUDED.metadata,input_hash=EXCLUDED.input_hash,
		updated_at=now()`, documentID, claim.TenantID, claim.DomainID, objectType,
		object.ObjectVersionID, viewType, sensitivity, document, metadataJSON, object.ContentHash)
	if err != nil {
		return false, err
	}
	return true, nil
}

func searchDocumentShape(objectType string) (string, string, bool) {
	switch objectType {
	case "MEASURE", "METRIC":
		// The question-facing Tool Host calls both governed measures and derived
		// metrics METRIC. Projecting MEASURE verbatim made a healthy release
		// invisible to retrieval because the index and tool enums no longer met.
		return "METRIC", "NAME_ALIAS", true
	case "SEMANTIC_MODEL", "DIMENSION":
		return objectType, "NAME_ALIAS", true
	case "BUSINESS_TERM":
		return objectType, "DEFINITION_QUESTION", true
	case "CERTIFIED_EXAMPLE":
		return objectType, "EXAMPLE_INTENT", true
	default:
		return "", "", false
	}
}

func searchDocumentText(contract map[string]any) string {
	keys := []string{"name", "code", "description", "term", "definition", "question", "targetCode", "defaultTimeExpression"}
	parts := make([]string, 0, len(keys)+8)
	for _, key := range keys {
		if value, ok := contract[key].(string); ok && strings.TrimSpace(value) != "" {
			parts = append(parts, strings.TrimSpace(value))
		}
	}
	for _, key := range []string{"aliases", "applicableQuestionPatterns", "positiveExamples"} {
		values, ok := contract[key].([]any)
		if !ok {
			continue
		}
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
	}
	parts = uniqueStrings(parts)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func (store *PostgresStore) Complete(ctx context.Context, claim Claim, workerID string, proof Proof) error {
	if store == nil || store.pool == nil || validateClaim(claim) != nil ||
		proof.ContentHash != claim.ContentHash || proof.ObjectCount < 1 ||
		strings.TrimSpace(proof.ResourceVersion) == "" || proof.Detail == nil {
		return ErrInvalidWork
	}
	detail, err := json.Marshal(proof.Detail)
	if err != nil {
		return err
	}
	var completed bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.complete_release_projection($1,$2,$3,$4,$5,$6,$7,$8)`,
		claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken,
		proof.ContentHash, proof.ResourceVersion, proof.ObjectCount, detail,
	).Scan(&completed); err != nil {
		return err
	}
	if !completed {
		return ErrLeaseLost
	}
	return nil
}

func (store *PostgresStore) Fail(
	ctx context.Context, claim Claim, workerID, code string, retryable bool,
) error {
	if store == nil || store.pool == nil || validateClaim(claim) != nil ||
		strings.TrimSpace(workerID) == "" || strings.TrimSpace(code) == "" {
		return ErrInvalidWork
	}
	var failed bool
	if err := store.pool.QueryRow(ctx, `SELECT askdata.fail_release_projection($1,$2,$3,$4,$5,$6)`,
		claim.TenantID, claim.ProjectionID, workerID, claim.LeaseToken, code, retryable,
	).Scan(&failed); err != nil {
		return err
	}
	if !failed {
		return ErrLeaseLost
	}
	return nil
}

func validateClaim(claim Claim) error {
	for _, value := range []string{claim.ProjectionID, claim.TenantID, claim.DomainID, claim.ReleaseID, claim.LeaseToken} {
		if uuid.Validate(value) != nil {
			return ErrInvalidWork
		}
	}
	if !validTarget(claim.Target) || strings.TrimSpace(claim.SemanticVersion) == "" ||
		len(claim.ContentHash) != 64 || claim.Attempt < 1 {
		return ErrInvalidWork
	}
	if _, err := hex.DecodeString(claim.ContentHash); err != nil {
		return ErrInvalidWork
	}
	return nil
}

// contentHash is retained for deterministic unit fixtures and future runtime
// projection formats that need to hash derived artifacts independently.
func contentHash(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func sortedTargets(values []Target) []Target {
	result := append([]Target(nil), values...)
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
