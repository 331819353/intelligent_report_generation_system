package report

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/platform/database"
	"intelligent-report-generation-system/internal/reportjson"
)

type PostgresStore struct{ pool *pgxpool.Pool }

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore { return &PostgresStore{pool: pool} }

// Replay 精确返回首次保存的响应快照；同一个键绑定不同请求时失败关闭。
func (s *PostgresStore) Replay(ctx context.Context, tenantID, actorID, reportID, scope, key, requestHash string) (record DraftRecord, found bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if scope != "CREATE" && scope != "UPDATE" {
			return ErrInvalidRequest
		}
		// 幂等响应仍包含完整草稿，重放前必须按当前权限重新授权，不能只依赖首次保存时的权限。
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, scope, reportID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return replayTx(ctx, tx, actorID, reportID, scope, key, requestHash, &record, &found)
	})
	return record, found, err
}

func (s *PostgresStore) Create(ctx context.Context, tenantID, actorID string, plan CreatePlan) (record DraftRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, tenantID, "CREATE", plan.IdempotencyKey); err != nil {
			return err
		}
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "CREATE", "")
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var found bool
		if err := replayTx(ctx, tx, actorID, "", "CREATE", plan.IdempotencyKey, plan.RequestHash, &record, &found); err != nil || found {
			return err
		}
		report := plan.Prepared.Document.Report
		if _, err := tx.Exec(ctx, `INSERT INTO platform.reports(id,tenant_id,code,name,description,report_type,status,created_by,updated_by) VALUES($1,$2,$3,$4,$5,$6,'DRAFT',$7,$7)`, plan.ID, tenantID, report.Code, report.Name, report.Description, report.Type, actorID); err != nil {
			return err
		}
		// 只有全局 CREATE 的分析员也必须能继续读取和编辑自己刚创建的对象。
		if _, err := tx.Exec(ctx, `INSERT INTO platform.object_permissions(tenant_id,subject_type,subject_id,object_type,object_id,action,granted_by) VALUES($1,'USER',$2,'REPORT',$3,'READ',$2),($1,'USER',$2,'REPORT',$3,'UPDATE',$2) ON CONFLICT DO NOTHING`, tenantID, actorID, plan.ID); err != nil {
			return err
		}
		editorJSON, err := json.Marshal(plan.EditorState)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_drafts(report_id,tenant_id,schema_version,definition_json,definition_hash,revision_no,editor_state_json,updated_by) VALUES($1,$2,$3,$4,$5,1,$6,$7)`, plan.ID, tenantID, plan.Prepared.Document.SchemaVersion, plan.Prepared.JSON, plan.Prepared.Hash, editorJSON, actorID); err != nil {
			return err
		}
		createPatch, err := json.Marshal([]map[string]any{{"op": "add", "path": "", "value": json.RawMessage(plan.Prepared.JSON)}})
		if err != nil {
			return err
		}
		sum := sha256.Sum256(createPatch)
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_revisions(tenant_id,report_id,base_revision_no,revision_no,idempotency_key,request_hash,change_index,change_count,operation_type,source,target_json,patch_json,patch_count,patch_hash,before_hash,after_hash,actor_user_id) VALUES($1,$2,0,1,$3,$4,1,1,'REPORT_CREATE','USER','{}',$5,1,$6,$7,$8,$9)`, tenantID, plan.ID, plan.IdempotencyKey, plan.RequestHash, createPatch, hex.EncodeToString(sum[:]), strings.Repeat("0", 64), plan.Prepared.Hash, actorID); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, plan.ID, 1, plan.Components, plan.Dependencies); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'CREATE','REPORT',$3,jsonb_build_object('revision',1,'definitionHash',$4::text))`, tenantID, actorID, plan.ID, plan.Prepared.Hash); err != nil {
			return err
		}
		if err := scanDraftTx(ctx, tx, plan.ID, &record); err != nil {
			return err
		}
		return insertIdempotency(ctx, tx, tenantID, actorID, "CREATE", plan.ID, plan.IdempotencyKey, plan.RequestHash, 201, record)
	})
	if err != nil {
		return DraftRecord{}, mapPostgresError(err)
	}
	return record, nil
}

func (s *PostgresStore) Get(ctx context.Context, tenantID, actorID, id, action string) (record DraftRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// 先受 RLS 约束地确认对象存在，再在同一租户事务中按当前操作者授权，避免跨租户枚举。
		if err := scanDraftTx(ctx, tx, id, &record); err != nil {
			return err
		}
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, action, id)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return nil
	})
	if err != nil {
		return DraftRecord{}, err
	}
	return record, nil
}

func (s *PostgresStore) Update(ctx context.Context, tenantID, actorID, id string, plan UpdatePlan) (record DraftRecord, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, tenantID, "UPDATE:"+id, plan.IdempotencyKey); err != nil {
			return err
		}
		// 权限必须在保存事务内重验，不能把前端 report:edit 或 HTTP 中间件当作授权事实。
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "UPDATE", id)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var found bool
		if err := replayTx(ctx, tx, actorID, id, "UPDATE", plan.IdempotencyKey, plan.RequestHash, &record, &found); err != nil || found {
			return err
		}
		var currentRevision int64
		var currentHash string
		err = tx.QueryRow(ctx, `SELECT d.revision_no,d.definition_hash FROM platform.reports r JOIN platform.report_drafts d ON d.report_id=r.id AND d.tenant_id=r.tenant_id WHERE r.id=$1 AND r.deleted_at IS NULL FOR UPDATE OF r,d`, id).Scan(&currentRevision, &currentHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentRevision != plan.ExpectedRevision || len(plan.Changes) == 0 || plan.Changes[0].BeforeHash != currentHash {
			return &ConflictError{Revision: currentRevision, Hash: currentHash}
		}
		reportOccupied, occupiedBlocks, err := occupiedResources(ctx, tx, id, plan.AffectedBlockIDs)
		if err != nil {
			return err
		}
		if reportOccupied || len(occupiedBlocks) > 0 {
			return &OccupiedError{ReportOccupied: reportOccupied, BlockIDs: occupiedBlocks}
		}
		priorOperations := make(map[string]PreparedChange, len(plan.Changes))
		for _, change := range plan.Changes {
			if change.ReferencedOperationID == "" {
				priorOperations[change.ClientOperationID] = change
				continue
			}
			// 同一保存批次按 changes 顺序形成修订，因此撤销/重做可引用本批次更早的操作，不能前向引用。
			var referencedTarget json.RawMessage
			var referencedType, referencedBeforeHash, referencedAfterHash string
			if prior, exists := priorOperations[change.ReferencedOperationID]; exists {
				referencedTarget, referencedType = prior.TargetJSON, prior.OperationType
				referencedBeforeHash, referencedAfterHash = prior.BeforeHash, prior.After.Hash
			} else {
				if err := tx.QueryRow(ctx, `SELECT target_json,operation_type,before_hash,after_hash FROM platform.report_revisions WHERE report_id=$1 AND client_operation_id=$2`, id, change.ReferencedOperationID).Scan(&referencedTarget, &referencedType, &referencedBeforeHash, &referencedAfterHash); errors.Is(err, pgx.ErrNoRows) {
					return fmt.Errorf("%w: referencedOperationId 不属于当前报告或不是本批次更早的操作", ErrInvalidRequest)
				} else if err != nil {
					return err
				}
			}
			if referencedType == "UNDO" || referencedType == "REDO" || !sameSemanticTarget(referencedTarget, change.TargetJSON) {
				return fmt.Errorf("%w: 撤销或重做引用的操作目标不一致", ErrInvalidRequest)
			}
			// 整份规范哈希必须精确回到被引用修订的 before/after，禁止把同实体的另一项合法修改伪装成补偿操作。
			invalidUndo := change.OperationType == "UNDO" && (change.BeforeHash != referencedAfterHash || change.After.Hash != referencedBeforeHash)
			invalidRedo := change.OperationType == "REDO" && (change.BeforeHash != referencedBeforeHash || change.After.Hash != referencedAfterHash)
			if invalidUndo || invalidRedo {
				return fmt.Errorf("%w: 撤销或重做与被引用操作不是精确互逆", ErrInvalidRequest)
			}
			priorOperations[change.ClientOperationID] = change
		}
		baseRevision := currentRevision
		beforeHash := currentHash
		for index, change := range plan.Changes {
			if change.BeforeHash != beforeHash {
				return ErrPatchMismatch
			}
			revision := baseRevision + int64(index) + 1
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_revisions(tenant_id,report_id,base_revision_no,revision_no,idempotency_key,request_hash,change_index,change_count,client_operation_id,operation_type,source,target_json,patch_json,patch_count,patch_hash,before_hash,after_hash,actor_user_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`, tenantID, id, revision-1, revision, plan.IdempotencyKey, plan.RequestHash, index+1, len(plan.Changes), change.ClientOperationID, change.OperationType, change.Source, change.TargetJSON, change.PatchJSON, patchOperationCount(change.PatchJSON), change.PatchHash, change.BeforeHash, change.After.Hash, actorID); err != nil {
				return err
			}
			beforeHash = change.After.Hash
		}
		finalRevision := baseRevision + int64(len(plan.Changes))
		editorJSON, err := json.Marshal(plan.EditorState)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.report_drafts SET schema_version=$1,definition_json=$2,definition_hash=$3,revision_no=$4,editor_state_json=$5,updated_by=$6 WHERE report_id=$7`, plan.Final.Document.SchemaVersion, plan.Final.JSON, plan.Final.Hash, finalRevision, editorJSON, actorID, id); err != nil {
			return err
		}
		report := plan.Final.Document.Report
		if _, err := tx.Exec(ctx, `UPDATE platform.reports SET name=$1,description=$2,version=version+$3,updated_by=$4 WHERE id=$5`, report.Name, report.Description, len(plan.Changes), actorID, id); err != nil {
			return err
		}
		if err := replaceDerived(ctx, tx, tenantID, id, finalRevision, plan.Components, plan.Dependencies); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail) VALUES($1,$2,'UPDATE_DRAFT','REPORT',$3,jsonb_build_object('fromRevision',$4::bigint,'toRevision',$5::bigint,'changeCount',$6::int,'definitionHash',$7::text,'idempotencyKey',$8::text))`, tenantID, actorID, id, baseRevision, finalRevision, len(plan.Changes), plan.Final.Hash, plan.IdempotencyKey); err != nil {
			return err
		}
		if err := scanDraftTx(ctx, tx, id, &record); err != nil {
			return err
		}
		return insertIdempotency(ctx, tx, tenantID, actorID, "UPDATE", id, plan.IdempotencyKey, plan.RequestHash, 200, record)
	})
	if err != nil {
		return DraftRecord{}, mapPostgresError(err)
	}
	return record, nil
}

