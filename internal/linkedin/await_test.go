package linkedin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"go.mau.fi/mautrix-linkedin/pkg/linkedingo"

	"github.com/EugeneShtoka/kith/internal/db"
)

// heldAsk is one request to the fake LinkedIn, held until the test answers it: a
// listing's body, or nil for a failure.
type heldAsk chan []byte

// heldAsks are the requests c makes, in order.
type heldAsks chan heldAsk

// connectHeld connects c, with session cookie liAt, to a fake LinkedIn that holds
// each request until the test answers it.
func connectHeld(t *testing.T, a *Adapter, c *conn, liAt string) heldAsks {
	t.Helper()
	asks, over := make(heldAsks, 16), make(chan struct{})
	t.Cleanup(func() { close(over) })
	fakeLinkedIn(t, liAt, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ask := make(heldAsk, 1)
		select {
		case asks <- ask:
		case <-over:
			return
		}
		select {
		case body := <-ask:
			if body == nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(body)
		case <-r.Context().Done():
		case <-over:
		}
	}))
	c.mu.Lock()
	c.client = a.clientFor(context.Background(), aSession(me, liAt), linkedingo.Handlers{})
	c.mu.Unlock()
	return asks
}

// next is the next request, within d.
func (h heldAsks) next(t *testing.T, d time.Duration, what string) heldAsk {
	t.Helper()
	select {
	case ask := <-h:
		return ask
	case <-time.After(d):
		t.Fatalf("LinkedIn was not asked %s within %v", what, d)
		return nil
	}
}

