package dataset

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const semanticNamingWriteGateSQL = `SELECT pg_advisory_xact_lock(
	hashtextextended(
		'semantic-governance-write:'||platform.current_tenant_id()::text,
		0
	)
)`

func saveSemanticNamingTagsTx(
	ctx context.Context,
	tx pgx.Tx,
	actorID, datasetID, versionID string,
	prepared Prepared,
	clearStaleSuggestions bool,
) error {
	evidence := prepared.SemanticNaming
	if evidence == nil {
		return nil
	}
	if uuid.Validate(evidence.AIRequestID) != nil ||
		evidence.PromptVersion == "" || len(evidence.Tags) > 16 {
		return fmt.Errorf("%w: invalid semantic naming evidence", ErrSemanticNamingInvalid)
	}
	if _, err := tx.Exec(ctx, semanticNamingWriteGateSQL); err != nil {
		return err
	}
	var requestValid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM platform.ai_requests
		WHERE id=$1::uuid
		  AND actor_user_id=$2::uuid
		  AND purpose='DATASET_SEMANTIC_NAMING'
		  AND prompt_version=$3
		  AND resource_type='DATASET_DRAFT_SAVE'
		  AND status='SUCCEEDED'
	)`, evidence.AIRequestID, actorID, evidence.PromptVersion).Scan(&requestValid); err != nil {
		return err
	}
	if !requestValid {
		return fmt.Errorf("%w: AI request audit is unavailable", ErrSemanticNamingInvalid)
	}
	if clearStaleSuggestions {
		if _, err := tx.Exec(ctx, `UPDATE platform.asset_tag_bindings
			SET status='REJECTED',
			    confidence=NULL,
			    evidence_json=evidence_json||jsonb_build_object(
			      'supersededBySemanticNaming',$3::text
			    ),
			    assigned_by=NULLIF($4,'')::uuid,
			    approved_by=NULL,
			    approved_at=NULL
			WHERE asset_type='DATASET_VERSION'
			  AND dataset_id=$1::uuid
			  AND dataset_version_id=$2::uuid
			  AND origin='LLM'
			  AND status='SUGGESTED'`,
			datasetID, versionID, evidence.AIRequestID, actorID,
		); err != nil {
			return err
		}
	}
	seen := map[string]bool{}
	for _, suggestion := range evidence.Tags {
		if uuid.Validate(suggestion.TagID) != nil || seen[suggestion.TagID] ||
			suggestion.Confidence < 0 || suggestion.Confidence > 1 ||
			suggestion.TagCode == "" || suggestion.TagName == "" ||
			suggestion.Category == "" || suggestion.Rationale == "" {
			return fmt.Errorf("%w: invalid controlled tag suggestion", ErrSemanticNamingInvalid)
		}
		seen[suggestion.TagID] = true
		var taxonomyValid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
			SELECT 1
			FROM platform.semantic_tags
			WHERE id=$1::uuid
			  AND code::text=$2
			  AND name=$3
			  AND category=$4
			  AND governance='CONTROLLED'
			  AND status='ACTIVE'
		)`, suggestion.TagID, suggestion.TagCode, suggestion.TagName,
			suggestion.Category).Scan(&taxonomyValid); err != nil {
			return err
		}
		if !taxonomyValid {
			return fmt.Errorf("%w: controlled taxonomy changed", ErrSemanticNamingInvalid)
		}
		detail, err := json.Marshal(map[string]any{
			"aiRequestId":             evidence.AIRequestID,
			"promptVersion":           evidence.PromptVersion,
			"schemaHash":              prepared.DSLHash,
			"category":                suggestion.Category,
			"rationale":               suggestion.Rationale,
			"containsBusinessSamples": false,
			"decisionPoint":           "DAG_SAVE",
		})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO platform.asset_tag_bindings(
				tenant_id,tag_id,asset_type,dataset_id,dataset_version_id,
				origin,status,confidence,evidence_json,assigned_by
			) VALUES(
				platform.current_tenant_id(),$1::uuid,'DATASET_VERSION',$2::uuid,$3::uuid,
				'LLM','SUGGESTED',$4,$5,NULLIF($6,'')::uuid
			)
			ON CONFLICT(
				tenant_id,tag_id,dataset_version_id
			) WHERE asset_type='DATASET_VERSION'
			DO UPDATE SET
				status='SUGGESTED',
				confidence=EXCLUDED.confidence,
				evidence_json=EXCLUDED.evidence_json,
				assigned_by=EXCLUDED.assigned_by,
				approved_by=NULL,
				approved_at=NULL
			WHERE asset_tag_bindings.origin='LLM'
			  AND asset_tag_bindings.status='REJECTED'
			  AND asset_tag_bindings.evidence_json ? 'supersededBySemanticNaming'`,
			suggestion.TagID, datasetID, versionID, suggestion.Confidence,
			detail, actorID,
		); err != nil {
			return err
		}
	}
	return nil
}

// copyDraftDatasetTagsTx freezes the governed draft vocabulary onto the new
// immutable published version. The asynchronous publication tag job may still
// add missing suggestions, but publication no longer has a window with no tags
// or a second LLM decision that contradicts the save-time decision.
func copyDraftDatasetTagsTx(
	ctx context.Context,
	tx pgx.Tx,
	datasetID, draftVersionID, publishedVersionID string,
) error {
	if _, err := tx.Exec(ctx, semanticNamingWriteGateSQL); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO platform.asset_tag_bindings(
			tenant_id,tag_id,asset_type,dataset_id,dataset_version_id,
			origin,status,confidence,evidence_json,assigned_by,
			approved_by,approved_at
		)
		SELECT
			platform.current_tenant_id(),binding.tag_id,'DATASET_VERSION',
			binding.dataset_id,$3::uuid,binding.origin,binding.status,
			binding.confidence,binding.evidence_json,binding.assigned_by,
			binding.approved_by,binding.approved_at
		FROM platform.asset_tag_bindings AS binding
		JOIN platform.semantic_tags AS tag
		  ON tag.id=binding.tag_id
		 AND tag.tenant_id=binding.tenant_id
		 AND tag.status='ACTIVE'
		WHERE binding.asset_type='DATASET_VERSION'
		  AND binding.dataset_id=$1::uuid
		  AND binding.dataset_version_id=$2::uuid
		  AND binding.status IN ('SUGGESTED','APPROVED')
		ON CONFLICT DO NOTHING`,
		datasetID, draftVersionID, publishedVersionID,
	)
	return err
}
