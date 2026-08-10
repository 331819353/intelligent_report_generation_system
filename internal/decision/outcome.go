package decision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"intelligent-report-generation-system/internal/askdata"
	askcompiler "intelligent-report-generation-system/internal/askdata/compiler"
	"intelligent-report-generation-system/internal/askdata/ir"
	askvalidator "intelligent-report-generation-system/internal/askdata/validator"
	"intelligent-report-generation-system/internal/platform/database"
	reportruntime "intelligent-report-generation-system/internal/report/runtime"
	reportstore "intelligent-report-generation-system/internal/report/store"
)

// GovernedOutcomeRunner recompiles a pinned Semantic IR with the current
// viewer's role set and executes it through the same coverage, EXPLAIN and
// read-only warehouse gates used by Ask Data and report runtime.
type GovernedOutcomeRunner struct {
	pool      *pgxpool.Pool
	scopes    reportruntime.ViewerScopeResolver
	compiler  *askcompiler.PinnedIRCompiler
	coverage  *askvalidator.CoverageControl
	validator *askvalidator.Validator
	executor  *askvalidator.Executor
}

func NewGovernedOutcomeRunner(pool *pgxpool.Pool, scopes reportruntime.ViewerScopeResolver, compiler *askcompiler.PinnedIRCompiler,
	coverage *askvalidator.CoverageControl, validator *askvalidator.Validator, executor *askvalidator.Executor) (*GovernedOutcomeRunner, error) {
	if pool == nil || scopes == nil || compiler == nil || coverage == nil || validator == nil || executor == nil {
		return nil, errors.New("decision outcome runner dependencies are incomplete")
	}
	return &GovernedOutcomeRunner{pool: pool, scopes: scopes, compiler: compiler, coverage: coverage, validator: validator, executor: executor}, nil
}

