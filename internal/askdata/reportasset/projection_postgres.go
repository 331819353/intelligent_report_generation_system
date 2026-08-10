package reportasset

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/platform/database"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/compiler"
)

type PostgresProjectionRuntimeStore struct{ pool *pgxpool.Pool }

func NewPostgresProjectionRuntimeStore(pool *pgxpool.Pool) *PostgresProjectionRuntimeStore {
	return &PostgresProjectionRuntimeStore{pool: pool}
}
func (store *PostgresProjectionRuntimeStore) TenantIDs(ctx context.Context) ([]string, error) {
	if store == nil || store.pool == nil {
		return nil, ErrAssetIneligible
	}
	rows, err := store.pool.Query(ctx, `SELECT tenant_id::text FROM askdata.list_report_asset_projection_tenants()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (store *PostgresProjectionRuntimeStore) ClaimExtraction(ctx context.Context, tenantID string, lease time.Duration) (*ExtractionClaim, error) {
	if uuid.Validate(tenantID) != nil || lease < 30*time.Second || lease > 10*time.Minute {
		return nil, ErrAssetIneligible
	}
	var claim *ExtractionClaim
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH picked AS(SELECT id FROM askdata.report_asset_extraction_outbox WHERE attempt<10 AND ((state IN('PENDING','FAILED') AND next_attempt_at<=now()) OR(state='RUNNING' AND lease_expires_at<=now())) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1),claimed AS(UPDATE askdata.report_asset_extraction_outbox work SET state='RUNNING',attempt=attempt+1,lease_token=gen_random_uuid(),lease_expires_at=now()+($1*interval '1 second'),updated_at=now() FROM picked WHERE work.id=picked.id RETURNING work.*)SELECT id::text,tenant_id::text,report_id::text,report_version_id::text,lease_token::text,attempt FROM claimed`, int64(lease/time.Second))
		var value ExtractionClaim
		if err := row.Scan(&value.ID, &value.TenantID, &value.ReportID, &value.ReportVersionID, &value.LeaseToken, &value.Attempt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		claim = &value
		return nil
	})
	return claim, err
}

