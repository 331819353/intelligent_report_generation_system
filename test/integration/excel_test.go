//go:build integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/xuri/excelize/v2"
	"golang.org/x/text/encoding/simplifiedchinese"
	"intelligent-report-generation-system/internal/datasource"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestExcelUploadVersionSyncAndRemovedColumn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tenantID := insertTenant(t, ctx, pool, fmt.Sprintf("excel-it-%d", time.Now().UnixNano()))
	storage, err := datasource.NewMinIOStorage(env("MINIO_ENDPOINT", "127.0.0.1:9000"), env("MINIO_ACCESS_KEY", "report_minio"), env("MINIO_SECRET_KEY", "local_minio_password"), false)
	if err != nil {
		t.Fatal(err)
	}
	repo := datasource.NewPostgresRepository(pool)
	manager := datasource.NewExcelManager(repo, storage, env("MINIO_BUCKET_UPLOADS", "uploads"))
	connector := datasource.NewExcelConnector(manager)
	service := datasource.NewService(repo, connector)
	var keys []string
	t.Cleanup(func() {
		for _, key := range keys {
			_ = storage.Delete(context.Background(), env("MINIO_BUCKET_UPLOADS", "uploads"), key)
		}
		if err := cleanupExcelTenant(pool, tenantID); err != nil {
			t.Errorf("cleanup excel tenant: %v", err)
		}
	})

	first := excelBytes(t, false)
	asset, err := manager.Upload(ctx, tenantID, "", "sales.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", bytes.NewReader(first), int64(len(first)), map[string]any{"headerRow": float64(1), "selectedSheets": []any{"Sales"}})
	if err != nil {
		t.Fatal(err)
	}
	source, err := service.Create(ctx, datasource.Source{TenantID: tenantID, Code: "excel", Name: "Excel", Type: datasource.TypeExcel, FileAssetID: asset.ID, Config: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Test(ctx, tenantID, source.ID); err != nil {
		t.Fatal(err)
	}
	result, err := service.Sync(ctx, tenantID, source.ID)
	if err != nil || result.Assets != 1 {
		t.Fatalf("sync=%#v err=%v", result, err)
	}

	second := excelBytes(t, true)
	version2, err := manager.Upload(ctx, tenantID, asset.ID, "sales.xlsx", asset.MimeType, bytes.NewReader(second), int64(len(second)), map[string]any{"headerRow": float64(1), "selectedSheets": []any{"Sales"}})
	if err != nil || version2.CurrentVersion != 2 {
		t.Fatalf("version=%#v err=%v", version2, err)
	}
	if _, err := service.Sync(ctx, tenantID, source.ID); err != nil {
		t.Fatal(err)
	}
	version3, err := manager.Upload(ctx, tenantID, asset.ID, "sales.xlsx", asset.MimeType, bytes.NewReader(first), int64(len(first)), map[string]any{"headerRow": float64(1), "selectedSheets": []any{"Sales"}})
	if err != nil || version3.CurrentVersion != 3 {
		t.Fatalf("version=%#v err=%v", version3, err)
	}
	if _, err := service.Sync(ctx, tenantID, source.ID); err != nil {
		t.Fatal(err)
	}
	versions, err := manager.Versions(ctx, tenantID, asset.ID)
	if err != nil || len(versions) != 3 || versions[0].CurrentVersion != 3 || versions[0].Version != 3 {
		t.Fatalf("versions=%#v err=%v", versions, err)
	}
	csvData, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("id;name;amount;active;date\n1;'华东;一区';12.50;true;2026-07-15\n2;'华北';8.00;false;2026-07-16\n"))
	if err != nil {
		t.Fatal(err)
	}
	csvConfig := map[string]any{"headerRow": float64(1), "csvOptions": map[string]any{"encoding": "GBK", "delimiter": "SEMICOLON", "quote": "'"}}
	csvAsset, err := manager.Upload(ctx, tenantID, "", "regions.csv", "text/csv", bytes.NewReader(csvData), int64(len(csvData)), csvConfig)
	if err != nil {
		t.Fatal(err)
	}
	csvSource, err := service.Create(ctx, datasource.Source{TenantID: tenantID, Code: "csv", Name: "CSV", Type: datasource.TypeExcel, FileAssetID: csvAsset.ID, Config: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Test(ctx, tenantID, csvSource.ID); err != nil {
		t.Fatal(err)
	}
	if result, err := service.Sync(ctx, tenantID, csvSource.ID); err != nil || result.Assets != 1 {
		t.Fatalf("csv sync=%#v err=%v", result, err)
	}

	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var versions int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.file_asset_versions WHERE file_asset_id=$1`, asset.ID).Scan(&versions); err != nil {
			return err
		}
		if versions != 3 {
			return fmt.Errorf("version count=%d", versions)
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT c.asset_status FROM platform.metadata_columns c JOIN platform.metadata_tables t ON t.id=c.table_id WHERE t.data_source_id=$1 AND c.column_name='region'`, source.ID).Scan(&status); err != nil {
			return err
		}
		if status != "INACTIVE" {
			return fmt.Errorf("removed column status=%s", status)
		}
		var csvColumns int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.metadata_columns c JOIN platform.metadata_tables t ON t.id=c.table_id WHERE t.data_source_id=$1 AND c.canonical_type IN ('NUMBER','DECIMAL','BOOLEAN','DATE')`, csvSource.ID).Scan(&csvColumns); err != nil {
			return err
		}
		if csvColumns != 4 {
			return fmt.Errorf("typed csv column count=%d", csvColumns)
		}
		rows, err := tx.Query(ctx, `SELECT storage_key FROM platform.file_asset_versions WHERE tenant_id=$1`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}

func excelBytes(t *testing.T, includeRegion bool) []byte {
	t.Helper()
	file := excelize.NewFile()
	defer file.Close()
	_ = file.SetSheetName("Sheet1", "Sales")
	headers := []any{"id", "amount", "active", "date"}
	if includeRegion {
		headers = append(headers, "region")
	}
	_ = file.SetSheetRow("Sales", "A1", &headers)
	row := []any{1, 12.5, true, "2026-07-15"}
	if includeRegion {
		row = append(row, "CN-SH")
	}
	_ = file.SetSheetRow("Sales", "A2", &row)
	var output bytes.Buffer
	if err := file.Write(&output); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func cleanupExcelTenant(pool *pgxpool.Pool, tenantID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		for _, table := range []string{"ai_metadata_suggestions", "ai_metadata_jobs", "asset_dependencies", "metadata_diffs", "metadata_snapshots", "metadata_columns", "metadata_tables", "data_sources", "file_asset_versions", "file_assets", "tenant_data_source_quotas"} {
			if _, err := tx.Exec(ctx, "DELETE FROM platform."+table+" WHERE tenant_id=$1", tenantID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `DELETE FROM platform.tenants WHERE id=$1`, tenantID)
	return err
}
