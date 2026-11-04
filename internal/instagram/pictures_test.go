package instagram

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

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A one-to-one thread shows its person's picture and a group its own, each downloaded
// once per picture though its link changes (Instagram re-signs them); a group without
// one has none.
func TestAThreadShowsItsPersonsOrItsOwnPicture(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var fetched []string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fetched = append(fetched, r.URL.Path)
		mu.Unlock()
		_, _ = w.Write([]byte("picture " + strings.TrimPrefix(r.URL.Path, "/v/")))
	}))
	t.Cleanup(cdn.Close)
	link := func(file, signature string) string { return cdn.URL + "/v/" + file + "?oe=" + signature }

	r := newRig(t)
	ctx := context.Background()
	pictures := func() map[domain.RoomID]string {
		out := map[domain.RoomID]string{}
		rooms, err := r.cache.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range rooms {
			data, err := r.cache.Picture(ctx, rooms[i].ID)
			if err != nil {
				t.Fatal(err)
			}
			out[rooms[i].ID] = rooms[i].Picture + "=" + string(data)
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
	dmRoom, groupRoom := roomID(self, danaChat), roomID(self, groupChat)

	d, g := dm(), group()
	d.Users[0].ProfilePicURL = link("dana1.jpg", "a")
	g.ThreadImageURL = link("hikers1.jpg", "a")
	r.thread(d)
	r.thread(g)
	settled(map[domain.RoomID]string{dmRoom: "dana1.jpg=picture dana1.jpg", groupRoom: "hikers1.jpg=picture hikers1.jpg"})

	// Listed again: Dana's link re-signed, the group's picture gone.
	mu.Lock()
	fetched = nil
	mu.Unlock()
	d, g = dm(), group()
	d.Users[0].ProfilePicURL = link("dana1.jpg", "b")
	r.thread(d)
	r.thread(g)
	settled(map[domain.RoomID]string{dmRoom: "dana1.jpg=picture dana1.jpg", groupRoom: "="})
	mu.Lock()
	defer mu.Unlock()
	if len(fetched) != 0 {
		t.Errorf("listing the same pictures again fetched %v", slices.Clone(fetched))
	}
}