func (s *PostgresStore) ResolvePublicationDependencies(ctx context.Context, tenantID, actorID, reportID string, dependencies []DependencyIndex) (snapshots []DependencySnapshot, issues []ValidationIssue, err error) {
	snapshots, issues = []DependencySnapshot{}, []ValidationIssue{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.reports WHERE id=$1 AND deleted_at IS NULL)`, reportID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		canUpdate, err := allowedTx(ctx, tx, tenantID, actorID, "UPDATE", reportID)
		if err != nil {
			return err
		}
		canPublish, err := allowedTx(ctx, tx, tenantID, actorID, "PUBLISH", reportID)
		if err != nil {
			return err
		}
		if !canUpdate && !canPublish {
			return ErrForbidden
		}
		snapshots, issues, err = validatePublicationDependenciesTx(ctx, tx, tenantID, actorID, dependencies, false)
		return err
	})
	return snapshots, issues, err
}

func (s *PostgresStore) ReplayPublication(ctx context.Context, tenantID, actorID, reportID, operation, key, requestHash string) (record PublishedVersion, found bool, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if operation != "PUBLISH" && operation != "ROLLBACK" {
			return ErrInvalidRequest
		}
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "PUBLISH", reportID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		return replayPublicationTx(ctx, tx, actorID, reportID, operation, key, requestHash, &record, &found)
	})
	return record, found, err
}

func (s *PostgresStore) Publish(ctx context.Context, tenantID, actorID, reportID string, plan PublishPlan) (record PublishedVersion, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, tenantID, "REPORT:PUBLISH:"+reportID, plan.IdempotencyKey); err != nil {
			return err
		}
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "PUBLISH", reportID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var found bool
		if err := replayPublicationTx(ctx, tx, actorID, reportID, "PUBLISH", plan.IdempotencyKey, plan.RequestHash, &record, &found); err != nil || found {
			return err
		}
		var currentRevision int64
		var currentHash string
		err = tx.QueryRow(ctx, `SELECT d.revision_no,d.definition_hash
			FROM platform.reports r JOIN platform.report_drafts d ON d.report_id=r.id AND d.tenant_id=r.tenant_id
			WHERE r.id=$1 AND r.deleted_at IS NULL FOR UPDATE OF r,d`, reportID).Scan(&currentRevision, &currentHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if currentRevision != plan.ExpectedRevision || currentHash != publicationSourceHash(plan.Prepared) {
			return &ConflictError{Revision: currentRevision, Hash: currentHash}
		}
		_, dependencies := deriveIndexes(plan.Prepared.Document)
		resolved, validationIssues, err := validatePublicationDependenciesTx(ctx, tx, tenantID, actorID, dependencies, true)
		if err != nil {
			return err
		}
		if len(validationIssues) > 0 {
			return &PublicationValidationError{Issues: validationIssues}
		}
		if !sameDependencySnapshots(resolved, plan.Dependencies) {
			return ErrDependencyChanged
		}
		var alreadyPublished bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.report_versions WHERE report_id=$1 AND source_revision_no=$2 AND source_definition_hash=$3)`, reportID, plan.ExpectedRevision, currentHash).Scan(&alreadyPublished); err != nil {
			return err
		}
		if alreadyPublished {
			return ErrAlreadyPublished
		}
		var nextVersion int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(max(version_no),0)+1 FROM platform.report_versions WHERE report_id=$1`, reportID).Scan(&nextVersion); err != nil {
			return err
		}
		var versionID, publishedAt string
		err = tx.QueryRow(ctx, `INSERT INTO platform.report_versions(
			tenant_id,report_id,version_no,source_revision_no,source_definition_hash,schema_version,
			definition_json,definition_bytes,definition_hash,size_bytes,object_uri,publish_comment,published_by
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id::text,published_at::text`, tenantID, reportID, nextVersion, plan.ExpectedRevision, currentHash,
			plan.Prepared.Document.SchemaVersion, plan.Prepared.JSON, plan.Prepared.JSON, plan.Prepared.Hash, len(plan.Prepared.JSON), plan.ObjectURI, plan.Comment, actorID).Scan(&versionID, &publishedAt)
		if err != nil {
			return err
		}
		for _, component := range plan.Components {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_version_component_indexes(
				tenant_id,report_version_id,report_id,page_id,block_id,component_id,component_type
			) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, versionID, reportID, component.PageID, component.BlockID, component.ComponentID, component.ComponentType); err != nil {
				return err
			}
		}
		for _, dependency := range plan.Dependencies {
			if _, err := tx.Exec(ctx, `INSERT INTO platform.report_version_dependencies(
				tenant_id,report_version_id,report_id,dependency_type,dependency_id,referenced_version_id,json_path
			) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, versionID, reportID, dependency.Type, dependency.ID, dependency.VersionID, dependency.Path); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.reports SET current_published_version_id=$1,status='PUBLISHED',version=version+1,updated_by=$2 WHERE id=$3`, versionID, actorID, reportID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'PUBLISH','REPORT',$3,jsonb_build_object('version',$4::int,'sourceRevision',$5::bigint,'sha256',$6::text,'sizeBytes',$7::bigint))`, tenantID, actorID, reportID, nextVersion, plan.ExpectedRevision, plan.Prepared.Hash, len(plan.Prepared.JSON)); err != nil {
			return err
		}
		record = PublishedVersion{ID: versionID, ReportID: reportID, Version: nextVersion, SourceRevision: plan.ExpectedRevision, SchemaVersion: plan.Prepared.Document.SchemaVersion, SHA256: plan.Prepared.Hash, SizeBytes: int64(len(plan.Prepared.JSON)), Comment: plan.Comment, PublishedBy: actorID, PublishedAt: publishedAt, Current: true, ObjectURI: plan.ObjectURI}
		return insertPublicationIdempotency(ctx, tx, tenantID, actorID, reportID, "PUBLISH", plan.IdempotencyKey, plan.RequestHash, record)
	})
	if err != nil {
		return PublishedVersion{}, mapPostgresError(err)
	}
	return record, nil
}

