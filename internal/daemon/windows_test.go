package daemon_test

import (
	"context"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// draftStore is a backend that keeps drafts and takes every write, in rooms it holds.
type draftStore struct {
	apitest.Nop
	rooms  []domain.Room
	mu     sync.Mutex
	drafts map[domain.RoomID]domain.StoredDraft
}

func (s *draftStore) Rooms(context.Context) ([]domain.Room, error) { return s.rooms, nil }

func (s *draftStore) ReplaceDraft(_ context.Context, draft, _ domain.StoredDraft) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts == nil {
		s.drafts = map[domain.RoomID]domain.StoredDraft{}
	}
	s.drafts[draft.RoomID] = draft
	return true, nil
}

func (s *draftStore) RebaseDraft(_ context.Context, draft, base domain.StoredDraft, unanswered []domain.StoredDraft) (domain.StoredDraft, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts == nil {
		s.drafts = map[domain.RoomID]domain.StoredDraft{}
	}
	stored, added := domain.RebaseDraft(draft, base, s.drafts[draft.RoomID], unanswered)
	s.drafts[draft.RoomID] = stored
	return stored, added, nil
}

func (s *draftStore) Drafts(context.Context) ([]domain.StoredDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Collect(maps.Values(s.drafts)), nil
}

// window is a client of h running with its streams open, in profile ("" the first); it
// stops at test end.
func window(t *testing.T, h *harness, profile string) *daemon.Remote {
	t.Helper()
	r := h.client()
	r.UseProfile(profile)
	attached := h.streams.Attached()
	go func() { _ = r.Start(context.Background()) }()
	t.Cleanup(r.Stop)
	h.waitAttached(attached + 1)
	// A switch to the profile it runs in returns once every stream is subscribed.
	if err := r.SwitchProfile(profile); err != nil {
		t.Fatal(err)
	}
	return r
}

// A window's write crosses the wire to be rebased in kithd, and comes back as stored,
// with the assistant's words it took in.
func TestARebasedWriteAnswersWhatKithdStored(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	w := h.client()
	read, _, err := w.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: mx + "!a:x", Body: "hi", Updated: time.UnixMilli(1000)}, domain.StoredDraft{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	appended := read
	appended.Body, appended.Updated = "hi\n\nthe report", time.UnixMilli(2000)
	if saved, aerr := h.client().ReplaceDraft(t.Context(), appended, read); !saved || aerr != nil {
		t.Fatalf("the assistant's append = %v, %v", saved, aerr)
	}
	stored, added, err := w.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: mx + "!a:x", Body: "hi there", Updated: time.UnixMilli(3000)}, read, nil)
	if err != nil || stored.Body != "hi there\n\nthe report" || added != "the report" || stored.Updated.UnixMilli() != 3000 {
		t.Fatalf("RebaseDraft = %+v, %q, %v; want the window's words, then the assistant's", stored, added, err)
	}
	if emptied, _, err := w.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: mx + "!a:x"}, stored, nil); err != nil || !emptied.Empty() || emptied.RoomID != mx+"!a:x" {
		t.Fatalf("RebaseDraft of an emptied draft = %+v, %v; want none stored", emptied, err)
	}
}

// Two windows write one draft over the same version at once: kithd keeps both, the
// other's words after the writer's, and each window reads the one draft stored. The seat
// refused the second window's write.
func TestTwoWindowsWritingOneDraftAtOnceKeepBoth(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{})
	a, b := window(t, h, ""), window(t, h, "")
	room := domain.RoomID(mx + "!a:x")
	base, _, err := a.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: room, Body: "hi", Updated: time.UnixMilli(1000)}, domain.StoredDraft{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = a.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: room, Body: "hi from the laptop", Updated: time.UnixMilli(2000)}, base, nil); err != nil {
		t.Fatalf("a's write: %v", err)
	}
	stored, added, err := b.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: room, Body: "hi from the phone", Updated: time.UnixMilli(3000)}, base, nil)
	if err != nil || stored.Body != "hi from the phone\n\nfrom the laptop" || added != "from the laptop" {
		t.Fatalf("b's write over the same version = %+v, %q, %v; want b's words, then a's", stored, added, err)
	}
	for _, w := range []*daemon.Remote{a, b} {
		if drafts, err := w.Drafts(t.Context()); err != nil || len(drafts) != 1 || drafts[0].Body != stored.Body {
			t.Errorf("a window reads %+v, %v; want the one draft stored", drafts, err)
		}
	}
}

