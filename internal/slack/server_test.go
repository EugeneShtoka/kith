package slack

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// fakeSlack answers Web API methods by name; what each was asked is kept.
type fakeSlack struct {
	mu      sync.Mutex
	methods map[string]func(form map[string]string) any
	asked   map[string][]map[string]string
	limited map[string]int // how many more times a method answers 429 first
}

func newFakeSlack(t *testing.T, options ...slackgo.Option) (*fakeSlack, *slackgo.Client) {
	t.Helper()
	f := &fakeSlack{methods: map[string]func(map[string]string) any{}, asked: map[string][]map[string]string{}, limited: map[string]int{}}
	f.methods["users.info"] = func(form map[string]string) any {
		var users []map[string]any
		for id := range strings.SplitSeq(form["users"], ",") {
			users = append(users, map[string]any{"id": id, "profile": map[string]string{"display_name": map[string]string{"U2": "Dana", "U3": "Sam"}[id]}})
		}
		return map[string]any{"ok": true, "users": users}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		_ = r.ParseForm()
		form := map[string]string{}
		for k := range r.Form {
			form[k] = r.Form.Get(k)
		}
		f.mu.Lock()
		f.asked[method] = append(f.asked[method], form)
		answer, limited := f.methods[method], f.limited[method] > 0
		if limited {
			f.limited[method]--
		}
		f.mu.Unlock()
		if limited {
			rw.Header().Set("Retry-After", "1")
			rw.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if answer == nil {
			_ = json.NewEncoder(rw).Encode(map[string]any{"ok": false, "error": "unknown_method"})
			return
		}
		_ = json.NewEncoder(rw).Encode(answer(form))
	}))
	t.Cleanup(srv.Close)
	return f, slackgo.New("xoxc-test", append([]slackgo.Option{slackgo.OptionAPIURL(srv.URL + "/api/")}, options...)...)
}

func (f *fakeSlack) on(method string, answer func(form map[string]string) any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods[method] = answer
}

func (f *fakeSlack) calls(method string) []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[method]
}

// connectedTo is an adapter over a fresh cache with "work" (T1, signed in as U1)
// connected through client.
func connectedTo(t *testing.T, client *slackgo.Client) (*Adapter, *workspace) {
	t.Helper()
	work := Account{Name: "work", Workspace: "acme"}
	a, _ := cached(t, work)
	w := newWorkspace(work, Credentials{Team: "T1", User: "U1"}, "Acme", client, 0)
	if !a.adopt(w) {
		t.Fatal("adopt refused")
	}
	return a, w
}

func msgJSON(user, ts, text string) map[string]any {
	return map[string]any{"type": "message", "user": user, "ts": ts, "text": text}
}

// History pages newest first from Slack and reads oldest first, its senders named;
// each page is cached, and Next goes on while Slack has more. A rate limit is waited
// out once.
func TestHistoryIsPagedAndCached(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("conversations.history", func(form map[string]string) any {
		if form["cursor"] == "" {
			return map[string]any{"ok": true, "has_more": true, "response_metadata": map[string]string{"next_cursor": "older"},
				"messages": []any{msgJSON("U2", "3.0", "newest"), msgJSON("U3", "2.0", "middle <@U2>")}}
		}
		return map[string]any{"ok": true, "messages": []any{msgJSON("U2", "1.0", "oldest")}}
	})
	f.limited["conversations.history"] = 1
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")

	page, err := a.Timeline(ctx, room, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].Body != "middle @Dana" || page.Messages[1].Body != "newest" || page.Next != "older" {
		t.Fatalf("first page = %+v, next %q; want middle then newest, more to come", page.Messages, page.Next)
	}
	if page.Messages[0].SenderName != "Sam" {
		t.Errorf("sender = %q, want Sam", page.Messages[0].SenderName)
	}
	if got := f.calls("conversations.history"); len(got) != 2 || got[1]["channel"] != "C1" || got[1]["limit"] != "2" {
		t.Errorf("history asked %v; want once limited, once answered, for C1 and 2", got)
	}
	older, err := a.Timeline(ctx, room, page.Next, 2)
	if err != nil || len(older.Messages) != 1 || older.Next != "" {
		t.Fatalf("older page = %+v, %v; want the oldest and the end", older, err)
	}
	for _, id := range []domain.EventID{"slack:T1/C1/1.0", "slack:T1/C1/2.0", "slack:T1/C1/3.0"} {
		if _, found, err := a.cache.MessageByID(ctx, room, id); err != nil || !found {
			t.Errorf("%s not cached: %v", id, err)
		}
	}
	if fetched, err := a.FetchEvent(ctx, room, "slack:T1/C1/2.0"); err != nil || fetched.Body != "middle @Dana" {
		t.Errorf("FetchEvent = %+v, %v; want it from the cache", fetched, err)
	}
}

