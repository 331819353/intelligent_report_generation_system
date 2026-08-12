package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/registry"
	"intelligent-report-generation-system/internal/askdata/search"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/askdata/understanding"
	"intelligent-report-generation-system/internal/askdata/understanding/dictionarysearch"
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
	objectTypes, ok := retrievalObjectTypes(completeAnalyticObjectTypes(input.ObjectTypes))
	if !ok {
		return output, toolhost.ErrInvalidInvocation
	}
	request := search.RetrievalRequest{
		Scope: binding.run.Scope, Mention: input.Mention,
		ObjectTypes: objectTypes, TopKPerType: input.Limit,
	}
	// Certified business vocabulary enters retrieval as deterministic exact
	// hits. A term that an Owner certified to mean a specific metric must beat
	// lexical similarity, otherwise certifying it changed nothing.
	dictionaryHits, dictionaryRefs := binding.dictionaryHits(ctx, input.Mention)
	request.DeterministicExact = dictionaryHits
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
	legacyMetricIDs := make([]string, 0)
	for _, candidate := range result.Candidates {
		if candidate.ObjectType == search.ObjectMeasureLegacy {
			legacyMetricIDs = append(legacyMetricIDs, string(candidate.ObjectVersionID))
		}
	}
	canonicalMetrics := map[string]string{}
	reportAssetIDs := make([]askdata.ID, 0)
	for _, candidate := range result.Candidates {
		if candidate.ObjectType == search.ObjectReportAsset {
			reportAssetIDs = append(reportAssetIDs, candidate.ObjectVersionID)
		}
	}
	reportSources := map[askdata.ID]toolhost.ReportSourceSummary{}
	if len(reportAssetIDs) > 0 {
		if binding.services.ReportSources == nil {
			return output, ErrToolUnavailable
		}
		reportSources, err = binding.services.ReportSources.ReportSources(ctx, binding.run.Scope, binding.run.DomainID, reportAssetIDs)
		if err != nil {
			return output, err
		}
	}
	if len(legacyMetricIDs) > 0 {
		if binding.services.Reader == nil {
			return output, ErrToolUnavailable
		}
		canonicalMetrics, err = binding.services.Reader.CanonicalMetricVersions(
			ctx, binding.run.Scope, binding.run.DomainID, legacyMetricIDs,
		)
		if err != nil {
			return output, err
		}
	}
	candidates := make([]toolhost.CandidateSummary, 0, len(result.Candidates))
	refs := make([]askdata.EvidenceRef, 0, len(result.Candidates))
	seenCandidates := map[string]bool{}
	for _, candidate := range result.Candidates {
		objectType, ok := toolObjectType(candidate.ObjectType)
		if !ok {
			return output, toolhost.ErrInvalidInvocation
		}
		versionID := candidate.ObjectVersionID
		if candidate.ObjectType == search.ObjectMeasureLegacy {
			mapped := canonicalMetrics[string(candidate.ObjectVersionID)]
			if mapped == "" {
				continue
			}
			versionID = askdata.ID(mapped)
		}
		key := string(objectType) + "\x00" + string(versionID)
		if !seenCandidates[key] {
			var reportSource *toolhost.ReportSourceSummary
			if objectType == toolhost.ObjectTypeReportAsset {
				if strongestEvidenceScore(candidate.Evidence) < 0.25 {
					continue
				}
				source, visible := reportSources[versionID]
				if !visible {
					continue
				}
				sourceCopy := source
				reportSource = &sourceCopy
				binding.reportSourcesMutex.Lock()
				binding.reportSources[versionID] = source
				binding.reportSourceScores[versionID] = candidate.Score
				binding.reportSourcesMutex.Unlock()
			}
			seenCandidates[key] = true
			candidates = append(candidates, toolhost.CandidateSummary{
				ObjectType:      objectType,
				ObjectVersionID: versionID,
				Score:           candidate.Score,
				// The strongest contributing source is the match type the model
				// sees; the per-source evidence stays on the retrieval artifact.
				MatchType: strongestSource(candidate.Evidence),
				// Only CERTIFIED objects are indexed for a release, so a returned
				// candidate is certified by construction.
				Status:       "CERTIFIED",
				ReportSource: reportSource,
			})
		}
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
	}
	output.EvidenceRefs = append(append([]askdata.EvidenceRef{evidence}, refs...), dictionaryRefs...)
	output.Result.EvidenceIDs = sortedEvidenceIDs(output.EvidenceRefs)
	output.MadeProgress = len(candidates) > 0
	return output, nil
}

