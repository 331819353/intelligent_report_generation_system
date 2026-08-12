package askdatahttp

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/report"
)

var ErrReportSeedInvalid = errors.New("REPORT_SEED_CONTEXT_INVALID")

type ReportSeedContextInput struct {
	ReportVersionID askdata.ID            `json:"reportVersionId"`
	ComponentID     askdata.ID            `json:"componentId"`
	SemanticIR      ircontract.SemanticIR `json:"semanticIr"`
	PinnedReleaseID askdata.ID            `json:"pinnedReleaseId"`
}

func (service *PostgresService) validateReportSeedContext(
	ctx context.Context, identity RequestIdentity, input ReportSeedContextInput,
) (orchestrator.SeedContext, askdata.ReleaseRef, error) {
	if service == nil || service.pool == nil || !canonicalUUID(input.ReportVersionID) ||
		input.ComponentID.Validate() != nil || !canonicalUUID(input.PinnedReleaseID) {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	normalizedInput, canonicalIR, irHash, err := ircontract.Canonicalize(input.SemanticIR)
	if err != nil {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	var definitionRaw []byte
	var selected askdata.ReleaseRef
	var selectedStatus string
	err = service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT version.definition_json
			FROM platform.report_versions AS version
			JOIN platform.reports AS report ON report.id=version.report_id AND report.tenant_id=version.tenant_id
			JOIN platform.domain_memberships AS membership
			  ON membership.tenant_id=report.tenant_id AND membership.domain_id=report.domain_id
			 AND membership.user_id=$4 AND membership.status='ACTIVE'
			WHERE version.id=$1 AND version.tenant_id=$2 AND report.domain_id=$3
			  AND report.status='ACTIVE' AND version.artifact_state='READY'
			  AND platform.report_v2_can_access(report.id,ARRAY['VIEW','EDIT','PUBLISH']::text[])`,
			input.ReportVersionID, identity.TenantID, identity.DomainID, identity.ActorID,
		).Scan(&definitionRaw); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT id::text,content_hash,status FROM askdata.releases
			WHERE id=$1 AND tenant_id=$2 AND domain_id=$3`,
			input.PinnedReleaseID, identity.TenantID, identity.DomainID,
		).Scan(&selected.ReleaseID, &selected.ContentHash, &selectedStatus)
	})
	if err != nil {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	definition, err := report.Decode(definitionRaw)
	if err != nil {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	seed, err := verifyReportSeedContext(definition, input, selected, selectedStatus, normalizedInput, canonicalIR, irHash)
	if err != nil {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, err
	}
	return seed, selected, nil
}

func verifyReportSeedContext(
	definition report.ReportDefinition,
	input ReportSeedContextInput,
	selected askdata.ReleaseRef,
	selectedStatus string,
	normalizedInput ircontract.SemanticIR,
	canonicalIR []byte,
	irHash askdata.ContentHash,
) (orchestrator.SeedContext, error) {
	var source *report.SemanticQueryRef
	for index := range definition.Components {
		component := definition.Components[index]
		if component.ID != input.ComponentID {
			continue
		}
		if source != nil || component.DataBinding == nil || component.DataBinding.BindingMode != report.BindingSemanticIR ||
			component.DataBinding.SemanticQueryRef == nil {
			return orchestrator.SeedContext{}, ErrReportSeedInvalid
		}
		copy := *component.DataBinding.SemanticQueryRef
		source = &copy
	}
	if source == nil {
		return orchestrator.SeedContext{}, ErrReportSeedInvalid
	}
	storedNormalized, _, storedHash, err := ircontract.Canonicalize(source.SemanticIR)
	if err != nil || storedHash != irHash || !reflect.DeepEqual(storedNormalized, normalizedInput) {
		return orchestrator.SeedContext{}, ErrReportSeedInvalid
	}
	if selected.ReleaseID == source.SemanticReleaseID {
		if selected.ContentHash != source.SemanticContentHash ||
			(selectedStatus != "ACTIVE" && selectedStatus != "SUPERSEDED" && selectedStatus != "RETAINED") {
			return orchestrator.SeedContext{}, ErrReportSeedInvalid
		}
	} else if selectedStatus != "ACTIVE" {
		// Choosing the latest definition is permitted only for the exact current
		// ACTIVE release. The historical IR remains a prior and is rebound.
		return orchestrator.SeedContext{}, ErrReportSeedInvalid
	}
	return orchestrator.SeedContext{
		Source:          orchestrator.SeedSourceReportComponent,
		ReportVersionID: input.ReportVersionID, ComponentID: input.ComponentID,
		SemanticIR: canonicalIR, SemanticIRHash: irHash, PinnedReleaseID: input.PinnedReleaseID,
	}, nil
}

func (service *PostgresService) validateSavedQuestionSeedContext(
	ctx context.Context, identity RequestIdentity, savedQuestionID askdata.ID,
) (orchestrator.SeedContext, askdata.ReleaseRef, error) {
	if service == nil || service.pool == nil || !canonicalUUID(savedQuestionID) {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	var raw []byte
	var storedIRHash askdata.ContentHash
	var release askdata.ReleaseRef
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT semantic_ir_json,semantic_ir_hash,
			semantic_release_id::text,semantic_release_content_hash
			FROM askdata.saved_questions
			WHERE id=$1 AND tenant_id=$2 AND domain_id=$3 AND status='ACTIVE'`,
			savedQuestionID, identity.TenantID, identity.DomainID,
		).Scan(&raw, &storedIRHash, &release.ReleaseID, &release.ContentHash)
	})
	if err != nil {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	semanticIR, err := ircontract.Decode(raw)
	if err != nil || semanticIR.DomainID != identity.DomainID ||
		semanticIR.SemanticReleaseID != release.ReleaseID || semanticIR.SemanticContentHash != release.ContentHash {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	_, canonical, hash, err := ircontract.Canonicalize(semanticIR)
	if err != nil || hash != storedIRHash {
		return orchestrator.SeedContext{}, askdata.ReleaseRef{}, ErrReportSeedInvalid
	}
	return orchestrator.SeedContext{
		Source: orchestrator.SeedSourceSavedQuestion, SavedQuestionID: savedQuestionID,
		SemanticIR: canonical, SemanticIRHash: hash, PinnedReleaseID: release.ReleaseID,
	}, release, nil
}

func (service *PostgresService) resolveExplicitReleaseScope(
	ctx context.Context, identity RequestIdentity, release askdata.ReleaseRef,
) (askdata.PolicyScope, error) {
	if release.Validate() != nil || !canonicalUUID(release.ReleaseID) {
		return askdata.PolicyScope{}, ErrReportSeedInvalid
	}
	roleIDs := []askdata.ID{}
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		var runnable bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM askdata.releases AS release
			WHERE release.id=$1 AND release.tenant_id=$2 AND release.domain_id=$3 AND release.content_hash=$4
			  AND (release.status IN ('ACTIVE','SUPERSEDED','RETAINED') OR (
				release.status='READY' AND EXISTS(
				  SELECT 1 FROM askdata.release_rollouts AS rollout
				  WHERE rollout.tenant_id=release.tenant_id AND rollout.domain_id=release.domain_id
				    AND rollout.candidate_release_id=release.id
				    AND rollout.state IN ('RUNNING','PAUSED','ACCEPTED')
				)
			  )))`,
			release.ReleaseID, identity.TenantID, identity.DomainID, release.ContentHash,
		).Scan(&runnable); err != nil || !runnable {
			return errors.Join(err, ErrReportSeedInvalid)
		}
		rows, err := tx.Query(ctx, `SELECT role.id::text FROM platform.user_roles AS assignment
			JOIN platform.roles AS role ON role.tenant_id=assignment.tenant_id AND role.id=assignment.role_id
			WHERE assignment.tenant_id=$1 AND assignment.user_id=$2
			  AND role.status='ACTIVE' AND role.deleted_at IS NULL ORDER BY role.id LIMIT $3`,
			identity.TenantID, identity.ActorID, askdata.MaxPolicyRoles+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id askdata.ID
			if err := rows.Scan(&id); err != nil {
				return err
			}
			roleIDs = append(roleIDs, id)
		}
		return rows.Err()
	})
	if err != nil || len(roleIDs) == 0 || len(roleIDs) > askdata.MaxPolicyRoles {
		return askdata.PolicyScope{}, fmt.Errorf("%w: resolve report seed scope", ErrReportSeedInvalid)
	}
	scope, err := askdata.NewPolicyScope(identity.TenantID, identity.ActorID, []askdata.ID{identity.DomainID}, roleIDs, release)
	if err != nil {
		return askdata.PolicyScope{}, ErrReportSeedInvalid
	}
	return scope, nil
}