// A draft is posted as mrkdwn, cached and streamed as sent; a reply goes in the thread
// of what it answers (the thread's root, when that is itself a reply).
func TestSendPostsAndKeepsTheMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	posted := 0
	f.on("chat.postMessage", func(map[string]string) any {
		posted++
		return map[string]any{"ok": true, "channel": "C1", "ts": []string{"", "5.0", "6.0"}[posted]}
	})
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")
	if err := a.cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "slack:T1/C1/2.0", RoomID: room, Body: "in a thread", ThreadRoot: "slack:T1/C1/1.0"},
	}); err != nil {
		t.Fatal(err)
	}

	dana := domain.Mention{UserID: "slack:T1.U2", Name: "@Dana"}
	if err := a.Send(ctx, room, domain.Draft{Body: "**hi** @Dana", Mentions: []domain.Mention{dana}}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("chat.postMessage")[0]; got["text"] != "*hi* <@U2>" || got["channel"] != "C1" || got["thread_ts"] != "" {
		t.Errorf("posted %v; want mrkdwn with the mention, to C1, not in a thread", got)
	}
	sent := <-a.Messages()
	if sent.ID != "slack:T1/C1/5.0" || sent.Body != "hi @Dana" || sent.Sender != "slack:T1.U1" || len(sent.Mentions) != 1 {
		t.Errorf("streamed %+v; want ours, by its ts, as it reads", sent)
	}
	if _, found, _ := a.cache.MessageByID(ctx, room, sent.ID); !found {
		t.Error("the sent message is not cached")
	}

	if err := a.Send(ctx, room, domain.Draft{Body: "answer", ReplyTo: "slack:T1/C1/2.0"}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("chat.postMessage")[1]["thread_ts"]; got != "1.0" {
		t.Errorf("reply's thread_ts = %q, want the thread's root 1.0", got)
	}
	if reply := <-a.Messages(); reply.ThreadRoot != "slack:T1/C1/1.0" {
		t.Errorf("reply's thread = %q", reply.ThreadRoot)
	}
	if err := a.Send(ctx, room, domain.Draft{Body: "x", Edits: "slack:T1/C1/5.0"}); err == nil {
		t.Error("an edit was sent as a new message")
	}
}

// A conversation's members are asked of Slack, named, cached, and offered for
// mentions.
func TestMembersAreAskedAndNamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("conversations.members", func(form map[string]string) any {
		if form["cursor"] == "" {
			return map[string]any{"ok": true, "members": []string{"U1", "U2"}, "response_metadata": map[string]string{"next_cursor": "more"}}
		}
		return map[string]any{"ok": true, "members": []string{"U3"}}
	})
	a, _ := connectedTo(t, client)
	room := roomID("T1", "C1")
	members, err := a.RefreshMembers(ctx, room)
	if err != nil || len(members) != 3 {
		t.Fatalf("members = %+v, %v; want all three pages' people", members, err)
	}
	names := map[string]string{}
	for _, m := range members {
		names[m.UserID] = m.DisplayName
	}
	if names["slack:T1.U2"] != "Dana" || names["slack:T1.U3"] != "Sam" {
		t.Errorf("names = %v", names)
	}
	if offered, err := a.MentionCandidates(ctx, room, 10); err != nil || len(offered) != 3 {
		t.Errorf("mention candidates = %+v, %v", offered, err)
	}
}

