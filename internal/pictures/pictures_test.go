package pictures

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// kept is a room's picture as the store holds it.
type kept struct {
	id, data string
	checked  time.Time
}

// memStore is a Store in memory, keeping as the cache does: a version always names
// bytes.
type memStore struct {
	mu    sync.Mutex
	rooms map[domain.RoomID]kept
}

func newStore(rooms map[domain.RoomID]kept) *memStore {
	if rooms == nil {
		rooms = map[domain.RoomID]kept{}
	}
	return &memStore{rooms: rooms}
}

func (m *memStore) PictureChecks(context.Context) (map[domain.RoomID]domain.PictureCheck, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[domain.RoomID]domain.PictureCheck{}
	for room, k := range m.rooms {
		out[room] = domain.PictureCheck{ID: k.id, Checked: k.checked}
	}
	return out, nil
}

func (m *memStore) SetPicture(_ context.Context, room domain.RoomID, id string, data []byte, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" || len(data) == 0 {
		id, data = "", nil
	}
	m.rooms[room] = kept{id: id, data: string(data), checked: at}
	return nil
}

func (m *memStore) PictureUnchanged(_ context.Context, room domain.RoomID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if k, ok := m.rooms[room]; ok {
		k.checked = at
		m.rooms[room] = k
	}
	return nil
}

func (m *memStore) get(room domain.RoomID) (kept, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k, ok := m.rooms[room]
	return k, ok
}

var (
	t0      = time.UnixMilli(1_000_000_000_000)
	errDown = errors.New("offline")
)

const room = domain.RoomID("net:acct/room")

// fetching is a Want by version whose download returns data, or fails with err; asked
// counts the downloads.
func fetching(version, data string, err error, asked *int) Want {
	return Want{Version: version, Fetch: func(context.Context) ([]byte, error) {
		*asked++
		return []byte(data), err
	}}
}

// asking is a Want learned by asking, every every: the network's picture is now
// (version, data), unless err.
func asking(version, data string, err error, every time.Duration, asked *int, keptSeen *string) Want {
	return Want{Every: every, Ask: func(_ context.Context, kept string) (string, []byte, error) {
		*asked++
		*keptSeen = kept
		if err != nil {
			return "", nil, err
		}
		if version == kept {
			return kept, nil, nil // a network says only that it is unchanged
		}
		return version, []byte(data), nil
	}}
}

// Every case of one room: never kept, kept as the same, another or no picture, recently
// checked or not; by version and by asking; failing, refused.
func TestOneRoomsPictureIsKeptAsItsNetworkHasIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	recent, old := t0.Add(-time.Hour), t0.Add(-48*time.Hour)
	for _, tc := range []struct {
		name    string
		before  *kept
		want    func(asked *int, seen *string) Want
		asks    int
		after   *kept // nil: nothing kept
		changed bool
		failed  bool
	}{
		{"a first picture by version", nil, func(n *int, _ *string) Want { return fetching("v1", "one", nil, n) },
			1, &kept{"v1", "one", t0}, true, false},
		{"the same version", &kept{"v1", "one", old}, func(n *int, _ *string) Want { return fetching("v1", "x", nil, n) },
			0, &kept{"v1", "one", old}, false, false},
		{"a new version", &kept{"v1", "one", old}, func(n *int, _ *string) Want { return fetching("v2", "two", nil, n) },
			1, &kept{"v2", "two", t0}, true, false},
		{"a picture removed", &kept{"v1", "one", old}, func(n *int, _ *string) Want { return fetching("", "", nil, n) },
			0, &kept{"", "", t0}, true, false},
		{"none, first heard", nil, func(n *int, _ *string) Want { return fetching("", "", nil, n) },
			0, &kept{"", "", t0}, false, false},
		{"a download that fails", &kept{"v1", "one", old}, func(n *int, _ *string) Want { return fetching("v2", "", errDown, n) },
			1, &kept{"v1", "one", old}, false, true},
		{"a version with no bytes", nil, func(n *int, _ *string) Want { return fetching("v1", "", nil, n) },
			1, &kept{"", "", t0}, false, false},
		{"asked, never before", nil, func(n *int, s *string) Want { return asking("a1", "one", nil, 24*time.Hour, n, s) },
			1, &kept{"a1", "one", t0}, true, false},
		{"asked recently: not again", &kept{"a1", "one", recent}, func(n *int, s *string) Want { return asking("a2", "two", nil, 24*time.Hour, n, s) },
			0, &kept{"a1", "one", recent}, false, false},
		{"due, unchanged", &kept{"a1", "one", old}, func(n *int, s *string) Want { return asking("a1", "x", nil, 24*time.Hour, n, s) },
			1, &kept{"a1", "one", t0}, false, false},
		{"due, changed", &kept{"a1", "one", old}, func(n *int, s *string) Want { return asking("a2", "two", nil, 24*time.Hour, n, s) },
			1, &kept{"a2", "two", t0}, true, false},
		{"due, removed", &kept{"a1", "one", old}, func(n *int, s *string) Want { return asking("", "", nil, 24*time.Hour, n, s) },
			1, &kept{"", "", t0}, true, false},
		{"every sweep, though recent", &kept{"a1", "one", recent}, func(n *int, s *string) Want { return asking("a2", "two", nil, 0, n, s) },
			1, &kept{"a2", "two", t0}, true, false},
		{"an ask that fails", &kept{"a1", "one", old}, func(n *int, s *string) Want { return asking("", "", errDown, 24*time.Hour, n, s) },
			1, &kept{"a1", "one", old}, false, true},
		{"an ask refused", nil, func(n *int, s *string) Want {
			return asking("", "", fmt.Errorf("PHOTO_INVALID: %w", ErrRefused), 24*time.Hour, n, s)
		}, 1, nil, false, false},
	} {
		start := map[domain.RoomID]kept{}
		if tc.before != nil {
			start[room] = *tc.before
		}
		store := newStore(start)
		asks, seen := 0, "unset"
		changed, err := Sweep(ctx, store, map[domain.RoomID]Want{room: tc.want(&asks, &seen)}, t0)
		got, ok := store.get(room)
		switch {
		case (err != nil) != tc.failed:
			t.Errorf("%s: Sweep error = %v, want failed %v", tc.name, err, tc.failed)
		case changed != tc.changed:
			t.Errorf("%s: changed = %v, want %v", tc.name, changed, tc.changed)
		case asks != tc.asks:
			t.Errorf("%s: asked the network %d times, want %d", tc.name, asks, tc.asks)
		case (tc.after == nil) != !ok || (tc.after != nil && got != *tc.after):
			t.Errorf("%s: kept %+v (%v), want %+v", tc.name, got, ok, tc.after)
		}
		if tc.before != nil && seen != "unset" && seen != tc.before.id {
			t.Errorf("%s: asked knowing %q, want the kept %q", tc.name, seen, tc.before.id)
		}
	}
}

