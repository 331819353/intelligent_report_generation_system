package decision

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	"intelligent-report-generation-system/internal/platform/database"
)

// PostgresEvidenceVerifier resolves polymorphic references against immutable
// source facts. It never copies source rows into the decision schema.
type PostgresEvidenceVerifier struct{ pool *pgxpool.Pool }

func NewPostgresEvidenceVerifier(pool *pgxpool.Pool) *PostgresEvidenceVerifier {
	return &PostgresEvidenceVerifier{pool: pool}
}

// Resolve derives every immutable evidence identity from an authorized source.
// Callers supply only the source kind and ID; hashes, release, as-of and policy
// scope are never trusted from the browser.
func (verifier *PostgresEvidenceVerifier) Resolve(ctx context.Context, identity Identity, sourceType SourceType, sourceID askdata.ID) (EvidenceInput, error) {
	if verifier == nil || verifier.pool == nil || identity.Validate() != nil || !validUUID(sourceID) ||
		(sourceType != SourceAnswerArtifact && sourceType != SourceReportVersion && sourceType != SourceInsightArtifact) {
		return EvidenceInput{}, ErrEvidenceInvalid
	}
	input := EvidenceInput{SourceType: sourceType, SourceID: sourceID}
	err := database.WithTenantTx(ctx, verifier.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		switch sourceType {
		case SourceAnswerArtifact:
			var dataAsOf string
			if err := tx.QueryRow(ctx, `SELECT artifact.artifact_hash,run.release_id::text,run.release_content_hash,
				run.policy_scope_hash,COALESCE((SELECT candidate.data_as_of FROM (
				  SELECT summary.artifact_index,COALESCE(summary.payload_json#>>'{resolvedTimeSpec,dataAvailableThrough}',
				    summary.payload_json#>>'{result,resolvedTimeSpec,dataAvailableThrough}',
				    summary.payload_json#>>'{result,evidence,quality,dataAsOf}',
				    summary.payload_json#>>'{result,evidence,time,end}',
				    summary.payload_json#>>'{result,summary,time,end}') AS data_as_of
				  FROM askdata.question_artifacts summary WHERE summary.tenant_id=run.tenant_id
				    AND summary.question_run_id=run.id AND summary.artifact_type IN ('RESULT_SUMMARY','ANSWER')
				) candidate WHERE btrim(COALESCE(candidate.data_as_of,''))<>''
				  ORDER BY candidate.artifact_index DESC LIMIT 1),'')
				FROM askdata.question_artifacts artifact JOIN askdata.question_runs run
				  ON run.tenant_id=artifact.tenant_id AND run.id=artifact.question_run_id
				WHERE artifact.tenant_id=$1 AND artifact.domain_id=$2 AND artifact.id=$3
				  AND artifact.artifact_type='ANSWER' AND run.current_state='ANSWERED'
				  AND run.completion_artifact_hash=artifact.artifact_hash`, identity.TenantID, identity.DomainID, sourceID).
				Scan(&input.SourceHash, &input.SemanticReleaseID, &input.SemanticReleaseHash, &input.PolicyScopeHash, &dataAsOf); err != nil {
				return err
			}
			parsed, err := time.Parse(time.RFC3339Nano, dataAsOf)
			if err != nil {
				return ErrEvidenceInvalid
			}
			input.AsOf, input.Summary = parsed.UTC(), "已固定的问数答案证据"
		case SourceReportVersion:
			if err := tx.QueryRow(ctx, `SELECT version.definition_hash,version.published_at
				FROM platform.report_versions version JOIN platform.reports report
				  ON report.tenant_id=version.tenant_id AND report.id=version.report_id
				WHERE version.tenant_id=$1 AND report.domain_id=$2 AND version.id=$3 AND version.artifact_state='READY'`,
				identity.TenantID, identity.DomainID, sourceID).Scan(&input.SourceHash, &input.AsOf); err != nil {
				return err
			}
			var dependencyCount int
			var dependencyID string
			if err := tx.QueryRow(ctx, `SELECT count(*),COALESCE(min(dependency_id),'')
				FROM platform.report_version_dependencies WHERE tenant_id=$1 AND report_version_id=$2
				  AND dependency_type='SEMANTIC_RELEASE'`, identity.TenantID, sourceID).Scan(&dependencyCount, &dependencyID); err != nil {
				return err
			}
			switch dependencyCount {
			case 0:
				if err := tx.QueryRow(ctx, `SELECT id::text,content_hash FROM askdata.releases
					WHERE tenant_id=$1 AND domain_id=$2 AND status='ACTIVE'
					ORDER BY activated_at DESC NULLS LAST,id DESC LIMIT 1`, identity.TenantID, identity.DomainID).
					Scan(&input.SemanticReleaseID, &input.SemanticReleaseHash); err != nil {
					return err
				}
				input.Summary = "已固定的发布报告版本证据（数据集报告按创建决策时的有效语义治理范围校验）"
			case 1:
				if err := tx.QueryRow(ctx, `SELECT id::text,content_hash FROM askdata.releases
					WHERE tenant_id=$1 AND domain_id=$2 AND id::text=$3 AND status IN ('ACTIVE','SUPERSEDED','RETAINED')`,
					identity.TenantID, identity.DomainID, dependencyID).Scan(&input.SemanticReleaseID, &input.SemanticReleaseHash); err != nil {
					return err
				}
				input.Summary = "已固定的发布报告版本证据"
			default:
				return ErrEvidenceInvalid
			}
			return resolveCurrentPolicyHashTx(ctx, tx, identity, &input)
		case SourceInsightArtifact:
			var raw []byte
			if err := tx.QueryRow(ctx, `SELECT insight.artifact_json,insight.created_at,release.id::text,release.content_hash
				FROM platform.report_insight_artifacts insight JOIN platform.reports report
				  ON report.tenant_id=insight.tenant_id AND report.id=insight.report_id
				JOIN platform.report_version_dependencies dependency ON dependency.tenant_id=report.tenant_id
				  AND dependency.report_version_id=report.current_published_version_id AND dependency.dependency_type='SEMANTIC_RELEASE'
				JOIN askdata.releases release ON release.tenant_id=report.tenant_id AND release.domain_id=report.domain_id
				  AND release.id::text=dependency.dependency_id
				WHERE insight.tenant_id=$1 AND report.domain_id=$2 AND insight.id=$3 AND insight.status IN ('CURRENT','STALE')
				  AND (SELECT count(*) FROM platform.report_version_dependencies dependency_count
				    WHERE dependency_count.tenant_id=report.tenant_id AND dependency_count.report_version_id=report.current_published_version_id
				      AND dependency_count.dependency_type='SEMANTIC_RELEASE')=1`, identity.TenantID, identity.DomainID, sourceID).
				Scan(&raw, &input.AsOf, &input.SemanticReleaseID, &input.SemanticReleaseHash); err != nil {
				return err
			}
			_, hash, err := canonicalJSON(raw)
			if err != nil {
				return err
			}
			input.SourceHash = hash
			input.Summary = "已固定的报告洞察证据"
			return resolveCurrentPolicyHashTx(ctx, tx, identity, &input)
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) || err != nil || input.Validate() != nil {
		return EvidenceInput{}, ErrEvidenceInvalid
	}
	return input, nil
}

func resolveCurrentPolicyHashTx(ctx context.Context, tx pgx.Tx, identity Identity, input *EvidenceInput) error {
	rows, err := tx.Query(ctx, `SELECT role.id::text FROM platform.user_roles assignment
		JOIN platform.roles role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
		WHERE assignment.tenant_id=$1 AND assignment.user_id=$2 AND role.status='ACTIVE' AND role.deleted_at IS NULL
		ORDER BY role.id`, identity.TenantID, identity.ActorID)
	if err != nil {
		return err
	}
	defer rows.Close()
	roleIDs := []askdata.ID{}
	for rows.Next() {
		var id askdata.ID
		if err = rows.Scan(&id); err != nil {
			return err
		}
		roleIDs = append(roleIDs, id)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	scope, err := askdata.NewPolicyScope(identity.TenantID, identity.ActorID, []askdata.ID{identity.DomainID}, roleIDs,
		askdata.ReleaseRef{ReleaseID: input.SemanticReleaseID, ContentHash: input.SemanticReleaseHash})
	if err != nil {
		return err
	}
	input.PolicyScopeHash = scope.PolicyHash
	return nil
}

func (verifier *PostgresEvidenceVerifier) Verify(ctx context.Context, identity Identity, input EvidenceInput) (Evidence, error) {
	if verifier == nil || verifier.pool == nil || identity.Validate() != nil || input.Validate() != nil {
		return Evidence{}, ErrEvidenceInvalid
	}
	result := Evidence{SchemaVersion: SchemaVersion, ID: askdata.ID(""), SourceType: input.SourceType, SourceID: input.SourceID,
		SourceHash: input.SourceHash, SemanticReleaseID: input.SemanticReleaseID, SemanticReleaseHash: input.SemanticReleaseHash,
		DataSnapshotID: input.DataSnapshotID, AsOf: input.AsOf.UTC(), PolicyScopeHash: input.PolicyScopeHash, Summary: input.Summary, Verified: true}
	err := database.WithTenantTx(ctx, verifier.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var releaseHash string
		if err := tx.QueryRow(ctx, `SELECT content_hash FROM askdata.releases
			WHERE tenant_id=$1 AND domain_id=$2 AND id=$3 AND status IN ('ACTIVE','SUPERSEDED','RETAINED')`,
			identity.TenantID, identity.DomainID, input.SemanticReleaseID).Scan(&releaseHash); err != nil {
			return err
		}
		if askdata.ContentHash(releaseHash) != input.SemanticReleaseHash {
			return ErrEvidenceInvalid
		}
		switch input.SourceType {
		case SourceAnswerArtifact:
			var hash, releaseID, contentHash, scopeHash, resultHash, dataAsOf string
			var answerPayload []byte
			var runID askdata.ID
			var completedAt time.Time
			err := tx.QueryRow(ctx, `SELECT artifact.artifact_hash,artifact.payload_json,run.id::text,
				run.release_id::text,run.release_content_hash,run.policy_scope_hash,run.result_hash,run.completed_at,
				COALESCE((SELECT candidate.data_as_of FROM (
				  SELECT summary.artifact_index,COALESCE(summary.payload_json#>>'{resolvedTimeSpec,dataAvailableThrough}',
				    summary.payload_json#>>'{result,resolvedTimeSpec,dataAvailableThrough}',
				    summary.payload_json#>>'{result,evidence,quality,dataAsOf}',
				    summary.payload_json#>>'{result,evidence,time,end}',
				    summary.payload_json#>>'{result,summary,time,end}') AS data_as_of
				  FROM askdata.question_artifacts summary
				  WHERE summary.tenant_id=run.tenant_id AND summary.question_run_id=run.id
				    AND summary.artifact_type IN ('RESULT_SUMMARY','ANSWER')
				) candidate WHERE btrim(COALESCE(candidate.data_as_of,''))<>''
				  ORDER BY candidate.artifact_index DESC LIMIT 1),'')
			FROM askdata.question_artifacts artifact
			JOIN askdata.question_runs run ON run.id=artifact.question_run_id AND run.tenant_id=artifact.tenant_id
			WHERE artifact.tenant_id=$1 AND artifact.domain_id=$2 AND artifact.id=$3
			  AND artifact.artifact_type='ANSWER' AND run.current_state='ANSWERED'
			  AND run.completion_artifact_hash=artifact.artifact_hash`, identity.TenantID, identity.DomainID, input.SourceID).
				Scan(&hash, &answerPayload, &runID, &releaseID, &contentHash, &scopeHash, &resultHash, &completedAt, &dataAsOf)
			if err != nil {
				return err
			}
			answerArtifact, answerErr := decodePersistedAnswer(answerPayload)
			verifiedAsOf, asOfErr := time.Parse(time.RFC3339Nano, dataAsOf)
			if askdata.ContentHash(hash) != input.SourceHash || askdata.ID(releaseID) != input.SemanticReleaseID ||
				askdata.ContentHash(contentHash) != input.SemanticReleaseHash || askdata.ContentHash(scopeHash) != input.PolicyScopeHash ||
				answerErr != nil || answerArtifact.RunID != runID || answerArtifact.Provenance.SemanticReleaseID != input.SemanticReleaseID ||
				answerArtifact.Provenance.ResultHash != askdata.ContentHash(resultHash) ||
				(!answerArtifact.Verification.Passed && !answerArtifact.Verification.Degraded) || asOfErr != nil ||
				!input.AsOf.UTC().Equal(verifiedAsOf.UTC()) || verifiedAsOf.After(completedAt) {
				return ErrEvidenceInvalid
			}
			result.AsOf = verifiedAsOf.UTC()
			if answerArtifact.Verification.Degraded {
				result.Summary = "已验证结构化答案（叙述已降级）：" + input.Summary
			}
		case SourceReportVersion:
			var hash string
			var publishedAt time.Time
			err := tx.QueryRow(ctx, `SELECT version.definition_hash,version.published_at
			FROM platform.report_versions version JOIN platform.reports report
			  ON report.id=version.report_id AND report.tenant_id=version.tenant_id
			WHERE version.tenant_id=$1 AND report.domain_id=$2 AND version.id=$3
			  AND version.artifact_state='READY' AND (
			    EXISTS(SELECT 1 FROM platform.report_version_dependencies dependency
			      WHERE dependency.tenant_id=version.tenant_id AND dependency.report_version_id=version.id
			        AND dependency.dependency_type='SEMANTIC_RELEASE' AND dependency.dependency_id=$4::text)
			    OR (NOT EXISTS(SELECT 1 FROM platform.report_version_dependencies dependency
			      WHERE dependency.tenant_id=version.tenant_id AND dependency.report_version_id=version.id
			        AND dependency.dependency_type='SEMANTIC_RELEASE')
			      AND EXISTS(SELECT 1 FROM askdata.releases release WHERE release.tenant_id=version.tenant_id
			        AND release.domain_id=report.domain_id AND release.id=$4::uuid AND release.status='ACTIVE'))
			  )`, identity.TenantID, identity.DomainID, input.SourceID, input.SemanticReleaseID).Scan(&hash, &publishedAt)
			if err != nil {
				return err
			}
			if askdata.ContentHash(hash) != input.SourceHash || !input.AsOf.UTC().Equal(publishedAt.UTC()) {
				return ErrEvidenceInvalid
			}
			result.AsOf = publishedAt.UTC()
		case SourceInsightArtifact:
			var raw []byte
			var createdAt time.Time
			err := tx.QueryRow(ctx, `SELECT insight.artifact_json,insight.created_at
			FROM platform.report_insight_artifacts insight JOIN platform.reports report
			  ON report.id=insight.report_id AND report.tenant_id=insight.tenant_id
			WHERE insight.tenant_id=$1 AND report.domain_id=$2 AND insight.id=$3
			  AND insight.status IN ('CURRENT','STALE') AND EXISTS(
			    SELECT 1 FROM platform.report_version_dependencies dependency
			    WHERE dependency.tenant_id=report.tenant_id AND dependency.report_version_id=report.current_published_version_id
			      AND dependency.dependency_type='SEMANTIC_RELEASE' AND dependency.dependency_id=$4::text
			  )`, identity.TenantID, identity.DomainID, input.SourceID, input.SemanticReleaseID).Scan(&raw, &createdAt)
			if err != nil {
				return err
			}
			canonical, hash, err := canonicalJSON(json.RawMessage(raw))
			if err != nil || len(canonical) == 0 || hash != input.SourceHash {
				return ErrEvidenceInvalid
			}
			if !input.AsOf.UTC().Equal(createdAt.UTC()) {
				return ErrEvidenceInvalid
			}
			result.AsOf = createdAt.UTC()
		default:
			return ErrEvidenceInvalid
		}
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrEvidenceInvalid
	}
	if err != nil {
		return Evidence{}, err
	}
	return result, nil
}

func decodePersistedAnswer(raw json.RawMessage) (answer.AnswerArtifact, error) {
	if value, err := answer.Decode(raw); err == nil {
		return value, nil
	}
	var envelope struct {
		Artifact json.RawMessage `json:"artifact"`
		Answer   json.RawMessage `json:"answer"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return answer.AnswerArtifact{}, ErrEvidenceInvalid
	}
	artifact := envelope.Artifact
	if len(artifact) == 0 {
		artifact = envelope.Answer
	}
	if len(artifact) == 0 {
		return answer.AnswerArtifact{}, ErrEvidenceInvalid
	}
	return answer.Decode(artifact)
}