func (runner *GovernedOutcomeRunner) Refresh(ctx context.Context, identity Identity, metric OutcomeMetric) (OutcomeRefresh, error) {
	if runner == nil || identity.Validate() != nil || metric.ID.Validate() != nil || metric.SemanticIRHash.Validate() != nil ||
		metric.SemanticReleaseID.Validate() != nil || metric.SemanticReleaseHash.Validate() != nil {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	semanticIR, err := ir.Decode(metric.SemanticIR)
	if err != nil || semanticIR.DomainID != identity.DomainID || semanticIR.SemanticReleaseID != metric.SemanticReleaseID ||
		semanticIR.SemanticContentHash != metric.SemanticReleaseHash || len(semanticIR.Metrics) != 1 ||
		string(semanticIR.Metrics[0].MetricVersionID) != metric.MetricVersionID || len(semanticIR.GroupBy) != 0 {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	release := askdata.ReleaseRef{ReleaseID: metric.SemanticReleaseID, ContentHash: metric.SemanticReleaseHash}
	scope, err := runner.scopes.ResolveViewerScope(ctx, reportstore.Identity{
		TenantID: identity.TenantID, DomainID: identity.DomainID, ActorID: identity.ActorID,
	}, release)
	if err != nil {
		return OutcomeRefresh{}, ErrForbidden
	}
	artifact, err := runner.compiler.CompilePinnedIR(ctx, askcompiler.PinnedIRCompileRequest{Scope: scope, SemanticIR: semanticIR})
	if err != nil {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	var validation askvalidator.ValidationArtifact
	if artifact.ResolvedTimeSpec != nil {
		ids, idsErr := outcomeMaterializationIDs(artifact)
		if idsErr != nil {
			return OutcomeRefresh{}, ErrOutcomeBlocked
		}
		verdict, coverageErr := runner.coverage.Evaluate(ctx, string(identity.TenantID), ids, *artifact.ResolvedTimeSpec)
		if coverageErr != nil {
			return OutcomeRefresh{}, ErrOutcomeBlocked
		}
		if verdict.Relation == askvalidator.CoverageNone {
			drifted, driftErr := runner.releaseDrifted(ctx, identity, release)
			if driftErr != nil {
				return OutcomeRefresh{}, driftErr
			}
			return OutcomeRefresh{PolicyScopeHash: scope.PolicyHash, Drifted: drifted, Status: "NO_DATA"}, nil
		}
		validation, err = runner.validator.ValidateCovered(ctx, artifact, verdict)
	} else {
		validation, err = runner.validator.Validate(ctx, artifact)
	}
	if err != nil {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	executed, err := runner.executor.Execute(ctx, askvalidator.ExecutionRequest{RunID: uuid.NewString(), Query: artifact, Validation: validation})
	if err != nil {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	rows, ok := executed.Rows(askcompiler.QueryRoleCurrent)
	if !ok || len(rows) == 0 || len(rows[0]) == 0 || rows[0][0] == nil {
		drifted, driftErr := runner.releaseDrifted(ctx, identity, release)
		if driftErr != nil {
			return OutcomeRefresh{}, driftErr
		}
		return OutcomeRefresh{PolicyScopeHash: scope.PolicyHash, Drifted: drifted, Status: "NO_DATA"}, nil
	}
	if len(rows) != 1 {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	value, err := outcomeNumber(rows[0][0])
	if err != nil {
		return OutcomeRefresh{}, ErrOutcomeBlocked
	}
	drifted, err := runner.releaseDrifted(ctx, identity, release)
	if err != nil {
		return OutcomeRefresh{}, err
	}
	return OutcomeRefresh{Value: value, ResultHash: executed.Artifact.ResultHash, PolicyScopeHash: scope.PolicyHash,
		AsOf: time.Now().UTC(), Drifted: drifted, Status: "SUCCEEDED"}, nil
}

func (runner *GovernedOutcomeRunner) releaseDrifted(ctx context.Context, identity Identity, pinned askdata.ReleaseRef) (bool, error) {
	drifted := false
	err := database.WithTenantTx(ctx, runner.pool, string(identity.TenantID), func(tx pgx.Tx) error {
		var activeID, activeHash string
		err := tx.QueryRow(ctx, `SELECT id::text,content_hash FROM askdata.releases
			WHERE tenant_id=$1 AND domain_id=$2 AND status='ACTIVE' ORDER BY activated_at DESC NULLS LAST,id DESC LIMIT 1`, identity.TenantID, identity.DomainID).Scan(&activeID, &activeHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrOutcomeBlocked
		}
		if err != nil {
			return err
		}
		drifted = activeID != string(pinned.ReleaseID) || activeHash != string(pinned.ContentHash)
		return nil
	})
	return drifted, err
}

func outcomeMaterializationIDs(artifact askcompiler.QueryArtifact) ([]string, error) {
	seen := map[string]struct{}{}
	for _, plan := range artifact.Plans {
		if plan.Source.MaterializationID.Validate() != nil {
			return nil, ErrOutcomeBlocked
		}
		seen[string(plan.Source.MaterializationID)] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, ErrOutcomeBlocked
	}
	return ids, nil
}

func outcomeNumber(value any) (string, error) {
	var text string
	switch typed := value.(type) {
	case string:
		text = typed
	case json.Number:
		text = typed.String()
	case int:
		text = strconv.Itoa(typed)
	case int8:
		text = strconv.FormatInt(int64(typed), 10)
	case int16:
		text = strconv.FormatInt(int64(typed), 10)
	case int32:
		text = strconv.FormatInt(int64(typed), 10)
	case int64:
		text = strconv.FormatInt(typed, 10)
	case uint:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint8:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint16:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint32:
		text = strconv.FormatUint(uint64(typed), 10)
	case uint64:
		text = strconv.FormatUint(typed, 10)
	case float32:
		text = strconv.FormatFloat(float64(typed), 'g', -1, 32)
	case float64:
		text = strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return "", fmt.Errorf("outcome value is not numeric")
	}
	if !validDecimal(text) {
		return "", fmt.Errorf("outcome value is invalid")
	}
	return text, nil
}
