//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	assetpkg "intelligent-report-generation-system/internal/asset"
	"intelligent-report-generation-system/internal/metadataai"
	"intelligent-report-generation-system/internal/platform/database"
)

type metadataProvider struct {
	output       metadataai.CompletionOutput
	beforeReturn func() error
}

func (metadataProvider) Name() string     { return "integration" }
func (metadataProvider) Model() string    { return "metadata-test-v1" }
func (metadataProvider) Configured() bool { return true }
func (p metadataProvider) Complete(context.Context, string, string, metadataai.CompletionInput) (metadataai.ProviderResult, error) {
	if p.beforeReturn != nil {
		if err := p.beforeReturn(); err != nil {
			return metadataai.ProviderResult{}, err
		}
	}
	return metadataai.ProviderResult{Output: p.output, Model: p.Model(), ModelVersion: "2026-07-15", Usage: metadataai.Usage{PromptTokens: 10, CompletionTokens: 20, TotalTokens: 30}}, nil
}

func TestMetadataAICompletionAppliesOnlySafeSuggestions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "metadata-ai-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "metadata-ai-foreign-"+suffix)
	t.Cleanup(func() { cleanupTenant(pool, tenantID); cleanupTenant(pool, foreignTenantID) })

	var actorID, sourceID, tableID, highColumnID, lowColumnID, lockedColumnID string
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO platform.users(tenant_id,email,display_name,password_hash) VALUES($1,'metadata-ai@it.test','metadata ai','integration-hash') RETURNING id`, tenantID).Scan(&actorID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(tenant_id,code,name,source_type,secret_ref) VALUES($1,'metadata-ai','Metadata AI','MYSQL','env://METADATA_AI') RETURNING id`, tenantID).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at) VALUES($1,$2,'sales','orders','TABLE',repeat('a',64),now()) RETURNING id`, tenantID, sourceID).Scan(&tableID); err != nil {
			return err
		}
		columns := []struct {
			name   string
			locked bool
			id     *string
		}{{"order_id", false, &highColumnID}, {"amount", false, &lowColumnID}, {"manual_note", true, &lockedColumnID}}
		for position, column := range columns {
			if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_columns(tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,structure_hash,last_sync_at,manual_locked,business_description)
				VALUES($1,$2,$3,$4,'varchar','TEXT',false,$3||'-hash',now(),$5,CASE WHEN $5 THEN '人工说明' ELSE '' END) RETURNING id`, tenantID, tableID, column.name, position+1, column.locked).Scan(column.id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	output := metadataai.CompletionOutput{
		SchemaVersion: metadataai.SchemaVersion,
		Table:         metadataai.SuggestionValue{TargetID: tableID, BusinessName: "订单", BusinessDescription: "订单事实表", Tags: []string{"领域:运营", "作用:事实表"}, SensitivityLevel: "INTERNAL", Confidence: 0.95},
		Columns: []metadataai.SuggestionValue{
			{TargetID: highColumnID, BusinessName: "订单编号", BusinessDescription: "订单唯一编号", Tags: []string{"作用:主数据"}, SensitivityLevel: "INTERNAL", SemanticType: "IDENTIFIER", Confidence: 0.95},
			{TargetID: lowColumnID, BusinessName: "订单金额", BusinessDescription: "订单金额", Tags: []string{"主题:经营分析"}, SensitivityLevel: "CONFIDENTIAL", SemanticType: "AMOUNT", Confidence: 0.6},
			{TargetID: lockedColumnID, BusinessName: "模型说明", BusinessDescription: "模型不得覆盖的说明", Tags: []string{"作用:辅助信息"}, SensitivityLevel: "INTERNAL", SemanticType: "TEXT", Confidence: 0.99},
		},
	}
	service := metadataai.NewService(metadataai.NewPostgresStore(pool), metadataProvider{output: output}, 5*time.Second, 0.8)
	result, err := service.Generate(ctx, tenantID, actorID, tableID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Status != "SUCCEEDED" || result.Job.TotalTokens != 30 || len(result.Suggestions) != 4 {
		t.Fatalf("result=%#v", result)
	}
	statuses := map[string]metadataai.Suggestion{}
	for _, suggestion := range result.Suggestions {
		statuses[suggestion.TargetID] = suggestion
	}
	if statuses[tableID].Status != "APPLIED" || statuses[highColumnID].Status != "APPLIED" {
		t.Fatalf("high confidence suggestions not applied: %#v", statuses)
	}
	if statuses[lowColumnID].PendingReason != "LOW_CONFIDENCE" || statuses[lockedColumnID].PendingReason != "MANUAL_LOCKED" {
		t.Fatalf("pending reasons=%#v", statuses)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var tableName, highDescription, lowDescription, lockedDescription, structureHash, enrichedHash, jobStructureHash string
		if err := tx.QueryRow(ctx, `SELECT business_name FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&tableName); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT business_description FROM platform.metadata_columns WHERE id=$1`, highColumnID).Scan(&highDescription); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT business_description FROM platform.metadata_columns WHERE id=$1`, lowColumnID).Scan(&lowDescription); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT business_description FROM platform.metadata_columns WHERE id=$1`, lockedColumnID).Scan(&lockedDescription); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT structure_hash,last_enriched_structure_hash FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&structureHash, &enrichedHash); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT metadata_structure_hash FROM platform.ai_metadata_jobs WHERE id=$1`, result.Job.ID).Scan(&jobStructureHash); err != nil {
			return err
		}
		if tableName != "订单" || highDescription != "订单唯一编号" || lowDescription != "" || lockedDescription != "人工说明" {
			return fmt.Errorf("unexpected formal metadata: %q %q %q %q", tableName, highDescription, lowDescription, lockedDescription)
		}
		if structureHash == "" || enrichedHash != structureHash || jobStructureHash != structureHash {
			return fmt.Errorf("metadata structure fence mismatch: current=%q enriched=%q job=%q", structureHash, enrichedHash, jobStructureHash)
		}
		var auditCount int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.audit_logs WHERE resource_type='AI_METADATA_JOB' AND resource_id=$1`, result.Job.ID).Scan(&auditCount); err != nil {
			return err
		}
		if auditCount != 2 {
			return fmt.Errorf("AI job audit count=%d", auditCount)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	accepted, err := service.DecideSuggestion(ctx, tenantID, actorID, statuses[lowColumnID].ID, "ACCEPT")
	if err != nil || accepted.Status != "ACCEPTED" {
		t.Fatalf("accept=%#v err=%v", accepted, err)
	}
	rejected, err := service.DecideSuggestion(ctx, tenantID, actorID, statuses[lockedColumnID].ID, "REJECT")
	if err != nil || rejected.Status != "REJECTED" {
		t.Fatalf("reject=%#v err=%v", rejected, err)
	}

	// 模型调用期间技术结构发生变化时，旧结果必须整体回滚且不能推进“已完善”结构标记。
	driftService := metadataai.NewService(metadataai.NewPostgresStore(pool), metadataProvider{
		output: output,
		beforeReturn: func() error {
			return database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE platform.metadata_tables SET structure_hash=repeat('b',64) WHERE id=$1`, tableID)
				return err
			})
		},
	}, 5*time.Second, 0.8)
	driftResult, err := driftService.Generate(ctx, tenantID, actorID, tableID)
	if !errors.Is(err, metadataai.ErrStructureChanged) || driftResult.Job.Status != "FAILED" || driftResult.Job.ErrorCode != "STRUCTURE_CHANGED" {
		t.Fatalf("drift result=%#v err=%v", driftResult, err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var suggestionCount int
		var structureHash, enrichedHash string
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.ai_metadata_suggestions WHERE job_id=$1`, driftResult.Job.ID).Scan(&suggestionCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT structure_hash,last_enriched_structure_hash FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&structureHash, &enrichedHash); err != nil {
			return err
		}
		if suggestionCount != 0 || structureHash == enrichedHash {
			return fmt.Errorf("stale AI result crossed structure fence: suggestions=%d current=%q enriched=%q", suggestionCount, structureHash, enrichedHash)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assets, total, err := assetpkg.NewRepository(pool).SearchTables(ctx, tenantID, assetpkg.Search{DataSourceID: sourceID, EnrichedOnly: true, Limit: 20})
	if err != nil || total != 0 || len(assets) != 0 {
		t.Fatalf("stale enrichment leaked into current-structure asset list: total=%d items=%#v err=%v", total, assets, err)
	}

	invalidOutput := output
	invalidOutput.Columns = append([]metadataai.SuggestionValue(nil), output.Columns...)
	invalidOutput.Columns[0].TargetID = "550e8400-e29b-41d4-a716-446655440099"
	invalidService := metadataai.NewService(metadataai.NewPostgresStore(pool), metadataProvider{output: invalidOutput}, 5*time.Second, 0.8)
	invalidResult, err := invalidService.Generate(ctx, tenantID, actorID, tableID)
	if !errors.Is(err, metadataai.ErrInvalidOutput) || invalidResult.Job.Status != "FAILED" || invalidResult.Job.ErrorCode != "INVALID_OUTPUT" {
		t.Fatalf("invalid result=%#v err=%v", invalidResult, err)
	}
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var suggestionCount int
		var tableName string
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.ai_metadata_suggestions WHERE job_id=$1`, invalidResult.Job.ID).Scan(&suggestionCount); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT business_name FROM platform.metadata_tables WHERE id=$1`, tableID).Scan(&tableName); err != nil {
			return err
		}
		if suggestionCount != 0 || tableName != "订单" {
			return fmt.Errorf("invalid output polluted assets: suggestions=%d tableName=%q", suggestionCount, tableName)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := service.ListSuggestions(ctx, foreignTenantID, result.Job.ID, "", 100)
	if err != nil || len(foreign) != 0 {
		t.Fatalf("cross-tenant suggestions leaked: %#v err=%v", foreign, err)
	}
}
