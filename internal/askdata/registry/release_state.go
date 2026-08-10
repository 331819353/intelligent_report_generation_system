package registry

import (
	"errors"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/materialization"
)

type ReleaseDataState string

const (
	ReleaseDataActive ReleaseDataState = "ACTIVE"
	ReleaseDataStale  ReleaseDataState = "STALE"
)

type SnapshotQueryDisposition string

const (
	SnapshotQueryReady          SnapshotQueryDisposition = "READY"
	SnapshotQueryQualityWarning SnapshotQueryDisposition = "QUALITY_WARNING"
	SnapshotQueryBlocked        SnapshotQueryDisposition = "BLOCKED"
)

type ReleaseRuntimeState struct {
	ReleaseState ReleaseDataState         `json:"releaseState"`
	QueryState   SnapshotQueryDisposition `json:"queryState"`
}

// EvaluateReleaseRuntimeState deliberately ignores snapshot_version and
// refresh timestamps. Only a canonical Dataset DSL schema-hash change can
// stale the semantic release; failed data quality is a query-time disposition.
func EvaluateReleaseRuntimeState(
	expectedSchemaHash askdata.ContentHash,
	meta materialization.MaterializationMeta,
	allowQualityWarning bool,
) (ReleaseRuntimeState, error) {
	if err := expectedSchemaHash.Validate(); err != nil {
		return ReleaseRuntimeState{}, err
	}
	actual := askdata.ContentHash(meta.SchemaHash)
	if err := actual.Validate(); err != nil {
		return ReleaseRuntimeState{}, errors.New("materialization schema hash is invalid")
	}
	state := ReleaseRuntimeState{
		ReleaseState: ReleaseDataActive,
		QueryState:   SnapshotQueryReady,
	}
	if actual != expectedSchemaHash {
		state.ReleaseState = ReleaseDataStale
		state.QueryState = SnapshotQueryBlocked
		return state, nil
	}
	if meta.QualityStatus == materialization.SnapshotQualityFail {
		if allowQualityWarning {
			state.QueryState = SnapshotQueryQualityWarning
		} else {
			state.QueryState = SnapshotQueryBlocked
		}
	}
	return state, nil
}
