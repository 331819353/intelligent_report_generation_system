package dataset

import (
	"errors"
	"testing"
)

func TestResolveUniqueVersionSourceRevisionRequiresExactlyOneMatch(t *testing.T) {
	exact := RevisionRecord{RevisionSummary: RevisionSummary{ID: "revision-exact"}}

	resolved, err := resolveUniqueVersionSourceRevision([]RevisionRecord{exact})
	if err != nil || resolved.ID != exact.ID {
		t.Fatalf("single match resolved=%#v err=%v", resolved, err)
	}
	for _, matches := range [][]RevisionRecord{nil, {exact, {RevisionSummary: RevisionSummary{ID: "revision-duplicate"}}}} {
		if _, err := resolveUniqueVersionSourceRevision(matches); !errors.Is(err, ErrVersionRollbackUnavailable) {
			t.Fatalf("matches=%#v error=%v", matches, err)
		}
	}
}
