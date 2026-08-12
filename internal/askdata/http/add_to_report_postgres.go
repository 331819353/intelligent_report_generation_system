package askdatahttp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
	askcompiler "intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/askdata/orchestrator"
	"intelligent-report-generation-system/internal/askdata/reportasset"
	reportmodel "intelligent-report-generation-system/internal/report"
	"intelligent-report-generation-system/internal/report/template"
)

func (service *PostgresService) AddToReport(
	ctx context.Context, identity RequestIdentity, input AddToReportInput,
) (AddToReportResult, error) {
	if service == nil || service.pool == nil || identity.validate() != nil || input.RunID.Validate() != nil ||
		input.ReportID.Validate() != nil || input.RunVersion < 1 || input.OutcomeHash.Validate() != nil ||
		input.IdempotencyKeyHash.Validate() != nil {
		return AddToReportResult{}, ErrInvalidRequest
	}
	snapshot, err := service.GetQuestion(ctx, identity, input.RunID)
	if err != nil {
		return AddToReportResult{}, err
	}
	if snapshot.Run.State != orchestrator.StateAnswered || snapshot.Run.RecordVersion != input.RunVersion {
		return AddToReportResult{}, ErrAddToReportNotAccepted
	}
	outcome, err := completionOutcome(snapshot)
	if err != nil || outcome.OutcomeHash != input.OutcomeHash || ValidateAddToReportOutcome(outcome) != nil {
		return AddToReportResult{}, ErrAddToReportNotAccepted
	}

	semanticIR, queryPlan, answerArtifact, err := reportExportArtifacts(snapshot)
	if err != nil {
		return AddToReportResult{}, err
	}
	pageID, sectionID, baseRevision, blockY, err := service.resolveReportTarget(ctx, identity, input)
	if err != nil {
		return AddToReportResult{}, err
	}
	registryValue, err := template.NewDefaultRegistry()
	if err != nil {
		return AddToReportResult{}, err
	}
	chart := answer.RecommendChart(chartRuleInput(semanticIR, queryPlan, answerArtifact), registryValue)
	datasetVersionID := queryPlan.Sources[0].DatasetVersionID
	for _, source := range queryPlan.Sources {
		if source.DatasetVersionID != datasetVersionID {
			return AddToReportResult{}, ErrAddToReportNotAccepted
		}
	}
	sourceRunID := input.RunID
	evidence := []askdata.EvidenceRef{
		{EvidenceID: "query-plan:" + askdata.ID(queryPlan.PlanHash[:16]), Kind: askdata.EvidenceKindQueryPlan, SourceID: input.RunID, ContentHash: queryPlan.PlanHash},
		{EvidenceID: "query-result:" + askdata.ID(answerArtifact.Provenance.ResultHash[:16]), Kind: askdata.EvidenceKindQueryResult, SourceID: input.RunID, ContentHash: answerArtifact.Provenance.ResultHash},
	}
	for _, value := range evidence {
		if value.Validate() != nil {
			return AddToReportResult{}, ErrAddToReportNotAccepted
		}
	}
	queryRef := reportmodel.SemanticQueryRef{
		SemanticReleaseID: snapshot.Run.Release.ReleaseID, SemanticContentHash: snapshot.Run.Release.ContentHash,
		SemanticIR: semanticIR, QueryPlanHash: queryPlan.PlanHash, SourceQuestionRunID: &sourceRunID,
		DatasetVersionID: &datasetVersionID, ResolvedTimeSpec: queryPlan.ResolvedTimeSpec,
		EvidenceRefs: evidence, ChartRuleVersion: answer.ChartRuleVersion,
	}
	intentService := reportasset.NewIntentService(reportasset.NewPostgresIntentRepository(service.pool))
	intent, err := intentService.CreatePreview(ctx, reportasset.IntentIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, DomainID: identity.DomainID,
	}, reportasset.BuildIntentRequest{
		QuestionRunID: input.RunID, ReportID: input.ReportID, TargetPageID: pageID, TargetSectionID: sectionID,
		RunVersion: input.RunVersion, BaseRevision: baseRevision, IdempotencyKeyHash: input.IdempotencyKeyHash,
		OutcomeFull: true, SemanticQueryRef: queryRef, Chart: chart,
		Title: reportComponentTitle(answerArtifact), BlockY: blockY,
	})
	if err != nil {
		return AddToReportResult{}, err
	}
	return AddToReportResult{IntentID: intent.ID, ReportID: input.ReportID, RunID: input.RunID,
		Status: string(intent.State), PreviewHash: intent.PreviewHash, Replayed: intent.Replayed}, nil
}

