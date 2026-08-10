package understanding

import (
	"errors"
	"testing"

	"intelligent-report-generation-system/internal/askdata"
)

func TestResolvePinnedRelease(t *testing.T) {
	active := askdata.ReleaseRef{ReleaseID: "release-active", ContentHash: askdata.HashBytes([]byte("active"))}
	old := askdata.ReleaseRef{ReleaseID: "release-old", ContentHash: askdata.HashBytes([]byte("old"))}
	tests := []struct {
		name   string
		input  ConversationRelease
		want   askdata.ReleaseRef
		action DriftAction
	}{
		{name: "new conversation pins only after binding", want: active, action: DriftPinAfterBinding},
		{name: "active pin continues", input: ConversationRelease{Pinned: active, PinnedState: ReleasePinActive}, want: active, action: DriftUseActive},
		{name: "superseded requires confirmation", input: ConversationRelease{Pinned: old, PinnedState: ReleasePinSuperseded}, want: old, action: DriftConfirmRequired},
		{name: "retained requires confirmation", input: ConversationRelease{Pinned: old, PinnedState: ReleasePinRetained}, want: old, action: DriftConfirmRequired},
		{name: "retired forces rebind", input: ConversationRelease{Pinned: old, PinnedState: ReleasePinRetired}, want: active, action: DriftForceRebind},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, action, err := ResolvePinnedRelease(test.input, active)
			if err != nil || got != test.want || action != test.action {
				t.Fatalf("ResolvePinnedRelease() = %#v, %q, %v", got, action, err)
			}
		})
	}
}

func TestResolvePinnedReleaseRejectsInconsistentState(t *testing.T) {
	active := askdata.ReleaseRef{ReleaseID: "release-active", ContentHash: askdata.HashBytes([]byte("active"))}
	_, _, err := ResolvePinnedRelease(ConversationRelease{
		Pinned: active, PinnedState: ReleasePinSuperseded,
	}, active)
	if !errors.Is(err, ErrConversationReleasePin) {
		t.Fatalf("error = %v", err)
	}
}
