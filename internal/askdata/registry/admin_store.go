package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/platform/database"
)

func (store *PostgresStore) ListDrafts(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	cursor string,
	limit int,
) (AdminPage, error) {
	if err := store.validateAdminRequest(ctx, scope); err != nil {
		return AdminPage{}, err
	}
	if limit < 1 || limit > 200 {
		return AdminPage{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrRegistryInvalidRequest)
	}
	position, err := decodeMetricCursor(cursor)
	if err != nil {
		return AdminPage{}, fmt.Errorf("%w: cursor is invalid", ErrRegistryInvalidRequest)
	}
	var page AdminPage
	err = database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		var listErr error
		page, listErr = listObjectsTx(ctx, tx, scope.DomainID, resource, StatusDraft, position, limit)
		return listErr
	})
	return page, normalizeAdminStoreError(err)
}

// ListObjects reads governed semantic objects at any lifecycle status. It is the
// read counterpart of ListDrafts: the draft APIs deliberately only ever see
// DRAFT rows because they feed the editing path, but inspecting an import,
// reviewing a release candidate and building the semantic workbench all need to
// read CERTIFIED and DEPRECATED objects too. Permission, tenant isolation and
// cursor semantics are identical to ListDrafts.
func (store *PostgresStore) ListObjects(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	filter AdminListFilter,
) (AdminPage, error) {
	if err := store.validateAdminRequest(ctx, scope); err != nil {
		return AdminPage{}, err
	}
	if filter.Limit < 1 || filter.Limit > 200 {
		return AdminPage{}, fmt.Errorf("%w: limit must be between 1 and 200", ErrRegistryInvalidRequest)
	}
	if !validObjectStatusFilter(filter.Status) {
		return AdminPage{}, fmt.Errorf("%w: status filter is not a known lifecycle status", ErrRegistryInvalidRequest)
	}
	position, err := decodeMetricCursor(filter.Cursor)
	if err != nil {
		return AdminPage{}, fmt.Errorf("%w: cursor is invalid", ErrRegistryInvalidRequest)
	}
	var page AdminPage
	err = database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, ""); err != nil {
			return err
		}
		var listErr error
		page, listErr = listObjectsTx(ctx, tx, scope.DomainID, resource, filter.Status, position, filter.Limit)
		return listErr
	})
	return page, normalizeAdminStoreError(err)
}

// GetObject reads a single governed semantic object at any lifecycle status.
func (store *PostgresStore) GetObject(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	resourceID string,
) (any, error) {
	if err := store.validateAdminRequest(ctx, scope); err != nil {
		return nil, err
	}
	if !canonicalAdminUUID(resourceID) {
		return nil, fmt.Errorf("%w: resource ID must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	var result any
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, resourceID); err != nil {
			return err
		}
		var getErr error
		result, getErr = getObjectTx(ctx, tx, scope.DomainID, resource, resourceID, "", false)
		return getErr
	})
	return result, normalizeAdminStoreError(err)
}

func (store *PostgresStore) GetDraft(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	resourceID string,
) (any, error) {
	if err := store.validateAdminRequest(ctx, scope); err != nil {
		return nil, err
	}
	if !canonicalAdminUUID(resourceID) {
		return nil, fmt.Errorf("%w: resource ID must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	var result any
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, AdminActionView, resourceID); err != nil {
			return err
		}
		var getErr error
		result, getErr = getObjectTx(ctx, tx, scope.DomainID, resource, resourceID, StatusDraft, false)
		return getErr
	})
	return result, normalizeAdminStoreError(err)
}

func (store *PostgresStore) CreateDraft(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	mutation AdminMutation,
	command AdminCommand,
) (AdminWriteResult, error) {
	payload, err := mutation.payload(resource)
	if err != nil {
		return AdminWriteResult{}, err
	}
	return store.runAdminWrite(ctx, scope, resource, AdminActionEditDraft, "", "DRAFT_CREATED", command,
		func(ctx context.Context, tx pgx.Tx) (AdminWriteResult, error) {
			return createDraftTx(ctx, tx, scope, resource, payload, command.RequestID)
		})
}

