package slack

import (
	"context"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// cached is an adapter over a fresh cache, never connected.
func cached(t *testing.T, accounts ...Account) (*Adapter, *db.Cache) {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return New(cache, &memSecrets{values: map[string]string{}}, accounts, nil), cache
}

// A workspace's listing reaches the cache as its rooms, its DMs' members and its
// space; Rooms and Spaces read back only Slack's, each workspace its rooms' home; a
// listing rewrites the account's rooms alone, and is heard.
func TestAListingIsCachedAsTheWorkspace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	work, club := Account{Name: "work", Workspace: "acme"}, Account{Name: "club", Workspace: "chess"}
	a, cache := cached(t, work, club)
	var heard atomic.Int32
	a.OnRoomsChanged(func() { heard.Add(1) })
	if err := cache.SaveRooms(ctx, domain.MatrixRooms, []domain.Room{{ID: "!m:x", Name: "Matrix room"}}); err != nil {
		t.Fatal(err)
	}
	conversations := func(ids ...string) []slackgo.Channel {
		var out []slackgo.Channel
		for _, id := range ids {
			out = append(out, conversation(id, func(c *slackgo.Channel) { c.Name = "ch-" + id }))
		}
		return append(out, conversation("D9", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U9" }))
	}
	acme := &workspace{account: work, creds: Credentials{Team: "T1", User: "U1"}}
	chess := &workspace{account: club, creds: Credentials{Team: "T2", User: "U2"}}
	for _, w := range []*workspace{acme, chess} {
		if err := a.save(ctx, w, listed(w.creds.Team, w.account.Name, conversations("C1", "C2"), map[string]string{"U9": "Dana"})); err != nil {
			t.Fatal(err)
		}
	}
	// acme leaves C2: its next listing drops it, and chess keeps its own.
	if err := a.save(ctx, acme, listed("T1", "work", conversations("C1"), nil)); err != nil {
		t.Fatal(err)
	}
	rooms, err := a.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rooms {
		ids = append(ids, string(r.ID))
	}
	slices.Sort(ids)
	if want := []string{"slack:T1/C1", "slack:T1/D9", "slack:T2/C1", "slack:T2/C2", "slack:T2/D9"}; !slices.Equal(ids, want) {
		t.Errorf("rooms = %v, want %v", ids, want)
	}
	spaces, err := a.Spaces(ctx)
	if err != nil || len(spaces) != 2 {
		t.Fatalf("spaces = %+v, %v; want the two workspaces", spaces, err)
	}
	for _, s := range spaces {
		if !s.Original || s.Bridge != domain.ProtocolSlack {
			t.Errorf("workspace %+v is not its rooms' home", s)
		}
	}
	members, err := cache.Members(ctx, "slack:T2/D9", 10)
	if err != nil || len(members) != 1 || members[0].DisplayName != "Dana" {
		t.Errorf("DM members = %+v, %v", members, err)
	}
	if heard.Load() != 3 {
		t.Errorf("heard %d listings, want 3", heard.Load())
	}
	if home, _ := a.CanonicalParent(ctx, "slack:T2/C1"); home != "slack:T2/T2" {
		t.Errorf("CanonicalParent = %q, want the workspace", home)
	}
	if home, _ := a.CanonicalParent(ctx, "!m:x"); home != "" {
		t.Errorf("a Matrix room's home here = %q", home)
	}
	if got, err := a.RefreshRooms(ctx); err != nil || len(got) != 0 {
		t.Errorf("RefreshRooms with nothing connected = %v, %v", got, err)
	}

	// Me is the person in each connected workspace.
	if !a.adopt(&workspace{account: work, creds: acme.creds}) || !a.adopt(&workspace{account: club, creds: chess.creds}) {
		t.Fatal("adopt refused a current connection")
	}
	if me := a.Me(); !slices.Equal(me, []string{"slack:T1.U1", "slack:T2.U2"}) {
		t.Errorf("Me = %v", me)
	}
}
