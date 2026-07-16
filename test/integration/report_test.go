//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/report"
)

func TestReportDraftPersistenceIdempotencyAndTenantIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "report-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "report-foreign-"+suffix)
	var actorID, secondActorID, createPermissionID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var roleID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,'report tester','integration-hash') RETURNING id`, tenantID, "report-"+suffix+"@it.test").Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,$2,'second report tester','integration-hash') RETURNING id`, tenantID, "report-second-"+suffix+"@it.test").Scan(&secondActorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.roles(tenant_id,code,name) VALUES($1,'report_creator','Report Creator') RETURNING id`, tenantID).Scan(&roleID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.permissions(tenant_id,code,name,resource_type,action) VALUES($1,'report.create','Create reports','REPORT','CREATE') RETURNING id`, tenantID).Scan(&createPermissionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by) VALUES($1,$2,$3,$2)`, tenantID, actorID, roleID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.user_roles(tenant_id,user_id,role_id,assigned_by) VALUES($1,$2,$3,$4)`, tenantID, secondActorID, roleID, actorID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.role_permissions(tenant_id,role_id,permission_id,granted_by) VALUES($1,$2,$3,$4)`, tenantID, roleID, createPermissionID, actorID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	definition, err := os.ReadFile("../../api/examples/report-json-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	service := report.NewService(report.NewPostgresStore(pool))
	created, err := service.Create(ctx, tenantID, actorID, "report-create-"+suffix, report.CreateInput{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if uuid.Validate(created.ID) != nil || created.Revision != 1 || created.Status != "DRAFT" {
		t.Fatalf("created=%#v", created)
	}
	if _, err := service.Create(ctx, tenantID, secondActorID, "report-create-"+suffix, report.CreateInput{Definition: definition}); !errors.Is(err, report.ErrIdempotencyConflict) {
		t.Fatalf("cross-actor Create replay error=%v, want ErrIdempotencyConflict", err)
	}
	if _, err := service.Get(ctx, foreignTenantID, actorID, created.ID); !errors.Is(err, report.ErrNotFound) {
		t.Fatalf("cross-tenant Get() error=%v, want ErrNotFound", err)
	}

	firstInput := stickyUpdateInput(t, created, 12)
	first, err := service.Update(ctx, tenantID, actorID, created.ID, "report-save-1-"+suffix, firstInput)
	if err != nil {
		t.Fatalf("first Update() error=%v", err)
	}
	if first.Revision != 2 {
		t.Fatalf("first revision=%d, want 2", first.Revision)
	}
	// 创建者只有全局 CREATE；对象级 READ/UPDATE 必须由创建事务授予，否则这里会被服务端事务重检拒绝。
	secondInput := stickyUpdateInput(t, first, 18)
	second, err := service.Update(ctx, tenantID, actorID, created.ID, "report-save-2-"+suffix, secondInput)
	if err != nil || second.Revision != 3 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	replayed, err := service.Update(ctx, tenantID, actorID, created.ID, "report-save-1-"+suffix, firstInput)
	if err != nil || replayed.Revision != 2 || replayed.DefinitionHash != first.DefinitionHash {
		t.Fatalf("idempotent replay=%#v err=%v", replayed, err)
	}
	conflictInput := stickyUpdateInput(t, first, 20)
	if _, err := service.Update(ctx, tenantID, actorID, created.ID, "report-stale-"+suffix, conflictInput); !errors.Is(err, report.ErrConflict) {
		t.Fatalf("stale Update() error=%v, want ErrConflict", err)
	}

	// 补偿操作必须精确回到被引用修订的 before/after 哈希，不能只在同一实体上执行另一项合法修改。
	referenceID := secondInput.Changes[0].ClientOperationID
	forgedUndo := compensatingStickyInput(t, second, 20, "UNDO", referenceID)
	if _, err := service.Update(ctx, tenantID, actorID, created.ID, "report-forged-undo-"+suffix, forgedUndo); !errors.Is(err, report.ErrInvalidRequest) {
		t.Fatalf("forged UNDO error=%v, want ErrInvalidRequest", err)
	}
	undoInput := compensatingStickyInput(t, second, 12, "UNDO", referenceID)
	undone, err := service.Update(ctx, tenantID, actorID, created.ID, "report-undo-"+suffix, undoInput)
	if err != nil || undone.Revision != 4 {
		t.Fatalf("undo=%#v err=%v", undone, err)
	}
	redoInput := compensatingStickyInput(t, undone, 18, "REDO", referenceID)
	redone, err := service.Update(ctx, tenantID, actorID, created.ID, "report-redo-"+suffix, redoInput)
	if err != nil || redone.Revision != 5 || redone.DefinitionHash != second.DefinitionHash {
		t.Fatalf("redo=%#v err=%v", redone, err)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var componentCount, dependencyCount, revisionCount, objectGrantCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.report_draft_component_indexes WHERE report_id=$1`, created.ID).Scan(&componentCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.report_draft_dependencies WHERE report_id=$1`, created.ID).Scan(&dependencyCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.report_revisions WHERE report_id=$1`, created.ID).Scan(&revisionCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.object_permissions WHERE object_type='REPORT' AND object_id=$1 AND subject_id=$2 AND action IN ('READ','UPDATE')`, created.ID, actorID).Scan(&objectGrantCount); err != nil {
			return err
		}
		if componentCount == 0 || dependencyCount == 0 || revisionCount != 5 || objectGrantCount != 2 {
			return fmt.Errorf("derived/audit state components=%d dependencies=%d revisions=%d grants=%d", componentCount, dependencyCount, revisionCount, objectGrantCount)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// 活跃占用必须在与版本更新相同的事务中阻止保存。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO platform.report_edit_guards(tenant_id,report_id,holder_type,holder_id,expires_at) VALUES($1,$2,'PUBLISH_TASK','integration-guard',now()+interval '5 minutes')`, tenantID, created.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	occupiedInput := stickyUpdateInput(t, redone, 22)
	if _, err := service.Update(ctx, tenantID, actorID, created.ID, "report-occupied-"+suffix, occupiedInput); !errors.Is(err, report.ErrResourceOccupied) {
		t.Fatalf("occupied Update() error=%v, want ErrResourceOccupied", err)
	}

	// 即使幂等响应已经存在，权限撤销后也不能借重放读取完整旧草稿。
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM platform.object_permissions WHERE object_type='REPORT' AND object_id=$1 AND subject_id=$2 AND action IN ('READ','UPDATE')`, created.ID, actorID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM platform.role_permissions WHERE permission_id=$1`, createPermissionID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(ctx, tenantID, actorID, created.ID, "report-save-2-"+suffix, secondInput); !errors.Is(err, report.ErrForbidden) {
		t.Fatalf("revoked Update replay error=%v, want ErrForbidden", err)
	}
	if _, err := service.Get(ctx, tenantID, actorID, created.ID); !errors.Is(err, report.ErrForbidden) {
		t.Fatalf("revoked Get error=%v, want ErrForbidden", err)
	}
	if _, _, err := service.ListRevisions(ctx, tenantID, actorID, created.ID, 50, 0); !errors.Is(err, report.ErrForbidden) {
		t.Fatalf("revoked ListRevisions error=%v, want ErrForbidden", err)
	}
	if _, err := service.Create(ctx, tenantID, actorID, "report-create-"+suffix, report.CreateInput{Definition: definition}); !errors.Is(err, report.ErrForbidden) {
		t.Fatalf("revoked Create replay error=%v, want ErrForbidden", err)
	}
}