// listing is LinkedIn's answer to a request for what changed: convs.
func listing(t *testing.T, convs ...linkedingo.Conversation) []byte {
	t.Helper()
	body, err := json.Marshal(linkedingo.GraphQlResponse{Data: linkedingo.GraphQLData{
		MessengerConversationsBySyncToken: &linkedingo.CollectionResponse[linkedingo.ConversationSyncMetadata, linkedingo.Conversation]{
			Metadata: linkedingo.ConversationSyncMetadata{NewSyncToken: "next"}, Elements: convs,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// awaited is what kith should know of a conversation a live message named before any
// listing had it.
type awaited struct {
	listed, deleted, stopped bool // LinkedIn listed or sent it; deleted it; the adapter stopped
	asking                   bool // kith is asking for it
	held                     bool // an ask is at LinkedIn, unanswered
	woken                    bool // a message came while it was
}

// shown is whether the conversation is a room.
func (m awaited) shown() bool { return m.listed && !m.deleted }

// phase is what kith's asking is doing.
func (m awaited) phase() string {
	switch {
	case m.held && m.woken:
		return "an ask out and a message after it"
	case m.held:
		return "an ask out"
	case m.asking:
		return "waiting to ask again"
	}
	return ""
}

// awaitRun is one path of steps over a fresh adapter.
type awaitRun struct {
	t     *testing.T
	a     *Adapter
	c     *conn
	cache *db.Cache
	asks  heldAsks
	ask   heldAsk // the ask held now
	sent  int
	m     awaited
}

// awaitStep is one thing that can happen while kith awaits the conversation: then is
// what kith should know after it, do makes it happen and reports whether it answered
// the held ask.
type awaitStep struct {
	name string
	can  func(awaited) bool
	then func(awaited) awaited
	do   func(*awaitRun) bool
}

var (
	always  = func(awaited) bool { return true }
	whileAt = func(m awaited) bool { return m.held }
)

// answered is m after LinkedIn answers the held ask without the conversation: a
// message that came meanwhile asks again at once.
func answered(m awaited) awaited {
	m.held = m.asking && m.woken
	m.woken = false
	return m
}

// arrived is m after the conversation came: whatever is asked now is the last ask.
func arrived(m awaited) awaited {
	if !m.stopped {
		m.asking, m.woken = false, false
	}
	return m
}

func awaitSteps(ctx context.Context) []awaitStep {
	conv := conversation(danaDM, "", 1000, 0, false, me, dana)
	answer := func(body func(*awaitRun) []byte) func(*awaitRun) bool {
		return func(r *awaitRun) bool {
			r.ask <- body(r)
			return true
		}
	}
	return []awaitStep{
		{"a message", always, func(m awaited) awaited {
			switch {
			case m.stopped || m.shown():
			case m.held: // asked again at once, once the ask out is answered
				m.asking, m.woken = true, true
			default:
				m.asking, m.held = true, true
			}
			return m
		}, func(r *awaitRun) bool {
			r.sent++
			r.a.event(ctx, r.c, liveMessage(message(danaDM, fmt.Sprint("m", r.sent), dana, "hello", 1000), 1000))
			return false
		}},
		{"LinkedIn fails", whileAt, answered, answer(func(*awaitRun) []byte { return nil })},
		{"LinkedIn lists others", whileAt, answered, answer(func(r *awaitRun) []byte { return listing(r.t) })},
		{"LinkedIn lists it", whileAt, func(m awaited) awaited {
			m.held, m.listed = false, true
			return arrived(m)
		}, answer(func(r *awaitRun) []byte { return listing(r.t, conv) })},
		{"a sync brings it", always, func(m awaited) awaited {
			m.listed = m.listed || !m.stopped
			return arrived(m)
		}, func(r *awaitRun) bool {
			r.a.takeConversations(ctx, r.c, []linkedingo.Conversation{conv}, nil)
			return false
		}},
		{"LinkedIn deletes it", always, func(m awaited) awaited {
			m.deleted = m.deleted || !m.stopped
			return arrived(m)
		}, func(r *awaitRun) bool {
			r.a.takeConversations(ctx, r.c, nil, []string{danaDM})
			return false
		}},
		{"kithd stops", func(m awaited) bool { return !m.stopped }, func(m awaited) awaited {
			m.stopped = true
			m.asking, m.held, m.woken = false, false, false
			return m
		}, func(r *awaitRun) bool {
			r.a.Stop()
			return false
		}},
	}
}

// soon bounds an ask made at once; the waits between asks are retryFirst.
const soon = retryFirst / 2

// step takes s on r, holding the ask it makes.
func (r *awaitRun) step(s awaitStep, path []string) {
	r.t.Helper()
	before := r.m
	r.m = s.then(before)
	answered := s.do(r)
	switch {
	case r.m.held && (answered || !before.held):
		r.ask = r.asks.next(r.t, soon, fmt.Sprintf("at once after %v", path))
	case answered: // the answer is taken in before its ask lets the turn go
		taken := make(chan struct{})
		go func() {
			defer close(taken)
			r.c.syncing.Lock()
			defer r.c.syncing.Unlock()
		}()
		select {
		case <-taken:
		case <-r.asks:
			r.t.Fatalf("after %v LinkedIn was asked again at once", path)
		}
	}
}

// shows checks the conversation is a room exactly when it should be.
func (r *awaitRun) shows(path []string) {
	r.t.Helper()
	if got := slices.Contains(cachedRooms(r.t, r.cache), "Dana Reyes"); got != r.m.shown() {
		r.t.Fatalf("after %v the conversation shown = %v, want %v (%+v)", path, got, r.m.shown(), r.m)
	}
}

// A message from someone new names a conversation no listing had: its room appears
// once LinkedIn lists the conversation, and kith asks until it does. Every path of
// these steps, up to awaitDepth, from no conversation: messages (one more while an ask
// is out), LinkedIn failing or listing only others, listing it, a sync or an event
// bringing it, LinkedIn deleting it, kithd stopping. After each, the room is shown
// exactly when listed and not deleted; whatever is still asked is then refused, and
// kith asks again after the wait, unasked, until LinkedIn lists it (so a failed listing
// is not the end of it), and never after it came, was deleted, or kithd stopped.
func TestAConversationNamedByAMessageIsAskedForUntilItComes(t *testing.T) {
	t.Parallel()
	const awaitDepth = 6
	ctx := context.Background()
	steps := awaitSteps(ctx)
	// A path's state is what kith should know, and how asking last changed: the same
	// state reached by a sync while an ask is out is not the one reached while waiting.
	type state struct {
		m   awaited
		how string
	}
	type node struct {
		path []int
		state
	}
	seen := map[state]bool{{}: true}
	var nodes []node
	for queue := []node{{}}; len(queue) > 0; queue = queue[1:] {
		n := queue[0]
		nodes = append(nodes, n)
		if len(n.path) == awaitDepth {
			continue
		}
		for i, s := range steps {
			if !s.can(n.m) {
				continue
			}
			next := state{s.then(n.m), n.how}
			if phase := n.m.phase(); phase != "" && phase != next.m.phase() {
				next.how = s.name + " with " + phase
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, node{append(slices.Clone(n.path), i), next})
		}
	}
	runs := make([]*awaitRun, 0, len(nodes))
	paths := make([][]string, 0, len(nodes))
	for k, n := range nodes {
		names := make([]string, 0, len(n.path))
		for _, i := range n.path {
			names = append(names, steps[i].name)
		}
		a, c, cache, _ := cachedAdapter(t)
		r := &awaitRun{t: t, a: a, c: c, cache: cache, asks: connectHeld(t, a, c, fmt.Sprint("held-", k))}
		t.Cleanup(a.Stop)
		for i, s := range n.path {
			r.step(steps[s], names[:i+1])
		}
		r.shows(names)
		for r.m.held {
			r.step(steps[1], append(names, "LinkedIn fails")) // refused, as long as asked at once
		}
		runs, paths = append(runs, r), append(paths, names)
	}
	// Every path's last ask was answered before now, so each waits out its first wait in
	// this one, and the paths still asking have asked again by its end.
	<-time.After(retryFirst + soon)
	for k, r := range runs {
		if !r.m.asking {
			select {
			case <-r.asks:
				t.Fatalf("after %v LinkedIn was asked again", paths[k])
			default:
			}
			continue
		}
		r.ask = r.asks.next(t, soon, fmt.Sprintf("after the wait, after %v", paths[k]))
		r.m.held = true
		names := slices.Concat(paths[k], []string{"LinkedIn lists it after the wait"})
		r.step(steps[3], names)
		r.shows(names)
	}
}
