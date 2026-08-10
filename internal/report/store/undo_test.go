package store

import "testing"

func TestRevisionStacksSupportMultipleUndoRedoAndBranchReset(t *testing.T) {
	chain := []Revision{
		{RevisionNo: 1, Source: "USER"},
		{RevisionNo: 2, Source: "USER"},
		{RevisionNo: 3, Source: "UNDO", InverseOfRevisionNo: revisionPointer(2)},
		{RevisionNo: 4, Source: "UNDO", InverseOfRevisionNo: revisionPointer(1)},
		{RevisionNo: 5, Source: "REDO", InverseOfRevisionNo: revisionPointer(4)},
	}
	undo, redo, err := revisionStacks(chain)
	if err != nil {
		t.Fatal(err)
	}
	if len(undo) != 1 || undo[0].RevisionNo != 5 || len(redo) != 1 || redo[0].RevisionNo != 3 {
		t.Fatalf("stacks = undo %#v / redo %#v", undo, redo)
	}
	chain = append(chain, Revision{RevisionNo: 6, Source: "USER"})
	_, redo, err = revisionStacks(chain)
	if err != nil || len(redo) != 0 {
		t.Fatalf("new edit did not clear redo stack: %#v, %v", redo, err)
	}
}

func TestRevisionStacksRejectInconsistentInverseChain(t *testing.T) {
	_, _, err := revisionStacks([]Revision{
		{RevisionNo: 1, Source: "USER"},
		{RevisionNo: 2, Source: "UNDO", InverseOfRevisionNo: revisionPointer(99)},
	})
	if err == nil {
		t.Fatal("inconsistent undo chain succeeded")
	}
}

func revisionPointer(value int64) *int64 { return &value }
