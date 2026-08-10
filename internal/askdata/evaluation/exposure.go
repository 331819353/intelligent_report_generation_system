package evaluation

import "time"

type SealedReadPurpose string

const (
	SealedReadEvaluator SealedReadPurpose = "EVALUATOR"
	SealedReadDetails   SealedReadPurpose = "DETAILS"
)

// RecordSealedRead makes detail exposure irreversible. The evaluation runner
// may read through its dedicated purpose without exposing the sealed content.
func RecordSealedRead(state SealedShardState, purpose SealedReadPurpose, readAt time.Time) (SealedShardState, error) {
	if err := validateSealedShardState(state); err != nil || readAt.IsZero() || state.RetiredAt != nil {
		return SealedShardState{}, ErrInvalidShardRotation
	}
	switch purpose {
	case SealedReadEvaluator:
		return state, nil
	case SealedReadDetails:
		exposed := readAt.UTC()
		state.ExposedAt = &exposed
		state.RetiredAt = &exposed
		state.RetireReason = ShardRetiredExposed
		return state, nil
	default:
		return SealedShardState{}, ErrInvalidShardRotation
	}
}