// A live message is cached and streamed; one in a conversation the cache does not
// know yet joins it. Slack ending the session signs the account out — unless the
// connection that heard it was already replaced.
func TestLiveEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	listed := make(chan struct{}, 4)
	f.on("users.conversations", func(map[string]string) any {
		listed <- struct{}{}
		return map[string]any{"ok": true, "channels": []any{map[string]any{"id": "C9", "name": "new", "is_channel": true}}}
	})
	a, w := connectedTo(t, client)
	var sessions []Session
	var mu sync.Mutex
	a.OnSession(func(_ Account, s Session, _ string) { mu.Lock(); sessions = append(sessions, s); mu.Unlock() })

	ev := &slackgo.MessageEvent{Msg: slackgo.Msg{Channel: "C9", User: "U2", Text: "hello", Timestamp: "7.0"}}
	if !a.onEvent(ctx, w, slackgo.RTMEvent{Type: "message", Data: ev}) {
		t.Fatal("a message ended the connection")
	}
	got := <-a.Messages()
	if got.ID != "slack:T1/C9/7.0" || got.SenderName != "Dana" {
		t.Errorf("streamed %+v", got)
	}
	if _, found, _ := a.cache.MessageByID(ctx, "slack:T1/C9", got.ID); !found {
		t.Error("the live message is not cached")
	}
	<-listed // a new conversation is listed again, for its name

	stale := newWorkspace(w.account, w.creds, "Acme", client, 0)
	if a.onEvent(ctx, stale, slackgo.RTMEvent{Type: "invalid_auth", Data: &slackgo.InvalidAuthEvent{}}) {
		t.Error("the connection went on after Slack ended its session")
	}
	if !a.current(w) {
		t.Error("an old connection's end let the current one go")
	}
	if a.onEvent(ctx, w, slackgo.RTMEvent{Type: "invalid_auth", Data: &slackgo.InvalidAuthEvent{}}) || a.current(w) {
		t.Error("the current connection was kept after Slack ended its session")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sessions) != 1 || sessions[0] != SignedOut {
		t.Errorf("sessions = %v, want one SignedOut", sessions)
	}
}

// A listing asks who is in each group DM and names it by the others' names, as its
// messages show them, with them as its members.
func TestAGroupDMIsNamedByItsPeople(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("users.conversations", func(map[string]string) any {
		return map[string]any{"ok": true, "channels": []any{
			map[string]any{"id": "G1", "name": "mpdm-me--dana--sam-1", "is_mpim": true},
		}}
	})
	f.on("conversations.members", func(form map[string]string) any {
		return map[string]any{"ok": true, "members": []string{"U1", "U2", "U3"}}
	})
	a, w := connectedTo(t, client)
	w.handle = "me"
	rooms, err := a.list(ctx, w)
	if err != nil || len(rooms) != 1 || rooms[0].Name != "Dana, Sam" {
		t.Fatalf("rooms = %+v, %v; want the group DM named Dana, Sam", rooms, err)
	}
	members, err := a.Members(ctx, rooms[0].ID, 0)
	if err != nil || len(members) != 2 {
		t.Errorf("members = %+v, %v; want Dana and Sam", members, err)
	}
}

