package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) SaveImportedDraft(ctx context.Context, draft ImportedDraft) error {
	if store == nil || store.pool == nil {
		return errors.New("semantic registry PostgreSQL store is not configured")
	}
	model := draft.SemanticModel
	if model.Status != VersionStatusDraft {
		return errors.New("imported semantic model must be DRAFT")
	}
	if err := model.Validate(); err != nil {
		return err
	}
	for _, measure := range draft.Measures {
		if measure.Status != VersionStatusDraft || measure.TenantID != model.TenantID ||
			measure.DomainID != model.DomainID || measure.SemanticModelVersionID != model.ID {
			return errors.New("imported measure crosses model scope or is not DRAFT")
		}
		if err := measure.Validate(); err != nil {
			return err
		}
	}
	for _, dimension := range draft.Dimensions {
		if dimension.Status != VersionStatusDraft || dimension.TenantID != model.TenantID ||
			dimension.DomainID != model.DomainID || dimension.SemanticModelVersionID != model.ID {
			return errors.New("imported dimension crosses model scope or is not DRAFT")
		}
		if err := dimension.Validate(); err != nil {
			return err
		}
	}
	return database.WithTenantTx(ctx, store.pool, model.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.domains(
			id,tenant_id,code,name,description,owner_id
		) SELECT domain.id,domain.tenant_id,domain.code,domain.name,domain.description,$3
		FROM platform.business_domains AS domain
		WHERE domain.id=$1 AND domain.tenant_id=$2 AND domain.status='ACTIVE'
		  AND domain.deleted_at IS NULL
		ON CONFLICT(id) DO NOTHING`, model.DomainID, model.TenantID, model.OwnerID); err != nil {
			return err
		}
		if err := insertSemanticModelDraft(ctx, tx, model); err != nil {
			return err
		}
		for _, measure := range draft.Measures {
			if err := insertMeasureDraft(ctx, tx, measure); err != nil {
				return err
			}
		}
		for _, dimension := range draft.Dimensions {
			if err := insertDimensionDraft(ctx, tx, dimension); err != nil {
				return err
			}
		}
		return nil
	})
}

func insertSemanticModelDraft(ctx context.Context, tx pgx.Tx, model SemanticModel) error {
	tag, err := tx.Exec(ctx, `INSERT INTO askdata.semantic_models(
		id,tenant_id,domain_id,model_id,version_no,code,name,description,
		entity_version_id,dataset_id,dataset_version_id,materialization_id,
		dataset_schema_hash,layer,grain_contract,primary_time_field_id,
		status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,$10,$11,$12,$13,$14,$15,$16,'DRAFT',$17,$18)
	ON CONFLICT(tenant_id,model_id,version_no) DO NOTHING`,
		model.ID, model.TenantID, model.DomainID, model.ObjectID, model.VersionNo,
		model.Code, model.Name, model.Description, model.EntityVersionID,
		model.DatasetID, model.DatasetVersionID, model.MaterializationID,
		model.DatasetSchemaHash, model.Layer, model.GrainContract,
		model.PrimaryTimeFieldID, model.ContentHash, model.OwnerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return verifyImportedHash(ctx, tx, "semantic_models", model.ID, model.ContentHash)
}

func insertMeasureDraft(ctx context.Context, tx pgx.Tx, measure Measure) error {
	tag, err := tx.Exec(ctx, `INSERT INTO askdata.measures(
		id,tenant_id,domain_id,measure_id,version_no,semantic_model_version_id,
		code,name,description,formula_ast,aggregation,additivity,data_type,unit,
		status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'DRAFT',$15,$16)
	ON CONFLICT(tenant_id,measure_id,version_no) DO NOTHING`,
		measure.ID, measure.TenantID, measure.DomainID, measure.ObjectID,
		measure.VersionNo, measure.SemanticModelVersionID, measure.Code,
		measure.Name, measure.Description, measure.FormulaAST, measure.Aggregation,
		measure.Additivity, measure.DataType, measure.Unit, measure.ContentHash, measure.OwnerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return verifyImportedHash(ctx, tx, "measures", measure.ID, measure.ContentHash)
}

func insertDimensionDraft(ctx context.Context, tx pgx.Tx, dimension Dimension) error {
	tag, err := tx.Exec(ctx, `INSERT INTO askdata.dimensions(
		id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,
		logical_field_id,code,name,description,dimension_kind,sensitivity,
		member_index_policy,high_cardinality,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'DRAFT',$15,$16)
	ON CONFLICT(tenant_id,dimension_id,version_no) DO NOTHING`,
		dimension.ID, dimension.TenantID, dimension.DomainID, dimension.ObjectID,
		dimension.VersionNo, dimension.SemanticModelVersionID, dimension.LogicalFieldID,
		dimension.Code, dimension.Name, dimension.Description, dimension.Kind,
		dimension.Sensitivity, dimension.MemberIndexPolicy, dimension.HighCardinality,
		dimension.ContentHash, dimension.OwnerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	return verifyImportedHash(ctx, tx, "dimensions", dimension.ID, dimension.ContentHash)
}

func verifyImportedHash(ctx context.Context, tx pgx.Tx, table, id string, expected any) error {
	if table != "semantic_models" && table != "measures" && table != "dimensions" {
		return errors.New("unsupported imported table")
	}
	var actual string
	query := fmt.Sprintf("SELECT content_hash FROM askdata.%s WHERE id=$1", table)
	if err := tx.QueryRow(ctx, query, id).Scan(&actual); err != nil {
		return err
	}
	if actual != fmt.Sprint(expected) {
		return ErrRegistryConflict
	}
	return nil
}
