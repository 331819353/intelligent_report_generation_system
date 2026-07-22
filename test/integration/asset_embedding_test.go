//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/assetembedding"
	"intelligent-report-generation-system/internal/platform/database"
)

func TestAssetEmbeddingRetrievalUsesEmptyScopeAndTenantRLS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, env("DATABASE_URL", "postgres://report_app:local_report_password@127.0.0.1:5432/intelligent_report?sslmode=disable"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenantID := insertTenant(t, ctx, pool, "asset-embedding-it-"+suffix)
	foreignTenantID := insertTenant(t, ctx, pool, "asset-embedding-foreign-"+suffix)
	t.Cleanup(func() { cleanupTenant(pool, tenantID); cleanupTenant(pool, foreignTenantID) })

	var tableID string
	if err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var sourceID string
		if err := tx.QueryRow(ctx, `INSERT INTO platform.data_sources(
			tenant_id,code,name,source_type,status,config,secret_ref
		) VALUES($1,$2,'Asset Retrieval','MYSQL','ACTIVE','{}','env://ASSET_IT') RETURNING id::text`,
			tenantID, "asset-source-"+suffix).Scan(&sourceID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO platform.metadata_tables(
			tenant_id,data_source_id,schema_name,table_name,table_type,structure_hash,last_sync_at,
			business_name,business_description,tags,table_structure_hash,last_enriched_table_structure_hash,
			last_enriched_structure_hash
		) VALUES($1,$2,'sales','orders','TABLE',$3,now(),'销售订单','订单事实',ARRAY['销售','订单'],$4,$4,$3)
		RETURNING id::text`, tenantID, sourceID, repeatHex('a'), repeatHex('b')).Scan(&tableID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.metadata_columns(
			tenant_id,table_id,column_name,ordinal_position,native_type,canonical_type,nullable,
			structure_hash,last_sync_at,business_name,business_description,tags,semantic_type,last_enriched_structure_hash
		) VALUES($1,$2,'amount',1,'decimal(18,2)','DECIMAL',false,$3,now(),
			'销售额','订单销售金额',ARRAY['销售','金额'],'AMOUNT',$3)`, tenantID, tableID, repeatHex('c')); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO platform.asset_embeddings(
			tenant_id,asset_type,asset_id,table_id,document_version,document,input_hash,embedding,
			embedding_model,model_version,status,embedded_at
		) VALUES($1,'TABLE',$2,$2,'asset-search-document-v1','销售订单 订单事实 销售 金额',$3,$4::halfvec,
			'test-model','test-model','SUCCEEDED',now())
		ON CONFLICT(tenant_id,asset_type,asset_id) DO UPDATE SET
			document=EXCLUDED.document,input_hash=EXCLUDED.input_hash,embedding=EXCLUDED.embedding,
			embedding_model=EXCLUDED.embedding_model,model_version=EXCLUDED.model_version,
			status='SUCCEEDED',embedded_at=now(),error_code=''`, tenantID, tableID, repeatHex('d'), vectorLiteral(2560))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	store := assetembedding.NewPostgresStore(pool)
	vector := make([]float32, 2560)
	vector[0] = 1
	vectorRanks, err := store.VectorRanks(ctx, tenantID, "TABLE", nil, vector, 10)
	if err != nil || len(vectorRanks) != 1 || vectorRanks[0].AssetID != tableID {
		t.Fatalf("VectorRanks() ranks=%#v err=%v", vectorRanks, err)
	}
	lexicalRanks, err := store.LexicalRanks(ctx, tenantID, "TABLE", nil, "销售订单", []string{"销售", "订单"}, 10)
	if err != nil || len(lexicalRanks) != 1 || lexicalRanks[0].AssetID != tableID {
		t.Fatalf("LexicalRanks() ranks=%#v err=%v", lexicalRanks, err)
	}
	foreignRanks, err := store.VectorRanks(ctx, foreignTenantID, "TABLE", nil, vector, 10)
	if err != nil || len(foreignRanks) != 0 {
		t.Fatalf("cross-tenant VectorRanks() ranks=%#v err=%v", foreignRanks, err)
	}
}

func repeatHex(value byte) string {
	result := make([]byte, 64)
	for index := range result {
		result[index] = value
	}
	return string(result)
}

func vectorLiteral(dimensions int) string {
	result := make([]byte, 0, dimensions*2+1)
	result = append(result, '[', '1')
	for index := 1; index < dimensions; index++ {
		result = append(result, ',', '0')
	}
	return string(append(result, ']'))
}