func stickyUpdateInput(t *testing.T, record report.DraftRecord, top int) report.UpdateInput {
	t.Helper()
	var definition map[string]any
	if err := json.Unmarshal(record.Definition, &definition); err != nil {
		t.Fatal(err)
	}
	component := definition["pages"].([]any)[0].(map[string]any)["blocks"].([]any)[0].(map[string]any)["components"].([]any)[1].(map[string]any)
	component["sticky"].(map[string]any)["top"] = float64(top)
	updated, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	value, _ := json.Marshal(top)
	return report.UpdateInput{
		ExpectedRevision: record.Revision,
		Definition:       updated,
		EditorState:      record.EditorState,
		Changes: []report.DraftChange{{
			ClientOperationID: uuid.NewString(), OperationType: "COMPONENT_STICKY_UPDATE", Source: "USER",
			Target: report.ChangeTarget{PageID: "page_overview", BlockID: "block_overview", ComponentID: "filter_stat_month"},
			Patch:  []report.PatchOperation{{Op: "replace", Path: "/pages/0/blocks/0/components/1/sticky/top", Value: value}},
		}},
	}
}

func compensatingStickyInput(t *testing.T, record report.DraftRecord, top int, operationType, referencedOperationID string) report.UpdateInput {
	t.Helper()
	input := stickyUpdateInput(t, record, top)
	input.Changes[0].OperationType = operationType
	input.Changes[0].Target.ReferencedOperationID = referencedOperationID
	return input
}