func (service *PostgresService) ConfirmAddToReport(
	ctx context.Context, identity RequestIdentity, intentID askdata.ID, previewHash askdata.ContentHash,
) (AddToReportResult, error) {
	repository := reportasset.NewPostgresIntentRepository(service.pool)
	intent, err := reportasset.NewIntentService(repository).Confirm(ctx, reportasset.IntentIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, DomainID: identity.DomainID,
	}, intentID, previewHash)
	if err != nil {
		return AddToReportResult{}, err
	}
	return addToReportIntentResult(intent), nil
}

func (service *PostgresService) GetAddToReportIntent(
	ctx context.Context, identity RequestIdentity, intentID askdata.ID,
) (AddToReportResult, error) {
	intent, err := reportasset.NewPostgresIntentRepository(service.pool).Get(ctx, reportasset.IntentIdentity{
		TenantID: identity.TenantID, ActorID: identity.ActorID, DomainID: identity.DomainID,
	}, intentID)
	if err != nil {
		return AddToReportResult{}, err
	}
	return addToReportIntentResult(intent), nil
}

func addToReportIntentResult(intent reportasset.Intent) AddToReportResult {
	return AddToReportResult{IntentID: intent.ID, ReportID: intent.ReportID, RunID: intent.QuestionRunID,
		Status: string(intent.State), PreviewHash: intent.PreviewHash, RejectionCode: intent.RejectionCode,
		RejectionDetail: intent.RejectionDetail, Replayed: intent.Replayed}
}

func reportExportArtifacts(snapshot orchestrator.ReplaySnapshot) (ircontract.SemanticIR, askcompiler.ReportQuerySnapshot, answer.AnswerArtifact, error) {
	var semanticIR ircontract.SemanticIR
	var query askcompiler.ReportQuerySnapshot
	var answerArtifact answer.AnswerArtifact
	irCount, planCount, answerCount := 0, 0, 0
	for _, artifact := range snapshot.Artifacts {
		switch artifact.Type {
		case orchestrator.ArtifactSemanticIR:
			value, err := decodeSemanticIRArtifact(artifact.Payload)
			if err != nil {
				return semanticIR, query, answerArtifact, ErrAddToReportNotAccepted
			}
			semanticIR, irCount = value, irCount+1
		case orchestrator.ArtifactQueryPlan:
			if artifact.SchemaVersion == askcompiler.ReportQuerySnapshotVersion {
				if askdata.DecodeStrictJSON(artifact.Payload, &query) != nil || query.Validate() != nil {
					return semanticIR, query, answerArtifact, ErrAddToReportNotAccepted
				}
			} else {
				var legacy askcompiler.QueryArtifact
				if askdata.DecodeStrictJSON(artifact.Payload, &legacy) != nil || legacy.Validate() != nil {
					return semanticIR, query, answerArtifact, ErrAddToReportNotAccepted
				}
				value, snapshotErr := askcompiler.NewReportQuerySnapshot(legacy)
				if snapshotErr != nil {
					return semanticIR, query, answerArtifact, ErrAddToReportNotAccepted
				}
				query = value
			}
			planCount++
		case orchestrator.ArtifactAnswer:
			if artifact.Hash != snapshot.Run.CompletionArtifact {
				continue
			}
			value, err := decodeAnswerEnvelope(artifact.Payload)
			if err != nil {
				return semanticIR, query, answerArtifact, ErrAddToReportNotAccepted
			}
			answerArtifact, answerCount = value, answerCount+1
		}
	}
	if irCount != 1 || planCount != 1 || answerCount != 1 || query.SemanticIRHash != snapshot.Run.Hashes.SemanticIR ||
		query.PlanHash != snapshot.Run.Hashes.QueryPlan || semanticIR.SemanticReleaseID != snapshot.Run.Release.ReleaseID ||
		semanticIR.SemanticContentHash != snapshot.Run.Release.ContentHash || len(query.Sources) == 0 {
		return semanticIR, query, answerArtifact, ErrAddToReportNotAccepted
	}
	return semanticIR, query, answerArtifact, nil
}

