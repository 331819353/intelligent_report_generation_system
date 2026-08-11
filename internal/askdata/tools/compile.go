package tools

import (
	"context"
	"encoding/json"
	"errors"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/graph"
	"intelligent-report-generation-system/internal/askdata/toolhost"
	"intelligent-report-generation-system/internal/askdata/validator"
)

// resolveGraphPlan resolves the join path for a bound semantic bundle.
//
// Join risks and graph degradation are surfaced to the model rather than
// silently absorbed: a fallback plan is still a plan the caller must know about.
func (binding *Binding) resolveGraphPlan(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.SemanticBundleInput,
) (toolhost.ToolOutput[toolhost.ResolveGraphPlanResult], error) {
	var output toolhost.ToolOutput[toolhost.ResolveGraphPlanResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Graph == nil {
		return output, ErrToolUnavailable
	}
	resolution, err := binding.services.Graph.Resolve(ctx, graph.PlanRequest{
		Scope: binding.run.Scope, DomainID: binding.run.DomainID,
		MetricRefs:    objectRefs(input.MetricVersionIDs),
		ModelRefs:     objectRefs(input.ModelVersionIDs),
		DimensionRefs: objectRefs(input.DimensionVersionIDs),
		MemberRefs:    objectRefs(input.MemberVersionIDs),
	})
	if err != nil {
		return output, err
	}

	models := make([]askdata.ID, 0, len(resolution.Plan.Models))
	for _, model := range resolution.Plan.Models {
		models = append(models, model.VersionID)
	}
	relationships := make([]askdata.ID, 0)
	risks := make([]toolhost.GraphRisk, 0)
	seenRelationship := map[askdata.ID]bool{}
	riskCounts := map[string]int{}
	for _, path := range resolution.Plan.JoinPaths {
		for _, step := range path.Steps {
			if !seenRelationship[step.RelationshipVersionID] {
				seenRelationship[step.RelationshipVersionID] = true
				relationships = append(relationships, step.RelationshipVersionID)
			}
		}
		for _, code := range path.RiskCodes {
			riskCounts[string(code)]++
		}
		// A path the graph refused is a blocking risk, not an omission.
		if !path.Allowed {
			riskCounts["JOIN_PATH_NOT_ALLOWED"]++
		}
	}
	for code, count := range riskCounts {
		risks = append(risks, toolhost.GraphRisk{
			Code: code, Blocking: count > 0 && code == "JOIN_PATH_NOT_ALLOWED",
		})
	}
	if resolution.Degraded && resolution.DegradationReason != "" {
		risks = append(risks, toolhost.GraphRisk{Code: resolution.DegradationReason})
	}

	payload, err := json.Marshal(resolution.Plan)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindGraphPath, binding.run.DomainID, payload)
	output.Result = toolhost.ResolveGraphPlanResult{
		GraphPlanHash: resolution.Plan.PlanHash, ModelVersionIDs: models,
		RelationshipIDs: relationships, Risks: risks,
		FallbackUsed:  resolution.Source == graph.ResolutionSourcePostgresFallback,
		GraphDegraded: resolution.Degraded,
		EvidenceIDs:   []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = len(models) > 0
	return output, nil
}

// compileSemanticQuery turns a Semantic IR into a non-executable query plan.
//
// It uses the pinned-IR compiler, which refuses any IR that does not declare
// the exact release pinned to this run and a domain inside the actor's scope.
// The model therefore cannot widen scope by supplying a different release, and
// no SQL leaves this boundary — the plan stays a typed artifact until execution.
//
// The compiled artifact is cached under its plan hash for this run only.
func (binding *Binding) compileSemanticQuery(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.CompileSemanticQueryInput,
) (toolhost.ToolOutput[toolhost.CompileSemanticQueryResult], error) {
	var output toolhost.ToolOutput[toolhost.CompileSemanticQueryResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Compiler == nil {
		return output, ErrToolUnavailable
	}
	artifact, err := binding.services.Compiler.CompilePinnedIR(ctx, compiler.PinnedIRCompileRequest{
		Scope: binding.run.Scope, SemanticIR: input.SemanticIR,
	})
	if err != nil {
		return output, err
	}
	binding.plans.put(artifact.PlanHash, artifact)

	shapes := make([]toolhost.ParameterShapeSummary, 0)
	seenShape := map[string]bool{}
	for _, plan := range artifact.Plans {
		for _, shape := range plan.ParameterShapes {
			if seenShape[shape.Code] {
				continue
			}
			seenShape[shape.Code] = true
			shapes = append(shapes, toolhost.ParameterShapeSummary{
				Code: shape.Code, DataType: shape.DataType, MultiValue: shape.MultiValue,
				Required: shape.Required, Cardinality: shape.Cardinality,
			})
		}
	}
	payload, err := json.Marshal(artifact)
	if err != nil {
		return output, err
	}
	evidence := binding.evidence(askdata.EvidenceKindQueryPlan, binding.run.DomainID, payload)
	output.Result = toolhost.CompileSemanticQueryResult{
		PlanHash: artifact.PlanHash, PlanCount: len(artifact.Plans),
		ParameterShapes: shapes, MaxRows: input.SemanticIR.Limit,
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = true
	return output, nil
}

// validateQueryPlan validates a plan compiled earlier in this same run.
//
// The plan hash resolves through the run-scoped cache and never through
// storage: a plan compiled under another run's policy scope must not be
// reachable here, so an unknown hash is an invalid invocation rather than a
// lookup miss.
func (binding *Binding) validateQueryPlan(
	ctx context.Context,
	authorization toolhost.AuthorizationContext,
	input toolhost.ValidateQueryPlanInput,
) (toolhost.ToolOutput[toolhost.ValidateQueryPlanResult], error) {
	var output toolhost.ToolOutput[toolhost.ValidateQueryPlanResult]
	if err := binding.authorize(authorization); err != nil {
		return output, err
	}
	if binding.services.Validator == nil {
		return output, ErrToolUnavailable
	}
	artifact, ok := binding.plans.get(input.PlanHash)
	if !ok {
		return output, toolhost.ErrInvalidInvocation
	}
	validation, err := binding.services.Validator.Validate(ctx, artifact)
	if err != nil {
		// A rejected plan is a governed outcome the model must see, not a tool
		// failure: it reports not-allowed with the rejection code as a risk.
		var rejection *validator.Rejection
		if errors.As(err, &rejection) {
			code := rejection.Code
			evidence := binding.evidence(
				askdata.EvidenceKindQueryPlan, binding.run.DomainID, []byte(code),
			)
			output.Result = toolhost.ValidateQueryPlanResult{
				Allowed: false, ValidationHash: evidence.ContentHash,
				Risks:       []toolhost.PlanRiskSummary{{Code: code, Count: 1, Blocking: true}},
				EvidenceIDs: []askdata.ID{evidence.EvidenceID},
			}
			output.EvidenceRefs = []askdata.EvidenceRef{evidence}
			output.MadeProgress = true
			return output, nil
		}
		return output, err
	}

	var maxCost float64
	var maxPlanRows int64
	for _, plan := range validation.Plans {
		if plan.Explain.TotalCost > maxCost {
			maxCost = plan.Explain.TotalCost
		}
		if plan.Explain.RootPlanRows > maxPlanRows {
			maxPlanRows = plan.Explain.RootPlanRows
		}
	}
	payload, err := json.Marshal(validation)
	if err != nil {
		return output, err
	}
	binding.plans.markValidated(artifact.PlanHash, validation)
	evidence := binding.evidence(askdata.EvidenceKindQueryPlan, binding.run.DomainID, payload)
	output.Result = toolhost.ValidateQueryPlanResult{
		Allowed: true, ValidationHash: validation.ValidationHash,
		MaxCost: maxCost, MaxPlanRows: maxPlanRows,
		Risks:       []toolhost.PlanRiskSummary{},
		EvidenceIDs: []askdata.ID{evidence.EvidenceID},
	}
	output.EvidenceRefs = []askdata.EvidenceRef{evidence}
	output.MadeProgress = true
	return output, nil
}

func objectRefs(values []askdata.ID) []graph.ObjectVersionRef {
	result := make([]graph.ObjectVersionRef, 0, len(values))
	for _, value := range values {
		result = append(result, graph.ObjectVersionRef{VersionID: value})
	}
	return result
}
