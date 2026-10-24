package matrix

import (
	"errors"
	"testing"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"
)

// The same sync failure five times running is said on the status line, once per run of
// it; a different failure starts the count over, and so does a sync that succeeds.
func TestASyncThatKeepsFailingSaysSo(t *testing.T) {
	t.Parallel()
	b := newBackend(nil)
	var said []string
	b.onSyncTrouble = func(detail string) { said = append(said, detail) }
	f := &failingSyncer{DefaultSyncer: mautrix.NewDefaultSyncer(), b: b}
	broken := errors.New("json: cannot unmarshal number into Go struct field")
	other := errors.New("dial tcp: connection refused")

	fail := func(err error, times int) {
		for range times {
			_, _ = f.OnFailedSync(nil, err)
		}
	}
	fail(broken, syncTroubleAfter-1)
	fail(other, 1)
	fail(broken, syncTroubleAfter-1)
	if len(said) != 0 {
		t.Fatalf("said %q before five identical failures in a row", said)
	}
	fail(broken, 1)
	fail(broken, 20)
	if len(said) != 1 {
		t.Fatalf("said %d times for one run of the same failure, want once: %q", len(said), said)
	}
	if err := f.ProcessResponse(t.Context(), &mautrix.RespSync{}, "since"); err != nil {
		t.Fatal(err)
	}
	fail(broken, syncTroubleAfter)
	if len(said) != 2 {
		t.Errorf("said %d times; a run after a good sync should be said again", len(said))
	}
}

// kith's own account data that does not parse is absent, not empty: the mirror keeps
// what it has instead of writing an empty set back over it.
func TestUnparseableOwnAccountDataIsAbsent(t *testing.T) {
	t.Parallel()
	evt := func(kind, raw string) *event.Event {
		return &event.Event{Type: event.Type{Type: kind, Class: event.AccountDataEventType}, Content: event.Content{VeryRaw: []byte(raw)}}
	}
	for name, kind := range map[string]string{"stars": starredType, "tags": tagsType, "people": peopleType} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			present := func(events ...*event.Event) bool {
				switch kind {
				case starredType:
					_, p := testIDs.starredFrom(events)
					return p
				case tagsType:
					_, p := tagsFrom(events)
					return p
				default:
					_, p := peopleFrom(events)
					return p
				}
			}
			if present(evt(kind, `{"events": [`)) {
				t.Error("an unparseable event read as present: the mirror would be emptied")
			}
			if !present(evt(kind, `{"events": [`), evt(kind, `{}`)) {
				t.Error("a parseable event after a broken one was not taken")
			}
		})
	}
}
