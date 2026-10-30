package protoconv_test

import (
	"math"
	"reflect"
	"testing"

	v1 "github.com/EugeneShtoka/kith/internal/api/backend/v1"
	"github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A zero time must stay zero, not become the epoch: the UI tests IsZero.
func TestZeroTimestampStaysZero(t *testing.T) {
	t.Parallel()

	if got := protoconv.ProtoToMessage(protoconv.MessageToProto(domain.Message{ID: "$x"})); !got.Timestamp.IsZero() {
		t.Errorf("message Timestamp = %v, want zero", got.Timestamp)
	}
	if got := protoconv.ProtoToSearchHit(protoconv.SearchHitToProto(domain.SearchHit{RoomID: "!a"})); !got.Timestamp.IsZero() {
		t.Errorf("hit Timestamp = %v, want zero", got.Timestamp)
	}
	if got := protoconv.ProtoToDeletion(protoconv.DeletionToProto(domain.Deletion{})); got.Happened() {
		t.Errorf("zero deletion came back as %+v", got)
	}
}

// An unset message yields a zero value rather than panicking.
func TestNilProtoYieldsZeroValue(t *testing.T) {
	t.Parallel()

	for name, got := range map[string]any{
		"Room":           protoconv.ProtoToRoom(nil),
		"Message":        protoconv.ProtoToMessage(nil),
		"Space":          protoconv.ProtoToSpace(nil),
		"Unread":         protoconv.ProtoToUnread(nil),
		"Reaction":       protoconv.ProtoToReaction(nil),
		"ReactionUpdate": protoconv.ProtoToReactionUpdate(nil),
		"Draft":          protoconv.ProtoToDraft(nil),
		"SearchHit":      protoconv.ProtoToSearchHit(nil),
		"TimelinePage":   protoconv.ProtoToTimelinePage(nil),
		"TagVerdict":     protoconv.ProtoToVerdict(nil),
		"PersonVerdict":  protoconv.ProtoToPersonVerdict(nil),
		"Scheduled":      protoconv.ProtoToScheduled(nil),
	} {
		if !reflect.ValueOf(got).IsZero() {
			t.Errorf("ProtoTo%s(nil) = %+v, want zero", name, got)
		}
	}
	if got := protoconv.ProtoToMedia(nil); got != nil {
		t.Errorf("ProtoToMedia(nil) = %+v, want nil", got)
	}
}

// Every verification kind has a distinct wire form; unknown is refused rather than
// defaulting to VerificationRequested.
// Each change crosses the wire as its own value and comes back; one this side does not
// know comes back as none.
func TestChangesSurviveTheRoundTrip(t *testing.T) {
	t.Parallel()

	seen := map[v1.Change]bool{}
	for _, c := range []domain.Change{domain.ChangeRooms, domain.ChangeAccounts, domain.ChangeRail} {
		wire := protoconv.ChangeToProto(c)
		if wire == v1.Change_CHANGE_UNSPECIFIED || seen[wire] {
			t.Errorf("change %d: wire form %v is unspecified or shared", c, wire)
		}
		seen[wire] = true
		if back := protoconv.ProtoToChange(wire); back != c {
			t.Errorf("ProtoToChange(%v) = %d, want %d", wire, back, c)
		}
	}
	for _, wire := range []v1.Change{v1.Change_CHANGE_UNSPECIFIED, 99} {
		if back := protoconv.ProtoToChange(wire); back != 0 {
			t.Errorf("ProtoToChange(%v) = %d, want none", wire, back)
		}
	}
}

// Every outcome crosses and comes back, each to a wire value of its own; anything else
// is refused, never read as confirmed.
func TestPairingOutcomes(t *testing.T) {
	t.Parallel()

	seen := map[v1.PairingOutcome]bool{}
	for _, o := range []domain.PairingOutcome{
		domain.PairingConfirmed, domain.PairingRefused, domain.PairingExpired, domain.PairingAbandoned,
	} {
		wire := protoconv.PairingOutcomeToProto(o)
		if wire == v1.PairingOutcome_PAIRING_OUTCOME_UNSPECIFIED || seen[wire] {
			t.Errorf("outcome %d: wire form %v is unspecified or shared", o, wire)
		}
		seen[wire] = true
		if back, ok := protoconv.ProtoToPairingOutcome(wire); !ok || back != o {
			t.Errorf("ProtoToPairingOutcome(%v) = %d, %t; want %d", wire, back, ok, o)
		}
	}
	for _, wire := range []v1.PairingOutcome{v1.PairingOutcome_PAIRING_OUTCOME_UNSPECIFIED, 99} {
		if o, ok := protoconv.ProtoToPairingOutcome(wire); ok {
			t.Errorf("ProtoToPairingOutcome(%v) = %d, accepted", wire, o)
		}
	}
}

func TestVerificationKinds(t *testing.T) {
	t.Parallel()

	seen := map[v1.VerificationKind]bool{}
	for _, kind := range []domain.VerificationKind{
		domain.VerificationRequested, domain.VerificationSAS, domain.VerificationDone,
		domain.VerificationCanceled, domain.VerificationRestored,
	} {
		wire := protoconv.VerificationKindToProto(kind)
		if wire == v1.VerificationKind_VERIFICATION_KIND_UNSPECIFIED || seen[wire] {
			t.Errorf("kind %d: wire form %v is unspecified or shared", kind, wire)
		}
		seen[wire] = true
		if back, ok := protoconv.ProtoToVerificationKind(wire); !ok || back != kind {
			t.Errorf("ProtoToVerificationKind(%v) = %d, %t; want %d", wire, back, ok, kind)
		}
	}

	for _, wire := range []v1.VerificationKind{v1.VerificationKind_VERIFICATION_KIND_UNSPECIFIED, 99} {
		if _, ok := protoconv.ProtoToVerificationKind(wire); ok {
			t.Errorf("ProtoToVerificationKind(%v) accepted", wire)
		}
		if _, ok := protoconv.ProtoToVerification(&v1.Verification{Kind: wire}); ok {
			t.Errorf("ProtoToVerification(kind %v) accepted", wire)
		}
	}
	if _, ok := protoconv.ProtoToVerification(nil); ok {
		t.Error("ProtoToVerification(nil) accepted")
	}
}

func TestClampInt32(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   int
		want int32
	}{
		{0, 0}, {42, 42}, {-7, -7},
		{math.MaxInt32 + 1, math.MaxInt32},
		{math.MinInt32 - 1, math.MinInt32},
	} {
		if got := protoconv.ClampInt32(tc.in); got != tc.want {
			t.Errorf("ClampInt32(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
