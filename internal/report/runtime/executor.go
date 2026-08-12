package runtime

import (
	"context"
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
	askdatair "intelligent-report-generation-system/internal/askdata/ir"
	"intelligent-report-generation-system/internal/askdata/ircontract"
	"intelligent-report-generation-system/internal/report"
)

// SemanticExecutionRequest is the only report-to-AskData execution boundary.
// The implementation must resolve and deterministically recompile the pinned
// IR under the viewer identity carried by ctx, then compare FixedPlanHash.
type SemanticExecutionRequest struct {
	ReportID              askdata.ID
	ReportVersionID       askdata.ID
	SourceRunID           askdata.ID
	CompilationArtifactID askdata.ID
	ReleaseID             askdata.ID
	ContentHash           askdata.ContentHash
	IR                    ircontract.SemanticIR
	ResolvedTimeSpec      *askdatair.ResolvedTimeSpec
	FixedPlanHash         askdata.ContentHash
	PolicyScopeHash       string
	Filters               []ResolvedFilter
	Limit                 int
}

// DatasetExecutionRequest is a logical field request. There is deliberately
// no SQL or physical relation field at this boundary.
type DatasetExecutionRequest struct {
	DatasetID        askdata.ID
	DatasetVersionID askdata.ID
	DataContextID    askdata.ID
	Dimensions       []report.FieldBinding
	Measures         []report.FieldBinding
	Filters          []ResolvedFilter
	Parameters       []report.DefaultParameter
	PolicyScopeHash  string
	Limit            int
}

type SemanticIRRunner interface {
	CompileAndExecuteSemanticIR(context.Context, SemanticExecutionRequest) (QueryResult, error)
}

type DatasetFieldRunner interface {
	ExecuteDatasetFields(context.Context, DatasetExecutionRequest) (QueryResult, error)
}

// GovernedQueryExecutor dispatches the closed binding union. Both runners are
// responsible for applying current viewer row/column policy on every call;
// publication-time access never substitutes for this execution-time check.
type GovernedQueryExecutor struct {
	Semantic SemanticIRRunner
	Dataset  DatasetFieldRunner
}

func (executor GovernedQueryExecutor) ExecuteReportQuery(ctx context.Context, request QueryRequest) (QueryResult, error) {
	if askdata.ContentHash(request.PolicyScopeHash).Validate() != nil || request.Limit < 1 || request.Limit > ircontract.MaxResultRows {
		return QueryResult{}, errors.New("report query request is invalid")
	}
	switch request.BindingMode {
	case report.BindingSemanticIR:
		// A pinned semantic query is released by the database only for an
		// immutable report version, so it can never be executed as a draft. The
		// planner already blocks it; refusing here too keeps the invariant local
		// to the executor rather than depending on the caller having planned
		// correctly.
		if request.Draft {
			return QueryResult{}, errors.New("semantic report bindings require a published version")
		}
		if executor.Semantic == nil || request.ReportID.Validate() != nil || request.ReportVersionID.Validate() != nil ||
			(request.SourceQuestionRunID == "") == (request.CompilationArtifactID == "") ||
			(request.SourceQuestionRunID != "" && request.SourceQuestionRunID.Validate() != nil) ||
			(request.CompilationArtifactID != "" && request.CompilationArtifactID.Validate() != nil) ||
			request.SemanticReleaseID.Validate() != nil ||
			request.SemanticContentHash.Validate() != nil || request.FixedQueryPlanHash.Validate() != nil {
			return QueryResult{}, errors.New("semantic report runner is unavailable or the pinned contract is invalid")
		}
		semanticIR, err := ircontract.Decode(request.SemanticIR)
		if err != nil {
			return QueryResult{}, fmt.Errorf("decode pinned semantic IR: %w", err)
		}
		if semanticIR.SemanticReleaseID != request.SemanticReleaseID || semanticIR.SemanticContentHash != request.SemanticContentHash {
			return QueryResult{}, errors.New("semantic report request release identity does not match its IR")
		}
		var resolvedTimeSpec *askdatair.ResolvedTimeSpec
		if len(request.ResolvedTimeSpec) != 0 {
			var value askdatair.ResolvedTimeSpec
			if err := askdata.DecodeStrictJSON(request.ResolvedTimeSpec, &value); err != nil {
				return QueryResult{}, errors.New("semantic report resolved time contract is invalid")
			}
			resolvedTimeSpec = &value
		}
		result, err := executor.Semantic.CompileAndExecuteSemanticIR(ctx, SemanticExecutionRequest{
			ReportID: request.ReportID, ReportVersionID: request.ReportVersionID,
			SourceRunID: request.SourceQuestionRunID, CompilationArtifactID: request.CompilationArtifactID,
			ReleaseID: request.SemanticReleaseID, ContentHash: request.SemanticContentHash,
			IR: semanticIR, ResolvedTimeSpec: resolvedTimeSpec, FixedPlanHash: request.FixedQueryPlanHash,
			PolicyScopeHash: request.PolicyScopeHash, Filters: append([]ResolvedFilter(nil), request.Filters...),
			Limit: request.Limit,
		})
		return enforceResultLimit(result, request.Limit, err)
	case report.BindingDatasetField:
		// A dataset binding is fully described by the definition itself, so it is
		// executable against a draft. A published run still requires its version
		// pin; a draft run requires the absence of one, so a preview cannot be
		// replayed as if it came from a version.
		versionPinned := request.ReportVersionID.Validate() == nil
		if request.Draft == versionPinned {
			return QueryResult{}, errors.New("dataset report request has an inconsistent version pin")
		}
		if executor.Dataset == nil || request.ReportID.Validate() != nil ||
			request.DatasetID.Validate() != nil || request.DatasetVersionID.Validate() != nil || request.DataContextID.Validate() != nil ||
			len(request.Measures) == 0 {
			return QueryResult{}, errors.New("dataset report runner is unavailable or the logical request is invalid")
		}
		result, err := executor.Dataset.ExecuteDatasetFields(ctx, DatasetExecutionRequest{
			DatasetID: request.DatasetID, DatasetVersionID: request.DatasetVersionID, DataContextID: request.DataContextID,
			Dimensions:      append([]report.FieldBinding(nil), request.Dimensions...),
			Measures:        append([]report.FieldBinding(nil), request.Measures...),
			Filters:         append([]ResolvedFilter(nil), request.Filters...),
			Parameters:      append([]report.DefaultParameter(nil), request.Parameters...),
			PolicyScopeHash: request.PolicyScopeHash, Limit: request.Limit,
		})
		return enforceResultLimit(result, request.Limit, err)
	default:
		return QueryResult{}, fmt.Errorf("unsupported report binding mode %q", request.BindingMode)
	}
}

func enforceResultLimit(result QueryResult, limit int, err error) (QueryResult, error) {
	if err != nil {
		return QueryResult{}, err
	}
	if len(result.Rows) > limit {
		return QueryResult{}, errors.New("report query runner returned more rows than requested")
	}
	return result, nil
}