func (s *PostgresStore) ListVersions(ctx context.Context, tenantID, actorID, reportID string, limit, offset int) (items []PublishedVersion, total int, err error) {
	items = []PublishedVersion{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "READ", reportID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.reports WHERE id=$1 AND deleted_at IS NULL)`, reportID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.report_versions WHERE report_id=$1`, reportID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT v.id::text,v.report_id::text,v.version_no,v.source_revision_no,v.schema_version,
			v.definition_hash,v.size_bytes,v.publish_comment,COALESCE(v.published_by::text,''),v.published_at::text,
			(r.current_published_version_id=v.id)
			FROM platform.report_versions v JOIN platform.reports r ON r.id=v.report_id AND r.tenant_id=v.tenant_id
			WHERE v.report_id=$1 ORDER BY (r.current_published_version_id=v.id) DESC,v.version_no DESC LIMIT $2 OFFSET $3`, reportID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item PublishedVersion
			if err := rows.Scan(&item.ID, &item.ReportID, &item.Version, &item.SourceRevision, &item.SchemaVersion, &item.SHA256, &item.SizeBytes, &item.Comment, &item.PublishedBy, &item.PublishedAt, &item.Current); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func (s *PostgresStore) GetVersionArtifact(ctx context.Context, tenantID, actorID, reportID string, version int) (artifact VersionArtifact, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "READ", reportID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var item PublishedVersion
		err = tx.QueryRow(ctx, `SELECT v.id::text,v.report_id::text,v.version_no,v.source_revision_no,v.schema_version,
			v.definition_hash,v.size_bytes,v.publish_comment,COALESCE(v.published_by::text,''),v.published_at::text,
			(r.current_published_version_id=v.id),v.definition_bytes,COALESCE(v.object_uri,'')
			FROM platform.report_versions v JOIN platform.reports r ON r.id=v.report_id AND r.tenant_id=v.tenant_id
			WHERE v.report_id=$1 AND v.version_no=$2 AND r.deleted_at IS NULL`, reportID, version).Scan(
			&item.ID, &item.ReportID, &item.Version, &item.SourceRevision, &item.SchemaVersion, &item.SHA256,
			&item.SizeBytes, &item.Comment, &item.PublishedBy, &item.PublishedAt, &item.Current, &artifact.Definition, &item.ObjectURI)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		artifact.Version = item
		return nil
	})
	return artifact, err
}

func (s *PostgresStore) Rollback(ctx context.Context, tenantID, actorID, reportID string, version int, idempotencyKey, requestHash, comment string) (record PublishedVersion, err error) {
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		if err := lockIdempotency(ctx, tx, tenantID, "REPORT:ROLLBACK:"+reportID, idempotencyKey); err != nil {
			return err
		}
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "PUBLISH", reportID)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		var found bool
		if err := replayPublicationTx(ctx, tx, actorID, reportID, "ROLLBACK", idempotencyKey, requestHash, &record, &found); err != nil || found {
			return err
		}
		var currentID string
		if err := tx.QueryRow(ctx, `SELECT COALESCE(current_published_version_id::text,'') FROM platform.reports WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, reportID).Scan(&currentID); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id::text,report_id::text,version_no,source_revision_no,schema_version,definition_hash,
			size_bytes,publish_comment,COALESCE(published_by::text,''),published_at::text
			FROM platform.report_versions WHERE report_id=$1 AND version_no=$2`, reportID, version).Scan(
			&record.ID, &record.ReportID, &record.Version, &record.SourceRevision, &record.SchemaVersion, &record.SHA256,
			&record.SizeBytes, &record.Comment, &record.PublishedBy, &record.PublishedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrVersionNotFound
		}
		if err != nil {
			return err
		}
		if currentID == record.ID {
			record.Current = true
			return insertPublicationIdempotency(ctx, tx, tenantID, actorID, reportID, "ROLLBACK", idempotencyKey, requestHash, record)
		}
		if _, err := tx.Exec(ctx, `UPDATE platform.reports SET current_published_version_id=$1,status='PUBLISHED',version=version+1,updated_by=$2 WHERE id=$3`, record.ID, actorID, reportID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.audit_logs(tenant_id,actor_user_id,action,resource_type,resource_id,detail)
			VALUES($1,$2,'ROLLBACK','REPORT',$3,jsonb_build_object('targetVersion',$4::int,'previousVersionId',COALESCE($5::text,''),'comment',$6::text))`, tenantID, actorID, reportID, version, currentID, comment); err != nil {
			return err
		}
		record.Current = true
		return insertPublicationIdempotency(ctx, tx, tenantID, actorID, reportID, "ROLLBACK", idempotencyKey, requestHash, record)
	})
	return record, err
}

