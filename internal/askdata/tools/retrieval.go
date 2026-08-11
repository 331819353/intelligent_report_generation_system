package tools

import (
	"context"
	"encoding/json"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/toolhost"
)

// searchSemanticObjects runs governed hybrid retrieval.
//
// The embedding is fetched per call. When no embedding provider is configured
// the retriever still runs lexical + exact and reports the degradation, rather
// than failing the run: a deployment without a vector service must still be
// able to answer questions, but must never silently claim full recall.
func (binding *Binding) searchSemanticObjects(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.SearchSemanticObjectsInput,
) (toolhost.ToolOutput[toolhost.SearchSemanticObjectsResult], error) {
	var output toolhost.ToolOutput[toolhost.SearchSemanticObjectsResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Retriever == nil {
		return output, ErrToolUnavailable
	}
	request := search.RetrievalRequest{
		Scope: binding.run.Scope, Mention: input.Mention,
		ObjectTypes: retrievalObjectTypes(input.ObjectTypes), TopKPerType: input.Limit,
	}
	if binding.services.Embedder != nil {
		vector, model, err := binding.services.Embedder.Embed(ctx, input.Mention)
		// A failing embedding service degrades retrieval; it does not fail the
		// question. The degradation is surfaced on the result.
		if err == nil {
			request.Embedding, request.EmbeddingModel = vector, model
		}
	}
	result, err := binding.services.Retriever.Retrieve(ctx, request)
	if err != nil {
		return output, err
	}
	candidates := make([]toolhost.CandidateSummary, 0, len(result.Candidates))
	refs := make([]askdata.EvidenceRef, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		candidates = append(candidates, toolhost.CandidateSummary{
			ObjectType:      toolhost.ObjectType(candidate.ObjectType),
			ObjectVersionID: candidate.ObjectVersionID,
			Score:           candidate.Score,
			// The strongest contributing source is the match type the model
			// sees; the per-source evidence stays on the retrieval artifact.
			MatchType: strongestSource(candidate.Evidence),
			// Only CERTIFIED objects are indexed for a release, so a returned
			// candidate is certified by construction.
			Status: "CERTIFIED",
		})
		for _, item := range candidate.Evidence {
			refs = append(refs, item.Evidence)
		}
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(
		askdata.EvidenceKindCandidateSet, binding.run.Scope.Release.ReleaseID, payload,
	)
	output.Result = toolhost.SearchSemanticObjectsResult{
		Candidates: candidates, Truncated: result.Degraded,
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = append([]askdata.EvidenceRef{evidence}, refs...)
	output.MadeProgress = len(candidates) > 0
	return output, nil
}

// getSemanticContracts returns release-pinned contracts for candidate objects.
func (binding *Binding) getSemanticContracts(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.GetSemanticContractsInput,
) (toolhost.ToolOutput[toolhost.GetSemanticContractsResult], error) {
	var output toolhost.ToolOutput[toolhost.GetSemanticContractsResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Reader == nil {
		return output, ErrToolUnavailable
	}
	rows, err := binding.services.Reader.Contracts(
		ctx, binding.run.Scope, binding.run.DomainID, plainIDs(input.ObjectVersionIDs),
	)
	if err != nil {
		return output, err
	}
	contracts := make([]toolhost.SemanticContractSummary, 0, len(rows))
	refs := make([]askdata.EvidenceRef, 0, len(rows))
	ids := make([]askdata.ID, 0, len(rows))
	for _, row := range rows {
		summary, ok := contractSummary(row)
		if !ok {
			// Object types outside the tool contract's enum (measures,
			// relationships, members) are not surfaced as contracts; the binder
			// reaches them through the graph plan instead.
			continue
		}
		evidence := binding.evidence(
			askdata.EvidenceKindSemanticContract, askdata.ID(row.ObjectVersionID), row.Contract,
		)
		summary.ContentHash = row.ContentHash
		contracts = append(contracts, summary)
		refs = append(refs, evidence)
		ids = append(ids, evidence.EvidenceID)
	}
	output.Result = toolhost.GetSemanticContractsResult{Contracts: contracts, EvidenceIDs: ids}
	output.EvidenceRefs = refs
	output.MadeProgress = len(contracts) > 0
	return output, nil
}

// lookupDimensionValues resolves a business mention to governed member values.
func (binding *Binding) lookupDimensionValues(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.LookupDimensionValuesInput,
) (toolhost.ToolOutput[toolhost.LookupDimensionValuesResult], error) {
	var output toolhost.ToolOutput[toolhost.LookupDimensionValuesResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Reader == nil {
		return output, ErrToolUnavailable
	}
	lookup, err := binding.services.Reader.DimensionMembers(
		ctx, binding.run.Scope, binding.run.DomainID,
		string(input.DimensionVersionID), input.Mention, input.Limit,
	)
	if err != nil {
		return output, err
	}
	members := make([]toolhost.DimensionValueSummary, 0, len(lookup.Members))
	for _, member := range lookup.Members {
		members = append(members, toolhost.DimensionValueSummary{
			MemberVersionID: askdata.ID(member.MemberVersionID),
			DisplayLabel:    member.DisplayLabel,
			Aliases:         member.Aliases,
			HierarchyPath:   stableIDs(member.HierarchyPath),
			Sensitive:       member.Sensitive,
		})
	}
	payload, err := json.Marshal(members)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(
		askdata.EvidenceKindDimensionProfile, input.DimensionVersionID, payload,
	)
	output.Result = toolhost.LookupDimensionValuesResult{
		DimensionVersionID: input.DimensionVersionID, Members: members,
		Truncated: lookup.Truncated, EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = len(members) > 0
	return output, nil
}

// getCertifiedExamples returns certified question priors for the run's release.
func (binding *Binding) getCertifiedExamples(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.GetCertifiedExamplesInput,
) (toolhost.ToolOutput[toolhost.GetCertifiedExamplesResult], error) {
	var output toolhost.ToolOutput[toolhost.GetCertifiedExamplesResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Reader == nil {
		return output, ErrToolUnavailable
	}
	rows, err := binding.services.Reader.CertifiedExamples(
		ctx, binding.run.Scope, binding.run.DomainID, input.QuestionSummary, input.Limit,
	)
	if err != nil {
		return output, err
	}
	examples := make([]toolhost.CertifiedExampleSummary, 0, len(rows))
	refs := make([]askdata.EvidenceRef, 0, len(rows))
	ids := make([]askdata.ID, 0, len(rows))
	for _, row := range rows {
		evidence := binding.evidence(
			askdata.EvidenceKindCertifiedExample, askdata.ID(row.ExampleVersionID),
			[]byte(row.ContentHash),
		)
		examples = append(examples, toolhost.CertifiedExampleSummary{
			ExampleID: askdata.ID(row.ExampleVersionID), QuestionSummary: row.Question,
			ExpectedMetricVersionIDs: stableIDs(row.ExpectedMetricIDs),
			ExpectedDimensionIDs:     stableIDs(row.ExpectedDimensionIDs),
			ExpectedTimeExpression:   row.ExpectedTimeExpression,
			ContentHash:              row.ContentHash,
			SimilarityPermillion:     row.SimilarityPermillion,
		})
		refs = append(refs, evidence)
		ids = append(ids, evidence.EvidenceID)
	}
	output.Result = toolhost.GetCertifiedExamplesResult{Examples: examples, EvidenceIDs: ids}
	output.EvidenceRefs = refs
	output.MadeProgress = len(examples) > 0
	return output, nil
}

// getDataQualityStatus reports governed quality rules for the touched models.
//
// askdata.quality_rules currently has no authoring path, so this reports
// UNKNOWN in practice. UNKNOWN is deliberately not PASSED: the answer layer has
// to be able to tell "verified clean" from "never checked".
func (binding *Binding) getDataQualityStatus(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.DataQualityInput,
) (toolhost.ToolOutput[toolhost.GetDataQualityStatusResult], error) {
	var output toolhost.ToolOutput[toolhost.GetDataQualityStatusResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Reader == nil {
		return output, ErrToolUnavailable
	}
	status, err := binding.services.Reader.DataQuality(
		ctx, binding.run.Scope, binding.run.DomainID, plainIDs(input.ModelVersionIDs),
	)
	if err != nil {
		return output, err
	}
	rules := make([]toolhost.QualityRuleSummary, 0, len(status.Rules))
	for _, rule := range status.Rules {
		rules = append(rules, toolhost.QualityRuleSummary{
			Code: rule.Code, Severity: rule.Severity, Passed: rule.Passed,
		})
	}
	payload, err := json.Marshal(rules)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(
		askdata.EvidenceKindDataQuality, binding.run.Scope.Release.ReleaseID, payload,
	)
	output.Result = toolhost.GetDataQualityStatusResult{
		Status: status.Status, Rules: rules,
		DataAsOf:      input.TimeRange.EndExclusive,
		CoverageStart: input.TimeRange.Start,
		CoverageEnd:   input.TimeRange.EndExclusive,
		EvidenceIDs:   []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	// Reporting UNKNOWN is a real answer about quality, not a failure to progress.
	output.MadeProgress = true
	return output, nil
}

func retrievalObjectTypes(values []toolhost.ObjectType) []search.ObjectType {
	result := make([]search.ObjectType, 0, len(values))
	for _, value := range values {
		result = append(result, search.ObjectType(value))
	}
	return result
}

// contractSummary maps a release-pinned contract document onto the tool
// contract. Only object types the tool enum admits are returned.
func contractSummary(row registry.ContractRow) (toolhost.SemanticContractSummary, bool) {
	objectType, ok := contractObjectType(row.ObjectType)
	if !ok {
		return toolhost.SemanticContractSummary{}, false
	}
	var document struct {
		Name          string `json:"name"`
		Code          string `json:"code"`
		Definition    string `json:"definition"`
		Unit          string `json:"unit"`
		GrainContract string `json:"grainContract"`
	}
	// A malformed contract document is not fatal: the object still exists in the
	// release, and the binder can work from identity plus hash alone.
	_ = json.Unmarshal(row.Contract, &document)
	name := document.Name
	if name == "" {
		name = document.Code
	}
	return toolhost.SemanticContractSummary{
		ObjectType: objectType, ObjectVersionID: askdata.ID(row.ObjectVersionID),
		Name: name, Definition: document.Definition, Unit: document.Unit,
		OwnerID: askdata.ID(row.OwnerID), Status: row.Status, Grain: document.GrainContract,
	}, true
}

func contractObjectType(value string) (toolhost.ObjectType, bool) {
	switch value {
	case "METRIC":
		return toolhost.ObjectTypeMetric, true
	case "DIMENSION":
		return toolhost.ObjectTypeDimension, true
	case "SEMANTIC_MODEL":
		return toolhost.ObjectTypeModel, true
	case "BUSINESS_TERM":
		return toolhost.ObjectTypeTerm, true
	default:
		return "", false
	}
}

// strongestSource names the retrieval source that contributed the best rank.
// It is descriptive only: ranking itself already happened in the retriever.
func strongestSource(evidence []search.SourceEvidence) string {
	best := ""
	bestRank := 0
	for _, item := range evidence {
		if best == "" || item.Rank < bestRank {
			best, bestRank = string(item.Source), item.Rank
		}
	}
	if best == "" {
		return "LEXICAL"
	}
	return best
}
