package slack

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/pictures"
)

// A DM shows its person's own picture: downloaded when its hash is new, not again
// while it is the same, none for Slack's generated default; a download that fails
// stops the sweep with nothing written for it.
func TestADMShowsItsPersonsPicture(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mu sync.Mutex
	var fetched []string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched = append(fetched, r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/gone") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("avatar " + r.URL.Path))
	}))
	t.Cleanup(cdn.Close)

	work := Account{Name: "work", Workspace: "acme"}
	a, cache := cached(t, work)
	w := newWorkspace(work, Credentials{Team: "T1", User: "U1"}, "", nil, 0)
	convs := []slackgo.Channel{
		conversation("C1", func(c *slackgo.Channel) { c.Name = "general" }),
		conversation("D7", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U7" }),
		conversation("D8", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U8" }),
		conversation("D9", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U9" }),
	}
	if err := a.save(ctx, w, listed("T1", "work", self{}, convs, nil, nil), time.Now()); err != nil {
		t.Fatal(err)
	}
	person := func(id, hash string, custom bool) *slackgo.User {
		u := &slackgo.User{ID: id, Name: id}
		u.Profile.IsCustomImage, u.Profile.AvatarHash, u.Profile.Image72 = custom, hash, cdn.URL+"/"+hash
		return u
	}
	pictures := func() map[domain.RoomID]string {
		out := map[domain.RoomID]string{}
		rooms, err := a.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range rooms {
			data, err := cache.Picture(ctx, rooms[i].ID)
			if err != nil {
				t.Fatal(err)
			}
			out[rooms[i].ID] = rooms[i].Picture + "=" + string(data)
		}
		return out
	}

	w.knowUser(person("U7", "h7", true))
	w.knowUser(person("U8", "h8", false)) // Slack's default
	w.knowUser(person("U9", "h9", true))
	if err := sweep(ctx, cache, w.dmPictures(convs)); err != nil {
		t.Fatal(err)
	}
	want := map[domain.RoomID]string{"slack:T1/C1": "=", "slack:T1/D7": "h7=avatar /h7", "slack:T1/D8": "=", "slack:T1/D9": "h9=avatar /h9"}
	if got := pictures(); !maps.Equal(got, want) {
		t.Fatalf("pictures = %v, want %v", got, want)
	}

	// U7's is unchanged, U8 sets one of their own, and U9 changes theirs to one the CDN
	// lost: that download fails last, keeping U9's old one.
	mu.Lock()
	fetched = nil
	mu.Unlock()
	w.knowUser(person("U8", "h8b", true))
	w.knowUser(person("U9", "gone", true))
	if err := sweep(ctx, cache, w.dmPictures(convs)); err == nil {
		t.Fatal("a sweep whose download failed succeeded")
	}
	want["slack:T1/D8"] = "h8b=avatar /h8b"
	if got := pictures(); !maps.Equal(got, want) {
		t.Fatalf("after a failed download, pictures = %v, want %v", got, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(fetched, []string{"/h8b", "/gone"}) {
		t.Errorf("the second sweep fetched %v, want only the changed pictures", fetched)
	}
}

// sweep keeps pics as the workspace's sweeper would, synchronously.
func sweep(ctx context.Context, cache pictures.Store[domain.RoomID], pics map[domain.RoomID]picture) error {
	_, err := pictures.Sweep(ctx, cache, pictureWants(pics), time.Now())
	return err
}

// The people messages name have their own pictures kept, once any of them is new: a
// custom one downloaded, Slack's default kept as none, and someone not yet known left
// for when they are. Someone known but named by no message (the directory's) is not.
func TestThePeopleMessagesNameHaveTheirPicturesKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("avatar " + r.URL.Path))
	}))
	t.Cleanup(cdn.Close)
	work := Account{Name: "work", Workspace: "acme"}
	a, cache := cached(t, work)
	changed := make(chan struct{}, 4)
	a.OnRoomsChanged(func() { changed <- struct{}{} })
	w := newWorkspace(work, Credentials{Team: "T1", User: "U1"}, "", nil, 0)
	person := func(id, hash string, custom bool) *slackgo.User {
		u := &slackgo.User{ID: id, Name: id}
		u.Profile.IsCustomImage, u.Profile.AvatarHash, u.Profile.Image72 = custom, hash, cdn.URL+"/"+hash
		return u
	}
	w.knowUser(person("U7", "h7", true))
	w.knowUser(person("U8", "h8", false)) // Slack's default
	w.knowUser(person("U5", "h5", true))  // the directory's
	a.seePeople(w, []string{"U7", "U8", "U9"})
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no change was heard")
	}
	checks, err := cache.PersonPictures().PictureChecks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for p, c := range checks {
		got[p] = c.ID
	}
	want := map[string]string{personID("T1", "U7"): "h7", personID("T1", "U8"): ""}
	if !maps.Equal(got, want) {
		t.Fatalf("people's pictures = %v, want %v", got, want)
	}
}
