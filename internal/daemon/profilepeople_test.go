package daemon_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// peopleWorld is a profileWorld whose backend counts unread as a profile does
// (ProfileUnread): in a profile hiding Alice, her message is not counted. It records
// the profile each count ran in.
type peopleWorld struct {
	*profileWorld

	mu      sync.Mutex
	counted []string
}

func (w *peopleWorld) ProfileUnread(ctx context.Context, unread []domain.Unread) ([]domain.Unread, error) {
	w.mu.Lock()
	w.counted = append(w.counted, domain.ProfileIn(ctx))
	w.mu.Unlock()
	out := slices.Clone(unread)
	if domain.ProfileIn(ctx) == "Home" {
		for i := range out {
			out[i].Messages = max(out[i].Messages-1, 0) // Alice's one message
		}
	}
	return out, nil
}

// A profile with an archive of its own and people of its own: a room its archive takes
// out of it is neither counted nor notified there, whoever writes; a room it keeps is
// counted without its people, and their messages skip only its topic. The profiles'
// two filters meet without either moving the other.
func TestAProfilesArchiveAndPeopleCompose(t *testing.T) {
	t.Parallel()
	w := &peopleWorld{profileWorld: &profileWorld{Nop: nopWithChannels()}}
	h := serve(t, w)
	dm, group := domain.RoomID(mx+"!dm:x"), domain.RoomID(mx+"!group:x")
	w.set([]domain.Room{{ID: dm, IsDirect: true}, {ID: group}}, nil, nil)
	cfg := notifsOn("all")
	cfg.Tags = []config.Tag{{Name: "Archived"}}
	cfg.Profiles = []config.Profile{
		{Name: "first", Include: []string{"*"}},
		{
			Name: "Home", Include: []string{"*"}, Exclude: []string{"tag:Archived"},
			Archive: config.ProfileArchive{Rooms: []string{"dm"}},
			People:  config.ProfilePeople{Exclude: []string{alice}},
		},
	}
	if err := h.notifications.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	h.notifications.InvalidateScope()
	h.notifications.UseHidden(func(ctx context.Context, m domain.Message) (bool, error) {
		return domain.ProfileIn(ctx) == "Home" && m.Sender == alice, nil
	})
	ctx := t.Context()
	home, first := h.client(), h.client()
	home.UseProfile("Home")
	for _, c := range []interface{ Start(context.Context) error }{home, first} {
		go func() { _ = c.Start(ctx) }()
	}
	defer home.Stop()
	defer first.Stop()
	h.waitAttached(2)
	homeNotes, firstNotes := listen(t, home), listen(t, first)

	for _, m := range []domain.Message{msg(dm, bob, "dm:bob"), msg(group, alice, "group:alice"), msg(group, bob, "group:bob")} {
		h.notifications.Deliver(ctx, m)
	}
	var gotHome, gotFirst []string
	quiet := time.After(settle)
	for len(gotFirst) < 3 || len(gotHome) < 1 {
		select {
		case v := <-homeNotes:
			gotHome = append(gotHome, v.Alert.Body)
		case v := <-firstNotes:
			gotFirst = append(gotFirst, v.Alert.Body)
		case <-quiet:
			t.Fatalf("notifications: Home %v, first %v", gotHome, gotFirst)
		}
	}
	select {
	case v := <-homeNotes:
		gotHome = append(gotHome, v.Alert.Body)
	case <-time.After(50 * time.Millisecond):
	}
	if !slices.Equal(gotHome, []string{"group:bob"}) || len(gotFirst) != 3 {
		t.Errorf("notifications: Home %v, want Bob's in the group alone; first %v, want all three", gotHome, gotFirst)
	}

	unread, err := home.CachedUnread(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := roomsOf(unread, func(u domain.Unread) domain.RoomID { return u.RoomID }); !slices.Equal(got, []domain.RoomID{group}) {
		t.Errorf("Home's unread holds %v, want the group alone: its archive takes the DM", got)
	}
	everyone, err := first.CachedUnread(ctx)
	if err != nil || len(everyone) != 2 {
		t.Errorf("first's unread = %v, %v; want both rooms", everyone, err)
	}
}

// A profile that excludes a person gets none of their notifications, while the other
// profiles holding the room do; its unread counts come counted in it, on a call and on
// the stream, under its name as the config spells it, however the client wrote it.
func TestAProfileHidesItsPeoplesNotificationsAndCounts(t *testing.T) {
	t.Parallel()
	w := &peopleWorld{profileWorld: &profileWorld{Nop: nopWithChannels()}}
	h := serve(t, w)
	room := domain.RoomID(mx + "!a:x")
	w.set([]domain.Room{{ID: room}}, nil, nil)
	cfg := notifsOn("all")
	cfg.Profiles = []config.Profile{
		{Name: "first", Include: []string{"*"}},
		{Name: "Home", Include: []string{"*"}, People: config.ProfilePeople{Exclude: []string{alice}}},
	}
	if err := h.notifications.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	// The cache's answer: Alice is hidden in Home alone.
	h.notifications.UseHidden(func(ctx context.Context, m domain.Message) (bool, error) {
		return domain.ProfileIn(ctx) == "Home" && m.Sender == alice, nil
	})
	ctx := t.Context()
	home, first := h.client(), h.client()
	home.UseProfile("HOME")
	for _, c := range []interface{ Start(context.Context) error }{home, first} {
		go func() { _ = c.Start(ctx) }()
	}
	defer home.Stop()
	defer first.Stop()
	h.waitAttached(2)
	homeNotes, firstNotes := listen(t, home), listen(t, first)

	for _, sender := range []string{alice, bob} {
		h.notifications.Deliver(ctx, msg(room, sender, sender))
	}
	var gotHome, gotFirst []string
	quiet := time.After(settle)
	for len(gotFirst) < 2 || len(gotHome) < 1 {
		select {
		case v := <-homeNotes:
			gotHome = append(gotHome, v.Alert.Body)
		case v := <-firstNotes:
			gotFirst = append(gotFirst, v.Alert.Body)
		case <-quiet:
			t.Fatalf("notifications: Home %v, first %v", gotHome, gotFirst)
		}
	}
	select {
	case v := <-homeNotes:
		gotHome = append(gotHome, v.Alert.Body)
	case <-time.After(50 * time.Millisecond):
	}
	slices.Sort(gotFirst)
	if !slices.Equal(gotHome, []string{bob}) || !slices.Equal(gotFirst, []string{alice, bob}) {
		t.Errorf("notifications: Home %v, want Bob's alone; first %v, want both", gotHome, gotFirst)
	}

	w.Unreads <- domain.Unread{RoomID: room, Messages: 2, Counted: true}
	select {
	case u := <-home.Unread():
		if u.Messages != 1 {
			t.Errorf("Home's unread stream counted %d, want 1 without Alice", u.Messages)
		}
	case <-time.After(settle):
		t.Fatal("Home's unread stream gave nothing")
	}
	select {
	case u := <-first.Unread():
		if u.Messages != 2 {
			t.Errorf("first's unread stream counted %d, want 2", u.Messages)
		}
	case <-time.After(settle):
		t.Fatal("first's unread stream gave nothing")
	}
	w.mu.Lock()
	w.counted = nil
	w.mu.Unlock()
	if unread, err := home.CachedUnread(ctx); err != nil || len(unread) != 1 {
		t.Fatalf("Home's CachedUnread = %v, %v", unread, err)
	}
	if _, err := first.CachedUnread(ctx); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !slices.Equal(w.counted, []string{"Home", "first"}) {
		t.Errorf("counted in %v, want Home then first, as the config spells them", w.counted)
	}
}
