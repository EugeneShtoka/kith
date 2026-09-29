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
		"SpamVerdict":    protoconv.ProtoToSpam(nil),
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

// Every verification kind has a distinct wire form, and anything unknown is
// refused rather than defaulting to VerificationRequested (which would prompt the
// user to accept a flow nothing started).
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
