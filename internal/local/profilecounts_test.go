package local

import (
	"context"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// profileRoom is the room the profile counts are taken in (invented IDs).
const profileRoom = domain.RoomID("matrix:0123456789abcdef/!a:x")

// A profile that hides a person counts a room without them: their unread messages and
// mentions come off the network's count, in the main timeline and in each thread, a
// thread left with nothing unread drops out, and no count goes below nothing. A
// profile hiding nobody more, a request in no profile, and a room the network counted
// itself (its read position unknown here) keep the network's numbers.
func TestAProfileCountsUnreadWithoutItsPeople(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const me, ann, bob = "matrix:@me:x", "matrix:@ann:x", "matrix:@bob:x"
	s := backendAs(t, me)
	start := time.Unix(1700000000, 0)
	at := func(n int) time.Time { return start.Add(time.Duration(n) * time.Minute) }
	if err := s.cache.SaveUnread(ctx, domain.Unread{RoomID: profileRoom}, at(0).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	msgs := []domain.Message{
		{ID: "matrix:0123456789abcdef/$1", Sender: ann, Body: "a", Timestamp: at(1), Mentioned: true},
		{ID: "matrix:0123456789abcdef/$2", Sender: bob, Body: "b", Timestamp: at(2)},
		{ID: "matrix:0123456789abcdef/$3", Sender: ann, Body: "c", Timestamp: at(3)},
		{ID: "matrix:0123456789abcdef/$4", Sender: bob, Body: "root", Timestamp: at(4)},
		{ID: "matrix:0123456789abcdef/$5", Sender: ann, Body: "r1", Timestamp: at(5), ThreadRoot: "matrix:0123456789abcdef/$4", Mentioned: true},
		{ID: "matrix:0123456789abcdef/$6", Sender: bob, Body: "r2", Timestamp: at(6), ThreadRoot: "matrix:0123456789abcdef/$4"},
		{ID: "matrix:0123456789abcdef/$7", Sender: bob, Body: "root2", Timestamp: at(7)},
		{ID: "matrix:0123456789abcdef/$8", Sender: ann, Body: "r3", Timestamp: at(8), ThreadRoot: "matrix:0123456789abcdef/$7"},
	}
	for i := range msgs {
		msgs[i].RoomID = profileRoom
	}
	if err := s.cache.SaveMessages(ctx, profileRoom, msgs); err != nil {
		t.Fatal(err)
	}
	if err := s.cache.UseProfileHidden(ctx, map[string]domain.ExcludedPeople{"home": {IDs: []string{ann}}}); err != nil {
		t.Fatal(err)
	}

	// The network's count, as an adapter takes it: everyone's hidden people only.
	messages, mentions, counted, err := s.cache.CountUnread(ctx, []string{me}, profileRoom)
	if err != nil || !counted {
		t.Fatalf("CountUnread counted %v, %v", counted, err)
	}
	threads, err := s.cache.CountThreadUnread(ctx, []string{me}, profileRoom)
	if err != nil {
		t.Fatal(err)
	}
	network := domain.Unread{RoomID: profileRoom, Messages: messages, Mentions: mentions, Counted: true}.WithThreads(threads)
	if network.Messages != 8 || network.Mentions != 2 || len(network.Threads) != 2 {
		t.Fatalf("the network counts %+v, want 8 messages, 2 mentions, 2 threads", network)
	}
	server := domain.Unread{RoomID: "matrix:0123456789abcdef/!b:x", Notifications: 5, Highlights: 1}

	got, err := s.ProfileUnread(domain.WithProfile(ctx, "home"), []domain.Unread{network, server})
	if err != nil {
		t.Fatal(err)
	}
	// Ann's $1, $3 (main), $5 (thread $4) and $8 (thread $7, which then has nothing) go.
	home := got[0]
	if home.Messages != 4 || home.Mentions != 0 || len(home.Threads) != 1 || home.Threads[0].Root != "matrix:0123456789abcdef/$4" ||
		home.Threads[0].Unread != 1 || home.Threads[0].Mentions != 0 {
		t.Errorf("home counts %+v, want 4 messages, no mentions, thread $4 with 1", home)
	}
	if got[1].Notifications != 5 || got[1].Highlights != 1 || got[1].Messages != 0 {
		t.Errorf("a room the network counted itself became %+v", got[1])
	}
	single, err := s.ProfileUnread(domain.WithProfile(ctx, "home"), []domain.Unread{network})
	if err != nil || single[0].Messages != 4 || single[0].Mentions != 0 {
		t.Errorf("one room asked alone: %+v, %v; want as among all", single, err)
	}

	for _, profile := range []string{"", "work"} {
		same, sameErr := s.ProfileUnread(domain.WithProfile(ctx, profile), []domain.Unread{network})
		if sameErr != nil || same[0].Messages != network.Messages || same[0].Mentions != network.Mentions || len(same[0].Threads) != 2 {
			t.Errorf("profile %q: %+v, %v; want the network's %+v", profile, same, sameErr, network)
		}
	}

	// The network already counted less than the cache (it read further): never below nothing.
	short := domain.Unread{RoomID: profileRoom, Messages: 1, Mentions: 0, Counted: true}
	low, err := s.ProfileUnread(domain.WithProfile(ctx, "home"), []domain.Unread{short})
	if err != nil || low[0].Messages != 0 || low[0].Mentions != 0 {
		t.Errorf("a short count became %+v, %v; want nothing left", low, err)
	}
}