func validatePublicationDependenciesTx(ctx context.Context, tx pgx.Tx, tenantID, actorID string, dependencies []DependencyIndex, lock bool) ([]DependencySnapshot, []ValidationIssue, error) {
	snapshots := make([]DependencySnapshot, 0, len(dependencies))
	issues := []ValidationIssue{}
	for _, dependency := range dependencies {
		switch dependency.Type {
		case "SOURCE_TRACE":
			snapshots = append(snapshots, DependencySnapshot{Type: "SOURCE_TRACE", ID: dependency.ID, Path: dependency.Path})
		case "DATASET_VERSION":
			if uuid.Validate(dependency.ID) != nil {
				issues = append(issues, dependencyValidationIssue("DATASET_VERSION_NOT_FOUND", dependency, "数据集版本不存在或不可发布"))
				continue
			}
			query := `SELECT v.id::text,d.id::text FROM platform.dataset_versions v
				JOIN platform.datasets d ON d.id=v.dataset_id AND d.tenant_id=v.tenant_id
				WHERE v.id=$1 AND v.status='PUBLISHED' AND d.status='PUBLISHED'
				  AND d.current_published_version_id=v.id AND d.deleted_at IS NULL`
			if lock {
				query += ` FOR SHARE OF v,d`
			}
			var versionID, datasetID string
			err := tx.QueryRow(ctx, query, dependency.ID).Scan(&versionID, &datasetID)
			if errors.Is(err, pgx.ErrNoRows) {
				issues = append(issues, dependencyValidationIssue("DATASET_VERSION_NOT_FOUND", dependency, "数据集版本不存在、未发布或已失效"))
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			allowed, err := resourceAllowedTx(ctx, tx, tenantID, actorID, "DATASET", "READ", datasetID)
			if err != nil {
				return nil, nil, err
			}
			if !allowed {
				issues = append(issues, dependencyValidationIssue("DATASET_FORBIDDEN", dependency, "没有数据集读取权限"))
				continue
			}
			snapshots = append(snapshots, DependencySnapshot{Type: "DATASET_VERSION", ID: dependency.ID, VersionID: versionID, Path: dependency.Path})
		case "METRIC":
			if uuid.Validate(dependency.ID) != nil {
				issues = append(issues, dependencyValidationIssue("METRIC_VERSION_NOT_FOUND", dependency, "指标不存在或没有可发布版本"))
				continue
			}
			query := `SELECT m.id::text,v.id::text FROM platform.metrics m
				JOIN platform.metric_versions v ON v.id=m.current_published_version_id AND v.metric_id=m.id AND v.tenant_id=m.tenant_id
				WHERE m.id=$1 AND m.status='PUBLISHED' AND v.status='PUBLISHED' AND m.deleted_at IS NULL`
			if lock {
				query += ` FOR SHARE OF m,v`
			}
			var metricID, versionID string
			err := tx.QueryRow(ctx, query, dependency.ID).Scan(&metricID, &versionID)
			if errors.Is(err, pgx.ErrNoRows) {
				issues = append(issues, dependencyValidationIssue("METRIC_VERSION_NOT_FOUND", dependency, "指标不存在、未发布或已失效"))
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			allowed, err := resourceAllowedTx(ctx, tx, tenantID, actorID, "METRIC", "READ", metricID)
			if err != nil {
				return nil, nil, err
			}
			if !allowed {
				issues = append(issues, dependencyValidationIssue("METRIC_FORBIDDEN", dependency, "没有指标读取权限"))
				continue
			}
			snapshots = append(snapshots, DependencySnapshot{Type: "METRIC_VERSION", ID: dependency.ID, VersionID: versionID, Path: dependency.Path})
		}
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return dependencySnapshotKey(snapshots[i]) < dependencySnapshotKey(snapshots[j])
	})
	sortValidationIssues(issues)
	return snapshots, issues, nil
}

func dependencyValidationIssue(code string, dependency DependencyIndex, message string) ValidationIssue {
	return ValidationIssue{Level: "error", Code: code, Path: dependency.Path, Message: message, DependencyID: dependency.ID}
}

func resourceAllowedTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, resourceType, action, objectID string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM platform.user_roles ur
		JOIN platform.roles r ON r.tenant_id=ur.tenant_id AND r.id=ur.role_id AND r.status='ACTIVE' AND r.deleted_at IS NULL
		JOIN platform.role_permissions rp ON rp.tenant_id=ur.tenant_id AND rp.role_id=ur.role_id
		JOIN platform.permissions p ON p.tenant_id=rp.tenant_id AND p.id=rp.permission_id
		WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND p.resource_type=$3 AND p.action=$4
		UNION ALL
		SELECT 1 FROM platform.object_permissions op
		WHERE op.tenant_id=$1 AND op.object_type=$3 AND op.object_id=$5 AND op.action=$4
		  AND (op.subject_type='USER' AND op.subject_id=$2 OR op.subject_type='ROLE' AND EXISTS (
		    SELECT 1 FROM platform.user_roles ur JOIN platform.roles r ON r.tenant_id=ur.tenant_id AND r.id=ur.role_id
		    WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND ur.role_id=op.subject_id AND r.status='ACTIVE' AND r.deleted_at IS NULL))
	)`, tenantID, actorID, resourceType, action, objectID).Scan(&allowed)
	return allowed, err
}

func publicationSourceHash(prepared reportjson.Prepared) string {
	publication, _ := prepared.Document.Extensions["publication"].(map[string]any)
	value, _ := publication["sourceHash"].(string)
	return value
}

func sameDependencySnapshots(left, right []DependencySnapshot) bool {
	leftCopy, rightCopy := append([]DependencySnapshot(nil), left...), append([]DependencySnapshot(nil), right...)
	sort.Slice(leftCopy, func(i, j int) bool { return dependencySnapshotKey(leftCopy[i]) < dependencySnapshotKey(leftCopy[j]) })
	sort.Slice(rightCopy, func(i, j int) bool { return dependencySnapshotKey(rightCopy[i]) < dependencySnapshotKey(rightCopy[j]) })
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func dependencySnapshotKey(value DependencySnapshot) string {
	return value.Type + "\x00" + value.ID + "\x00" + value.VersionID + "\x00" + value.Path
}

func replayPublicationTx(ctx context.Context, tx pgx.Tx, actorID, reportID, operation, key, requestHash string, record *PublishedVersion, found *bool) error {
	var storedActorID, storedHash string
	var response []byte
	err := tx.QueryRow(ctx, `SELECT actor_user_id::text,request_hash,response_json FROM platform.report_publication_idempotency
		WHERE report_id=$1 AND operation=$2 AND idempotency_key=$3`, reportID, operation, key).Scan(&storedActorID, &storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		*found = false
		return nil
	}
	if err != nil {
		return err
	}
	if storedActorID != actorID || storedHash != requestHash {
		return ErrIdempotencyConflict
	}
	if err := json.Unmarshal(response, record); err != nil {
		return err
	}
	*found = true
	return nil
}

func insertPublicationIdempotency(ctx context.Context, tx pgx.Tx, tenantID, actorID, reportID, operation, key, requestHash string, record PublishedVersion) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.report_publication_idempotency(
		tenant_id,report_id,actor_user_id,operation,idempotency_key,request_hash,response_json
	) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, reportID, actorID, operation, key, requestHash, payload)
	return err
}

func sameSemanticTarget(referenced, current json.RawMessage) bool {
	var referencedTarget, currentTarget map[string]any
	if json.Unmarshal(referenced, &referencedTarget) != nil || json.Unmarshal(current, &currentTarget) != nil {
		return false
	}
	delete(referencedTarget, "referencedOperationId")
	delete(currentTarget, "referencedOperationId")
	return reflect.DeepEqual(referencedTarget, currentTarget)
}

func (s *PostgresStore) ListRevisions(ctx context.Context, tenantID, actorID, id string, limit, offset int) (items []RevisionRecord, total int, err error) {
	items = []RevisionRecord{}
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM platform.reports WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		allowed, err := allowedTx(ctx, tx, tenantID, actorID, "READ", id)
		if err != nil {
			return err
		}
		if !allowed {
			return ErrForbidden
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM platform.report_revisions WHERE report_id=$1`, id).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text,base_revision_no,revision_no,COALESCE(client_operation_id::text,''),operation_type,source,target_json,patch_json,patch_hash,before_hash,after_hash,COALESCE(actor_user_id::text,''),created_at::text FROM platform.report_revisions WHERE report_id=$1 ORDER BY revision_no DESC LIMIT $2 OFFSET $3`, id, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RevisionRecord
			var targetJSON []byte
			if err := rows.Scan(&item.ID, &item.BaseRevision, &item.Revision, &item.ClientOperationID, &item.OperationType, &item.Source, &targetJSON, &item.Patch, &item.PatchHash, &item.BeforeHash, &item.AfterHash, &item.ActorUserID, &item.CreatedAt); err != nil {
				return err
			}
			if err := json.Unmarshal(targetJSON, &item.Target); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, total, err
}

func replayTx(ctx context.Context, tx pgx.Tx, actorID, reportID, scope, key, requestHash string, record *DraftRecord, found *bool) error {
	query := `SELECT actor_user_id::text,request_hash,response_json FROM platform.report_idempotency_records WHERE scope=$1 AND idempotency_key=$2`
	args := []any{scope, key}
	if scope == "UPDATE" {
		query += ` AND report_id=$3`
		args = append(args, reportID)
	}
	var storedActorID, storedHash string
	var response []byte
	err := tx.QueryRow(ctx, query, args...).Scan(&storedActorID, &storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		*found = false
		return nil
	}
	if err != nil {
		return err
	}
	// 幂等键只属于首次请求的可信操作者，不能成为同租户跨用户读取响应的旁路凭据。
	if storedActorID != actorID || storedHash != requestHash {
		return ErrIdempotencyConflict
	}
	if err := json.Unmarshal(response, record); err != nil {
		return err
	}
	*found = true
	return nil
}

func lockIdempotency(ctx context.Context, tx pgx.Tx, tenantID, scope, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, tenantID+":"+scope+":"+key)
	return err
}

func insertIdempotency(ctx context.Context, tx pgx.Tx, tenantID, actorID, scope, reportID, key, requestHash string, status int, record DraftRecord) error {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO platform.report_idempotency_records(tenant_id,actor_user_id,scope,report_id,idempotency_key,request_hash,http_status,response_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, tenantID, actorID, scope, reportID, key, requestHash, status, payload)
	return err
}

func scanDraftTx(ctx context.Context, tx pgx.Tx, id string, record *DraftRecord) error {
	var editorJSON []byte
	err := tx.QueryRow(ctx, `SELECT r.id::text,r.code::text,r.name,r.description,r.report_type,r.status,d.revision_no,d.definition_hash,d.definition_json,d.editor_state_json,r.created_at::text,d.updated_at::text FROM platform.reports r JOIN platform.report_drafts d ON d.report_id=r.id AND d.tenant_id=r.tenant_id WHERE r.id=$1 AND r.deleted_at IS NULL`, id).Scan(&record.ID, &record.Code, &record.Name, &record.Description, &record.Type, &record.Status, &record.Revision, &record.DefinitionHash, &record.Definition, &editorJSON, &record.CreatedAt, &record.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(editorJSON, &record.EditorState)
}

func allowedTx(ctx context.Context, tx pgx.Tx, tenantID, actorID, action, reportID string) (bool, error) {
	var objectID any
	if reportID != "" {
		objectID = reportID
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (
      SELECT 1 FROM platform.user_roles ur
      JOIN platform.roles r ON r.tenant_id=ur.tenant_id AND r.id=ur.role_id AND r.status='ACTIVE' AND r.deleted_at IS NULL
      JOIN platform.role_permissions rp ON rp.tenant_id=ur.tenant_id AND rp.role_id=ur.role_id
      JOIN platform.permissions p ON p.tenant_id=rp.tenant_id AND p.id=rp.permission_id
      WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND p.resource_type='REPORT' AND p.action=$3
      UNION ALL
      SELECT 1 FROM platform.object_permissions op
      WHERE op.tenant_id=$1 AND op.object_type='REPORT' AND op.object_id=$4 AND op.action=$3
        AND (op.subject_type='USER' AND op.subject_id=$2 OR op.subject_type='ROLE' AND EXISTS (
          SELECT 1 FROM platform.user_roles ur JOIN platform.roles r ON r.tenant_id=ur.tenant_id AND r.id=ur.role_id
          WHERE ur.tenant_id=$1 AND ur.user_id=$2 AND ur.role_id=op.subject_id AND r.status='ACTIVE' AND r.deleted_at IS NULL))
    )`, tenantID, actorID, action, objectID).Scan(&allowed)
	return allowed, err
}

