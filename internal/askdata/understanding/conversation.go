package understanding

import (
	"errors"
	"fmt"

	"intelligent-report-generation-system/internal/askdata"
)

var ErrConversationReleasePin = errors.New("conversation release pin is invalid")

// ReleasePinStatus is the governed lifecycle state of the release currently
// pinned by a conversation. Only states that can legitimately be observed by
// a previously successful conversation are accepted here.
type ReleasePinStatus string

const (
	ReleasePinActive     ReleasePinStatus = "ACTIVE"
	ReleasePinSuperseded ReleasePinStatus = "SUPERSEDED"
	ReleasePinRetained   ReleasePinStatus = "RETAINED"
	ReleasePinRetired    ReleasePinStatus = "RETIRED"
)

type DriftAction string

const (
	DriftUseActive       DriftAction = "USE_ACTIVE"
	DriftPinAfterBinding DriftAction = "PIN_AFTER_BINDING"
	DriftConfirmRequired DriftAction = "CONFIRM_REQUIRED"
	DriftForceRebind     DriftAction = "FORCE_REBIND"
)

type ConversationRelease struct {
	Pinned       askdata.ReleaseRef
	PinnedState  ReleasePinStatus
	Acknowledged bool
}

// ResolvePinnedRelease reconciles the durable conversation pin with the
// domain's current ACTIVE release. It never silently switches a SUPERSEDED or
// RETAINED pin; RETIRED is the only forced switch because it is no longer
// runnable even for retained semantic lookups.
func ResolvePinnedRelease(
	conversation ConversationRelease,
	active askdata.ReleaseRef,
) (askdata.ReleaseRef, DriftAction, error) {
	if err := active.Validate(); err != nil || active.ReleaseID == "" {
		return askdata.ReleaseRef{}, "", fmt.Errorf("%w: active release", ErrConversationReleasePin)
	}
	if conversation.Pinned.ReleaseID == "" && conversation.Pinned.ContentHash == "" {
		if conversation.PinnedState != "" || conversation.Acknowledged {
			return askdata.ReleaseRef{}, "", fmt.Errorf("%w: empty pin metadata", ErrConversationReleasePin)
		}
		return active, DriftPinAfterBinding, nil
	}
	if err := conversation.Pinned.Validate(); err != nil || conversation.Pinned.ReleaseID == "" {
		return askdata.ReleaseRef{}, "", fmt.Errorf("%w: pinned release", ErrConversationReleasePin)
	}
	if conversation.Pinned == active {
		if conversation.PinnedState != ReleasePinActive {
			return askdata.ReleaseRef{}, "", fmt.Errorf("%w: matching release is not ACTIVE", ErrConversationReleasePin)
		}
		return active, DriftUseActive, nil
	}
	switch conversation.PinnedState {
	case ReleasePinSuperseded, ReleasePinRetained:
		return conversation.Pinned, DriftConfirmRequired, nil
	case ReleasePinRetired:
		return active, DriftForceRebind, nil
	default:
		return askdata.ReleaseRef{}, "", fmt.Errorf("%w: unsupported pinned state %q", ErrConversationReleasePin, conversation.PinnedState)
	}
}
