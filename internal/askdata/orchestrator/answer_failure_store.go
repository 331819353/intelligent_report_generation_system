package orchestrator

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/askdata/answer"
)

type narrativeFailureSampleStore interface {
	RecordNarrativeFailureSamples(
		context.Context, askdata.PolicyScope, Run, answer.NarrativeRunType,
		[]answer.NarrativeFailureSample,
	) error
}

func (store *PostgresStore) RecordNarrativeFailureSamples(
	ctx context.Context,
	scope askdata.PolicyScope,
	run Run,
	runType answer.NarrativeRunType,
	samples []answer.NarrativeFailureSample,
) error {
	if store == nil || (store.pool == nil && store.runner == nil) || ctx == nil ||
		scope.Validate() != nil || run.Validate() != nil || run.TenantID != scope.TenantID ||
		run.ActorID != scope.ActorID ||
		(runType != answer.NarrativeRunAskData && runType != answer.NarrativeRunReport) ||
		len(samples) > 256 {
		return ErrAnswerVerification
	}
	if len(samples) == 0 {
		return nil
	}
	return store.withActorTx(ctx, pgx.TxOptions{}, string(run.TenantID), func(tx pgx.Tx) error {
		for _, sample := range samples {
			if sample.Attempt < 1 || sample.Attempt > 2 ||
				sample.RejectedTextHash.Validate() != nil || sample.FailureCode == "" ||
				sample.FailureSpan.Start < 0 || sample.FailureSpan.End < sample.FailureSpan.Start ||
				len(sample.MetricVersionIDs) > 64 || len(sample.DimensionVersionIDs) > 64 {
				return ErrAnswerVerification
			}
			metricIDs, err := narrativeSampleIDs(sample.MetricVersionIDs)
			if err != nil {
				return err
			}
			dimensionIDs, err := narrativeSampleIDs(sample.DimensionVersionIDs)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO askdata.narrative_verification_failures(
				tenant_id,domain_id,actor_id,run_id,run_type,attempt,failure_code,
				failure_span_start,failure_span_end,rejected_text_hash,
				metric_version_ids,dimension_version_ids
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			ON CONFLICT(tenant_id,run_id,run_type,attempt,failure_code,
				failure_span_start,failure_span_end,rejected_text_hash) DO NOTHING`,
				run.TenantID, run.DomainID, run.ActorID, run.ID, runType, sample.Attempt,
				sample.FailureCode, sample.FailureSpan.Start, sample.FailureSpan.End,
				sample.RejectedTextHash, metricIDs, dimensionIDs)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func narrativeSampleIDs(values []askdata.ID) ([]string, error) {
	result := make([]string, len(values))
	for index, value := range values {
		if value.Validate() != nil {
			return nil, errors.New("narrative failure sample semantic ID is invalid")
		}
		result[index] = string(value)
	}
	return result, nil
}

var _ narrativeFailureSampleStore = (*PostgresStore)(nil)