func occupiedResources(ctx context.Context, tx pgx.Tx, reportID string, blockIDs []string) (bool, []string, error) {
	rows, err := tx.Query(ctx, `SELECT block_id FROM platform.report_edit_guards WHERE report_id=$1 AND released_at IS NULL AND expires_at>now() AND (block_id IS NULL OR block_id=ANY($2::text[])) ORDER BY block_id NULLS FIRST`, reportID, blockIDs)
	if err != nil {
		return false, nil, err
	}
	defer rows.Close()
	reportOccupied := false
	result := []string{}
	for rows.Next() {
		var id *string
		if err := rows.Scan(&id); err != nil {
			return false, nil, err
		}
		if id == nil {
			reportOccupied = true
			continue
		}
		result = append(result, *id)
	}
	return reportOccupied, result, rows.Err()
}

func replaceDerived(ctx context.Context, tx pgx.Tx, tenantID, reportID string, revision int64, components []ComponentIndex, dependencies []DependencyIndex) error {
	if _, err := tx.Exec(ctx, `DELETE FROM platform.report_draft_component_indexes WHERE report_id=$1`, reportID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM platform.report_draft_dependencies WHERE report_id=$1`, reportID); err != nil {
		return err
	}
	for _, component := range components {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_draft_component_indexes(tenant_id,report_id,revision_no,page_id,block_id,component_id,component_type) VALUES($1,$2,$3,$4,$5,$6,$7)`, tenantID, reportID, revision, component.PageID, component.BlockID, component.ComponentID, component.ComponentType); err != nil {
			return err
		}
	}
	for _, dependency := range dependencies {
		if _, err := tx.Exec(ctx, `INSERT INTO platform.report_draft_dependencies(tenant_id,report_id,revision_no,dependency_type,dependency_id,json_path) VALUES($1,$2,$3,$4,$5,$6)`, tenantID, reportID, revision, dependency.Type, dependency.ID, dependency.Path); err != nil {
			return err
		}
	}
	return nil
}

func patchOperationCount(raw json.RawMessage) int {
	var operations []any
	_ = json.Unmarshal(raw, &operations)
	return len(operations)
}

func mapPostgresError(err error) error {
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != "23505" {
		return err
	}
	if strings.Contains(pgError.ConstraintName, "reports_tenant_id_code") {
		return ErrAlreadyExists
	}
	if pgError.ConstraintName == "report_idempotency_create_idx" || pgError.ConstraintName == "report_idempotency_update_idx" {
		return ErrIdempotencyConflict
	}
	if pgError.ConstraintName == "report_publication_idempotency_key" {
		return ErrIdempotencyConflict
	}
	if pgError.ConstraintName == "report_versions_source_revision_key" {
		return ErrAlreadyPublished
	}
	if strings.Contains(pgError.ConstraintName, "report_revisions_tenant_id_report_id_client_operation_id") {
		return fmt.Errorf("%w: clientOperationId 已用于当前报告", ErrInvalidRequest)
	}
	return err
}
