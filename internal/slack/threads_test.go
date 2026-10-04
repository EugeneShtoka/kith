package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// threadUnread is what the room's count says of each of its threads, by root ts.
func threadUnread(t *testing.T, a *Adapter, room domain.RoomID) map[string]int {
	t.Helper()
	rows, err := a.CachedUnread(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, u := range rows {
		if u.RoomID != room {
			continue
		}
		for _, th := range u.Threads {
			_, ts, _ := cutLast(domain.ParseID(string(th.Root)).Native)
			out[ts] = th.Unread
		}
	}
	return out
}

// A history page naming a thread's newest reply, which the cache lacks, has the
// thread read in the background, page after page, each beginning with the root again;
// Slack's last_read on the root is the thread's read position. ThreadPage reads the
// whole thread at once, so there is never an older page.
func TestThreadsAreReadFromSlack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	root := func() map[string]any {
		m := msgJSON("U2", "10.0", "who is in?")
		m["thread_ts"], m["reply_count"], m["latest_reply"], m["last_read"] = "10.0", 2, "12.0", "11.0"
		return m
	}
	reply := func(user, ts, text string) map[string]any {
		m := msgJSON(user, ts, text)
		m["thread_ts"] = "10.0"
		return m
	}
	f.on("conversations.history", func(map[string]string) any {
		return map[string]any{"ok": true, "messages": []any{msgJSON("U2", "20.0", "later"), root()}}
	})
	f.on("conversations.replies", func(form map[string]string) any {
		if form["cursor"] == "" {
			return map[string]any{"ok": true, "has_more": true, "response_metadata": map[string]string{"next_cursor": "p2"},
				"messages": []any{root(), reply("U3", "11.0", "me")}}
		}
		return map[string]any{"ok": true, "messages": []any{root(), reply("U3", "12.0", "and <@U1>?")}}
	})
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")
	a.readTo(ctx, room, "", tsTime("20.0")) // the channel itself is read

	if _, err := a.Timeline(ctx, room, "", 50); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		_, found, _ := a.cache.MessageByID(ctx, room, "slack:T1/C1/12.0")
		return found
	})
	got, found, err := a.cache.MessageByID(ctx, room, "slack:T1/C1/11.0")
	if err != nil || !found || got.ThreadRoot != "slack:T1/C1/10.0" {
		t.Fatalf("reply 11.0 = %+v (found %v, %v), want it cached in thread 10.0", got, found, err)
	}
	if calls := f.calls("conversations.replies"); len(calls) != 2 || calls[0]["ts"] != "10.0" || calls[1]["cursor"] != "p2" {
		t.Errorf("replies asked %v, want two pages of thread 10.0", calls)
	}
	// 11.0 is read (Slack's last_read); 12.0 names us, so the thread is followed.
	waitFor(t, func() bool { return threadUnread(t, a, room)["10.0"] == 1 })

	threads, err := a.ListThreads(ctx, room)
	if err != nil || len(threads) != 1 || threads[0].Count != 2 || threads[0].Unread != 1 || threads[0].Mentions != 1 {
		t.Errorf("ListThreads = %+v, %v; want thread 10.0, two replies, one unread naming us", threads, err)
	}

	page, err := a.ThreadPage(ctx, room, "slack:T1/C1/10.0", "", 0)
	if err != nil || len(page.Messages) != 3 || page.Next != "" || page.Messages[0].ID != "slack:T1/C1/10.0" {
		t.Errorf("ThreadPage = %+v, %v; want the root and both replies, nothing older", page, err)
	}
	if _, err := a.ThreadPage(ctx, room, "slack:T1/C2/10.0", "", 0); err == nil {
		t.Error("ThreadPage took a root of another conversation")
	}
	// A page naming the same newest reply again does not read the thread again.
	before := len(f.calls("conversations.replies"))
	if _, err := a.Timeline(ctx, room, "", 50); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if after := len(f.calls("conversations.replies")); after != before {
		t.Errorf("thread read again (%d calls, was %d) for a page naming no newer reply", after, before)
	}
}

