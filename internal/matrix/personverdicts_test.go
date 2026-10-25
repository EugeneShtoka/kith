package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/event"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// peopleStored is the person verdicts the server holds.
func (s *accountDataServer) peopleStored(t *testing.T) peopleContent {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out peopleContent
	if err := json.Unmarshal([]byte(s.stored[peopleType]), &out); err != nil {
		t.Fatalf("person account data = %s: %v", s.stored[peopleType], err)
	}
	return out
}

// Marks and releases are written per person and tag into the account's one value, a
// retag moves them, and the mirror follows.
func TestPersonMarksAreWrittenToTheAccount(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	b := backendWith(t, srv.Server, nil)
	for _, v := range []domain.PersonVerdict{
		{Person: "matrix:@ann:x", Tag: "Spam", Rule: domain.MoveAlways, Filter: "crypto", At: time.Now()},
		{Person: "matrix:@bob:x", Tag: "Spam", Rule: domain.MoveAlways, Filter: "crypto"},
		{Person: "matrix:@bob:x", Tag: "SPAM", Released: true},
	} {
		if err := b.MarkPerson(ctx, v); err != nil {
			t.Fatalf("MarkPerson(%+v) = %v", v, err)
		}
	}
	got := srv.peopleStored(t).Verdicts
	if got["@ann:x"]["spam"].Filter != "crypto" || !got["@bob:x"]["spam"].Released || got["@bob:x"]["spam"].At == 0 {
		t.Fatalf("account data = %+v, want Ann marked and Bob released with a time", got)
	}
	if err := b.RetagVerdicts(ctx, "Spam", "Junk"); err != nil {
		t.Fatalf("RetagVerdicts = %v", err)
	}
	if got := srv.peopleStored(t).Verdicts; got["@ann:x"]["junk"].Filter != "crypto" || !got["@bob:x"]["junk"].Released {
		t.Errorf("after retag: %+v, want both under junk", got)
	}
	mirrored, err := b.cache.PersonVerdicts(ctx)
	if err != nil || len(mirrored) != 2 {
		t.Fatalf("mirror = %+v, %v; want both", mirrored, err)
	}
}

// A filter's mark never overwrites a release, even one only on the homeserver (made on
// another machine), and the release found is mirrored.
func TestAMarkCannotOverwriteAReleaseElsewhere(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	srv.stored[peopleType] = `{"verdicts":{"@ann:x":{"spam":{"released":true,"at_ms":1700000000000}}}}`
	b := backendWith(t, srv.Server, nil)

	err := b.MarkPerson(ctx, domain.PersonVerdict{Person: "matrix:@ann:x", Tag: "Spam", Rule: domain.MoveAlways, Filter: "crypto"})
	if !errors.Is(err, domain.ErrPersonReleased) {
		t.Fatalf("MarkPerson(over a release) = %v, want ErrPersonReleased", err)
	}
	if srv.writes() != 0 {
		t.Errorf("the release was overwritten: %+v", srv.peopleStored(t))
	}
	if got, _ := b.cache.PersonVerdicts(ctx); len(got) != 1 || !got[0].Released {
		t.Errorf("mirror = %+v, want the release the server had", got)
	}
}

// Person account data in a sync is the whole set: it replaces the mirror, so a release
// from another machine shows the person again here.
func TestPersonAccountDataInASyncIsMirrored(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	srv := newAccountDataServer(t)
	b := backendWith(t, srv.Server, nil)
	if err := b.cache.SetPersonVerdicts(ctx, domain.ProtocolMatrix, []domain.PersonVerdict{
		{Person: "matrix:@ann:x", Tag: "spam", Rule: domain.MoveAlways, At: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	evt := &event.Event{Type: event.Type{Type: peopleType, Class: event.AccountDataEventType}}
	evt.Content.VeryRaw = json.RawMessage(`{"verdicts":{"@ann:x":{"spam":{"released":true,"at_ms":1700000000000}},"@bob:x":{"spam":{"rule":1,"filter":"crypto","at_ms":1700000000000}}}}`)
	resp := &mautrix.RespSync{}
	resp.AccountData.Events = []*event.Event{evt}
	b.onSync(ctx, resp, "")
	got, err := b.cache.PersonVerdicts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byPerson := map[string]domain.PersonVerdict{}
	for _, v := range got {
		byPerson[v.Person] = v
	}
	if !byPerson["matrix:@ann:x"].Released || !byPerson["matrix:@bob:x"].In() || byPerson["matrix:@bob:x"].Filter != "crypto" {
		t.Errorf("mirror after the sync = %+v, want Ann released and Bob marked", got)
	}
	if _, present := peopleFrom([]*event.Event{{Type: event.Type{Type: "m.other"}}}); present {
		t.Error("another type read as person data")
	}
}