func (store *PostgresProjectionRuntimeStore) Extract(ctx context.Context, claim ExtractionClaim) error {
	return database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		var raw []byte
		var domainID, reportTitle, description string
		err := tx.QueryRow(ctx, `SELECT version.definition_json,COALESCE(report.domain_id::text,''),report.name,version.definition_json->'metadata'->>'description' FROM platform.report_versions version JOIN platform.reports report ON report.id=version.report_id AND report.tenant_id=version.tenant_id WHERE version.id=$1 AND version.report_id=$2 AND version.artifact_state='READY'`, claim.ReportVersionID, claim.ReportID).Scan(&raw, &domainID, &reportTitle, &description)
		if err != nil {
			return err
		}
		if domainID == "" {
			return nil
		}
		var definition reportmodel.ReportDefinition
		if json.Unmarshal(raw, &definition) != nil || definition.Validate() != nil {
			return ErrAssetIneligible
		}
		indexes := compiler.BuildIndexes(definition)
		location := map[askdata.ID]compiler.ComponentIndex{}
		for _, item := range indexes.Components {
			if _, exists := location[item.ComponentID]; !exists {
				location[item.ComponentID] = item
			}
		}
		for _, component := range definition.Components {
			if component.DataBinding == nil || component.DataBinding.BindingMode != reportmodel.BindingSemanticIR || component.DataBinding.SemanticQueryRef == nil {
				continue
			}
			reference := component.DataBinding.SemanticQueryRef
			normalized, canonical, irHash, err := ircontract.Canonicalize(reference.SemanticIR)
			if err != nil || irHash == "" || normalized.SemanticReleaseID != reference.SemanticReleaseID || normalized.SemanticContentHash != reference.SemanticContentHash {
				return ErrAssetIneligible
			}
			componentRaw, _ := json.Marshal(component)
			componentHash := askdata.HashBytes(componentRaw)
			metrics, dimensions, members := semanticObjectIDs(Candidate{SemanticIR: normalized})
			metricStrings, dimensionStrings, memberStrings := idStrings(metrics), idStrings(dimensions), idStrings(members)
			position := location[component.ID]
			assetID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("report-semantic-asset-v1\x00"+string(claim.TenantID)+"\x00"+string(claim.ReportVersionID)+"\x00"+string(component.ID))).String()
			_, err = tx.Exec(ctx, `INSERT INTO askdata.report_semantic_assets(id,tenant_id,domain_id,report_id,report_version_id,page_id,section_id,block_id,component_id,semantic_release_id,semantic_release_content_hash,semantic_ir_json,semantic_ir_hash,query_plan_hash,metric_version_ids,dimension_version_ids,member_version_ids,chart_type,chart_version,narrative_role,component_content_hash,state,sensitivity,report_title,report_description,section_purpose,block_title,contains_uncertified_free_text,projection_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15::uuid[],$16::uuid[],$17::uuid[],$18,$19,NULLIF($20,''),$21,'PENDING','INTERNAL',$22,$23,$24,$25,$26,'PENDING') ON CONFLICT(report_version_id,component_id) DO NOTHING`, assetID, claim.TenantID, domainID, claim.ReportID, claim.ReportVersionID, position.PageID, position.SectionID, position.BlockID, component.ID, reference.SemanticReleaseID, reference.SemanticContentHash, canonical, irHash, reference.QueryPlanHash, metricStrings, dimensionStrings, memberStrings, component.TemplateRef.Type, component.TemplateRef.Version, component.Options.InsightRole, componentHash, reportTitle, description, "", component.Options.Title, strings.TrimSpace(component.Options.RichText) != "")
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *PostgresProjectionRuntimeStore) FinishExtraction(ctx context.Context, claim ExtractionClaim, runErr error) error {
	return database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		state, code := "DONE", ""
		if runErr != nil {
			state, code = "FAILED", "REPORT_ASSET_EXTRACTION_FAILED"
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.report_asset_extraction_outbox SET state=$1,error_code=$2,next_attempt_at=CASE WHEN $1='FAILED' THEN now()+(LEAST(300,power(2,attempt)::integer)*interval '1 second') ELSE next_attempt_at END,lease_token=NULL,lease_expires_at=NULL,updated_at=now() WHERE id=$3 AND state='RUNNING' AND lease_token=$4`, state, code, claim.ID, claim.LeaseToken)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrAssetIneligible)
		}
		return nil
	})
}

func (store *PostgresProjectionRuntimeStore) ClaimProjection(ctx context.Context, tenantID string, lease time.Duration) (*AssetProjectionClaim, error) {
	if uuid.Validate(tenantID) != nil || lease < 30*time.Second || lease > 10*time.Minute {
		return nil, ErrAssetIneligible
	}
	var claim *AssetProjectionClaim
	err := database.WithTenantTx(ctx, store.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `WITH picked AS(SELECT id FROM askdata.report_asset_projection_outbox WHERE attempt<10 AND ((state IN('PENDING','FAILED') AND next_attempt_at<=now())OR(state='RUNNING' AND lease_expires_at<=now())) ORDER BY next_attempt_at,id FOR UPDATE SKIP LOCKED LIMIT 1),claimed AS(UPDATE askdata.report_asset_projection_outbox work SET state='RUNNING',attempt=attempt+1,lease_token=gen_random_uuid(),lease_expires_at=now()+($1*interval '1 second'),updated_at=now() FROM picked WHERE work.id=picked.id RETURNING work.*)SELECT id::text,tenant_id::text,report_semantic_asset_id::text,operation,component_content_hash,lease_token::text,attempt FROM claimed`, int64(lease/time.Second))
		var value AssetProjectionClaim
		if err := row.Scan(&value.ID, &value.TenantID, &value.AssetID, &value.Operation, &value.ContentHash, &value.LeaseToken, &value.Attempt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		claim = &value
		return nil
	})
	return claim, err
}

func (store *PostgresProjectionRuntimeStore) LoadProjection(ctx context.Context, claim AssetProjectionClaim) (Candidate, error) {
	var result Candidate
	err := database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		var raw []byte
		var metricIDs, dimensionIDs, memberIDs []string
		var sensitivity string
		err := tx.QueryRow(ctx, `SELECT asset.id::text,asset.tenant_id::text,asset.domain_id::text,asset.report_id::text,asset.report_version_id::text,COALESCE(asset.page_id,''),COALESCE(asset.section_id,''),COALESCE(asset.block_id,''),asset.component_id,asset.semantic_ir_json,asset.semantic_ir_hash,asset.query_plan_hash,asset.component_content_hash,asset.contains_uncertified_free_text,asset.report_title,asset.report_description,asset.section_purpose,asset.block_title,asset.chart_type,asset.chart_version,COALESCE(asset.narrative_role,''),asset.sensitivity,asset.metric_version_ids::text[],asset.dimension_version_ids::text[],asset.member_version_ids::text[],version.artifact_state='READY',version.id IS NOT NULL FROM askdata.report_semantic_assets asset JOIN platform.report_versions version ON version.id=asset.report_version_id AND version.report_id=asset.report_id AND version.tenant_id=asset.tenant_id WHERE asset.id=$1 AND asset.state='CERTIFIED' AND asset.component_content_hash=$2`, claim.AssetID, claim.ContentHash).Scan(&result.ID, &result.TenantID, &result.DomainID, &result.ReportID, &result.ReportVersionID, &result.PageID, &result.SectionID, &result.BlockID, &result.ComponentID, &raw, &result.SemanticIRHash, &result.QueryPlanHash, &result.ComponentContentHash, &result.ContainsUncertifiedFreeText, &result.ReportTitle, &result.ReportDescription, &result.SectionPurpose, &result.BlockTitle, &result.ComponentType, &result.ComponentVersion, &result.NarrativeRole, &sensitivity, &metricIDs, &dimensionIDs, &memberIDs, &result.ReportPublished, &result.ReportVersionImmutable)
		if err != nil {
			return err
		}
		result.Sensitivity = registry.Sensitivity(sensitivity)
		if json.Unmarshal(raw, &result.SemanticIR) != nil {
			return ErrAssetIneligible
		}
		rows, err := tx.Query(ctx, `SELECT 'METRIC',id::text,status FROM askdata.metric_versions WHERE tenant_id=$1 AND id=ANY($2::uuid[]) UNION ALL SELECT 'DIMENSION',id::text,status FROM askdata.dimensions WHERE tenant_id=$1 AND id=ANY($3::uuid[]) UNION ALL SELECT 'MEMBER',id::text,status FROM askdata.dimension_members WHERE tenant_id=$1 AND id=ANY($4::uuid[])`, claim.TenantID, metricIDs, dimensionIDs, memberIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item ObjectCertification
			if err := rows.Scan(&item.Kind, &item.VersionID, &item.Status); err != nil {
				return err
			}
			result.Certifications = append(result.Certifications, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		approvalRows, err := tx.Query(ctx, `SELECT approver_user_id::text,approver_role,component_content_hash FROM askdata.report_asset_certifications WHERE tenant_id=$1 AND report_semantic_asset_id=$2`, claim.TenantID, claim.AssetID)
		if err != nil {
			return err
		}
		defer approvalRows.Close()
		for approvalRows.Next() {
			var approval Approval
			if err := approvalRows.Scan(&approval.ApproverUserID, &approval.Role, &approval.ContentHash); err != nil {
				return err
			}
			result.Approvals = append(result.Approvals, approval)
		}
		return approvalRows.Err()
	})
	return result, err
}

func (store *PostgresProjectionRuntimeStore) PersistSearchProjection(ctx context.Context, claim AssetProjectionClaim, projection Projection) error {
	return database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		document := projection.SearchDocument
		documentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("report-asset-search-v1\x00"+string(claim.TenantID)+"\x00"+string(claim.AssetID))).String()
		tag, err := tx.Exec(ctx, `INSERT INTO askdata.search_documents(id,tenant_id,domain_id,object_type,object_version_id,view_type,sensitivity,index_policy,document,metadata,input_hash,embedding_status) SELECT $1,asset.tenant_id,asset.domain_id,$2,asset.id,$3,$4,$5,$6,$7,$8,'PENDING' FROM askdata.report_semantic_assets asset WHERE asset.id=$9 AND asset.state='CERTIFIED' ON CONFLICT(tenant_id,object_type,object_version_id,view_type) DO UPDATE SET sensitivity=EXCLUDED.sensitivity,index_policy=EXCLUDED.index_policy,document=EXCLUDED.document,metadata=EXCLUDED.metadata,input_hash=EXCLUDED.input_hash,updated_at=now() WHERE askdata.search_documents.input_hash IS DISTINCT FROM EXCLUDED.input_hash`, documentID, document.ObjectType, document.ViewType, document.Sensitivity, document.IndexPolicy, document.Text, document.Metadata, document.InputHash, claim.AssetID)
		if err != nil {
			return errors.Join(err, ErrAssetIneligible)
		}
		if tag.RowsAffected() == 0 {
			var current bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM askdata.search_documents WHERE tenant_id=$1 AND object_type='REPORT_ASSET' AND object_version_id=$2 AND view_type='REPORT_PRIOR' AND input_hash=$3)`, claim.TenantID, claim.AssetID, document.InputHash).Scan(&current); err != nil || !current {
				return errors.Join(err, ErrAssetIneligible)
			}
		}
		if document.IndexPolicy != "LEXICAL" {
			_, err = tx.Exec(ctx, `INSERT INTO askdata.embedding_outbox(tenant_id,domain_id,search_document_id,input_hash) SELECT tenant_id,domain_id,id,input_hash FROM askdata.search_documents WHERE id=$1 ON CONFLICT(tenant_id,search_document_id,input_hash) DO NOTHING`, documentID)
		}
		return err
	})
}
func (store *PostgresProjectionRuntimeStore) RemoveSearchProjection(ctx context.Context, claim AssetProjectionClaim) error {
	return database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM askdata.search_documents WHERE tenant_id=$1 AND object_type='REPORT_ASSET' AND object_version_id=$2`, claim.TenantID, claim.AssetID)
		return err
	})
}

func (store *PostgresProjectionRuntimeStore) LoadGraphProjection(ctx context.Context, claim AssetProjectionClaim, _ Projection) (ReportGraphProjection, error) {
	var result ReportGraphProjection
	err := database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		var modelID string
		var metricIDs, dimensionIDs, memberIDs []string
		err := tx.QueryRow(ctx, `SELECT asset.tenant_id::text,asset.domain_id::text,asset.report_id::text,asset.report_version_id::text,asset.id::text,asset.component_id,COALESCE(asset.semantic_release_content_hash,release.content_hash),asset.component_content_hash,asset.semantic_ir_json->>'modelVersionId',asset.metric_version_ids::text[],asset.dimension_version_ids::text[],asset.member_version_ids::text[] FROM askdata.report_semantic_assets asset JOIN askdata.releases release ON release.id=asset.semantic_release_id WHERE asset.id=$1`, claim.AssetID).Scan(&result.TenantID, &result.DomainID, &result.ReportID, &result.ReportVersionID, &result.AssetID, &result.ComponentID, &result.ReleaseHash, &result.ComponentContentHash, &modelID, &metricIDs, &dimensionIDs, &memberIDs)
		if err != nil {
			return err
		}
		result.Model, err = loadGraphRef(ctx, tx, "SEMANTIC_MODEL", modelID)
		if err != nil && claim.Operation == ProjectionUpsert {
			return err
		}
		for _, set := range []struct {
			kind   string
			ids    []string
			target *[]GraphObjectRef
		}{{"METRIC", metricIDs, &result.Metrics}, {"DIMENSION", dimensionIDs, &result.Dimensions}, {"MEMBER", memberIDs, &result.Members}} {
			for _, id := range set.ids {
				ref, loadErr := loadGraphRef(ctx, tx, set.kind, id)
				if loadErr != nil {
					return loadErr
				}
				*set.target = append(*set.target, ref)
			}
		}
		return nil
	})
	return result, err
}

func loadGraphRef(ctx context.Context, tx pgx.Tx, kind, id string) (GraphObjectRef, error) {
	var ref GraphObjectRef
	ref.Type, ref.VersionID = kind, askdata.ID(id)
	var err error
	switch kind {
	case "SEMANTIC_MODEL":
		err = tx.QueryRow(ctx, `SELECT semantic_model_id::text,version_no FROM askdata.semantic_models WHERE id=$1`, id).Scan(&ref.ObjectID, &ref.Version)
	case "METRIC":
		err = tx.QueryRow(ctx, `SELECT metric_id::text,version_no FROM askdata.metric_versions WHERE id=$1`, id).Scan(&ref.ObjectID, &ref.Version)
	case "DIMENSION":
		err = tx.QueryRow(ctx, `SELECT dimension_id::text,version_no FROM askdata.dimensions WHERE id=$1`, id).Scan(&ref.ObjectID, &ref.Version)
	case "MEMBER":
		err = tx.QueryRow(ctx, `SELECT member_id::text,version_no FROM askdata.dimension_members WHERE id=$1`, id).Scan(&ref.ObjectID, &ref.Version)
	default:
		err = ErrAssetIneligible
	}
	return ref, err
}

func (store *PostgresProjectionRuntimeStore) FinishProjection(ctx context.Context, claim AssetProjectionClaim, runErr error) error {
	return database.WithTenantTx(ctx, store.pool, string(claim.TenantID), func(tx pgx.Tx) error {
		state, code := "DONE", ""
		if runErr != nil {
			state, code = "FAILED", "REPORT_ASSET_PROJECTION_FAILED"
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.report_asset_projection_outbox SET state=$1,error_code=$2,next_attempt_at=CASE WHEN $1='FAILED' THEN now()+(LEAST(300,power(2,attempt)::integer)*interval '1 second') ELSE next_attempt_at END,lease_token=NULL,lease_expires_at=NULL,updated_at=now() WHERE id=$3 AND state='RUNNING' AND lease_token=$4`, state, code, claim.ID, claim.LeaseToken)
		if err != nil || tag.RowsAffected() != 1 {
			return errors.Join(err, ErrAssetIneligible)
		}
		if runErr == nil {
			projectionState := "READY"
			if claim.Operation == ProjectionRemove {
				projectionState = "REMOVED"
			}
			_, err = tx.Exec(ctx, `UPDATE askdata.report_semantic_assets SET projection_state=$1,updated_at=now() WHERE id=$2`, projectionState, claim.AssetID)
		}
		return err
	})
}

func idStrings(ids []askdata.ID) []string {
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = string(id)
	}
	sort.Strings(result)
	return result
}