// A sweep goes in room order, past a refused picture, and stops at a failure: the rooms
// after it are left for the next sweep.
func TestASweepGoesPastARefusalAndStopsAtAFailure(t *testing.T) {
	t.Parallel()
	var order []string
	want := func(name string, err error) Want {
		return Want{Version: name, Fetch: func(context.Context) ([]byte, error) {
			order = append(order, name)
			return []byte(name), err
		}}
	}
	store := newStore(nil)
	_, err := Sweep(context.Background(), store, map[domain.RoomID]Want{
		"r4": want("d", nil), "r1": want("a", nil), "r2": want("b", ErrRefused), "r3": want("c", errDown),
	}, t0)
	if !errors.Is(err, errDown) || !slices.Equal(order, []string{"a", "b", "c"}) {
		t.Fatalf("Sweep = %v, fetched %v; want it stopped at c after a and the refused b", err, order)
	}
	if _, ok := store.get("r4"); ok {
		t.Error("a room after the failure was kept")
	}
	if k, _ := store.get("r1"); k.id != "a" {
		t.Errorf("r1 = %+v, want a", k)
	}
}

// Wants given while a sweep runs are swept after it, only the latest: a picture only an
// overtaken want named is never fetched, and the last word on each room is the latest.
func TestASweeperSweepsOnlyTheLatestWantsAfterTheRunningOne(t *testing.T) {
	t.Parallel()
	store := newStore(nil)
	started, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var fetched []string
	fetch := func(name string, block bool) Want {
		return Want{Version: name, Fetch: func(context.Context) ([]byte, error) {
			mu.Lock()
			fetched = append(fetched, name)
			mu.Unlock()
			if block {
				close(started)
				<-release
			}
			return []byte(name), nil
		}}
	}
	changes := make(chan struct{}, 4)
	failures := make(chan error, 4)
	s := &Sweeper{Store: store, Changed: func() { changes <- struct{}{} }, Failed: func(err error) { failures <- err }}
	ctx := context.Background()
	s.Later(ctx, map[domain.RoomID]Want{room: fetch("first", true)})
	wait(t, started)
	s.Later(ctx, map[domain.RoomID]Want{room: fetch("overtaken", false), "net:acct/other": fetch("only-overtaken", false)})
	s.Later(ctx, map[domain.RoomID]Want{room: fetch("latest", false)})
	close(release)
	wait(t, changes)
	wait(t, changes)
	if k, _ := store.get(room); k.id != "latest" {
		t.Fatalf("the room = %+v, want the latest want's picture", k)
	}
	mu.Lock()
	got := slices.Clone(fetched)
	mu.Unlock()
	if !slices.Equal(got, []string{"first", "latest"}) {
		t.Errorf("fetched %v, want the running sweep's, then only the latest want's", got)
	}
	select {
	case err := <-failures:
		t.Errorf("a sweep failed: %v", err)
	default:
	}
	// Idle again: the next want runs at once.
	s.Later(ctx, map[domain.RoomID]Want{room: fetch("again", false)})
	wait(t, changes)
	if k, _ := store.get(room); k.id != "again" {
		t.Errorf("after going idle, the room = %+v, want again", k)
	}
}

// wait waits for ch, failing after 5 s rather than hanging.
func wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("waited 5 s for the sweeper")
	}
}