func (store *PostgresStore) UpdateDraft(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	resourceID string,
	mutation AdminMutation,
	command AdminCommand,
) (AdminWriteResult, error) {
	if !canonicalAdminUUID(resourceID) {
		return AdminWriteResult{}, fmt.Errorf("%w: resource ID must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	payload, err := mutation.payload(resource)
	if err != nil {
		return AdminWriteResult{}, err
	}
	return store.runAdminWrite(ctx, scope, resource, AdminActionEditDraft, resourceID, "DRAFT_UPDATED", command,
		func(ctx context.Context, tx pgx.Tx) (AdminWriteResult, error) {
			return updateDraftTx(ctx, tx, scope, resource, resourceID, payload)
		})
}

func (store *PostgresStore) DeleteDraft(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	resourceID string,
	input DeleteDraftInput,
	command AdminCommand,
) (AdminWriteResult, error) {
	if !canonicalAdminUUID(resourceID) {
		return AdminWriteResult{}, fmt.Errorf("%w: resource ID must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	return store.runAdminWrite(ctx, scope, resource, AdminActionEditDraft, resourceID, "DRAFT_DELETED", command,
		func(ctx context.Context, tx pgx.Tx) (AdminWriteResult, error) {
			result, err := deleteDraftTx(ctx, tx, scope.DomainID, resource, resourceID, input)
			if adminForeignKeyViolation(err) {
				return AdminWriteResult{}, ErrRegistryDraftInUse
			}
			return result, err
		})
}

func (store *PostgresStore) CreateAdminReleaseDraft(
	ctx context.Context,
	scope AdminScope,
	input ReleaseDraftInput,
	command AdminCommand,
) (AdminWriteResult, error) {
	return store.runAdminWrite(ctx, scope, AdminResourceRelease, AdminActionRelease, "",
		"RELEASE_DRAFT_CREATED", command, func(ctx context.Context, tx pgx.Tx) (AdminWriteResult, error) {
			manifest, err := BuildReleaseManifest(input.Objects)
			if err != nil {
				return AdminWriteResult{}, fmt.Errorf("%w: %v", ErrRegistryInvalidRequest, err)
			}
			semanticVersion := strings.TrimSpace(input.SemanticVersion)
			if semanticVersion != input.SemanticVersion {
				return AdminWriteResult{}, fmt.Errorf("%w: semanticVersion must be trimmed", ErrRegistryInvalidRequest)
			}
			releaseID := stableAdminID(command.RequestID, string(AdminResourceRelease), "record")
			tag, err := tx.Exec(ctx, `INSERT INTO askdata.releases(
				id,tenant_id,domain_id,semantic_version,content_hash,status,
				object_count,created_by,updated_by
			) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$7)
			ON CONFLICT(tenant_id,domain_id,semantic_version) DO NOTHING`,
				releaseID, scope.TenantID, scope.DomainID, semanticVersion,
				manifest.ContentHash, len(manifest.Objects), scope.ActorID)
			if err != nil {
				return AdminWriteResult{}, err
			}
			if tag.RowsAffected() != 1 {
				var existingID, existingHash, status string
				if err := tx.QueryRow(ctx, `SELECT id::text,content_hash,status
					FROM askdata.releases WHERE domain_id=$1 AND semantic_version=$2`,
					scope.DomainID, semanticVersion).Scan(&existingID, &existingHash, &status); err != nil {
					return AdminWriteResult{}, err
				}
				if existingHash != string(manifest.ContentHash) || status != "DRAFT" {
					return AdminWriteResult{}, ErrRegistryConflict
				}
				releaseID = existingID
			}
			if tag.RowsAffected() == 1 {
				for _, object := range manifest.Objects {
					if _, err := tx.Exec(ctx, `INSERT INTO askdata.release_objects(
						tenant_id,domain_id,release_id,object_type,object_id,
						object_version_id,content_hash,sensitivity,contract_json
					) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
						scope.TenantID, scope.DomainID, releaseID, object.Type,
						object.ObjectID, object.ObjectVersionID, object.ContentHash,
						object.Sensitivity, object.Contract); err != nil {
						return AdminWriteResult{}, err
					}
				}
			}
			return AdminWriteResult{
				ResourceType: AdminResourceRelease, ResourceID: releaseID,
				ContentHash: manifest.ContentHash, Status: "DRAFT",
				SemanticVersion: semanticVersion,
			}, nil
		})
}

func (store *PostgresStore) validateAdminRequest(ctx context.Context, scope AdminScope) error {
	if store == nil || store.pool == nil {
		return errors.New("semantic registry PostgreSQL store is not configured")
	}
	return scope.Validate(ctx)
}

func (store *PostgresStore) runAdminWrite(
	ctx context.Context,
	scope AdminScope,
	resource AdminResource,
	action, permissionObjectID, eventType string,
	command AdminCommand,
	execute func(context.Context, pgx.Tx) (AdminWriteResult, error),
) (AdminWriteResult, error) {
	if err := store.validateAdminRequest(ctx, scope); err != nil {
		return AdminWriteResult{}, err
	}
	if err := command.Validate(); err != nil {
		return AdminWriteResult{}, err
	}
	var result AdminWriteResult
	err := database.WithTenantTx(ctx, store.pool, scope.TenantID, func(tx pgx.Tx) error {
		if err := requireSemanticPermissionTx(ctx, tx, scope, action, permissionObjectID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
			"askdata-admin:"+scope.TenantID+":"+scope.ActorID+":"+command.RequestID); err != nil {
			return err
		}
		var existingHash string
		var detail json.RawMessage
		err := tx.QueryRow(ctx, `SELECT action_hash,detail
			FROM askdata.audit_events
			WHERE request_id=$1 AND actor_id=$2 AND domain_id=$3
			ORDER BY created_at,id LIMIT 1`, command.RequestID, scope.ActorID, scope.DomainID).
			Scan(&existingHash, &detail)
		if err == nil {
			if existingHash != string(command.ActionHash) {
				return ErrRegistryIdempotencyConflict
			}
			var replay struct {
				Result AdminWriteResult `json:"result"`
			}
			if json.Unmarshal(detail, &replay) != nil || replay.Result.ResourceID == "" {
				return errors.New("semantic registry idempotency audit is corrupt")
			}
			result = replay.Result
			result.Replayed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result, err = execute(ctx, tx)
		if err != nil {
			return err
		}
		result.Replayed = false
		auditDetail, err := json.Marshal(struct {
			Result AdminWriteResult `json:"result"`
		}{Result: result})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO askdata.audit_events(
			id,tenant_id,domain_id,actor_id,event_type,resource_type,
			resource_id,request_id,action_hash,detail
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			uuid.NewString(), scope.TenantID, scope.DomainID, scope.ActorID,
			eventType, resource, result.ResourceID, command.RequestID,
			command.ActionHash, auditDetail)
		return err
	})
	return result, normalizeAdminStoreError(err)
}

func requireSemanticPermissionTx(
	ctx context.Context,
	tx pgx.Tx,
	scope AdminScope,
	action, objectID string,
) error {
	resourceType := "SEMANTIC_ASSET"
	if action == AdminActionRelease {
		resourceType = "SEMANTIC_RELEASE"
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.user_roles AS assignment
		JOIN platform.roles AS role
		  ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
		WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
		  AND role.code::text='platform_admin'
		  AND role.status='ACTIVE' AND role.deleted_at IS NULL
		UNION ALL
		SELECT 1
		FROM platform.domain_memberships AS membership
		JOIN platform.business_domains AS domain
		  ON domain.id=membership.domain_id AND domain.tenant_id=membership.tenant_id
		WHERE membership.tenant_id=$1 AND membership.user_id=$2
		  AND membership.domain_id=$3 AND membership.status='ACTIVE'
		  AND domain.status='ACTIVE' AND domain.deleted_at IS NULL
		  AND ($4='VIEW' OR membership.member_role='DOMAIN_ADMIN')
		UNION ALL
		SELECT 1
		FROM platform.user_roles AS assignment
		JOIN platform.roles AS role
		  ON role.id=assignment.role_id AND role.tenant_id=assignment.tenant_id
		JOIN platform.role_permissions AS role_permission
		  ON role_permission.role_id=role.id AND role_permission.tenant_id=role.tenant_id
		JOIN platform.permissions AS permission
		  ON permission.id=role_permission.permission_id AND permission.tenant_id=role_permission.tenant_id
		WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
		  AND role.status='ACTIVE' AND role.deleted_at IS NULL
		  AND permission.resource_type=$5 AND permission.action=$4
		UNION ALL
		SELECT 1
		FROM platform.object_permissions AS permission
		WHERE $6<>'' AND permission.tenant_id=$1
		  AND permission.object_type=$5 AND permission.object_id::text=$6
		  AND permission.action=$4 AND (
			(permission.subject_type='USER' AND permission.subject_id=$2)
			OR (permission.subject_type='ROLE' AND EXISTS(
				SELECT 1 FROM platform.user_roles AS object_assignment
				JOIN platform.roles AS object_role
				  ON object_role.id=object_assignment.role_id
				 AND object_role.tenant_id=object_assignment.tenant_id
				WHERE object_assignment.tenant_id=$1
				  AND object_assignment.user_id=$2
				  AND object_assignment.role_id=permission.subject_id
				  AND object_role.status='ACTIVE' AND object_role.deleted_at IS NULL
			))
		  )
	)`, scope.TenantID, scope.ActorID, scope.DomainID, action, resourceType, objectID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrRegistryPermissionDenied
	}
	return nil
}

func createDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	scope AdminScope,
	resource AdminResource,
	payload any,
	requestID string,
) (AdminWriteResult, error) {
	recordID := stableAdminID(requestID, string(resource), "record")
	switch resource {
	case AdminResourceSemanticModel:
		input := payload.(*SemanticModelDraftInput)
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID = recordID
		model := SemanticModel{
			VersionIdentity: identity, Code: input.Code, Name: input.Name,
			Description: input.Description, EntityVersionID: input.EntityVersionID,
			DatasetID: input.DatasetID, DatasetVersionID: input.DatasetVersionID,
			MaterializationID: input.MaterializationID, DatasetSchemaHash: input.DatasetSchemaHash,
			Layer: input.Layer, GrainContract: input.GrainContract,
			PrimaryTimeFieldID:    input.PrimaryTimeFieldID,
			TimeContractVersionID: input.TimeContractVersionID,
		}
		model.ContentHash = contentHashForContract(semanticModelContract(model))
		if err := model.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertSemanticModelAdminTx(ctx, tx, &model); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, model.VersionIdentity), nil
	case AdminResourceMeasure:
		input := payload.(*MeasureDraftInput)
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID = recordID
		measure := Measure{VersionIdentity: identity,
			SemanticModelVersionID: input.SemanticModelVersionID,
			Code:                   input.Code, Name: input.Name, Description: input.Description,
			FormulaAST: input.FormulaAST, Aggregation: input.Aggregation,
			Additivity: input.Additivity, SemiAdditiveTimeAggregation: input.SemiAdditiveTimeAggregation,
			AggregationRestriction: input.AggregationRestriction,
			NonAdditiveDimensions:  sortedAdminIDs(input.NonAdditiveDimensions),
			DataType:               input.DataType, Unit: input.Unit, Currency: input.Currency,
			ZeroDenominatorPolicy: input.ZeroDenominatorPolicy, DisplayPrecision: input.DisplayPrecision,
			AdditivitySuggestion: input.AdditivitySuggestion,
		}
		applyAdditivityDefaultsToMeasure(&measure)
		confirmMeasureAdditivity(&measure, scope.ActorID, nil)
		measure.ContentHash = contentHashForContract(measureContract(measure))
		if err := measure.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertMeasureAdminTx(ctx, tx, &measure); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, measure.VersionIdentity), nil
	case AdminResourceMetric:
		input := payload.(*MetricDraftInput)
		if input.ExpectedVersion != 0 {
			return AdminWriteResult{}, fmt.Errorf("%w: expectedVersion is not accepted on create", ErrRegistryInvalidRequest)
		}
		metric := Metric{
			ID: recordID, TenantID: scope.TenantID, DomainID: scope.DomainID,
			Code: input.Code, Name: input.Name, Description: input.Description,
			Status: "DRAFT", OwnerID: defaultAdminOwner(input.OwnerID, scope.ActorID), Version: 1,
		}
		if err := metric.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO askdata.metrics(
			id,tenant_id,domain_id,code,name,description,status,owner_id,version
		) VALUES($1,$2,$3,$4,$5,$6,'DRAFT',$7,1)
		RETURNING created_at,updated_at,version`, metric.ID, metric.TenantID,
			metric.DomainID, metric.Code, metric.Name, metric.Description, metric.OwnerID).
			Scan(&metric.CreatedAt, &metric.UpdatedAt, &metric.Version); err != nil {
			return AdminWriteResult{}, err
		}
		return metricWriteResult(metric), nil
	case AdminResourceMetricVersion:
		input := payload.(*MetricVersionDraftInput)
		if input.ObjectID != "" && input.ObjectID != input.MetricID {
			return AdminWriteResult{}, fmt.Errorf("%w: objectId must equal metricId", ErrRegistryInvalidRequest)
		}
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID, identity.ObjectID = recordID, input.MetricID
		metric := MetricVersion{VersionIdentity: identity, MetricID: input.MetricID,
			SemanticModelVersionID: input.SemanticModelVersionID,
			FormulaAST:             input.FormulaAST, DefaultFiltersAST: input.DefaultFiltersAST,
			Unit: input.Unit, Currency: input.Currency, TimeGrain: input.TimeGrain, Additivity: input.Additivity,
			SemiAdditiveTimeAggregation: input.SemiAdditiveTimeAggregation,
			AggregationRestriction:      input.AggregationRestriction,
			NonAdditiveDimensions:       sortedAdminIDs(input.NonAdditiveDimensions),
			ZeroDenominatorPolicy:       input.ZeroDenominatorPolicy, DisplayPrecision: input.DisplayPrecision,
			AdditivitySuggestion:           input.AdditivitySuggestion,
			NullPolicy:                     input.NullPolicy,
			IncompletePeriodPolicyOverride: input.IncompletePeriodPolicyOverride,
			MeasureVersionIDs:              sortedAdminIDs(input.MeasureVersionIDs),
		}
		applyMetricVersionDefaults(&metric)
		applyAdditivityDefaultsToMetric(&metric)
		confirmMetricAdditivity(&metric, scope.ActorID, nil)
		metric.ContentHash = metricVersionContentHash(metric)
		if err := metric.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertMetricVersionAdminTx(ctx, tx, &metric); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, metric.VersionIdentity), nil
	case AdminResourceDimension:
		input := payload.(*DimensionDraftInput)
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID = recordID
		dimension := Dimension{VersionIdentity: identity,
			SemanticModelVersionID: input.SemanticModelVersionID,
			LogicalFieldID:         input.LogicalFieldID, Code: input.Code, Name: input.Name,
			Description: input.Description, Kind: input.Kind, Sensitivity: input.Sensitivity,
			MemberIndexPolicy: input.MemberIndexPolicy, HighCardinality: input.HighCardinality,
		}
		dimension.ContentHash = contentHashForContract(dimensionContract(dimension))
		if err := dimension.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertDimensionAdminTx(ctx, tx, &dimension); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, dimension.VersionIdentity), nil
	case AdminResourceBusinessTerm:
		input := payload.(*BusinessTermDraftInput)
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID = recordID
		term := BusinessTerm{
			VersionIdentity: identity, Term: input.Term, TermType: input.TermType,
			TargetObjectType: input.TargetObjectType, TargetVersionID: input.TargetVersionID,
			TargetCode: input.TargetCode, MatchMode: input.MatchMode, MatchPattern: input.MatchPattern,
			Priority: input.Priority, NegativeContexts: sortedAdminAliases(input.NegativeContexts),
			ApplicableRoleIDs: sortedAdminIDs(input.ApplicableRoleIDs),
			ValidFrom:         input.ValidFrom, ValidTo: input.ValidTo, Source: input.Source,
			Code: input.Code, Name: input.Name, Definition: input.Definition,
			Aliases: sortedAdminAliases(input.Aliases),
		}
		applyBusinessTermDefaults(&term)
		term.ContentHash = businessTermContentHash(term)
		if err := term.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := validateBusinessTermReferencesTx(ctx, tx, term); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertBusinessTermAdminTx(ctx, tx, &term); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, term.VersionIdentity), nil
	case AdminResourceKPIBundle:
		input := payload.(*KPIBundleDraftInput)
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID = recordID
		bundle := KPIBundle{
			VersionIdentity: identity, Code: input.Code, Name: input.Name,
			Items: input.Items, DefaultDimensionVersionIDs: input.DefaultDimensionVersionIDs,
			DefaultTimeExpression: input.DefaultTimeExpression,
			DefaultChartTypes:     input.DefaultChartTypes, RoleMapping: input.RoleMapping,
			ApplicableQuestionPatterns: input.ApplicableQuestionPatterns,
		}
		normalizeKPIBundle(&bundle)
		bundle.ContentHash = KPIBundleContentHash(bundle)
		if err := bundle.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := validateKPIBundleReferencesTx(ctx, tx, bundle); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertKPIBundleAdminTx(ctx, tx, &bundle); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, bundle.VersionIdentity), nil
	case AdminResourceRelationship:
		input := payload.(*RelationshipDraftInput)
		identity, err := newVersionIdentity(scope, input.VersionedDraftInput, requestID, resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		identity.ID = recordID
		relationship := Relationship{VersionIdentity: identity,
			LeftModelVersionID:  input.LeftModelVersionID,
			RightModelVersionID: input.RightModelVersionID, Type: input.Type,
			JoinType: input.JoinType, Cardinality: input.Cardinality,
			JoinAST: input.JoinAST, FanoutPolicy: input.FanoutPolicy,
			BridgeModelVersionID: input.BridgeModelVersionID,
		}
		relationship.ContentHash = relationshipContentHash(relationship)
		if err := relationship.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := insertRelationshipAdminTx(ctx, tx, &relationship); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, relationship.VersionIdentity), nil
	default:
		return AdminWriteResult{}, fmt.Errorf("%w: unsupported semantic resource", ErrRegistryInvalidRequest)
	}
}

func updateDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	scope AdminScope,
	resource AdminResource,
	resourceID string,
	payload any,
) (AdminWriteResult, error) {
	existing, err := getObjectTx(ctx, tx, scope.DomainID, resource, resourceID, StatusDraft, true)
	if err != nil {
		return AdminWriteResult{}, err
	}
	switch resource {
	case AdminResourceSemanticModel:
		current, input := existing.(SemanticModel), payload.(*SemanticModelDraftInput)
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		current.Code, current.Name, current.Description = input.Code, input.Name, input.Description
		current.EntityVersionID, current.DatasetID = input.EntityVersionID, input.DatasetID
		current.DatasetVersionID, current.MaterializationID = input.DatasetVersionID, input.MaterializationID
		current.DatasetSchemaHash, current.Layer = input.DatasetSchemaHash, input.Layer
		current.GrainContract, current.PrimaryTimeFieldID = input.GrainContract, input.PrimaryTimeFieldID
		current.TimeContractVersionID = input.TimeContractVersionID
		current.OwnerID = updatedAdminOwner(input.OwnerID, current.OwnerID)
		current.ContentHash = contentHashForContract(semanticModelContract(current))
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.semantic_models SET
			code=$1,name=$2,description=$3,entity_version_id=NULLIF($4,'')::uuid,
			dataset_id=$5,dataset_version_id=$6,materialization_id=$7,
			dataset_schema_hash=$8,layer=$9,grain_contract=$10,
			primary_time_field_id=$11,time_contract_version_id=NULLIF($12,'')::uuid,
			content_hash=$13,owner_id=$14
			WHERE id=$15 AND domain_id=$16 AND status='DRAFT' AND updated_at=$17
			RETURNING updated_at`, current.Code, current.Name, current.Description,
			current.EntityVersionID, current.DatasetID, current.DatasetVersionID,
			current.MaterializationID, current.DatasetSchemaHash, current.Layer,
			current.GrainContract, current.PrimaryTimeFieldID, current.TimeContractVersionID,
			current.ContentHash, current.OwnerID, current.ID, current.DomainID,
			input.ExpectedUpdatedAt).Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return versionWriteResult(resource, current.VersionIdentity), err
	case AdminResourceMeasure:
		current, input := existing.(Measure), payload.(*MeasureDraftInput)
		previous := current
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		current.SemanticModelVersionID = input.SemanticModelVersionID
		current.Code, current.Name, current.Description = input.Code, input.Name, input.Description
		current.FormulaAST, current.Aggregation = input.FormulaAST, input.Aggregation
		current.Additivity, current.DataType, current.Unit = input.Additivity, input.DataType, input.Unit
		current.SemiAdditiveTimeAggregation = input.SemiAdditiveTimeAggregation
		current.AggregationRestriction = input.AggregationRestriction
		current.NonAdditiveDimensions = sortedAdminIDs(input.NonAdditiveDimensions)
		current.Currency, current.ZeroDenominatorPolicy = input.Currency, input.ZeroDenominatorPolicy
		current.DisplayPrecision, current.AdditivitySuggestion = input.DisplayPrecision, input.AdditivitySuggestion
		current.OwnerID = updatedAdminOwner(input.OwnerID, current.OwnerID)
		applyAdditivityDefaultsToMeasure(&current)
		confirmMeasureAdditivity(&current, scope.ActorID, &previous)
		current.ContentHash = contentHashForContract(measureContract(current))
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.measures SET
			semantic_model_version_id=$1,code=$2,name=$3,description=$4,
			formula_ast=$5,aggregation=$6,additivity=NULLIF($7,'')::text,
			semi_additive_time_aggregation=NULLIF($8,'')::text,
			aggregation_restriction=NULLIF($9,'')::text,non_additive_dimensions=$10,
			data_type=$11,unit=$12,currency=NULLIF($13,'')::text,
			zero_denominator_policy=$14,display_precision=$15,
			additivity_suggestion=NULLIF($16,'')::text,
			additivity_confirmed_by=NULLIF($17,'')::uuid,additivity_confirmed_at=$18,
			content_hash=$19,owner_id=$20
			WHERE id=$21 AND domain_id=$22 AND status='DRAFT' AND updated_at=$23
			RETURNING updated_at`, current.SemanticModelVersionID, current.Code,
			current.Name, current.Description, current.FormulaAST, current.Aggregation,
			current.Additivity, current.SemiAdditiveTimeAggregation, current.AggregationRestriction,
			current.NonAdditiveDimensions, current.DataType, current.Unit, current.Currency,
			current.ZeroDenominatorPolicy, current.DisplayPrecision, current.AdditivitySuggestion,
			current.AdditivityConfirmedBy, current.AdditivityConfirmedAt, current.ContentHash,
			current.OwnerID, current.ID, current.DomainID, input.ExpectedUpdatedAt).
			Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return versionWriteResult(resource, current.VersionIdentity), err
	case AdminResourceMetric:
		current, input := existing.(Metric), payload.(*MetricDraftInput)
		if input.ExpectedVersion < 1 || input.ExpectedVersion != current.Version {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		current.Code, current.Name, current.Description = input.Code, input.Name, input.Description
		current.OwnerID = updatedAdminOwner(input.OwnerID, current.OwnerID)
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.metrics SET
			code=$1,name=$2,description=$3,owner_id=$4,version=version+1
			WHERE id=$5 AND domain_id=$6 AND status='DRAFT' AND version=$7
			RETURNING version,updated_at`, current.Code, current.Name, current.Description,
			current.OwnerID, current.ID, current.DomainID, input.ExpectedVersion).
			Scan(&current.Version, &current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return metricWriteResult(current), err
	case AdminResourceMetricVersion:
		current, input := existing.(MetricVersion), payload.(*MetricVersionDraftInput)
		previous := current
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		if input.MetricID != "" && input.MetricID != current.MetricID {
			return AdminWriteResult{}, fmt.Errorf("%w: metricId cannot change", ErrRegistryInvalidRequest)
		}
		current.SemanticModelVersionID, current.FormulaAST = input.SemanticModelVersionID, input.FormulaAST
		current.DefaultFiltersAST, current.Unit = input.DefaultFiltersAST, input.Unit
		current.Currency, current.TimeGrain, current.Additivity = input.Currency, input.TimeGrain, input.Additivity
		current.SemiAdditiveTimeAggregation = input.SemiAdditiveTimeAggregation
		current.AggregationRestriction = input.AggregationRestriction
		current.NonAdditiveDimensions = sortedAdminIDs(input.NonAdditiveDimensions)
		current.ZeroDenominatorPolicy, current.DisplayPrecision = input.ZeroDenominatorPolicy, input.DisplayPrecision
		current.AdditivitySuggestion = input.AdditivitySuggestion
		current.NullPolicy = input.NullPolicy
		current.IncompletePeriodPolicyOverride = input.IncompletePeriodPolicyOverride
		current.MeasureVersionIDs = sortedAdminIDs(input.MeasureVersionIDs)
		current.OwnerID = updatedAdminOwner(input.OwnerID, current.OwnerID)
		applyMetricVersionDefaults(&current)
		applyAdditivityDefaultsToMetric(&current)
		confirmMetricAdditivity(&current, scope.ActorID, &previous)
		current.ContentHash = metricVersionContentHash(current)
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		tag, err := tx.Exec(ctx, `UPDATE askdata.metric_versions SET
			semantic_model_version_id=$1,formula_ast=$2,default_filters_ast=$3,
			unit=$4,currency=NULLIF($5,'')::text,time_grain=$6,additivity=NULLIF($7,'')::text,
			semi_additive_time_aggregation=NULLIF($8,'')::text,
			aggregation_restriction=NULLIF($9,'')::text,non_additive_dimensions=$10,
			zero_denominator_policy=$11,display_precision=$12,
			additivity_suggestion=NULLIF($13,'')::text,
			additivity_confirmed_by=NULLIF($14,'')::uuid,additivity_confirmed_at=$15,
			null_policy=$16,incomplete_period_policy_override=NULLIF($17,'')::text,
			content_hash=$18,owner_id=$19
			WHERE id=$20 AND domain_id=$21 AND status='DRAFT' AND updated_at=$22`,
			current.SemanticModelVersionID, current.FormulaAST, current.DefaultFiltersAST,
			current.Unit, current.Currency, current.TimeGrain, current.Additivity,
			current.SemiAdditiveTimeAggregation, current.AggregationRestriction,
			current.NonAdditiveDimensions, current.ZeroDenominatorPolicy, current.DisplayPrecision,
			current.AdditivitySuggestion, current.AdditivityConfirmedBy, current.AdditivityConfirmedAt,
			current.NullPolicy, current.IncompletePeriodPolicyOverride, current.ContentHash, current.OwnerID,
			current.ID, current.DomainID, input.ExpectedUpdatedAt)
		if err != nil {
			return AdminWriteResult{}, err
		}
		if tag.RowsAffected() != 1 {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM askdata.metric_version_measures
			WHERE tenant_id=$1 AND metric_version_id=$2`, current.TenantID, current.ID); err != nil {
			return AdminWriteResult{}, err
		}
		for index, measureID := range current.MeasureVersionIDs {
			if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_version_measures(
				tenant_id,domain_id,metric_version_id,measure_version_id,ordinal
			) VALUES($1,$2,$3,$4,$5)`, current.TenantID, current.DomainID,
				current.ID, measureID, index+1); err != nil {
				return AdminWriteResult{}, err
			}
		}
		if err := tx.QueryRow(ctx, `SELECT updated_at FROM askdata.metric_versions WHERE id=$1`,
			current.ID).Scan(&current.UpdatedAt); err != nil {
			return AdminWriteResult{}, err
		}
		return versionWriteResult(resource, current.VersionIdentity), nil
	case AdminResourceDimension:
		current, input := existing.(Dimension), payload.(*DimensionDraftInput)
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		current.SemanticModelVersionID, current.LogicalFieldID = input.SemanticModelVersionID, input.LogicalFieldID
		current.Code, current.Name, current.Description = input.Code, input.Name, input.Description
		current.Kind, current.Sensitivity = input.Kind, input.Sensitivity
		current.MemberIndexPolicy, current.HighCardinality = input.MemberIndexPolicy, input.HighCardinality
		current.OwnerID = updatedAdminOwner(input.OwnerID, current.OwnerID)
		current.ContentHash = contentHashForContract(dimensionContract(current))
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.dimensions SET
			semantic_model_version_id=$1,logical_field_id=$2,code=$3,name=$4,
			description=$5,dimension_kind=$6,sensitivity=$7,member_index_policy=$8,
			high_cardinality=$9,content_hash=$10,owner_id=$11
			WHERE id=$12 AND domain_id=$13 AND status='DRAFT' AND updated_at=$14
			RETURNING updated_at`, current.SemanticModelVersionID, current.LogicalFieldID,
			current.Code, current.Name, current.Description, current.Kind, current.Sensitivity,
			current.MemberIndexPolicy, current.HighCardinality, current.ContentHash,
			current.OwnerID, current.ID, current.DomainID, input.ExpectedUpdatedAt).
			Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return versionWriteResult(resource, current.VersionIdentity), err
	case AdminResourceBusinessTerm:
		current, input := existing.(BusinessTerm), payload.(*BusinessTermDraftInput)
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		current.Term, current.TermType = input.Term, input.TermType
		current.TargetObjectType, current.TargetVersionID = input.TargetObjectType, input.TargetVersionID
		current.TargetCode, current.MatchMode = input.TargetCode, input.MatchMode
		current.MatchPattern, current.Priority = input.MatchPattern, input.Priority
		current.NegativeContexts = sortedAdminAliases(input.NegativeContexts)
		current.ApplicableRoleIDs = sortedAdminIDs(input.ApplicableRoleIDs)
		current.ValidFrom, current.ValidTo, current.Source = input.ValidFrom, input.ValidTo, input.Source
		current.Code, current.Name, current.Definition = input.Code, input.Name, input.Definition
		current.Aliases, current.OwnerID = sortedAdminAliases(input.Aliases), updatedAdminOwner(input.OwnerID, current.OwnerID)
		applyBusinessTermDefaults(&current)
		current.ContentHash = businessTermContentHash(current)
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := validateBusinessTermReferencesTx(ctx, tx, current); err != nil {
			return AdminWriteResult{}, err
		}
		tag, identityErr := tx.Exec(ctx, `UPDATE askdata.business_terms SET term=$1,term_type=$2
			WHERE id=$3 AND domain_id=$4 AND NOT EXISTS(
				SELECT 1 FROM askdata.business_term_versions
				WHERE business_term_id=$3 AND status='CERTIFIED'
			)`, current.Term, current.TermType, current.ObjectID, current.DomainID)
		if identityErr != nil {
			return AdminWriteResult{}, identityErr
		}
		if tag.RowsAffected() != 1 {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.business_term_versions SET
			target_object_type=$1,target_version_id=$2,target_code=$3,match_mode=$4,
			match_pattern=NULLIF($5,''),priority=$6,negative_contexts=$7,
			applicable_role_ids=$8,valid_from=$9,valid_to=$10,source=$11,
			review_status='PENDING',reviewed_by=NULL,reviewed_at=NULL,
			code=$12,name=$13,definition=$14,aliases=$15,content_hash=$16,owner_id=$17
			WHERE id=$18 AND domain_id=$19 AND status='DRAFT' AND updated_at=$20
			RETURNING updated_at`, current.TargetObjectType, current.TargetVersionID,
			current.TargetCode, current.MatchMode, current.MatchPattern, current.Priority,
			current.NegativeContexts, current.ApplicableRoleIDs, current.ValidFrom, current.ValidTo,
			current.Source, current.Code, current.Name, current.Definition, current.Aliases,
			current.ContentHash, current.OwnerID, current.ID, current.DomainID,
			input.ExpectedUpdatedAt).Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return versionWriteResult(resource, current.VersionIdentity), err
	case AdminResourceKPIBundle:
		current, input := existing.(KPIBundle), payload.(*KPIBundleDraftInput)
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		current.Code, current.Name, current.Items = input.Code, input.Name, input.Items
		current.DefaultDimensionVersionIDs = input.DefaultDimensionVersionIDs
		current.DefaultTimeExpression, current.DefaultChartTypes = input.DefaultTimeExpression, input.DefaultChartTypes
		current.RoleMapping, current.ApplicableQuestionPatterns = input.RoleMapping, input.ApplicableQuestionPatterns
		current.OwnerID = updatedAdminOwner(input.OwnerID, current.OwnerID)
		normalizeKPIBundle(&current)
		current.ContentHash = KPIBundleContentHash(current)
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		if err := validateKPIBundleReferencesTx(ctx, tx, current); err != nil {
			return AdminWriteResult{}, err
		}
		tag, identityErr := tx.Exec(ctx, `UPDATE askdata.kpi_bundles SET code=$1,name=$2
			WHERE id=$3 AND domain_id=$4 AND NOT EXISTS(
				SELECT 1 FROM askdata.kpi_bundle_versions
				WHERE kpi_bundle_id=$3 AND status='CERTIFIED'
			)`, current.Code, current.Name, current.ObjectID, current.DomainID)
		if identityErr != nil {
			return AdminWriteResult{}, identityErr
		}
		if tag.RowsAffected() != 1 {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		items, err := CanonicalValue(current.Items)
		if err != nil {
			return AdminWriteResult{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.kpi_bundle_versions SET
			items=$1,default_dimension_version_ids=$2,default_time_expression=$3,
			default_chart_types=$4,role_mapping=$5,applicable_question_patterns=$6,
			content_hash=$7,owner_id=$8
			WHERE id=$9 AND domain_id=$10 AND status='DRAFT' AND updated_at=$11
			RETURNING updated_at`, items, current.DefaultDimensionVersionIDs,
			current.DefaultTimeExpression, current.DefaultChartTypes, current.RoleMapping,
			current.ApplicableQuestionPatterns, current.ContentHash, current.OwnerID,
			current.ID, current.DomainID, input.ExpectedUpdatedAt).Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return versionWriteResult(resource, current.VersionIdentity), err
	case AdminResourceRelationship:
		current, input := existing.(Relationship), payload.(*RelationshipDraftInput)
		if err := validateVersionedUpdate(input.VersionedDraftInput, current.VersionIdentity); err != nil {
			return AdminWriteResult{}, err
		}
		current.LeftModelVersionID, current.RightModelVersionID = input.LeftModelVersionID, input.RightModelVersionID
		current.Type, current.JoinType = input.Type, input.JoinType
		current.Cardinality, current.JoinAST = input.Cardinality, input.JoinAST
		current.FanoutPolicy, current.OwnerID = input.FanoutPolicy, updatedAdminOwner(input.OwnerID, current.OwnerID)
		current.BridgeModelVersionID = input.BridgeModelVersionID
		current.ContentHash = relationshipContentHash(current)
		if err := current.Validate(); err != nil {
			return AdminWriteResult{}, err
		}
		err = tx.QueryRow(ctx, `UPDATE askdata.relationships SET
			left_model_version_id=$1,right_model_version_id=$2,relationship_type=$3,
			join_type=$4,cardinality=$5,join_ast=$6,fanout_policy=$7,
			bridge_model_version_id=NULLIF($8,'')::uuid,content_hash=$9,owner_id=$10
			WHERE id=$11 AND domain_id=$12 AND status='DRAFT' AND updated_at=$13
			RETURNING updated_at`, current.LeftModelVersionID, current.RightModelVersionID,
			current.Type, current.JoinType, current.Cardinality, current.JoinAST,
			current.FanoutPolicy, current.BridgeModelVersionID, current.ContentHash, current.OwnerID, current.ID,
			current.DomainID, input.ExpectedUpdatedAt).Scan(&current.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		return versionWriteResult(resource, current.VersionIdentity), err
	default:
		return AdminWriteResult{}, fmt.Errorf("%w: unsupported semantic resource", ErrRegistryInvalidRequest)
	}
}

func deleteDraftTx(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	resource AdminResource,
	resourceID string,
	input DeleteDraftInput,
) (AdminWriteResult, error) {
	existing, err := getObjectTx(ctx, tx, domainID, resource, resourceID, StatusDraft, true)
	if err != nil {
		return AdminWriteResult{}, err
	}
	result := AdminWriteResult{ResourceType: resource, ResourceID: resourceID, Status: "DRAFT"}
	var tag pgconn.CommandTag
	if resource == AdminResourceMetric {
		metric := existing.(Metric)
		if input.ExpectedVersion < 1 || input.ExpectedVersion != metric.Version {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		result.RecordVersion = metric.Version
		tag, err = tx.Exec(ctx, `DELETE FROM askdata.metrics
			WHERE id=$1 AND domain_id=$2 AND status='DRAFT' AND version=$3`,
			resourceID, domainID, input.ExpectedVersion)
	} else {
		identity := versionIdentityOf(existing)
		if input.ExpectedUpdatedAt == nil || !input.ExpectedUpdatedAt.Equal(identity.UpdatedAt) {
			return AdminWriteResult{}, ErrRegistryVersionConflict
		}
		result.ObjectID, result.ContentHash = identity.ObjectID, identity.ContentHash
		updated := identity.UpdatedAt
		result.UpdatedAt = &updated
		table, err := adminTable(resource)
		if err != nil {
			return AdminWriteResult{}, err
		}
		query := fmt.Sprintf(`DELETE FROM askdata.%s
			WHERE id=$1 AND domain_id=$2 AND status='DRAFT' AND updated_at=$3`, table)
		tag, err = tx.Exec(ctx, query, resourceID, domainID, input.ExpectedUpdatedAt)
	}
	if err != nil {
		return AdminWriteResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return AdminWriteResult{}, ErrRegistryVersionConflict
	}
	return result, nil
}

func getObjectTx(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	resource AdminResource,
	resourceID string,
	status string,
	forUpdate bool,
) (any, error) {
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	var err error
	switch resource {
	case AdminResourceSemanticModel:
		var value SemanticModel
		err = scanSemanticModel(tx.QueryRow(ctx, semanticModelAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceMeasure:
		var value Measure
		err = scanMeasure(tx.QueryRow(ctx, measureAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceMetric:
		var value Metric
		err = scanMetric(tx.QueryRow(ctx, metricSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceMetricVersion:
		var value MetricVersion
		if forUpdate {
			err = scanMetricVersionCore(tx.QueryRow(ctx, metricVersionCoreAdminSelect+`
				WHERE id=$1 AND domain_id=$2 AND status='DRAFT' FOR UPDATE`, resourceID, domainID), &value)
			if err == nil {
				err = tx.QueryRow(ctx, `SELECT COALESCE(array_agg(measure_version_id::text ORDER BY ordinal),'{}'::text[])
					FROM askdata.metric_version_measures WHERE metric_version_id=$1`, resourceID).
					Scan(&value.MeasureVersionIDs)
			}
		} else {
			err = scanMetricVersion(tx.QueryRow(ctx, metricVersionAdminSelect+` WHERE version.id=$1 AND version.domain_id=$2 AND ($3::text IS NULL OR version.status=$3)
				GROUP BY version.id`, resourceID, domainID, statusArg(status)), &value)
		}
		return value, adminNoRows(err)
	case AdminResourceDimension:
		var value Dimension
		err = scanDimension(tx.QueryRow(ctx, dimensionAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceBusinessTerm:
		var value BusinessTerm
		err = scanBusinessTerm(tx.QueryRow(ctx, businessTermAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceKPIBundle:
		var value KPIBundle
		err = scanKPIBundle(tx.QueryRow(ctx, kpiBundleAdminSelect+` WHERE version.id=$1 AND version.domain_id=$2 AND ($3::text IS NULL OR version.status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceRelationship:
		var value Relationship
		err = scanRelationship(tx.QueryRow(ctx, relationshipAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceMember:
		var value DimensionMember
		err = scanDimensionMember(tx.QueryRow(ctx, dimensionMemberAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceHierarchy:
		var value Hierarchy
		err = scanHierarchy(tx.QueryRow(ctx, hierarchyAdminSelect+` WHERE hierarchy.id=$1 AND hierarchy.domain_id=$2 AND ($3::text IS NULL OR hierarchy.status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceCertifiedExample:
		var value CertifiedExample
		err = scanCertifiedExample(tx.QueryRow(ctx, certifiedExampleAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	case AdminResourceMetricDimension:
		var value MetricDimension
		err = scanMetricDimension(tx.QueryRow(ctx, metricDimensionAdminSelect+` WHERE id=$1 AND domain_id=$2 AND ($3::text IS NULL OR status=$3)`+lock, resourceID, domainID, statusArg(status)), &value)
		return value, adminNoRows(err)
	default:
		return nil, fmt.Errorf("%w: unsupported semantic resource", ErrRegistryInvalidRequest)
	}
}

func listObjectsTx(
	ctx context.Context,
	tx pgx.Tx,
	domainID string,
	resource AdminResource,
	status string,
	position metricCursor,
	limit int,
) (AdminPage, error) {
	var cursorID any
	if position.ID != "" {
		cursorID = position.ID
	}
	suffix := ` WHERE domain_id=$1 AND ($5::text IS NULL OR status=$5)
		AND ($2::timestamptz IS NULL OR (updated_at,id)<($2,$3::uuid))
		ORDER BY updated_at DESC,id DESC LIMIT $4`
	args := []any{domainID, position.UpdatedAt, cursorID, limit + 1, statusArg(status)}
	switch resource {
	case AdminResourceSemanticModel:
		rows, err := tx.Query(ctx, semanticModelAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []SemanticModel{}
		for rows.Next() {
			var item SemanticModel
			if err := scanSemanticModel(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value SemanticModel) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceMeasure:
		rows, err := tx.Query(ctx, measureAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []Measure{}
		for rows.Next() {
			var item Measure
			if err := scanMeasure(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value Measure) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceMetric:
		rows, err := tx.Query(ctx, metricSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []Metric{}
		for rows.Next() {
			var item Metric
			if err := scanMetric(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value Metric) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceMetricVersion:
		metricSuffix := ` WHERE version.domain_id=$1 AND ($5::text IS NULL OR version.status=$5)
			AND ($2::timestamptz IS NULL OR (version.updated_at,version.id)<($2,$3::uuid))
			GROUP BY version.id ORDER BY version.updated_at DESC,version.id DESC LIMIT $4`
		rows, err := tx.Query(ctx, metricVersionAdminSelect+metricSuffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []MetricVersion{}
		for rows.Next() {
			var item MetricVersion
			if err := scanMetricVersion(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value MetricVersion) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceDimension:
		rows, err := tx.Query(ctx, dimensionAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []Dimension{}
		for rows.Next() {
			var item Dimension
			if err := scanDimension(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value Dimension) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceBusinessTerm:
		rows, err := tx.Query(ctx, businessTermAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []BusinessTerm{}
		for rows.Next() {
			var item BusinessTerm
			if err := scanBusinessTerm(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value BusinessTerm) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceKPIBundle:
		bundleSuffix := ` WHERE version.domain_id=$1 AND ($5::text IS NULL OR version.status=$5)
			AND ($2::timestamptz IS NULL OR (version.updated_at,version.id)<($2,$3::uuid))
			ORDER BY version.updated_at DESC,version.id DESC LIMIT $4`
		rows, err := tx.Query(ctx, kpiBundleAdminSelect+bundleSuffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []KPIBundle{}
		for rows.Next() {
			var item KPIBundle
			if err := scanKPIBundle(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value KPIBundle) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceRelationship:
		rows, err := tx.Query(ctx, relationshipAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []Relationship{}
		for rows.Next() {
			var item Relationship
			if err := scanRelationship(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value Relationship) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceMember:
		rows, err := tx.Query(ctx, dimensionMemberAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []DimensionMember{}
		for rows.Next() {
			var item DimensionMember
			if err := scanDimensionMember(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value DimensionMember) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceHierarchy:
		hierarchySuffix := ` WHERE hierarchy.domain_id=$1 AND ($5::text IS NULL OR hierarchy.status=$5)
			AND ($2::timestamptz IS NULL OR (hierarchy.updated_at,hierarchy.id)<($2,$3::uuid))
			ORDER BY hierarchy.updated_at DESC,hierarchy.id DESC LIMIT $4`
		rows, err := tx.Query(ctx, hierarchyAdminSelect+hierarchySuffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []Hierarchy{}
		for rows.Next() {
			var item Hierarchy
			if err := scanHierarchy(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value Hierarchy) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceCertifiedExample:
		rows, err := tx.Query(ctx, certifiedExampleAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []CertifiedExample{}
		for rows.Next() {
			var item CertifiedExample
			if err := scanCertifiedExample(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value CertifiedExample) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	case AdminResourceMetricDimension:
		rows, err := tx.Query(ctx, metricDimensionAdminSelect+suffix, args...)
		if err != nil {
			return AdminPage{}, err
		}
		defer rows.Close()
		items := []MetricDimension{}
		for rows.Next() {
			var item MetricDimension
			if err := scanMetricDimension(rows, &item); err != nil {
				return AdminPage{}, err
			}
			items = append(items, item)
		}
		return finishAdminPage(items, limit, func(value MetricDimension) (time.Time, string) { return value.UpdatedAt, value.ID }, rows.Err())
	default:
		return AdminPage{}, fmt.Errorf("%w: unsupported semantic resource", ErrRegistryInvalidRequest)
	}
}

func finishAdminPage[T any](
	items []T,
	limit int,
	position func(T) (time.Time, string),
	rowErr error,
) (AdminPage, error) {
	if rowErr != nil {
		return AdminPage{}, rowErr
	}
	page := AdminPage{Items: items}
	if len(items) <= limit {
		return page, nil
	}
	updatedAt, id := position(items[limit-1])
	cursor, err := encodeMetricCursor(metricCursor{UpdatedAt: &updatedAt, ID: id})
	if err != nil {
		return AdminPage{}, err
	}
	page.Items, page.NextCursor = items[:limit], cursor
	return page, nil
}

const semanticModelAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,model_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,code::text,name,description,
	COALESCE(entity_version_id::text,''),dataset_id::text,dataset_version_id::text,
	materialization_id::text,dataset_schema_hash,layer,grain_contract,
	primary_time_field_id,COALESCE(time_contract_version_id::text,'')
	FROM askdata.semantic_models`

const measureAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,measure_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,semantic_model_version_id::text,
	code::text,name,description,formula_ast,aggregation,COALESCE(additivity,''),
	COALESCE(semi_additive_time_aggregation,''),COALESCE(aggregation_restriction,''),
	non_additive_dimensions,data_type,unit,COALESCE(currency,''),zero_denominator_policy,
	display_precision,COALESCE(additivity_suggestion,''),
	COALESCE(additivity_confirmed_by::text,''),additivity_confirmed_at
	FROM askdata.measures`

const metricVersionAdminSelect = `SELECT
	version.id::text,version.tenant_id::text,version.domain_id::text,
	version.metric_id::text,version.version_no,version.status,version.content_hash,
	version.owner_id::text,version.created_at,version.updated_at,
	version.semantic_model_version_id::text,version.formula_ast,
	version.default_filters_ast,version.unit,COALESCE(version.currency,''),version.time_grain,
	COALESCE(version.additivity,''),COALESCE(version.semi_additive_time_aggregation,''),
	COALESCE(version.aggregation_restriction,''),version.non_additive_dimensions,
	version.zero_denominator_policy,version.display_precision,
	COALESCE(version.additivity_suggestion,''),COALESCE(version.additivity_confirmed_by::text,''),
	version.additivity_confirmed_at,version.null_policy,
	COALESCE(version.incomplete_period_policy_override,''),
	COALESCE(array_agg(link.measure_version_id::text ORDER BY link.ordinal)
		FILTER(WHERE link.measure_version_id IS NOT NULL),'{}'::text[])
	FROM askdata.metric_versions AS version
	LEFT JOIN askdata.metric_version_measures AS link
	  ON link.metric_version_id=version.id AND link.tenant_id=version.tenant_id`

const metricVersionCoreAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,metric_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,semantic_model_version_id::text,
	formula_ast,default_filters_ast,unit,COALESCE(currency,''),time_grain,
	COALESCE(additivity,''),COALESCE(semi_additive_time_aggregation,''),
	COALESCE(aggregation_restriction,''),non_additive_dimensions,
	zero_denominator_policy,display_precision,COALESCE(additivity_suggestion,''),
	COALESCE(additivity_confirmed_by::text,''),additivity_confirmed_at,null_policy,
	COALESCE(incomplete_period_policy_override,'')
	FROM askdata.metric_versions `

const dimensionAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,dimension_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,semantic_model_version_id::text,
	logical_field_id,code::text,name,description,dimension_kind,sensitivity,
	member_index_policy,high_cardinality
	FROM askdata.dimensions`

const businessTermAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,term_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,term,term_type,
	target_object_type,target_version_id::text,target_code,match_mode,
	COALESCE(match_pattern,''),priority,negative_contexts,applicable_role_ids::text[],
	valid_from,valid_to,source,review_status,COALESCE(reviewed_by::text,''),reviewed_at,
	code::text,name,definition,aliases
	FROM (
		SELECT version.id,version.tenant_id,version.domain_id,
			version.business_term_id AS term_id,version.version_no,version.status,
			version.content_hash,version.owner_id,version.created_at,version.updated_at,
			identity.term,identity.term_type,version.target_object_type,
			version.target_version_id,version.target_code,version.match_mode,
			version.match_pattern,version.priority,version.negative_contexts,
			version.applicable_role_ids,version.valid_from,version.valid_to,
			version.source,version.review_status,version.reviewed_by,version.reviewed_at,
			version.code,version.name,version.definition,version.aliases
		FROM askdata.business_term_versions AS version
		JOIN askdata.business_terms AS identity
		  ON identity.id=version.business_term_id
		 AND identity.tenant_id=version.tenant_id
		 AND identity.domain_id=version.domain_id
	) AS term_view`

const kpiBundleAdminSelect = `SELECT
	version.id::text,version.tenant_id::text,version.domain_id::text,
	version.kpi_bundle_id::text,version.version_no,version.status,version.content_hash,
	version.owner_id::text,version.created_at,version.updated_at,identity.code::text,
	identity.name,version.items,version.default_dimension_version_ids::text[],
	version.default_time_expression,version.default_chart_types,version.role_mapping,
	version.applicable_question_patterns
	FROM askdata.kpi_bundle_versions AS version
	JOIN askdata.kpi_bundles AS identity
	  ON identity.id=version.kpi_bundle_id AND identity.tenant_id=version.tenant_id
	 AND identity.domain_id=version.domain_id`

const dimensionMemberAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,member_id::text,version_no,status,
	content_hash,created_by::text,created_at,updated_at,dimension_version_id::text,
	member_key,member_key_hash,canonical_label,
	COALESCE(parent_member_version_id::text,''),sensitivity
	FROM askdata.dimension_members`

const hierarchyAdminSelect = `SELECT
	hierarchy.id::text,hierarchy.tenant_id::text,hierarchy.domain_id::text,
	hierarchy.hierarchy_id::text,hierarchy.version_no,hierarchy.status,
	hierarchy.content_hash,hierarchy.owner_id::text,hierarchy.created_at,hierarchy.updated_at,
	hierarchy.code::text,hierarchy.name,hierarchy.description,
	COALESCE((
	  SELECT array_agg(level.dimension_version_id::text ORDER BY level.ordinal)
	  FROM askdata.hierarchy_levels AS level
	  WHERE level.hierarchy_version_id=hierarchy.id AND level.tenant_id=hierarchy.tenant_id
	),'{}'::text[])
	FROM askdata.hierarchies AS hierarchy`

const certifiedExampleAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,certified_example_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,question,
	expected_metric_version_ids::text[],expected_dimension_version_ids::text[],
	expected_time_expression,notes
	FROM askdata.certified_example_versions`

const metricDimensionAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,metric_dimension_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,
	metric_version_id::text,dimension_version_id::text,compatible,role
	FROM askdata.metric_dimension_versions`

const relationshipAdminSelect = `SELECT
	id::text,tenant_id::text,domain_id::text,relationship_id::text,version_no,status,
	content_hash,owner_id::text,created_at,updated_at,left_model_version_id::text,
	right_model_version_id::text,relationship_type,join_type,cardinality,join_ast,
	fanout_policy,COALESCE(bridge_model_version_id::text,'') FROM askdata.relationships`

func scanSemanticModel(row rowScanner, value *SemanticModel) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.Code, &value.Name,
		&value.Description, &value.EntityVersionID, &value.DatasetID,
		&value.DatasetVersionID, &value.MaterializationID, &value.DatasetSchemaHash,
		&value.Layer, &value.GrainContract, &value.PrimaryTimeFieldID,
		&value.TimeContractVersionID)
}

func scanMeasure(row rowScanner, value *Measure) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.SemanticModelVersionID,
		&value.Code, &value.Name, &value.Description, &value.FormulaAST,
		&value.Aggregation, &value.Additivity, &value.SemiAdditiveTimeAggregation,
		&value.AggregationRestriction, &value.NonAdditiveDimensions, &value.DataType,
		&value.Unit, &value.Currency, &value.ZeroDenominatorPolicy, &value.DisplayPrecision,
		&value.AdditivitySuggestion, &value.AdditivityConfirmedBy, &value.AdditivityConfirmedAt)
}

func scanMetricVersion(row rowScanner, value *MetricVersion) error {
	err := row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.MetricID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.SemanticModelVersionID,
		&value.FormulaAST, &value.DefaultFiltersAST, &value.Unit, &value.Currency, &value.TimeGrain,
		&value.Additivity, &value.SemiAdditiveTimeAggregation, &value.AggregationRestriction,
		&value.NonAdditiveDimensions, &value.ZeroDenominatorPolicy, &value.DisplayPrecision,
		&value.AdditivitySuggestion, &value.AdditivityConfirmedBy, &value.AdditivityConfirmedAt,
		&value.NullPolicy, &value.IncompletePeriodPolicyOverride,
		&value.MeasureVersionIDs)
	value.ObjectID = value.MetricID
	return err
}

func scanMetricVersionCore(row rowScanner, value *MetricVersion) error {
	err := row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.MetricID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.SemanticModelVersionID,
		&value.FormulaAST, &value.DefaultFiltersAST, &value.Unit, &value.Currency, &value.TimeGrain,
		&value.Additivity, &value.SemiAdditiveTimeAggregation, &value.AggregationRestriction,
		&value.NonAdditiveDimensions, &value.ZeroDenominatorPolicy, &value.DisplayPrecision,
		&value.AdditivitySuggestion, &value.AdditivityConfirmedBy, &value.AdditivityConfirmedAt,
		&value.NullPolicy, &value.IncompletePeriodPolicyOverride)
	value.ObjectID = value.MetricID
	return err
}

func scanDimension(row rowScanner, value *Dimension) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.SemanticModelVersionID,
		&value.LogicalFieldID, &value.Code, &value.Name, &value.Description,
		&value.Kind, &value.Sensitivity, &value.MemberIndexPolicy,
		&value.HighCardinality)
}

func scanBusinessTerm(row rowScanner, value *BusinessTerm) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.Term, &value.TermType,
		&value.TargetObjectType, &value.TargetVersionID, &value.TargetCode,
		&value.MatchMode, &value.MatchPattern, &value.Priority,
		&value.NegativeContexts, &value.ApplicableRoleIDs, &value.ValidFrom, &value.ValidTo,
		&value.Source, &value.ReviewStatus, &value.ReviewedBy, &value.ReviewedAt,
		&value.Code, &value.Name,
		&value.Definition, &value.Aliases)
}

func scanKPIBundle(row rowScanner, value *KPIBundle) error {
	var items json.RawMessage
	err := row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.Code, &value.Name, &items,
		&value.DefaultDimensionVersionIDs, &value.DefaultTimeExpression,
		&value.DefaultChartTypes, &value.RoleMapping, &value.ApplicableQuestionPatterns)
	if err != nil {
		return err
	}
	return json.Unmarshal(items, &value.Items)
}

func scanDimensionMember(row rowScanner, value *DimensionMember) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.DimensionVersionID,
		&value.MemberKey, &value.MemberKeyHash, &value.CanonicalLabel,
		&value.ParentMemberVersionID, &value.Sensitivity)
}

func scanHierarchy(row rowScanner, value *Hierarchy) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.Code, &value.Name,
		&value.Description, &value.DimensionVersionIDs)
}

func scanCertifiedExample(row rowScanner, value *CertifiedExample) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.Question,
		&value.ExpectedMetricVersionIDs, &value.ExpectedDimensionVersionIDs,
		&value.ExpectedTimeExpression, &value.Notes)
}

func scanMetricDimension(row rowScanner, value *MetricDimension) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.MetricVersionID,
		&value.DimensionVersionID, &value.Compatible, &value.Role)
}

func scanRelationship(row rowScanner, value *Relationship) error {
	return row.Scan(&value.ID, &value.TenantID, &value.DomainID, &value.ObjectID,
		&value.VersionNo, &value.Status, &value.ContentHash, &value.OwnerID,
		&value.CreatedAt, &value.UpdatedAt, &value.LeftModelVersionID,
		&value.RightModelVersionID, &value.Type, &value.JoinType,
		&value.Cardinality, &value.JoinAST, &value.FanoutPolicy,
		&value.BridgeModelVersionID)
}

func insertSemanticModelAdminTx(ctx context.Context, tx pgx.Tx, value *SemanticModel) error {
	return tx.QueryRow(ctx, `INSERT INTO askdata.semantic_models(
		id,tenant_id,domain_id,model_id,version_no,code,name,description,
		entity_version_id,dataset_id,dataset_version_id,materialization_id,
		dataset_schema_hash,layer,grain_contract,primary_time_field_id,
		time_contract_version_id,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid,$10,$11,$12,
		$13,$14,$15,$16,NULLIF($17,'')::uuid,'DRAFT',$18,$19)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID,
		value.ObjectID, value.VersionNo, value.Code, value.Name, value.Description,
		value.EntityVersionID, value.DatasetID, value.DatasetVersionID,
		value.MaterializationID, value.DatasetSchemaHash, value.Layer,
		value.GrainContract, value.PrimaryTimeFieldID, value.TimeContractVersionID,
		value.ContentHash, value.OwnerID).Scan(&value.CreatedAt, &value.UpdatedAt)
}

func insertMeasureAdminTx(ctx context.Context, tx pgx.Tx, value *Measure) error {
	return tx.QueryRow(ctx, `INSERT INTO askdata.measures(
		id,tenant_id,domain_id,measure_id,version_no,semantic_model_version_id,
		code,name,description,formula_ast,aggregation,additivity,
		semi_additive_time_aggregation,aggregation_restriction,non_additive_dimensions,
		data_type,unit,currency,zero_denominator_policy,display_precision,
		additivity_suggestion,additivity_confirmed_by,additivity_confirmed_at,
		status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,'')::text,
		NULLIF($13,'')::text,NULLIF($14,'')::text,$15,$16,$17,NULLIF($18,'')::text,
		$19,$20,NULLIF($21,'')::text,NULLIF($22,'')::uuid,$23,'DRAFT',$24,$25)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID,
		value.ObjectID, value.VersionNo, value.SemanticModelVersionID, value.Code,
		value.Name, value.Description, value.FormulaAST, value.Aggregation,
		value.Additivity, value.SemiAdditiveTimeAggregation, value.AggregationRestriction,
		value.NonAdditiveDimensions, value.DataType, value.Unit, value.Currency,
		value.ZeroDenominatorPolicy, value.DisplayPrecision, value.AdditivitySuggestion,
		value.AdditivityConfirmedBy, value.AdditivityConfirmedAt, value.ContentHash,
		value.OwnerID).Scan(&value.CreatedAt, &value.UpdatedAt)
}

func insertMetricVersionAdminTx(ctx context.Context, tx pgx.Tx, value *MetricVersion) error {
	if err := tx.QueryRow(ctx, `INSERT INTO askdata.metric_versions(
		id,tenant_id,domain_id,metric_id,version_no,semantic_model_version_id,
		formula_ast,default_filters_ast,unit,currency,time_grain,additivity,
		semi_additive_time_aggregation,aggregation_restriction,non_additive_dimensions,
		zero_denominator_policy,display_precision,additivity_suggestion,
		additivity_confirmed_by,additivity_confirmed_at,null_policy,
		incomplete_period_policy_override,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,'')::text,$11,NULLIF($12,'')::text,
		NULLIF($13,'')::text,NULLIF($14,'')::text,$15,$16,$17,NULLIF($18,'')::text,
		NULLIF($19,'')::uuid,$20,$21,NULLIF($22,'')::text,'DRAFT',$23,$24)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID, value.MetricID,
		value.VersionNo, value.SemanticModelVersionID, value.FormulaAST,
		value.DefaultFiltersAST, value.Unit, value.Currency, value.TimeGrain, value.Additivity,
		value.SemiAdditiveTimeAggregation, value.AggregationRestriction, value.NonAdditiveDimensions,
		value.ZeroDenominatorPolicy, value.DisplayPrecision, value.AdditivitySuggestion,
		value.AdditivityConfirmedBy, value.AdditivityConfirmedAt, value.NullPolicy,
		value.IncompletePeriodPolicyOverride, value.ContentHash,
		value.OwnerID).Scan(&value.CreatedAt, &value.UpdatedAt); err != nil {
		return err
	}
	for index, measureID := range value.MeasureVersionIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO askdata.metric_version_measures(
			tenant_id,domain_id,metric_version_id,measure_version_id,ordinal
		) VALUES($1,$2,$3,$4,$5)`, value.TenantID, value.DomainID, value.ID,
			measureID, index+1); err != nil {
			return err
		}
	}
	return nil
}

func insertDimensionAdminTx(ctx context.Context, tx pgx.Tx, value *Dimension) error {
	return tx.QueryRow(ctx, `INSERT INTO askdata.dimensions(
		id,tenant_id,domain_id,dimension_id,version_no,semantic_model_version_id,
		logical_field_id,code,name,description,dimension_kind,sensitivity,
		member_index_policy,high_cardinality,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'DRAFT',$15,$16)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID,
		value.ObjectID, value.VersionNo, value.SemanticModelVersionID,
		value.LogicalFieldID, value.Code, value.Name, value.Description, value.Kind,
		value.Sensitivity, value.MemberIndexPolicy, value.HighCardinality,
		value.ContentHash, value.OwnerID).Scan(&value.CreatedAt, &value.UpdatedAt)
}

func insertBusinessTermAdminTx(ctx context.Context, tx pgx.Tx, value *BusinessTerm) error {
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.business_terms(
		id,tenant_id,domain_id,term,term_type,created_by
	) VALUES($1,$2,$3,$4,$5,$6)
	ON CONFLICT(id) DO NOTHING`, value.ObjectID, value.TenantID, value.DomainID,
		value.Term, value.TermType, value.OwnerID); err != nil {
		return err
	}
	return tx.QueryRow(ctx, `INSERT INTO askdata.business_term_versions(
		id,tenant_id,domain_id,business_term_id,version_no,status,
		target_object_type,target_version_id,target_code,match_mode,match_pattern,
		priority,negative_contexts,applicable_role_ids,valid_from,valid_to,
		source,review_status,code,name,definition,aliases,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$8,$9,NULLIF($10,''),$11,
		$12,$13,$14,$15,$16,'PENDING',$17,$18,$19,$20,$21,$22)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID,
		value.ObjectID, value.VersionNo, value.TargetObjectType, value.TargetVersionID,
		value.TargetCode, value.MatchMode, value.MatchPattern, value.Priority,
		value.NegativeContexts, value.ApplicableRoleIDs, value.ValidFrom, value.ValidTo,
		value.Source, value.Code, value.Name, value.Definition, value.Aliases,
		value.ContentHash, value.OwnerID).
		Scan(&value.CreatedAt, &value.UpdatedAt)
}

func insertKPIBundleAdminTx(ctx context.Context, tx pgx.Tx, value *KPIBundle) error {
	if _, err := tx.Exec(ctx, `INSERT INTO askdata.kpi_bundles(
		id,tenant_id,domain_id,code,name,owner_user_id
	) VALUES($1,$2,$3,$4,$5,$6)
	ON CONFLICT(id) DO NOTHING`, value.ObjectID, value.TenantID, value.DomainID,
		value.Code, value.Name, value.OwnerID); err != nil {
		return err
	}
	items, err := CanonicalValue(value.Items)
	if err != nil {
		return err
	}
	return tx.QueryRow(ctx, `INSERT INTO askdata.kpi_bundle_versions(
		id,tenant_id,domain_id,kpi_bundle_id,version_no,status,items,
		default_dimension_version_ids,default_time_expression,default_chart_types,
		role_mapping,applicable_question_patterns,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,'DRAFT',$6,$7,$8,$9,$10,$11,$12,$13)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID,
		value.ObjectID, value.VersionNo, items, value.DefaultDimensionVersionIDs,
		value.DefaultTimeExpression, value.DefaultChartTypes, value.RoleMapping,
		value.ApplicableQuestionPatterns, value.ContentHash, value.OwnerID).
		Scan(&value.CreatedAt, &value.UpdatedAt)
}

func insertRelationshipAdminTx(ctx context.Context, tx pgx.Tx, value *Relationship) error {
	return tx.QueryRow(ctx, `INSERT INTO askdata.relationships(
		id,tenant_id,domain_id,relationship_id,version_no,left_model_version_id,
		right_model_version_id,relationship_type,join_type,cardinality,join_ast,
		fanout_policy,bridge_model_version_id,status,content_hash,owner_id
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NULLIF($13,'')::uuid,'DRAFT',$14,$15)
	RETURNING created_at,updated_at`, value.ID, value.TenantID, value.DomainID,
		value.ObjectID, value.VersionNo, value.LeftModelVersionID,
		value.RightModelVersionID, value.Type, value.JoinType, value.Cardinality,
		value.JoinAST, value.FanoutPolicy, value.BridgeModelVersionID,
		value.ContentHash, value.OwnerID).
		Scan(&value.CreatedAt, &value.UpdatedAt)
}

func newVersionIdentity(
	scope AdminScope,
	input VersionedDraftInput,
	requestID string,
	resource AdminResource,
) (VersionIdentity, error) {
	if input.ExpectedUpdatedAt != nil {
		return VersionIdentity{}, fmt.Errorf("%w: expectedUpdatedAt is not accepted on create", ErrRegistryInvalidRequest)
	}
	objectID := input.ObjectID
	if objectID == "" {
		objectID = stableAdminID(requestID, string(resource), "object")
	}
	if !canonicalAdminUUID(objectID) {
		return VersionIdentity{}, fmt.Errorf("%w: objectId must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	if input.VersionNo < 1 {
		return VersionIdentity{}, ValidationErrors{Issues: []ValidationIssue{{
			Code: validationCodeRequired, Path: "versionNo", Message: "must be positive",
		}}}
	}
	ownerID := defaultAdminOwner(input.OwnerID, scope.ActorID)
	if !canonicalAdminUUID(ownerID) {
		return VersionIdentity{}, fmt.Errorf("%w: ownerId must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	return VersionIdentity{
		TenantID: scope.TenantID, DomainID: scope.DomainID, ObjectID: objectID,
		VersionNo: input.VersionNo, Status: VersionStatusDraft, OwnerID: ownerID,
	}, nil
}

func validateVersionedUpdate(input VersionedDraftInput, current VersionIdentity) error {
	if input.ExpectedUpdatedAt == nil || !input.ExpectedUpdatedAt.Equal(current.UpdatedAt) {
		return ErrRegistryVersionConflict
	}
	if input.ObjectID != "" && input.ObjectID != current.ObjectID {
		return fmt.Errorf("%w: objectId cannot change", ErrRegistryInvalidRequest)
	}
	if input.VersionNo != 0 && input.VersionNo != current.VersionNo {
		return fmt.Errorf("%w: versionNo cannot change", ErrRegistryInvalidRequest)
	}
	if input.OwnerID != "" && !canonicalAdminUUID(input.OwnerID) {
		return fmt.Errorf("%w: ownerId must be a canonical UUID", ErrRegistryInvalidRequest)
	}
	return nil
}

func applyMetricVersionDefaults(metric *MetricVersion) {
	if len(metric.DefaultFiltersAST) == 0 {
		metric.DefaultFiltersAST = json.RawMessage(`{"type":"TRUE"}`)
	}
	if metric.TimeGrain == "" {
		metric.TimeGrain = "NONE"
	}
	if metric.NullPolicy == "" {
		metric.NullPolicy = "PRESERVE"
	}
}

func metricVersionContentHash(metric MetricVersion) askdata.ContentHash {
	dependencies := sortedAdminIDs(metric.MeasureVersionIDs)
	contract := metricContractDocument{
		Type: "METRIC", MetricID: metric.MetricID, VersionNo: metric.VersionNo,
		SemanticModelVersionID: metric.SemanticModelVersionID,
		FormulaAST:             metric.FormulaAST, DefaultFiltersAST: metric.DefaultFiltersAST,
		Unit: metric.Unit, Currency: metric.Currency, TimeGrain: metric.TimeGrain,
		Additivity:                     metric.Additivity,
		SemiAdditiveTimeAggregation:    metric.SemiAdditiveTimeAggregation,
		AggregationRestriction:         metric.AggregationRestriction,
		NonAdditiveDimensions:          append([]string(nil), metric.NonAdditiveDimensions...),
		ZeroDenominatorPolicy:          metric.ZeroDenominatorPolicy,
		DisplayPrecision:               metric.DisplayPrecision,
		NullPolicy:                     metric.NullPolicy,
		IncompletePeriodPolicyOverride: metric.IncompletePeriodPolicyOverride,
		MeasureVersionIDs:              dependencies,
	}
	return contentHashForContract(contract)
}

func businessTermContentHash(term BusinessTerm) askdata.ContentHash {
	contract := struct {
		Type              string     `json:"type"`
		TermID            string     `json:"termId"`
		VersionNo         int        `json:"versionNo"`
		Term              string     `json:"term"`
		TermType          string     `json:"termType"`
		TargetObjectType  string     `json:"targetObjectType"`
		TargetVersionID   string     `json:"targetVersionId"`
		TargetCode        string     `json:"targetCode"`
		MatchMode         string     `json:"matchMode"`
		MatchPattern      string     `json:"matchPattern,omitempty"`
		Priority          int        `json:"priority"`
		NegativeContexts  []string   `json:"negativeContexts"`
		ApplicableRoleIDs []string   `json:"applicableRoleIds"`
		ValidFrom         *time.Time `json:"validFrom,omitempty"`
		ValidTo           *time.Time `json:"validTo,omitempty"`
		Source            string     `json:"source"`
		Code              string     `json:"code"`
		Name              string     `json:"name"`
		Definition        string     `json:"definition"`
		Aliases           []string   `json:"aliases"`
	}{
		"BUSINESS_TERM", term.ObjectID, term.VersionNo, term.Term, term.TermType,
		term.TargetObjectType, term.TargetVersionID, term.TargetCode, term.MatchMode,
		term.MatchPattern, term.Priority, sortedAdminAliases(term.NegativeContexts),
		sortedAdminIDs(term.ApplicableRoleIDs), term.ValidFrom, term.ValidTo, term.Source,
		term.Code, term.Name, term.Definition, sortedAdminAliases(term.Aliases),
	}
	return contentHashForContract(contract)
}

func relationshipContentHash(relationship Relationship) askdata.ContentHash {
	contract := struct {
		Type                 string           `json:"type"`
		RelationshipID       string           `json:"relationshipId"`
		VersionNo            int              `json:"versionNo"`
		LeftModelVersionID   string           `json:"leftModelVersionId"`
		RightModelVersionID  string           `json:"rightModelVersionId"`
		RelationshipType     RelationshipType `json:"relationshipType"`
		JoinType             JoinType         `json:"joinType"`
		Cardinality          Cardinality      `json:"cardinality"`
		JoinAST              json.RawMessage  `json:"joinAst"`
		FanoutPolicy         FanoutPolicy     `json:"fanoutPolicy"`
		BridgeModelVersionID string           `json:"bridgeModelVersionId,omitempty"`
	}{
		"RELATIONSHIP", relationship.ObjectID, relationship.VersionNo,
		relationship.LeftModelVersionID, relationship.RightModelVersionID,
		relationship.Type, relationship.JoinType, relationship.Cardinality,
		relationship.JoinAST, relationship.FanoutPolicy, relationship.BridgeModelVersionID,
	}
	return contentHashForContract(contract)
}

func versionWriteResult(resource AdminResource, identity VersionIdentity) AdminWriteResult {
	updatedAt := identity.UpdatedAt
	return AdminWriteResult{
		ResourceType: resource, ResourceID: identity.ID, ObjectID: identity.ObjectID,
		ContentHash: identity.ContentHash, Status: string(identity.Status),
		UpdatedAt: &updatedAt,
	}
}

func metricWriteResult(metric Metric) AdminWriteResult {
	updatedAt := metric.UpdatedAt
	return AdminWriteResult{
		ResourceType: AdminResourceMetric, ResourceID: metric.ID,
		Status: metric.Status, RecordVersion: metric.Version, UpdatedAt: &updatedAt,
	}
}

func versionIdentityOf(value any) VersionIdentity {
	switch typed := value.(type) {
	case SemanticModel:
		return typed.VersionIdentity
	case Measure:
		return typed.VersionIdentity
	case MetricVersion:
		return typed.VersionIdentity
	case Dimension:
		return typed.VersionIdentity
	case BusinessTerm:
		return typed.VersionIdentity
	case KPIBundle:
		return typed.VersionIdentity
	case Relationship:
		return typed.VersionIdentity
	default:
		panic("unsupported versioned semantic draft")
	}
}

func adminTable(resource AdminResource) (string, error) {
	switch resource {
	case AdminResourceSemanticModel:
		return "semantic_models", nil
	case AdminResourceMeasure:
		return "measures", nil
	case AdminResourceMetricVersion:
		return "metric_versions", nil
	case AdminResourceDimension:
		return "dimensions", nil
	case AdminResourceBusinessTerm:
		return "business_term_versions", nil
	case AdminResourceKPIBundle:
		return "kpi_bundle_versions", nil
	case AdminResourceRelationship:
		return "relationships", nil
	default:
		return "", fmt.Errorf("%w: unsupported semantic resource", ErrRegistryInvalidRequest)
	}
}

func stableAdminID(parts ...string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("askdata/admin/v1/"+strings.Join(parts, "\x00"))).String()
}

func defaultAdminOwner(ownerID, actorID string) string {
	if ownerID == "" {
		return actorID
	}
	return ownerID
}

func updatedAdminOwner(ownerID, current string) string {
	if ownerID == "" {
		return current
	}
	return ownerID
}

func sortedAdminIDs(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sortedAdminAliases(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.TrimSpace(value)
	}
	sort.Slice(result, func(left, right int) bool {
		return strings.ToLower(result[left]) < strings.ToLower(result[right])
	})
	return result
}

func canonicalAdminUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && parsed.String() == strings.ToLower(value)
}

func adminNoRows(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRegistryNotFound
	}
	return err
}

func adminForeignKeyViolation(err error) bool {
	var databaseError *pgconn.PgError
	return errors.As(err, &databaseError) && databaseError.Code == "23503"
}

func normalizeAdminStoreError(err error) error {
	if err == nil || errors.Is(err, ErrRegistryNotFound) ||
		errors.Is(err, ErrRegistryVersionConflict) || errors.Is(err, ErrRegistryConflict) ||
		errors.Is(err, ErrRegistryPermissionDenied) ||
		errors.Is(err, ErrRegistryIdempotencyConflict) ||
		errors.Is(err, ErrRegistryDraftInUse) || errors.Is(err, ErrRegistryInvalidRequest) {
		return err
	}
	var validation ValidationErrors
	if errors.As(err, &validation) {
		return validation
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) {
		switch databaseError.Code {
		case "23505":
			return ErrRegistryConflict
		case "23503", "23514", "22P02", "22001":
			return fmt.Errorf("%w: semantic draft violates a governed database constraint", ErrRegistryInvalidRequest)
		case "55000":
			return ErrRegistryVersionConflict
		}
	}
	return err
}
