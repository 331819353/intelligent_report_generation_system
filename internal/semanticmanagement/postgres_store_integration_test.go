package semanticmanagement

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
)

// Opt-in: point only at a disposable database with migrations through v61 or later.
func TestPostgresStoreTagAliasLifecycleAndTenantIsolation(t *testing.T) {
	databaseURL := os.Getenv("SEMANTIC_MANAGEMENT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SEMANTIC_MANAGEMENT_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tenantID, otherTenantID, actorID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO platform.tenants(id,code,name) VALUES
		($1,$2,'Semantic API integration'),($3,$4,'Other semantic tenant')`,
		tenantID, "semantic_"+tenantID[:8], otherTenantID, "semantic_"+otherTenantID[:8]); err != nil {
		t.Fatal(err)
	}
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.users(
			id,tenant_id,email,display_name,password_hash
		) VALUES($1,$2,$3,'Semantic manager','test-hash')`,
			actorID, tenantID, actorID+"@example.test")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	tag, err := store.CreateTag(ctx, tenantID, actorID, CreateTagInput{
		Code: "home_ecosystem", Name: "智家生态圈",
		Category: "BUSINESS_DOMAIN", Governance: "CONTROLLED", Status: "ACTIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	alias, err := store.CreateTagAlias(ctx, tenantID, actorID, CreateTagAliasInput{
		TagID: tag.ID, Alias: "690", AliasType: "LEGACY", LanguageCode: "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateTagAlias(ctx, tenantID, actorID, alias.ID, UpdateTagAliasInput{
		ExpectedRecordVersion: alias.RecordVersion, TagID: tag.ID,
		Alias: "690", AliasType: "LEGACY", LanguageCode: "zh-CN",
	})
	if err != nil || updated.RecordVersion == alias.RecordVersion {
		t.Fatalf("alias update=%+v err=%v", updated, err)
	}
	if _, err := store.UpdateTagAlias(ctx, tenantID, actorID, alias.ID, UpdateTagAliasInput{
		ExpectedRecordVersion: alias.RecordVersion, TagID: tag.ID,
		Alias: "690", AliasType: "LEGACY", LanguageCode: "zh-CN",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale alias update error=%v", err)
	}
	updated, err = exerciseUpdateTagAliasDirectSQLGateOrder(
		ctx, pool, store, tenantID, actorID, updated,
	)
	if err != nil {
		t.Fatal(err)
	}

	aliases, total, err := store.ListTagAliases(ctx, tenantID, AliasFilter{
		Page: Page{Limit: 50}, TagID: tag.ID, Query: "690", AliasType: "LEGACY",
	})
	if err != nil || total != 1 || len(aliases) != 1 || aliases[0].TagID != tag.ID {
		t.Fatalf("tenant aliases=%+v total=%d err=%v", aliases, total, err)
	}
	otherAliases, otherTotal, err := store.ListTagAliases(ctx, otherTenantID, AliasFilter{
		Page: Page{Limit: 50}, Query: "690",
	})
	if err != nil || otherTotal != 0 || len(otherAliases) != 0 {
		t.Fatalf("cross-tenant aliases=%+v total=%d err=%v", otherAliases, otherTotal, err)
	}
	if err := store.DeleteTagAlias(ctx, tenantID, actorID, alias.ID, updated.RecordVersion); err != nil {
		t.Fatal(err)
	}
	tag, err = store.UpdateTag(ctx, tenantID, actorID, tag.ID, UpdateTagInput{
		ExpectedVersion: tag.Version, Code: tag.Code, Name: tag.Name,
		Description: "governed business domain", Category: tag.Category,
		Governance: tag.Governance, Status: tag.Status,
	})
	if err != nil || tag.Version != 2 {
		t.Fatalf("tag update=%+v err=%v", tag, err)
	}
	tag, err = exerciseUpdateTagDirectSQLGateOrder(
		ctx, pool, store, tenantID, actorID, tag,
	)
	if err != nil {
		t.Fatal(err)
	}

	child, err := store.CreateTag(ctx, tenantID, actorID, CreateTagInput{
		ParentTagID: tag.ID, Code: "home_ecosystem_child", Name: "子域",
		Category: "BUSINESS_DOMAIN", Governance: "CONTROLLED", Status: "DRAFT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateTag(ctx, tenantID, actorID, tag.ID, UpdateTagInput{
		ExpectedVersion: tag.Version, ParentTagID: child.ID,
		Code: tag.Code, Name: tag.Name, Description: tag.Description,
		Category: tag.Category, Governance: tag.Governance, Status: tag.Status,
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("taxonomy cycle error=%v", err)
	}
	deprecated, err := store.DeprecateTag(ctx, tenantID, actorID, child.ID, child.Version)
	if err != nil || deprecated.Status != "DEPRECATED" || deprecated.Version != child.Version+1 {
		t.Fatalf("deprecated tag=%+v err=%v", deprecated, err)
	}
	if _, err := store.CreateTagAlias(ctx, tenantID, actorID, CreateTagAliasInput{
		TagID: child.ID, Alias: "deprecated-child", AliasType: "BUSINESS", LanguageCode: "en",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("deprecated tag alias error=%v", err)
	}

	datasetID, datasetVersionID := uuid.NewString(), uuid.NewString()
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.datasets(
				id,tenant_id,code,name,dataset_type,status,created_by,updated_by,layer
			) VALUES($1,$2,$3,'Semantic binding fixture','SINGLE_SOURCE','DRAFT',$4,$4,'ODS')`,
			datasetID, tenantID, "semantic_dataset_"+datasetID[:8], actorID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.dataset_versions(
				id,tenant_id,dataset_id,version_no,status,dsl_version,dsl_json,
				schema_hash,logical_plan_json,plan_hash,created_by,updated_by,layer
			) VALUES($1,$2,$3,1,'DRAFT','1.0','{}',$4,'{}',$4,$5,$5,'ODS')`,
			datasetVersionID, tenantID, datasetID,
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", actorID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE platform.datasets
			SET current_draft_version_id=$1 WHERE id=$2`, datasetVersionID, datasetID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	confidence := 0.95
	binding, err := store.CreateAssetTagBinding(ctx, tenantID, actorID, CreateAssetTagBindingInput{
		TagID: tag.ID, AssetType: "DATASET_VERSION",
		DatasetID: datasetID, DatasetVersionID: datasetVersionID,
		Origin: "LLM", Status: "APPROVED", Confidence: &confidence,
		Evidence: []byte(`{"reason":"table function"}`),
	})
	if err != nil || binding.ApprovedBy != actorID || binding.ApprovedAt == nil ||
		binding.RecordVersion == "" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	rejected, err := store.UpdateAssetTagBinding(ctx, tenantID, actorID, binding.ID, UpdateAssetTagBindingInput{
		ExpectedRecordVersion: binding.RecordVersion,
		Origin:                "USER", Status: "REJECTED", Evidence: []byte(`{"reason":"reviewed"}`),
	})
	if err != nil || rejected.ApprovedBy != "" || rejected.ApprovedAt != nil ||
		rejected.RecordVersion == binding.RecordVersion {
		t.Fatalf("rejected binding=%+v err=%v", rejected, err)
	}
	if _, err := store.UpdateAssetTagBinding(ctx, tenantID, actorID, binding.ID, UpdateAssetTagBindingInput{
		ExpectedRecordVersion: binding.RecordVersion,
		Origin:                "USER", Status: "REJECTED", Evidence: []byte(`{}`),
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale binding update error=%v", err)
	}
	bindings, bindingTotal, err := store.ListAssetTagBindings(ctx, tenantID, BindingFilter{
		Page: Page{Limit: 50}, TagID: tag.ID, AssetType: "DATASET_VERSION", Status: "REJECTED",
	})
	if err != nil || bindingTotal != 1 || len(bindings) != 1 {
		t.Fatalf("bindings=%+v total=%d err=%v", bindings, bindingTotal, err)
	}
	if err := store.DeleteAssetTagBinding(ctx, tenantID, actorID, binding.ID, rejected.RecordVersion); err != nil {
		t.Fatal(err)
	}

	var auditCount, outboxCount int
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM platform.audit_logs
			WHERE resource_type IN ('SEMANTIC_TAG','SEMANTIC_TAG_ALIAS','ASSET_TAG_BINDING')`).Scan(&auditCount); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*)::int FROM platform.semantic_change_outbox
			WHERE subject_type='TAG'`).Scan(&outboxCount)
	}); err != nil {
		t.Fatal(err)
	}
	if auditCount < 10 || outboxCount < 1 {
		t.Fatalf("audit=%d outbox=%d", auditCount, outboxCount)
	}
}

// exerciseUpdateTagDirectSQLGateOrder creates the historical deadlock shape
// deterministically: the API update pauses on its lexeme lock after reaching
// the tag row, while a direct SQL UPDATE takes the migration-000071 statement
// gate. Gate-first code completes in order; row-first code forms row <-> gate
// and PostgreSQL reports 40P01.
func exerciseUpdateTagDirectSQLGateOrder(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *PostgresStore,
	tenantID, actorID string,
	tag Tag,
) (Tag, error) {
	raceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	lexemeLocked := make(chan struct{})
	releaseLexeme := make(chan struct{})
	blockerResult := make(chan error, 1)
	go func() {
		blockerResult <- database.WithTenantTx(
			raceCtx, pool, tenantID,
			func(tx pgx.Tx) error {
				if _, err := tx.Exec(raceCtx, `SELECT pg_advisory_xact_lock(
					hashtextextended(
					  platform.current_tenant_id()::text||':'||lower($1),
					  0
					)
				)`, tag.Code); err != nil {
					return err
				}
				close(lexemeLocked)
				select {
				case <-releaseLexeme:
					return nil
				case <-raceCtx.Done():
					return raceCtx.Err()
				}
			},
		)
	}()
	select {
	case <-lexemeLocked:
	case <-raceCtx.Done():
		return Tag{}, fmt.Errorf("acquire lexeme blocker: %w", raceCtx.Err())
	}

	type updateResult struct {
		item Tag
		err  error
	}
	apiResult := make(chan updateResult, 1)
	go func() {
		item, err := store.UpdateTag(
			raceCtx, tenantID, actorID, tag.ID,
			UpdateTagInput{
				ExpectedVersion: tag.Version,
				ParentTagID:     tag.ParentTagID,
				Code:            tag.Code,
				Name:            tag.Name,
				Description:     "governance gate order verified",
				Category:        tag.Category,
				Governance:      tag.Governance,
				Status:          tag.Status,
			},
		)
		apiResult <- updateResult{item: item, err: err}
	}()
	if err := waitForTagRowLock(
		raceCtx, pool, tenantID, tag.ID,
	); err != nil {
		close(releaseLexeme)
		<-blockerResult
		return Tag{}, err
	}

	directResult := make(chan error, 1)
	go func() {
		directResult <- database.WithTenantTx(
			raceCtx, pool, tenantID,
			func(tx pgx.Tx) error {
				_, err := tx.Exec(raceCtx, `UPDATE platform.semantic_tags
					SET description=description WHERE id=$1::uuid`, tag.ID)
				return err
			},
		)
	}()
	if err := waitForGovernanceGateHeld(raceCtx, pool, tenantID); err != nil {
		close(releaseLexeme)
		<-blockerResult
		return Tag{}, err
	}
	close(releaseLexeme)

	if err := <-blockerResult; err != nil {
		return Tag{}, fmt.Errorf("release lexeme blocker: %w", err)
	}
	api := <-apiResult
	directErr := <-directResult
	if postgresErrorCode(api.err) == "40P01" ||
		postgresErrorCode(directErr) == "40P01" {
		return Tag{}, fmt.Errorf(
			"semantic governance gate deadlocked: api=%v direct=%v",
			api.err, directErr,
		)
	}
	if api.err != nil {
		return Tag{}, fmt.Errorf("update tag after gate barrier: %w", api.err)
	}
	if directErr != nil {
		return Tag{}, fmt.Errorf("direct tag update after gate barrier: %w", directErr)
	}
	return api.item, nil
}

var errDirectSemanticProbeRollback = errors.New(
	"rollback direct semantic governance probe",
)

func exerciseUpdateTagAliasDirectSQLGateOrder(
	ctx context.Context,
	pool *pgxpool.Pool,
	store *PostgresStore,
	tenantID, actorID string,
	alias TagAlias,
) (TagAlias, error) {
	raceCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	lexemeLocked := make(chan struct{})
	releaseLexeme := make(chan struct{})
	blockerResult := make(chan error, 1)
	go func() {
		blockerResult <- database.WithTenantTx(
			raceCtx, pool, tenantID,
			func(tx pgx.Tx) error {
				if _, err := tx.Exec(raceCtx, `SELECT pg_advisory_xact_lock(
					hashtextextended(
					  platform.current_tenant_id()::text||':'||lower($1),
					  0
					)
				)`, alias.Alias); err != nil {
					return err
				}
				close(lexemeLocked)
				select {
				case <-releaseLexeme:
					return nil
				case <-raceCtx.Done():
					return raceCtx.Err()
				}
			},
		)
	}()
	select {
	case <-lexemeLocked:
	case <-raceCtx.Done():
		return TagAlias{}, fmt.Errorf(
			"acquire alias lexeme blocker: %w", raceCtx.Err(),
		)
	}

	type updateResult struct {
		item TagAlias
		err  error
	}
	apiResult := make(chan updateResult, 1)
	go func() {
		item, err := store.UpdateTagAlias(
			raceCtx, tenantID, actorID, alias.ID,
			UpdateTagAliasInput{
				ExpectedRecordVersion: alias.RecordVersion,
				TagID:                 alias.TagID,
				Alias:                 alias.Alias,
				AliasType:             alias.AliasType,
				LanguageCode:          alias.LanguageCode,
			},
		)
		apiResult <- updateResult{item: item, err: err}
	}()
	if err := waitForTagAliasRowLock(
		raceCtx, pool, tenantID, alias.ID,
	); err != nil {
		close(releaseLexeme)
		<-blockerResult
		return TagAlias{}, err
	}

	directResult := make(chan error, 1)
	go func() {
		directResult <- database.WithTenantTx(
			raceCtx, pool, tenantID,
			func(tx pgx.Tx) error {
				if _, err := tx.Exec(raceCtx, `UPDATE platform.semantic_tag_aliases
					SET language_code=language_code WHERE id=$1::uuid`,
					alias.ID,
				); err != nil {
					return err
				}
				return errDirectSemanticProbeRollback
			},
		)
	}()
	if err := waitForGovernanceGateHeld(raceCtx, pool, tenantID); err != nil {
		close(releaseLexeme)
		<-blockerResult
		return TagAlias{}, err
	}
	close(releaseLexeme)

	if err := <-blockerResult; err != nil {
		return TagAlias{}, fmt.Errorf("release alias lexeme blocker: %w", err)
	}
	api := <-apiResult
	directErr := <-directResult
	if postgresErrorCode(api.err) == "40P01" ||
		postgresErrorCode(directErr) == "40P01" {
		return TagAlias{}, fmt.Errorf(
			"semantic alias governance gate deadlocked: api=%v direct=%v",
			api.err, directErr,
		)
	}
	if api.err != nil {
		return TagAlias{}, fmt.Errorf(
			"update tag alias after gate barrier: %w", api.err,
		)
	}
	if !errors.Is(directErr, errDirectSemanticProbeRollback) {
		return TagAlias{}, fmt.Errorf(
			"direct tag alias gate probe did not roll back: %w", directErr,
		)
	}
	return api.item, nil
}

func waitForTagRowLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, tagID string,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT 1 FROM platform.semantic_tags
				WHERE id=$1::uuid FOR UPDATE NOWAIT`, tagID)
			return err
		})
		if postgresErrorCode(err) == "55P03" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("probe tag row lock: %w", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("wait for tag row lock: %w", ctx.Err())
		}
	}
}

func waitForTagAliasRowLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID, aliasID string,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `SELECT 1
				FROM platform.semantic_tag_aliases
				WHERE id=$1::uuid FOR UPDATE NOWAIT`, aliasID)
			return err
		})
		if postgresErrorCode(err) == "55P03" {
			return nil
		}
		if err != nil {
			return fmt.Errorf("probe tag alias row lock: %w", err)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("wait for tag alias row lock: %w", ctx.Err())
		}
	}
}

func waitForGovernanceGateHeld(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID string,
) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		acquired := false
		err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(
				hashtextextended(
				  'semantic-governance-write:'||
				    platform.current_tenant_id()::text,
				  0
				)
			)`).Scan(&acquired)
		})
		if err != nil {
			return fmt.Errorf("probe semantic governance gate: %w", err)
		}
		if !acquired {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return fmt.Errorf("wait for semantic governance gate: %w", ctx.Err())
		}
	}
}
