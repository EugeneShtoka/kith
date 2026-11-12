package route

import (
	"context"
	"slices"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberless is a network whose rooms have no members cached until a refresh fetches
// found. Each refresh takes the next of outcomes (success once they run out) and, while
// held is set, waits for it to close before it ends.
type memberless struct {
	*fake
	found []domain.Member

	mu        sync.Mutex
	cached    map[domain.RoomID][]domain.Member
	outcomes  []error
	held      chan struct{}
	refreshes int
}

const memberlessRoom domain.RoomID = "telegram:42/-11"

func newMemberless(cached, found []domain.Member, outcomes ...error) (*Router, *memberless) {
	m := &memberless{
		fake: newFake("telegram"), found: found, outcomes: outcomes,
		cached: map[domain.RoomID][]domain.Member{memberlessRoom: cached},
	}
	return routed(nil, map[domain.Protocol]Adapter{domain.ProtocolTelegram: m}), m
}

func (m *memberless) Members(_ context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	got := m.cached[room]
	if limit > 0 && len(got) > limit {
		got = got[:limit]
	}
	return slices.Clone(got), nil
}

func (m *memberless) RefreshMembers(ctx context.Context, room domain.RoomID) ([]domain.Member, error) {
	m.mu.Lock()
	m.refreshes++
	held := m.held
	var err error
	if len(m.outcomes) > 0 {
		err, m.outcomes = m.outcomes[0], m.outcomes[1:]
	}
	m.mu.Unlock()
	if held != nil {
		<-held
	}
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.cached[room] = slices.Clone(m.found)
	m.mu.Unlock()
	return m.Members(ctx, room, 0)
}

func (m *memberless) MentionCandidates(ctx context.Context, room domain.RoomID, limit int) ([]domain.Member, error) {
	return m.Members(ctx, room, limit)
}

func (m *memberless) fetches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refreshes
}

var dana = []domain.Member{{UserID: "telegram:7", DisplayName: "Dana"}}

// A room with no members cached has them fetched at its first mention, and only once a
// run, even when the network lists nobody; a failed fetch is tried again at the next
// mention, and the mention answers from the cache meanwhile, without an error.
func TestMentionsFetchAnEmptyRoomsMembersOnceARun(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name          string
		cached, found []domain.Member
		outcomes      []error
		wantFetches   int
	}{
		{name: "members found", found: dana, wantFetches: 1},
		{name: "a room that lists nobody", wantFetches: 1},
		{name: "members already cached", cached: dana, wantFetches: 0},
		{name: "offline twice, then found", found: dana, outcomes: []error{errFake, errFake}, wantFetches: 3},
	} {
		r, m := newMemberless(tc.cached, tc.found, tc.outcomes...)
		for i := range 5 {
			got, err := r.MentionCandidates(t.Context(), memberlessRoom, 0)
			if err != nil {
				t.Errorf("%s: mention %d = %v, want the cache's answer", tc.name, i, err)
			}
			want := tc.cached
			if i >= len(tc.outcomes) && tc.cached == nil {
				want = tc.found
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: mention %d = %v, want %v", tc.name, i, got, want)
			}
		}
		if got := m.fetches(); got != tc.wantFetches {
			t.Errorf("%s: fetched %d times over five mentions, want %d", tc.name, got, tc.wantFetches)
		}
	}
}

// Mentions asked at once while a room's members are fetched share that one fetch, and
// answer when it ends; one whose context ends stops waiting. After a failed fetch the
// next mention tries again.
func TestMentionsAtOnceShareOneFetch(t *testing.T) {
	t.Parallel()
	for _, failed := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			var outcomes []error
			if failed {
				outcomes = []error{errFake}
			}
			r, m := newMemberless(nil, dana, outcomes...)
			m.held = make(chan struct{})
			const callers = 4
			answers := make(chan []domain.Member, callers)
			for range callers {
				go func() {
					got, err := r.MentionCandidates(t.Context(), memberlessRoom, 0)
					if err != nil {
						t.Errorf("a mention during the fetch = %v", err)
					}
					answers <- got
				}()
			}
			synctest.Wait()
			if got := m.fetches(); got != 1 {
				t.Errorf("%d mentions at once fetched %d times, want once", callers, got)
			}

			ctx, cancel := context.WithCancel(t.Context())
			gone := make(chan struct{})
			go func() {
				_, _ = r.MentionCandidates(ctx, memberlessRoom, 0)
				close(gone)
			}()
			synctest.Wait()
			cancel()
			synctest.Wait()
			select {
			case <-gone:
			default:
				t.Error("a mention whose context ended still waits for another's fetch")
			}

			close(m.held)
			want := dana
			if failed {
				want = nil
			}
			for range callers {
				if got := <-answers; !slices.Equal(got, want) {
					t.Errorf("failed %t: a mention during the fetch = %v, want %v", failed, got, want)
				}
			}
			m.mu.Lock()
			m.held = nil
			m.mu.Unlock()
			if _, err := r.MentionCandidates(t.Context(), memberlessRoom, 0); err != nil {
				t.Fatal(err)
			}
			wantFetches := 1
			if failed {
				wantFetches = 2
			}
			if got := m.fetches(); got != wantFetches {
				t.Errorf("failed %t: fetched %d times after the next mention, want %d", failed, got, wantFetches)
			}
		})
	}
}
