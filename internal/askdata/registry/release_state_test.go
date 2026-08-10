package registry

import (
	"fmt"
	"strings"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
	"intelligent-report-generation-system/internal/materialization"
)

func TestReleaseStateIgnoresTenSameSchemaRefreshes(t *testing.T) {
	schemaHash := askdata.ContentHash(strings.Repeat("a", 64))
	for refresh := 1; refresh <= 10; refresh++ {
		state, err := EvaluateReleaseRuntimeState(schemaHash, materialization.MaterializationMeta{
			SchemaHash:      string(schemaHash),
			SnapshotVersion: fmt.Sprintf("refresh-%02d", refresh),
			QualityStatus:   materialization.SnapshotQualityOK,
		}, false)
		if err != nil {
			t.Fatalf("refresh %d: %v", refresh, err)
		}
		if state.ReleaseState != ReleaseDataActive || state.QueryState != SnapshotQueryReady {
			t.Fatalf("refresh %d state = %+v", refresh, state)
		}
	}
}

func TestReleaseStateStalesOnlyOnSchemaChange(t *testing.T) {
	state, err := EvaluateReleaseRuntimeState(
		askdata.ContentHash(strings.Repeat("a", 64)),
		materialization.MaterializationMeta{
			SchemaHash: strings.Repeat("b", 64), QualityStatus: materialization.SnapshotQualityOK,
		},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if state.ReleaseState != ReleaseDataStale || state.QueryState != SnapshotQueryBlocked {
		t.Fatalf("state = %+v", state)
	}
}

func TestFailedSnapshotWarnsOrBlocksWithoutStalingRelease(t *testing.T) {
	schemaHash := askdata.ContentHash(strings.Repeat("c", 64))
	meta := materialization.MaterializationMeta{
		SchemaHash: string(schemaHash), QualityStatus: materialization.SnapshotQualityFail,
	}
	warning, err := EvaluateReleaseRuntimeState(schemaHash, meta, true)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := EvaluateReleaseRuntimeState(schemaHash, meta, false)
	if err != nil {
		t.Fatal(err)
	}
	if warning.ReleaseState != ReleaseDataActive ||
		warning.QueryState != SnapshotQueryQualityWarning {
		t.Fatalf("warning state = %+v", warning)
	}
	if blocked.ReleaseState != ReleaseDataActive ||
		blocked.QueryState != SnapshotQueryBlocked {
		t.Fatalf("blocked state = %+v", blocked)
	}
}
