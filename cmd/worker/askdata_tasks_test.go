package main

import (
	"reflect"
	"testing"
)

func TestWorkerTaskSelectionIsExplicitStableAndFailClosed(t *testing.T) {
	selection, dedicated, err := parseWorkerTaskSelection(" profile,EMBEDDING,evaluator,projector ")
	if err != nil || !dedicated || !reflect.DeepEqual(selection.names(), []string{
		"EMBEDDING", "EVALUATOR", "PROFILE", "PROJECTOR",
	}) {
		t.Fatalf("selection=%v dedicated=%v err=%v", selection.names(), dedicated, err)
	}
	if selection, dedicated, err := parseWorkerTaskSelection(""); err != nil || dedicated || selection != nil {
		t.Fatalf("legacy selection=%v dedicated=%v err=%v", selection, dedicated, err)
	}
	for _, raw := range []string{"profile,profile", "profile,unknown", ",profile"} {
		if _, _, err := parseWorkerTaskSelection(raw); err == nil {
			t.Fatalf("invalid selection %q was accepted", raw)
		}
	}
}
