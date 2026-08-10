package workitem

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestPostgresUnifiedInboxQueryAgainstMigratedDatabase(t *testing.T) {
	adminURL := os.Getenv("ASKDATA_INTEGRATION_ADMIN_DATABASE_URL")
	appURL := os.Getenv("ASKDATA_INTEGRATION_DATABASE_URL")
	if adminURL == "" || appURL == "" {
		t.Skip("set ASKDATA_INTEGRATION_ADMIN_DATABASE_URL and ASKDATA_INTEGRATION_DATABASE_URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	appPool, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer appPool.Close()

	var tenantID, actorID, domainID string
	err = adminPool.QueryRow(ctx, `SELECT tenant.id::text,actor.id::text,domain.id::text
		FROM platform.tenants tenant
		JOIN platform.users actor ON actor.tenant_id=tenant.id AND actor.status='ACTIVE' AND actor.deleted_at IS NULL
		JOIN platform.user_roles assignment ON assignment.tenant_id=actor.tenant_id AND assignment.user_id=actor.id
		JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
		JOIN platform.business_domains domain ON domain.tenant_id=tenant.id AND domain.status='ACTIVE'
		WHERE role.code='PLATFORM_ADMIN' AND role.status='ACTIVE' AND role.deleted_at IS NULL
		ORDER BY tenant.id,actor.id,domain.id LIMIT 1`).Scan(&tenantID, &actorID, &domainID)
	if err != nil {
		t.Skipf("no active platform administrator/domain fixture: %v", err)
	}

	identity := Identity{
		TenantID: askdata.ID(tenantID),
		ActorID:  askdata.ID(actorID),
		DomainID: askdata.ID(domainID),
	}
	requestContext := database.WithAccessContext(ctx, actorID, domainID)
	page, err := NewStore(appPool).ListPage(requestContext, identity, false, "", 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if page.Total < len(page.Items) || page.UnreadTotal < 0 || page.UnreadTotal > page.Total {
		t.Fatalf("invalid page metadata: %#v", page)
	}
	if page.NextCursor != "" {
		next, err := NewStore(appPool).ListPage(requestContext, identity, false, "", 20, page.NextCursor)
		if err != nil {
			t.Fatal(err)
		}
		if next.Total != page.Total || next.UnreadTotal != page.UnreadTotal {
			t.Fatalf("page counts changed across cursor: first=%#v next=%#v", page, next)
		}
	}
	approvals, err := NewStore(appPool).ListPageKind(requestContext, identity, false, "", "APPROVAL", 200, "")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := NewStore(appPool).ListPageKind(requestContext, identity, false, "", "TASK", 200, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range approvals.Items {
		if item.Kind != "APPROVAL" {
			t.Fatalf("approval page contained %#v", item)
		}
	}
	for _, item := range tasks.Items {
		if item.Kind != "TASK" {
			t.Fatalf("task page contained %#v", item)
		}
	}
	for _, item := range append(append([]Item{}, approvals.Items...), tasks.Items...) {
		detail, err := NewStore(appPool).Detail(requestContext, identity, item.Type, item.ObjectID)
		if err != nil {
			t.Fatalf("detail %s/%s: %v", item.Type, item.ObjectID, err)
		}
		if detail.Item.ObjectID != item.ObjectID || detail.ActionContext.SourceObjectID != item.ObjectID || detail.ActionContext.ExpectedVersion == "" {
			t.Fatalf("invalid detail for %#v: %#v", item, detail)
		}
		if len(item.AllowedActions) > 0 && len(detail.ActionContext.Commands) == 0 {
			t.Fatalf("detail has no source command for %#v", item)
		}
		for _, command := range detail.ActionContext.Commands {
			if command.Href == "" || command.Method == "" {
				t.Fatalf("invalid source command for %#v: %#v", item, command)
			}
		}
	}
	if approvals.Total+tasks.Total != page.Total || approvals.UnreadTotal+tasks.UnreadTotal != page.UnreadTotal {
		t.Fatalf("kind totals do not partition inbox: all=%#v approvals=%#v tasks=%#v", page, approvals, tasks)
	}
	if _, err := NewStore(appPool).ListPageKind(requestContext, identity, false, "", "UNKNOWN", 20, ""); err != ErrInvalid {
		t.Fatalf("invalid kind error = %v", err)
	}
}