// Catching up reads each listed conversation Slack says has newer messages than the
// cache, from where the cache left off (all its recent ones when none are cached), so
// every conversation's last message is known; one up to date is not asked again, nor
// one not listed. One Slack does not count (a quiet DM) is read once, while nothing of
// it is cached.
func TestCatchingUpReadsOnlyWhatIsNewer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("client.counts", func(map[string]string) any {
		return map[string]any{"ok": true,
			"channels": []any{
				map[string]any{"id": "C1", "latest": "5.000000"}, // behind: cached up to 2.0
				map[string]any{"id": "C3", "latest": "1.000500"}, // up to date, to the millisecond
				map[string]any{"id": "C4", "latest": "9.000000"}, // not listed
			},
			"ims": []any{map[string]any{"id": "D2", "latest": "3.000000"}}, // nothing cached
		}
	})
	f.on("conversations.history", func(form map[string]string) any {
		switch form["channel"] {
		case "C1":
			return map[string]any{"ok": true, "messages": []any{msgJSON("U2", "5.0", "new"), msgJSON("U2", "4.0", "newer")}}
		default:
			return map[string]any{"ok": true, "messages": []any{msgJSON("U3", "3.0", "hi")}}
		}
	})
	a, w := connectedTo(t, client)
	w.knowChannels([]domain.Room{{ID: roomID("T1", "C1")}, {ID: roomID("T1", "D2")}, {ID: roomID("T1", "C3")}, {ID: roomID("T1", "D5")}})
	for channel, ts := range map[string]string{"C1": "2.0", "C3": "1.0"} {
		room := roomID("T1", channel)
		if err := a.cache.SaveMessages(ctx, room, []domain.Message{{ID: messageID("T1", channel, ts), RoomID: room, Body: "old", Timestamp: tsTime(ts)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.catchUp(ctx, w); err != nil {
		t.Fatal(err)
	}
	asked := map[string]string{}
	for _, form := range f.calls("conversations.history") {
		asked[form["channel"]] = form["oldest"]
	}
	if want := map[string]string{"C1": "2.000000", "D2": "", "D5": ""}; !maps.Equal(asked, want) {
		t.Errorf("history asked of %v (channel → oldest), want %v", asked, want)
	}
	last, err := a.cache.LastMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !last[roomID("T1", "C1")].Equal(time.Unix(5, 0)) || !last[roomID("T1", "D2")].Equal(time.Unix(3, 0)) {
		t.Errorf("last messages = %v; want C1 at 5, D2 at 3", last)
	}
	again := len(f.calls("conversations.history"))
	if err := a.catchUp(ctx, w); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("conversations.history")[again:]; len(got) != 0 {
		t.Errorf("caught up twice, asked again: %v", got)
	}
}

// Our reaction goes out by Slack's name and is cached; the same again takes it back.
// An edit goes out by chat.update and shows; a deletion goes out by chat.delete.
func TestReactingEditingAndDeleting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	ok := func(map[string]string) any { return map[string]any{"ok": true} }
	f.on("reactions.add", ok)
	f.on("reactions.remove", ok)
	f.on("chat.update", func(map[string]string) any { return map[string]any{"ok": true, "channel": "C1", "ts": "5.0"} })
	f.on("chat.delete", func(map[string]string) any { return map[string]any{"ok": true, "channel": "C1", "ts": "5.0"} })
	a, _ := connectedTo(t, client)
	room, target := roomID("T1", "C1"), messageID("T1", "C1", "5.0")
	if err := a.cache.SaveMessages(ctx, room, []domain.Message{{ID: target, RoomID: room, Sender: "slack:T1.U1", Body: "first", Timestamp: tsTime("5.0")}}); err != nil {
		t.Fatal(err)
	}

	if err := a.SendReaction(ctx, room, target, "👍"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("reactions.add"); len(got) != 1 || got[0]["name"] != "+1" || got[0]["timestamp"] != "5.0" {
		t.Errorf("reactions.add asked %v", got)
	}
	if rs, _ := a.cache.Reactions(ctx, room); len(rs) != 1 || rs[0].Key != "👍" {
		t.Errorf("cached reactions %+v", rs)
	}
	if err := a.SendReaction(ctx, room, target, "👍"); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("reactions.remove"); len(got) != 1 {
		t.Errorf("the same reaction again did not take it back: %v", got)
	}
	if rs, _ := a.cache.Reactions(ctx, room); len(rs) != 0 {
		t.Errorf("after taking it back: %+v", rs)
	}

	if err := a.Send(ctx, room, domain.Draft{Body: "**second**", Edits: target}); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("chat.update"); len(got) != 1 || got[0]["text"] != "*second*" || got[0]["ts"] != "5.0" {
		t.Errorf("chat.update asked %v", got)
	}
	if m, _, _ := a.cache.MessageByID(ctx, room, target); m.Body != "second" || !m.Edited {
		t.Errorf("after editing: %+v", m)
	}
	if err := a.Redact(ctx, room, target, ""); err != nil {
		t.Fatal(err)
	}
	if m, _, _ := a.cache.MessageByID(ctx, room, target); !m.Redacted {
		t.Errorf("after deleting: %+v", m)
	}
}

// Unread counts what came after Slack's last_read (and after our own newest message,
// which reads what came before it); a read on another client moves it; marking read
// here tells Slack.
func TestUnreadFollowsSlacksReadPosition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f, client := newFakeSlack(t)
	f.on("client.counts", func(map[string]string) any {
		return map[string]any{"ok": true, "channels": []any{map[string]any{"id": "C1", "latest": "4.000000", "last_read": "2.000000"}}}
	})
	f.on("conversations.history", func(map[string]string) any {
		return map[string]any{"ok": true, "messages": []any{
			msgJSON("U2", "4.0", "four"), msgJSON("U2", "3.0", "three"), msgJSON("U2", "2.0", "two"), msgJSON("U1", "1.5", "mine"),
		}}
	})
	f.on("conversations.mark", func(map[string]string) any { return map[string]any{"ok": true} })
	a, w := connectedTo(t, client)
	w.knowChannels([]domain.Room{{ID: roomID("T1", "C1")}})
	if err := a.catchUp(ctx, w); err != nil {
		t.Fatal(err)
	}
	unread := func() domain.Unread {
		rows, err := a.CachedUnread(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range rows {
			if u.RoomID == roomID("T1", "C1") {
				return u
			}
		}
		return domain.Unread{}
	}
	if u := unread(); u.Messages != 2 {
		t.Errorf("unread = %+v, want two (three and four; two was read)", u)
	}
	a.onEvent(ctx, w, slackgo.RTMEvent{Data: &slackgo.ChannelMarkedEvent{Channel: "C1", Timestamp: "3.0"}})
	if u := unread(); u.Messages != 1 {
		t.Errorf("after a read on another client: %+v, want one", u)
	}
	if err := a.MarkRead(ctx, roomID("T1", "C1"), messageID("T1", "C1", "4.0"), false); err != nil {
		t.Fatal(err)
	}
	if got := f.calls("conversations.mark"); len(got) != 1 || got[0]["ts"] != "4.0" {
		t.Errorf("conversations.mark asked %v", got)
	}
	if u := unread(); u.Messages != 0 {
		t.Errorf("after marking read: %+v", u)
	}
}

// Who is typing is streamed when it changes, never ourselves; a typist whose notices
// stop is forgotten.
func TestTyping(t *testing.T) {
	t.Parallel()
	a, _ := cached(t, Account{Name: "work", Workspace: "acme"})
	w := liveWorkspace(t, a)
	a.onTyping(w, &slackgo.UserTypingEvent{User: "U1", Channel: "C1"}) // ourselves
	a.onTyping(w, &slackgo.UserTypingEvent{User: "U2", Channel: "C1"})
	a.onTyping(w, &slackgo.UserTypingEvent{User: "U2", Channel: "C1"}) // still typing: nothing new
	got := <-a.Activity()
	if got.RoomID != roomID("T1", "C1") || len(got.Typing) != 1 || got.Typing[0] != "slack:T1.U2" {
		t.Errorf("activity = %+v, want U2 typing", got)
	}
	select {
	case more := <-a.Activity():
		t.Errorf("a second notice streamed %+v", more)
	default:
	}
	a.stoppedTyping(roomID("T1", "C1"), "slack:T1.U2")
	if gone := <-a.Activity(); len(gone.Typing) != 0 {
		t.Errorf("after the notices stopped: %+v", gone)
	}
}
