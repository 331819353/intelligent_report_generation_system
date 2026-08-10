package asset

import (
	"reflect"
	"testing"
	"time"

	"intelligent-report-generation-system/internal/askdata"
)

func TestAssetCursorRoundTripAndRejectsTampering(t *testing.T) {
	want := cursorValue{UpdatedAt: time.Date(2026, 8, 10, 10, 12, 0, 0, time.FixedZone("CST", 8*60*60)), ID: askdata.ID("00000000-0000-4000-8000-000000000701")}
	encoded := encodeCursor(want.UpdatedAt, want.ID)
	got, err := decodeCursor(encoded)
	if err != nil || got.ID != want.ID || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Fatalf("cursor round trip = %#v, %v", got, err)
	}
	if _, err := decodeCursor(encoded + "!"); err == nil {
		t.Fatal("tampered cursor was accepted")
	}
}

func TestAllowedActionsFailClosedForOfflineAsset(t *testing.T) {
	got := allowedActions(LifecycleOffline, true, true, true, true, true, true, true)
	want := []Action{ActionVersions, ActionPermissions, ActionRestore}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("offline actions = %#v, want %#v", got, want)
	}
	active := allowedActions(LifecycleChanged, true, true, true, true, true, true, true)
	for _, required := range []Action{ActionView, ActionEdit, ActionPublish, ActionVersions, ActionPermissions, ActionArchive, ActionExport, ActionShare, ActionAIEdit} {
		if !containsAction(active, required) {
			t.Fatalf("active actions %#v are missing %s", active, required)
		}
	}
}

func TestGrantAndReasonValidation(t *testing.T) {
	validID := askdata.ID("00000000-0000-4000-8000-000000000701")
	if err := validateGrant(GrantInput{SubjectType: "ROLE", SubjectID: validID, Action: ActionPublish}); err != nil {
		t.Fatalf("valid grant: %v", err)
	}
	if err := validateGrant(GrantInput{SubjectType: "ROLE", SubjectID: validID, Action: Action("OWNER")}); err == nil {
		t.Fatal("unsupported report permission was accepted")
	}
	if err := validateReason("业务负责人确认下架"); err != nil {
		t.Fatalf("valid reason: %v", err)
	}
	for _, invalid := range []string{"", " padded ", "bad\nreason"} {
		if err := validateReason(invalid); err == nil {
			t.Fatalf("invalid reason %q was accepted", invalid)
		}
	}
}

func containsAction(values []Action, expected Action) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