// threadMarkedJSON is the websocket's word that a thread of C1 was read elsewhere.
func threadMarkedJSON(thread, lastRead string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"type":"thread_marked","subscription":{"type":"thread","channel":"C1","thread_ts":%q,"last_read":%q}}`, thread, lastRead))
}

// threadsRead reports whether no thread waits to be read, or is being.
func threadsRead(a *Adapter) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.readingThreads && len(a.threadQueue) == 0
}

// arrive delivers a live message in C1.
func arrive(a *Adapter, w *workspace, user, ts, thread, text string) {
	a.arrived(context.Background(), w, "C1", &slackgo.Msg{Type: "message", User: user, Timestamp: ts, ThreadTimestamp: thread, Text: text})
}

// Only followed threads count: one you spoke in, or one naming you. A thread is read
// here (MarkThreadRead) or on another client (thread_marked, live); marking rooms read
// reads their threads too.
func TestFollowedThreadsCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	var mu sync.Mutex
	threads := map[string][]any{} // what each thread holds, as Slack reads it back
	f.on("conversations.replies", func(form map[string]string) any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"ok": true, "messages": threads[form["ts"]]}
	})
	f.on("conversations.mark", func(map[string]string) any { return map[string]any{"ok": true} })
	a, w := connectedTo(t, client)
	for _, u := range []string{"U1", "U2", "U3"} {
		w.knowPerson(u, "person "+u)
	}
	say := func(user, ts, thread, text string) {
		if thread != "" {
			m := msgJSON(user, ts, text)
			m["thread_ts"] = thread
			mu.Lock()
			threads[thread] = append(threads[thread], m)
			mu.Unlock()
		}
		arrive(a, w, user, ts, thread, text)
	}
	room := roomID("T1", "C1")
	say("U2", "1.0", "", "a")
	say("U2", "2.0", "", "b")
	say("U2", "3.0", "", "c")
	say("U3", "4.0", "1.0", "in a thread nobody follows")
	say("U1", "5.0", "2.0", "mine")
	say("U3", "6.0", "2.0", "after mine")
	say("U3", "7.0", "3.0", "hey <@U1>")
	waitFor(t, func() bool { return len(f.calls("conversations.replies")) == 3 && threadsRead(a) })

	if got := threadUnread(t, a, room); len(got) != 2 || got["2.0"] != 1 || got["3.0"] != 1 {
		t.Fatalf("threads unread = %v, want 2.0 (spoke) and 3.0 (named), one each", got)
	}
	if err := a.MarkThreadRead(ctx, room, "slack:T1/C1/2.0", "slack:T1/C1/6.0", false); err != nil {
		t.Fatal(err)
	}
	a.onUnmapped(ctx, w, threadMarkedJSON("3.0", "7.0"))
	if got := threadUnread(t, a, room); len(got) != 0 {
		t.Fatalf("threads unread = %v after reading both, want none", got)
	}

	say("U2", "8.0", "2.0", "more")
	say("U2", "9.0", "3.0", "more")
	if got := threadUnread(t, a, room); len(got) != 2 {
		t.Fatalf("threads unread = %v, want both again", got)
	}
	if _, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false); err != nil {
		t.Fatal(err)
	}
	if got := threadUnread(t, a, room); len(got) != 0 {
		t.Errorf("threads unread = %v after marking the room read, want none", got)
	}
	if !a.ThreadParticipant(ctx, room, "slack:T1/C1/2.0") || a.ThreadParticipant(ctx, room, "slack:T1/C1/1.0") {
		t.Error("ThreadParticipant: want true for 2.0 (spoke), false for 1.0")
	}
}

// A draft written in a thread is posted in it, whatever it answers.
func TestADraftInAThreadIsPostedInIt(t *testing.T) {
	t.Parallel()
	f, client := newFakeSlack(t)
	f.on("chat.postMessage", func(map[string]string) any { return map[string]any{"ok": true, "channel": "C1", "ts": "50.0"} })
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")
	if err := a.Send(context.Background(), room, domain.Draft{Body: "in it", ThreadRoot: "slack:T1/C1/10.0"}); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls("chat.postMessage"); len(calls) != 1 || calls[0]["thread_ts"] != "10.0" {
		t.Errorf("posted %v, want thread_ts 10.0", calls)
	}
	sent, found, err := a.cache.MessageByID(context.Background(), room, "slack:T1/C1/50.0")
	if err != nil || !found || sent.ThreadRoot != "slack:T1/C1/10.0" {
		t.Errorf("cached %+v (found %v, %v), want it in thread 10.0", sent, found, err)
	}
}

// threadTruth is one thread as Slack holds it, and what kith was told of it.
type threadTruth struct {
	replies []truthReply
	// slackRead is the thread's read position on Slack (moved on another client).
	slackRead int
	// known is the newest read position kith was told of or set: Slack's, from a
	// thread_marked or a reading, or its own MarkThreadRead; floor is MarkRoomsRead's.
	known, floor int
	// cached is every message kith cached, by its second, deleted or not.
	cached map[int]bool
}

type truthReply struct {
	at             int // the reply's ts, in seconds
	user           string
	named, deleted bool
}

func secondTS(n int) string { return fmt.Sprintf("%d.000000", n) }

// snapshot is the thread as conversations.replies reads it now, root first.
func (tr *threadTruth) snapshot() ([]slackgo.Message, string) {
	out := []slackgo.Message{{Msg: slackgo.Msg{Type: "message", User: "U2", Timestamp: "100.000000", ThreadTimestamp: "100.000000", Text: "root"}}}
	for _, r := range tr.replies {
		if !r.deleted {
			out = append(out, slackgo.Message{Msg: replyMsg(r)})
		}
	}
	lastRead := ""
	if tr.slackRead > 0 {
		lastRead = secondTS(tr.slackRead)
	}
	return out, lastRead
}

func replyMsg(r truthReply) slackgo.Msg {
	text := "a reply"
	if r.named {
		text = "a reply for <@U1>"
	}
	return slackgo.Msg{Type: "message", User: r.user, Timestamp: secondTS(r.at), ThreadTimestamp: "100.000000", Text: text}
}

// wantUnread is what kith should count unread in the thread: replies it holds, not
// deleted and not ours, after every read position it knows and our own newest reply —
// in a thread we spoke in or that names us.
func (tr *threadTruth) wantUnread() int {
	after, spoke, named := max(tr.known, tr.floor), false, false
	for _, r := range tr.replies {
		if !tr.cached[r.at] {
			continue
		}
		if r.user == "U1" {
			spoke, after = true, max(after, r.at)
		}
		named = named || (r.named && !r.deleted)
	}
	n := 0
	for _, r := range tr.replies {
		if tr.cached[r.at] && !r.deleted && r.user != "U1" && r.at > after {
			n++
		}
	}
	if !spoke && !named {
		return 0
	}
	return n
}

// One thread lives while kith listens or not: replies arrive (live, or while it was
// away), are deleted (live or away), the thread is read on another client (said live,
// or not) and here, rooms are marked read, and readings of the thread are fetched at
// one step and written at a later one. Whatever the order: the thread's replies are
// Slack's — a stale reading never brings a deleted reply back nor drops one sent after
// it, a reading drops one deleted while kith was away — and its unread count is the
// followed thread's replies after every read position kith knows; when nothing
// happened away, after every step.
func TestThreadRepliesAndReadPositionsAgree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(80) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 7))
			f, client := newFakeSlack(t)
			f.on("conversations.mark", func(map[string]string) any { return map[string]any{"ok": true} })
			a, w := connectedTo(t, client)
			for _, u := range []string{"U1", "U2", "U3"} {
				w.knowPerson(u, "person "+u)
			}
			room, root := roomID("T1", "C1"), messageID("T1", "C1", "100.000000")
			a.mu.Lock()
			a.threadsAsked[root] = "" // the thread's readings are the test's to write
			a.mu.Unlock()
			arrive(a, w, "U2", "100.000000", "", "root")
			tr := &threadTruth{cached: map[int]bool{100: true}}
			offline := seed%2 == 1
			type reading struct {
				msgs     []slackgo.Message
				lastRead string
				read     int
				fetched  time.Time
			}
			var pending []reading
			write := func(r reading) {
				a.cacheThread(ctx, w, "C1", "100.000000", r.msgs, r.lastRead, r.fetched)
				tr.known = max(tr.known, r.read)
				for _, m := range r.msgs[1:] {
					tr.cached[int(tsTime(m.Timestamp).Unix())] = true
				}
			}
			check := func(step int) {
				t.Helper()
				if got, want := threadUnread(t, a, room)["100.000000"], tr.wantUnread(); got != want {
					t.Fatalf("step %d: thread unread %d, want %d (truth %+v)", step, got, want, tr)
				}
				cachedLive, err := a.cache.ThreadMessages(ctx, room, root, 1000)
				if err != nil {
					t.Fatal(err)
				}
				var got, want []string
				for _, m := range cachedLive {
					if m.ID != root && !m.Redacted {
						got = append(got, string(m.ID))
					}
				}
				for _, r := range tr.replies {
					if !r.deleted && tr.cached[r.at] {
						want = append(want, string(messageID("T1", "C1", secondTS(r.at))))
					}
				}
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("step %d: replies cached %v, want %v", step, got, want)
				}
			}
			next := 100
			for step := range 40 {
				kind := rng.IntN(9)
				if !offline && (kind == 1 || kind == 3) {
					kind = 0
				}
				switch kind {
				case 0, 1: // a reply, live or while away
					next++
					r := truthReply{at: next, user: []string{"U1", "U2", "U3", "U3"}[rng.IntN(4)], named: rng.IntN(4) == 0}
					tr.replies = append(tr.replies, r)
					if kind == 0 {
						m := replyMsg(r)
						a.arrived(ctx, w, "C1", &m)
						tr.cached[r.at] = true
					}
				case 2, 3: // a deletion, live or while away
					var live []int
					for i, r := range tr.replies {
						if !r.deleted {
							live = append(live, i)
						}
					}
					if len(live) == 0 {
						continue
					}
					i := live[rng.IntN(len(live))]
					tr.replies[i].deleted = true
					if kind == 2 {
						a.onDeleted(ctx, w, &slackgo.MessageEvent{Msg: slackgo.Msg{Channel: "C1", SubType: "message_deleted", DeletedTimestamp: secondTS(tr.replies[i].at), Timestamp: "999.0"}})
					}
				case 4: // a reading fetched now
					msgs, lastRead := tr.snapshot()
					pending = append(pending, reading{msgs, lastRead, tr.slackRead, time.Now()})
				case 5: // the oldest reading fetched is written
					if len(pending) == 0 {
						continue
					}
					write(pending[0])
					pending = pending[1:]
				case 6: // read here, up to the newest reply cached
					latest, err := a.cache.LatestInThread(ctx, room, root)
					if err != nil || latest == "" {
						continue
					}
					if err := a.MarkThreadRead(ctx, room, root, latest, false); err != nil {
						t.Fatal(err)
					}
					_, ts, _ := cutLast(domain.ParseID(string(latest)).Native)
					tr.known = max(tr.known, int(tsTime(ts).Unix()))
				case 7: // read on another client, said live or not
					if len(tr.replies) == 0 {
						continue
					}
					tr.slackRead = max(tr.slackRead, tr.replies[len(tr.replies)-1].at)
					if rng.IntN(2) == 0 {
						a.onUnmapped(ctx, w, threadMarkedJSON("100.000000", secondTS(tr.slackRead)))
						tr.known = max(tr.known, tr.slackRead)
					}
				case 8: // rooms marked read: the thread with them
					if rng.IntN(3) != 0 {
						continue
					}
					if _, err := a.MarkRoomsRead(ctx, []domain.RoomID{room}, false); err != nil {
						t.Fatal(err)
					}
					for n := range tr.cached {
						tr.floor = max(tr.floor, n)
					}
				}
				if !offline {
					check(step)
				}
			}
			for _, r := range pending {
				write(r)
			}
			msgs, lastRead := tr.snapshot()
			write(reading{msgs, lastRead, tr.slackRead, time.Now()})
			check(-1)
		})
	}
}

// A thread whose reading failed is read again when a page next names it.
func TestAThreadThatFailedIsReadAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	root := msgJSON("U2", "10.0", "root")
	root["thread_ts"], root["reply_count"], root["latest_reply"] = "10.0", 1, "11.0"
	f.on("conversations.history", func(map[string]string) any { return map[string]any{"ok": true, "messages": []any{root}} })
	f.on("conversations.replies", func(map[string]string) any { return map[string]any{"ok": false, "error": "internal_error"} })
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")
	for want := 1; want <= 2; want++ {
		if _, err := a.Timeline(ctx, room, "", 50); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return len(f.calls("conversations.replies")) == want && threadsRead(a) })
	}
}

// A thread whose newest reply was also sent to the channel (so is not cached as the
// thread's) is read once a run, not again for every page naming it.
func TestAThreadEndingInABroadcastIsReadOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	root := msgJSON("U2", "10.0", "root")
	root["thread_ts"], root["reply_count"], root["latest_reply"] = "10.0", 1, "11.0"
	broadcast := msgJSON("U3", "11.0", "and to the channel")
	broadcast["thread_ts"], broadcast["subtype"] = "10.0", "thread_broadcast"
	f.on("conversations.history", func(map[string]string) any {
		return map[string]any{"ok": true, "messages": []any{broadcast, root}}
	})
	f.on("conversations.replies", func(map[string]string) any {
		return map[string]any{"ok": true, "messages": []any{root, broadcast}}
	})
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")
	for range 3 {
		if _, err := a.Timeline(ctx, room, "", 50); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool { return threadsRead(a) })
	}
	if calls := f.calls("conversations.replies"); len(calls) != 1 {
		t.Errorf("thread read %d times, want once", len(calls))
	}
}