// waitDraftsChanged waits for CHANGE_DRAFTS on r, failing after settle.
func waitDraftsChanged(t *testing.T, r *daemon.Remote, what string) {
	t.Helper()
	deadline := time.After(settle)
	for {
		select {
		case got := <-r.Changed():
			if got == domain.ChangeDrafts {
				return
			}
		case <-deadline:
			t.Fatalf("%s: drafts changed was never said", what)
		}
	}
}

// Every window hears that a stored draft changed, whoever wrote it: another window
// (rebased) or kith-mcp (compare-and-set), in any profile.
func TestEveryWindowHearsADraftChange(t *testing.T) {
	t.Parallel()
	h := profiledServe(t, &draftStore{rooms: []domain.Room{{ID: mx + "!a:x"}}}, config.Profile{Name: "work", Include: []string{"*"}}, config.Profile{Name: "home", Include: []string{"*"}})
	a, b, c := window(t, h, "work"), window(t, h, "work"), window(t, h, "home")
	room := domain.RoomID(mx + "!a:x")
	stored, _, err := a.RebaseDraft(t.Context(), domain.StoredDraft{RoomID: room, Body: "hi", Updated: time.UnixMilli(1000)}, domain.StoredDraft{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, w := range map[string]*daemon.Remote{"a": a, "b": b, "c": c} {
		waitDraftsChanged(t, w, name+", after a's write")
	}
	appended := stored
	appended.Body, appended.Updated = "hi\n\nthe report", time.UnixMilli(2000)
	if saved, err := h.client().ReplaceDraft(t.Context(), appended, stored); !saved || err != nil {
		t.Fatalf("the assistant's append = %v, %v", saved, err)
	}
	for name, w := range map[string]*daemon.Remote{"a": a, "b": b, "c": c} {
		waitDraftsChanged(t, w, name+", after the assistant's append")
	}
}

// A web link goes to a window only where kith follows it: a chat link on a network a
// cached room is on, whichever profile holds it. Any other web link goes to none and is
// told back as the browser's; a link that is no web link goes to a window as before,
// since nothing else opens it.
func TestAWebLinkGoesToAWindowOnlyWhereKithFollowsIt(t *testing.T) {
	t.Parallel()
	h := serve(t, &draftStore{rooms: []domain.Room{{ID: "telegram:42/-1"}}})
	w := window(t, h, "")
	asker := h.client()
	t.Cleanup(asker.Stop)
	for _, tc := range []struct {
		uri string
		web bool
	}{
		{"https://t.me/somebody", false},
		{"https://example.org/a", true},
		{"https://wa.me/15551234567", true}, // no WhatsApp chat is cached
		{"matrix:roomid/abc:x", false},      // nor a Matrix one, but no browser opens it
	} {
		web, err := asker.FollowLink(t.Context(), tc.uri)
		if err != nil || web != tc.web {
			t.Fatalf("FollowLink(%s) = web %v, %v; want web %v", tc.uri, web, err, tc.web)
		}
		if !tc.web {
			// A web link handed over by mistake would arrive first.
			if got := take(t, w.Follows(), "Follows()"); got != tc.uri {
				t.Errorf("the window was handed %q, want %q", got, tc.uri)
			}
		}
	}
}

// With no window listening, a link is not delivered, so `kith --open` starts one.
func TestALinkWithNoWindowIsNotDelivered(t *testing.T) {
	t.Parallel()
	empty := serve(t, &draftStore{})
	if delivered, err := empty.client().Follow(t.Context(), "matrix:roomid/abc:x"); err != nil || delivered {
		t.Errorf("Follow with no window = %v, %v; want not delivered, so a terminal is started", delivered, err)
	}
}