// completeAnalyticObjectTypes guarantees that metric discovery also returns
// the executable semantic context required by the next stage. A metric alone
// cannot be compiled: graph validation requires a certified model and relative
// time questions require the model's certified time dimension. The caller may
// still request TERM-only lookup without widening that vocabulary search.
func completeAnalyticObjectTypes(values []toolhost.ObjectType) []toolhost.ObjectType {
	analytic := false
	terms := false
	for _, value := range values {
		switch value {
		case toolhost.ObjectTypeMetric, toolhost.ObjectTypeDimension, toolhost.ObjectTypeModel:
			analytic = true
		case toolhost.ObjectTypeTerm:
			terms = true
		case toolhost.ObjectTypeReportAsset:
			// A direct report-asset lookup is also analytical: the published
			// component is only a source prior, so executable semantic context
			// must be retrieved alongside it.
			analytic = true
		}
	}
	result := make([]toolhost.ObjectType, 0, 5)
	if analytic {
		result = append(result,
			toolhost.ObjectTypeMetric,
			toolhost.ObjectTypeDimension,
			toolhost.ObjectTypeModel,
			toolhost.ObjectTypeReportAsset,
		)
	}
	if terms {
		result = append(result, toolhost.ObjectTypeTerm)
	}
	return result
}

func sortedEvidenceIDs(values []askdata.EvidenceRef) []askdata.ID {
	seen := make(map[askdata.ID]bool, len(values))
	result := make([]askdata.ID, 0, len(values))
	for _, value := range values {
		if !seen[value.EvidenceID] {
			seen[value.EvidenceID] = true
			result = append(result, value.EvidenceID)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
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
	for _, row := range rows {
		summary, ok := contractSummary(row)
		if !ok {
			// Object types outside the question contract's enum (relationships
			// and members) are reached through the graph plan instead.
			continue
		}
		evidence := binding.evidence(
			askdata.EvidenceKindSemanticContract, askdata.ID(row.ObjectVersionID), row.Contract,
		)
		summary.ContentHash = row.ContentHash
		contracts = append(contracts, summary)
		refs = append(refs, evidence)
	}
	// Result contracts require exact, strictly sorted evidence closure. Query
	// order is by semantic object identity, while evidence IDs are content based;
	// reusing query order therefore rejected otherwise valid multi-contract
	// results at the Tool Host boundary.
	output.Result = toolhost.GetSemanticContractsResult{
		Contracts: contracts, EvidenceIDs: sortedEvidenceIDs(refs),
	}
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
	refs := make([]askdata.EvidenceRef, 0, len(rows)+1)
	for _, row := range rows {
		evidence := binding.evidence(
			askdata.EvidenceKindCertifiedExample, askdata.ID(row.ExampleVersionID),
			[]byte(row.ContentHash),
		)
		examples = append(examples, toolhost.CertifiedExampleSummary{
			ExampleID:                askdata.ID(row.ExampleVersionID),
			ExpectedMetricVersionIDs: stableIDs(row.ExpectedMetricIDs),
			ExpectedDimensionIDs:     stableIDs(row.ExpectedDimensionIDs),
			ExpectedTimeExpression:   row.ExpectedTimeExpression,
			ContentHash:              row.ContentHash,
			SimilarityPermillion:     row.SimilarityPermillion,
		})
		refs = append(refs, evidence)
	}
	// "No certified examples for this release and role" is itself a governed
	// retrieval fact. Persist one lookup-level evidence reference even when the
	// row set is empty, otherwise the optional prior lookup is rejected as an
	// invalid tool response and blocks an otherwise answerable question.
	lookupPayload, err := json.Marshal(examples)
	if err != nil {
		return output, err
	}
	refs = append(refs, binding.evidence(
		askdata.EvidenceKindCertifiedExample, binding.run.Scope.Release.ReleaseID, lookupPayload,
	))
	output.Result = toolhost.GetCertifiedExamplesResult{
		Examples: examples, EvidenceIDs: sortedEvidenceIDs(refs),
	}
	output.EvidenceRefs = refs
	output.MadeProgress = true
	return output, nil
}

// getDataQualityStatus reports governed quality rules for the touched models.
//
// Rules bind to materialization checks, so this reads the measurement produced
// for the model's pinned snapshot. UNKNOWN is deliberately not PASS: the answer
// layer has to distinguish "verified clean" from "never checked".
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

// retrievalObjectTypes translates the tool's object-type enum into the
// retrieval index's own enum. The two are declared independently and do not
// agree on spelling (TERM vs BUSINESS_TERM) or membership (the index holds no
// semantic-model document at all), so the conversion is written out rather
// than cast: a string cast sends an unknown type into the retriever, which
// rejects the whole request and turns a legal argument into a tool failure.
//
// ok is false when the caller asked only for types this index cannot serve.
// That is an invalid invocation, not an empty result — reporting it as "no
// candidates" would tell the model the release is empty when it is not.
func retrievalObjectTypes(values []toolhost.ObjectType) ([]search.ObjectType, bool) {
	result := make([]search.ObjectType, 0, len(values))
	for _, value := range values {
		switch value {
		case toolhost.ObjectTypeMetric:
			result = append(result, search.ObjectMetric, search.ObjectMeasureLegacy)
		case toolhost.ObjectTypeDimension:
			result = append(result, search.ObjectDimension)
		case toolhost.ObjectTypeModel:
			result = append(result, search.ObjectSemanticModel)
		case toolhost.ObjectTypeTerm:
			result = append(result, search.ObjectBusinessTerm)
		case toolhost.ObjectTypeReportAsset:
			result = append(result, search.ObjectReportAsset)
		default:
			continue
		}
	}
	return result, len(result) > 0
}

func toolObjectType(value search.ObjectType) (toolhost.ObjectType, bool) {
	switch value {
	case search.ObjectMetric, search.ObjectMeasureLegacy:
		return toolhost.ObjectTypeMetric, true
	case search.ObjectDimension:
		return toolhost.ObjectTypeDimension, true
	case search.ObjectSemanticModel:
		return toolhost.ObjectTypeModel, true
	case search.ObjectBusinessTerm:
		return toolhost.ObjectTypeTerm, true
	case search.ObjectReportAsset:
		return toolhost.ObjectTypeReportAsset, true
	default:
		return "", false
	}
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
	definition := document.Definition
	if definition == "" {
		// Physical measures are release-governed metric candidates but their
		// executable contract may only carry name, unit and aggregation. Reusing
		// the governed name is preferable to inventing prose or rejecting an
		// otherwise complete certified contract.
		definition = name
	}
	return toolhost.SemanticContractSummary{
		ObjectType: objectType, ObjectVersionID: askdata.ID(row.ObjectVersionID),
		Name: name, Definition: definition, Unit: document.Unit,
		OwnerID: askdata.ID(row.OwnerID), Status: row.Status, Grain: document.GrainContract,
	}, true
}

func contractObjectType(value string) (toolhost.ObjectType, bool) {
	switch value {
	case "METRIC", "MEASURE":
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

func strongestEvidenceScore(evidence []search.SourceEvidence) float64 {
	best := 0.0
	for _, item := range evidence {
		if item.SourceScore > best {
			best = item.SourceScore
		}
	}
	return best
}

// dictionaryHits resolves certified business terms in the mention and projects
// them into deterministic exact hits.
//
// Failures here degrade retrieval rather than failing the question: the
// dictionary is a precision aid, and losing it costs recall quality, not
// correctness. Dropped terms (expired, out of scope, negative context) are
// discarded by the matcher itself and never reach retrieval.
func (binding *Binding) dictionaryHits(
	ctx context.Context,
	mention string,
) ([]search.RawHit, []askdata.EvidenceRef) {
	if binding.services.Dictionary == nil || strings.TrimSpace(mention) == "" {
		return nil, nil
	}
	result, err := binding.services.Dictionary.Match(ctx, understanding.DictionaryMatchRequest{
		Scope: binding.run.Scope, Question: mention, Now: time.Now().UTC(),
	})
	if err != nil || len(result.Hits) == 0 {
		return nil, nil
	}
	hits, err := dictionarysearch.ExactHits(binding.run.Scope, mention, result.Hits)
	if err != nil {
		return nil, nil
	}
	refs := make([]askdata.EvidenceRef, 0, len(result.Hits))
	for _, hit := range result.Hits {
		refs = append(refs, askdata.EvidenceRef{
			EvidenceID: askdata.ID(hit.EvidenceHash), Kind: askdata.EvidenceKindExactAlias,
			SourceID: hit.TermVersionID, ContentHash: hit.EvidenceHash,
		})
	}
	return hits, refs
}
