package meta

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

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestAPicturesVersionIsItsFilesName(t *testing.T) {
	t.Parallel()
	for link, want := range map[string]string{
		"https://scontent.xx.fbcdn.net/v/t39.30808-1/123_456_n.jpg?stp=c0&oe=6A1B": "123_456_n.jpg",
		"https://scontent.xx.fbcdn.net/v/t39.30808-1/123_456_n.jpg?oe=7FFF":        "123_456_n.jpg",
		"":                       "",
		"https://fbcdn.example/": "",
	} {
		if got := pictureVersion(link); got != want {
			t.Errorf("pictureVersion(%q) = %q, want %q", link, got, want)
		}
	}
}

// A one-to-one thread shows its person's picture and a group its own, each downloaded
// once per picture though its link changes (Meta re-signs them); a removed picture is
// none, and a picture changed by a pushed table is downloaded again. The person's is
// kept as theirs too, the same way.
func TestAThreadShowsItsPersonsOrItsOwnPicture(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var fetched []string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched = append(fetched, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte("picture " + strings.TrimPrefix(r.URL.Path, "/")))
	}))
	t.Cleanup(cdn.Close)
	link := func(file, signature string) string { return cdn.URL + "/" + file + "?oe=" + signature }

	a, c, cache, _ := cachedAdapter(t)
	ctx := context.Background()
	dm, board, danaID := roomID(me, dana), roomID(me, group), domain.RoomID(personID(dana))
	pictures := func() map[domain.RoomID]string {
		out := map[domain.RoomID]string{}
		rooms, err := cache.Rooms(ctx)
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
		checks, cerr := cache.PersonPictures().PictureChecks(ctx)
		if cerr != nil {
			t.Fatal(cerr)
		}
		for p, c := range checks {
			out[domain.RoomID(p)] = c.ID // a person's, under their ID
		}
		return out
	}
	settled := func(want map[domain.RoomID]string) {
		t.Helper()
		var got map[domain.RoomID]string
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if got = pictures(); maps.Equal(got, want) {
				return
			}
		}
		t.Fatalf("pictures = %v, want %v", got, want)
	}

	board1 := threadRow(group, table.GROUP_THREAD, "Board games", "inbox", 20, 20, 0)
	board1.ThreadPictureUrl = link("g1.jpg", "a")
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor", ProfilePictureUrl: link("d1.jpg", "a")}},
		LSDeleteThenInsertThread:  []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 5, 0), board1},
	})
	settled(map[domain.RoomID]string{dm: "d1.jpg=picture d1.jpg", board: "g1.jpg=picture g1.jpg", danaID: "d1.jpg"})

	// Re-signed links to the same pictures fetch nothing; the group's picture changes,
	// then Dana removes hers.
	mu.Lock()
	fetched = nil
	mu.Unlock()
	a.take(ctx, c, &table.LSTable{
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: dana, Name: "Dana Mor", ProfilePictureUrl: link("d1.jpg", "b")}},
		LSSetThreadImageURL:      []*table.LSSetThreadImageURL{{ThreadKey: group, ImageURL: link("g2.jpg", "b")}},
	})
	settled(map[domain.RoomID]string{dm: "d1.jpg=picture d1.jpg", board: "g2.jpg=picture g2.jpg", danaID: "d1.jpg"})
	// A short row without a link keeps hers; a full one without it removes it.
	a.take(ctx, c, &table.LSTable{
		LSVerifyContactRowExists: []*table.LSVerifyContactRowExists{{ContactId: dana, Name: "Dana Mor"}},
	})
	time.Sleep(50 * time.Millisecond)
	settled(map[domain.RoomID]string{dm: "d1.jpg=picture d1.jpg", board: "g2.jpg=picture g2.jpg", danaID: "d1.jpg"})
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor"}},
	})
	settled(map[domain.RoomID]string{dm: "=", board: "g2.jpg=picture g2.jpg", danaID: ""})
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(fetched, []string{"/g2.jpg"}) {
		t.Errorf("after the first table, fetched %v, want only the group's new picture", fetched)
	}
}