func decodeSemanticIRArtifact(raw json.RawMessage) (ircontract.SemanticIR, error) {
	if value, err := ircontract.Decode(raw); err == nil {
		return value, nil
	}
	var envelope struct {
		SemanticIR json.RawMessage `json:"semanticIr"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return ircontract.SemanticIR{}, ErrAddToReportNotAccepted
	}
	return ircontract.Decode(envelope.SemanticIR)
}

// SavedQuestionSemanticIR returns the exact governed Semantic IR that produced
// an answered run.  Saved-question clients deliberately do not receive raw IR;
// the server resolves and validates the artifact at this trust boundary.
func SavedQuestionSemanticIR(snapshot orchestrator.ReplaySnapshot) (ircontract.SemanticIR, error) {
	if snapshot.Run.State != orchestrator.StateAnswered || snapshot.Run.Hashes.SemanticIR.Validate() != nil {
		return ircontract.SemanticIR{}, ErrAddToReportNotAccepted
	}
	var selected ircontract.SemanticIR
	count := 0
	for _, artifact := range snapshot.Artifacts {
		if artifact.Type != orchestrator.ArtifactSemanticIR {
			continue
		}
		value, err := decodeSemanticIRArtifact(artifact.Payload)
		if err != nil {
			return ircontract.SemanticIR{}, ErrAddToReportNotAccepted
		}
		selected, count = value, count+1
	}
	_, _, semanticHash, canonicalErr := ircontract.Canonicalize(selected)
	if count != 1 || canonicalErr != nil || semanticHash != snapshot.Run.Hashes.SemanticIR ||
		selected.SemanticReleaseID != snapshot.Run.Release.ReleaseID ||
		selected.SemanticContentHash != snapshot.Run.Release.ContentHash {
		return ircontract.SemanticIR{}, ErrAddToReportNotAccepted
	}
	return selected, nil
}

func decodeAnswerEnvelope(raw json.RawMessage) (answer.AnswerArtifact, error) {
	if value, err := answer.Decode(raw); err == nil {
		return value, nil
	}
	var envelope struct {
		Artifact json.RawMessage `json:"artifact"`
		Answer   json.RawMessage `json:"answer"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return answer.AnswerArtifact{}, ErrAddToReportNotAccepted
	}
	artifact := envelope.Artifact
	if len(artifact) == 0 {
		artifact = envelope.Answer
	}
	if len(artifact) == 0 {
		return answer.AnswerArtifact{}, ErrAddToReportNotAccepted
	}
	return answer.Decode(artifact)
}

func (service *PostgresService) resolveReportTarget(ctx context.Context, identity RequestIdentity, input AddToReportInput) (
	askdata.ID, askdata.ID, int64, int, error,
) {
	var raw []byte
	var revision int64
	err := service.scopeRunner(ctx, string(identity.TenantID), func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT draft.definition_json,draft.revision_no
			FROM platform.report_drafts AS draft JOIN platform.reports AS report
			ON report.id=draft.report_id AND report.tenant_id=draft.tenant_id
			WHERE report.id=$1 AND report.tenant_id=$2 AND report.domain_id=$3 AND report.status='ACTIVE'`,
			input.ReportID, identity.TenantID, identity.DomainID).Scan(&raw, &revision)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", 0, 0, ErrAddToReportNotAccepted
		}
		return "", "", 0, 0, err
	}
	definition, err := reportmodel.Decode(raw)
	if err != nil {
		return "", "", 0, 0, err
	}
	pageID, sectionID := input.TargetPageID, input.TargetSectionID
	if pageID == "" {
		if len(definition.Pages) == 0 {
			return "", "", 0, 0, ErrAddToReportNotAccepted
		}
		pageID = definition.Pages[0].ID
	}
	var selected *reportmodel.Section
	for pageIndex := range definition.Pages {
		if definition.Pages[pageIndex].ID == pageID {
			for sectionIndex := range definition.Pages[pageIndex].Sections {
				section := &definition.Pages[pageIndex].Sections[sectionIndex]
				if sectionID == "" || section.ID == sectionID {
					selected = section
					sectionID = section.ID
					break
				}
			}
			break
		}
	}
	if selected == nil {
		return "", "", 0, 0, ErrAddToReportNotAccepted
	}
	maxY := 0
	for _, block := range selected.Blocks {
		bottom := block.Layout.Desktop.Y + block.Layout.Desktop.H
		if bottom > maxY {
			maxY = bottom
		}
	}
	return pageID, sectionID, revision, maxY, nil
}

func chartRuleInput(ir ircontract.SemanticIR, query askcompiler.ReportQuerySnapshot, artifact answer.AnswerArtifact) answer.ChartRuleInput {
	nonTime := len(ir.GroupBy)
	timeGrain := ""
	for _, group := range ir.GroupBy {
		if group.Grain != nil {
			timeGrain = string(*group.Grain)
			nonTime--
		}
	}
	rowCount := 1
	if artifact.Layers.Structured.ChartSpec != nil {
		rowCount = 2
	}
	additive := true
	for _, metric := range query.MetricAggregations {
		if string(metric.Additivity) != "ADDITIVE" {
			additive = false
		}
	}
	return answer.ChartRuleInput{MetricCount: len(ir.Metrics), TimeGrain: timeGrain, NonTimeGroupByCount: nonTime,
		RowCount: rowCount, Additive: additive, HasComparison: ir.Comparison != nil}
}

func reportComponentTitle(artifact answer.AnswerArtifact) string {
	value := strings.TrimSpace(artifact.Layers.Narrative.Summary)
	if value == "" {
		return "问数结果"
	}
	runes := []rune(value)
	if len(runes) > 120 {
		runes = runes[:120]
	}
	return string(runes)
}

var _ AddToReportBackend = (*PostgresService)(nil)
var _ AddToReportConfirmationBackend = (*PostgresService)(nil)
