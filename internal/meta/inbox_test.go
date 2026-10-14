package meta

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const (
	me    = int64(100)
	dana  = int64(201)
	eli   = int64(202)
	group = int64(900)
)

// threadRow is a full thread row.
func threadRow(key int64, kind table.ThreadType, name, folder string, active, read, unread int64) *table.LSDeleteThenInsertThread {
	return &table.LSDeleteThenInsertThread{
		ThreadKey: key, ThreadType: kind, ThreadName: name, FolderName: folder,
		LastActivityTimestampMs: active, LastReadWatermarkTimestampMs: read, UnreadMessageCount: unread,
	}
}

// live is a live message; history (upsert) ones need a range, see page.
func live(thread, sender int64, mid, text string, at int64) *table.LSInsertMessage {
	return &table.LSInsertMessage{ThreadKey: thread, SenderId: sender, MessageId: mid, Text: text, TimestampMs: at}
}

// The threads Meta sends become rooms named as Messenger names them; what is not shown
// is counted by why, and a delete in the same table as a full row is Meta's
// delete-then-insert, not a delete.
func TestTheInboxKeepsThreadsAsMessengerShowsThem(t *testing.T) {
	t.Parallel()
	in := newInbox(me)
	ch := in.apply(&table.LSTable{
		LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor"}, {Id: eli, FirstName: "Eli"}},
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			threadRow(dana, table.ONE_TO_ONE, "ignored for a DM", "inbox", 10, 5, 2),
			threadRow(group, table.GROUP_THREAD, "Board games", "inbox", 20, 20, 0),
			threadRow(me, table.ONE_TO_ONE, "", "inbox", 1, 1, 0),
			threadRow(301, table.ONE_TO_ONE, "", "spam", 1, 0, 1),
			threadRow(302, table.ENCRYPTED_OVER_WA_ONE_TO_ONE, "", "inbox", 1, 0, 1),
			threadRow(303, table.ONE_TO_ONE, "", "e2ee_cutover", 1, 0, 1),
		},
		LSDeleteThread:                  []*table.LSDeleteThread{{ThreadKey: group}}, // delete-then-insert
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{{ThreadKey: group, ContactId: eli}, {ThreadKey: group, ContactId: me}},
	})
	if ch.left["spam"] != 1 || ch.left["encrypted"] != 2 {
		t.Errorf("left = %v, want 1 spam and 2 encrypted", ch.left)
	}
	if !ch.people || len(in.threads) != 3 {
		t.Fatalf("threads = %d, people changed %v", len(in.threads), ch.people)
	}
	for key, want := range map[int64]string{dana: "Dana Mor", group: "Board games", me: noteToSelf} {
		if got := in.room(in.threads[key]).Name; got != want {
			t.Errorf("thread %d is named %q, want %q", key, got, want)
		}
	}
	if r := in.room(in.threads[group]); !slices.Equal(r.Members, []string{personID(me), personID(eli)}) || r.IsDirect {
		t.Errorf("the group = %+v", r)
	}
	if r := in.room(in.threads[dana]); !r.IsDirect || !slices.Equal(r.Members, []string{personID(me), personID(dana)}) {
		t.Errorf("the DM = %+v", r)
	}

	// A plain delete, a move to end-to-end encryption, and leaving all take threads away.
	ch = in.apply(&table.LSTable{
		LSDeleteThread:                  []*table.LSDeleteThread{{ThreadKey: dana}},
		LSMoveThreadToE2EECutoverFolder: []*table.LSMoveThreadToE2EECutoverFolder{{ThreadKey: me}},
		LSRemoveParticipantFromThread:   []*table.LSRemoveParticipantFromThread{{ThreadKey: group, ParticipantId: me}},
	})
	slices.Sort(ch.gone)
	if !slices.Equal(ch.gone, []int64{me, dana, group}) || len(in.threads) != 0 {
		t.Errorf("gone = %v, left %d threads", ch.gone, len(in.threads))
	}
}

// Read marks only move forward, and writing in a thread reads it.
func TestReadMarksOnlyMoveForward(t *testing.T) {
	t.Parallel()
	in := newInbox(me)
	in.apply(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 50, 40, 1)}})
	ch := in.apply(&table.LSTable{
		LSMarkThreadReadV2: []*table.LSMarkThreadReadV2{{ThreadKey: dana, LastReadWatermarkTimestampMs: 30}},
		LSUpdateOrInsertThread: []*table.LSUpdateOrInsertThread{{
			ThreadKey: dana, ThreadType: table.ONE_TO_ONE, FolderName: "inbox", LastReadWatermarkTimestampMs: 20,
		}},
	})
	if in.threads[dana].readTo != 40 || len(ch.read) != 0 {
		t.Errorf("an older mark moved the thread back: readTo %d, read %v", in.threads[dana].readTo, ch.read)
	}
	ch = in.apply(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{live(dana, me, "mid.1", "hi", 60)}})
	if in.threads[dana].readTo != 60 || ch.read[dana] != 60 {
		t.Errorf("writing did not read the thread: readTo %d", in.threads[dana].readTo)
	}
}

// A message for a thread not heard of yet is kept, under a placeholder the thread's
// row names later.
func TestAMessageBeforeItsThread(t *testing.T) {
	t.Parallel()
	in := newInbox(me)
	ch := in.apply(&table.LSTable{LSInsertMessage: []*table.LSInsertMessage{live(group, eli, "mid.1", "hello", 10)}})
	if len(ch.messages) != 1 || in.threads[group] == nil {
		t.Fatalf("messages %d, thread %v", len(ch.messages), in.threads[group])
	}
	in.apply(&table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(group, table.GROUP_THREAD, "Board games", "inbox", 10, 0, 1)}})
	if got := in.room(in.threads[group]).Name; got != "Board games" {
		t.Errorf("the placeholder is named %q", got)
	}
}

// What a message shows: its text and what it carries; admin notices and unsent
// messages are not shown.
func TestMessagesShowWhatTheyCarry(t *testing.T) {
	t.Parallel()
	in := newInbox(me)
	in.people[dana] = "Dana Mor"
	base := func(text string) *table.WrappedMessage {
		return &table.WrappedMessage{LSInsertMessage: live(dana, dana, "mid.1", text, 1000)}
	}
	for _, tc := range []struct {
		name string
		m    *table.WrappedMessage
		want string
	}{
		{"text", base("hello"), "hello"},
		{"a photo with words", func() *table.WrappedMessage {
			m := base("look")
			m.BlobAttachments = []*table.LSInsertBlobAttachment{{AttachmentType: table.AttachmentTypeImage}}
			return m
		}(), "look\n[photo]"},
		{"a file", func() *table.WrappedMessage {
			m := base("")
			m.BlobAttachments = []*table.LSInsertBlobAttachment{{AttachmentType: table.AttachmentTypeFile, Filename: "plan.pdf"}}
			return m
		}(), "[file: plan.pdf]"},
		{"a like", func() *table.WrappedMessage { m := base(""); m.StickerId = 369239263222822; return m }(), "👍"},
		{"a link", func() *table.WrappedMessage {
			m := base("")
			m.XMAAttachments = []*table.WrappedXMA{{LSInsertXmaAttachment: &table.LSInsertXmaAttachment{TitleText: "Docs", ActionUrl: "https://example.org"}}}
			return m
		}(), "Docs https://example.org"},
	} {
		msg, ok := in.message(tc.m)
		if !ok || msg.Body != tc.want || msg.SenderName != "Dana Mor" || msg.Sender != personID(dana) {
			t.Errorf("%s: %+v (%v), want body %q", tc.name, msg, ok, tc.want)
		}
	}
	reply := base("yes")
	reply.ReplySourceId = "mid.0"
	if msg, _ := in.message(reply); msg.ReplyTo != eventID(me, dana, "mid.0") {
		t.Errorf("reply to = %s", msg.ReplyTo)
	}
	for _, change := range []func(*table.WrappedMessage){
		func(m *table.WrappedMessage) { m.IsAdminMessage = true },
		func(m *table.WrappedMessage) { m.IsUnsent = true },
		func(m *table.WrappedMessage) { m.Text = "  " },
	} {
		m := base("x")
		change(m)
		if _, ok := in.message(m); ok {
			t.Errorf("shown: %+v", m.LSInsertMessage)
		}
	}
}

// cachedAdapter is an adapter over a real cache with one connection, its live messages
// and announced ones collected.
func cachedAdapter(t *testing.T) (*Adapter, *conn, *db.Cache, *[]domain.Message) {
	t.Helper()
	cache, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	a := New(cache, newSecrets(), nil, nil)
	var announced []domain.Message
	a.OnCached(func(m domain.Message) { announced = append(announced, m) }, nil)
	a.UseConfig(context.Background(), withAccounts("fb"))
	c := a.newConn(Account{Name: "fb"}, aSession(me, "x"), nil)
	a.adopt(c)
	return a, c, cache, &announced
}

func cachedRooms(t *testing.T, cache *db.Cache) []string {
	t.Helper()
	rooms, err := cache.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := range rooms {
		if domain.NetworkOf(string(rooms[i].ID)) == domain.ProtocolMessenger {
			names = append(names, rooms[i].Name)
		}
	}
	slices.Sort(names)
	return names
}

// Meta never lists the inbox whole, so a later, shorter page must not sweep a thread
// an earlier one listed or the cache already held; only Meta's deletes take one away.
func TestALaterPageNeverSweepsAnEarlierThread(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		threadRow(group, table.GROUP_THREAD, "Board games", "inbox", 10, 10, 0),
		threadRow(901, table.GROUP_THREAD, "Old group", "inbox", 5, 5, 0),
	}})
	// A new connection starts from what is cached, then hears of one thread only.
	c2 := a.newConn(Account{Name: "fb"}, aSession(me, "y"), nil)
	a.adopt(c2)
	a.seedRooms(ctx, c2)
	a.take(ctx, c2, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		threadRow(902, table.GROUP_THREAD, "New group", "inbox", 30, 30, 0),
	}})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games", "New group", "Old group"}) {
		t.Errorf("rooms after a shorter page = %v", got)
	}
	a.take(ctx, c2, &table.LSTable{LSDeleteThread: []*table.LSDeleteThread{{ThreadKey: 902}}})
	if got := cachedRooms(t, cache); !slices.Equal(got, []string{"Board games", "Old group"}) {
		t.Errorf("rooms after a delete = %v", got)
	}
}

// A thread read in kith stays read whatever Meta says after: an older read mark, a
// stale unread count, a row that carries both. Only a newer message makes it unread.
func TestAThreadReadInKithStaysRead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	room := roomID(me, dana)
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 3000, 1000, 2)},
		LSUpsertMessage: []*table.LSUpsertMessage{
			{ThreadKey: dana, SenderId: dana, MessageId: "mid.2", Text: "two", TimestampMs: 2000},
			{ThreadKey: dana, SenderId: dana, MessageId: "mid.3", Text: "three", TimestampMs: 3000},
		},
		LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: dana, MinTimestampMs: 2000, MaxTimestampMs: 3000}},
	})
	unread := func() int {
		n, _, counted, err := cache.CountUnread(ctx, a.Me(), room)
		if err != nil || !counted {
			t.Fatalf("count: %d, counted %v, %v", n, counted, err)
		}
		return n
	}
	if n := unread(); n != 2 {
		t.Fatalf("before reading: %d unread, want 2", n)
	}
	if err := a.MarkRead(ctx, room, eventID(me, dana, "mid.3"), false); err != nil {
		t.Fatal(err)
	}
	// Meta, not having heard (offline), repeats its old view.
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 3000, 1000, 2)},
		LSMarkThreadReadV2:       []*table.LSMarkThreadReadV2{{ThreadKey: dana, LastReadWatermarkTimestampMs: 1000}},
	})
	if n := unread(); n != 0 {
		t.Errorf("after reading in kith and Meta's stale view: %d unread, want 0", n)
	}
	if owedMarks := a.reads.take(me); owedMarks[dana] != 3000 {
		t.Errorf("the read mark Meta has not taken is not owed: %v", owedMarks)
	}
	a.take(ctx, c, &table.LSTable{LSInsertMessage: []*table.LSInsertMessage{live(dana, dana, "mid.4", "four", 4000)}})
	if n := unread(); n != 1 {
		t.Errorf("after a newer message: %d unread, want 1", n)
	}
}

// History a page carries is cached quietly; a live message is announced and streamed.
func TestOnlyLiveMessagesAreAnnounced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, announced := cachedAdapter(t)
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(group, table.GROUP_THREAD, "G", "inbox", 2000, 0, 1)},
		LSUpsertMessage:          []*table.LSUpsertMessage{{ThreadKey: group, SenderId: eli, MessageId: "mid.1", Text: "old", TimestampMs: 1000}},
		LSInsertNewMessageRange:  []*table.LSInsertNewMessageRange{{ThreadKey: group, MinTimestampMs: 1000, MaxTimestampMs: 1000}},
		LSInsertMessage:          []*table.LSInsertMessage{live(group, eli, "mid.2", "new", 2000)},
	})
	if len(*announced) != 1 || (*announced)[0].Body != "new" {
		t.Errorf("announced %+v, want only the live message", *announced)
	}
	msgs, err := cache.Messages(ctx, roomID(me, group), 10)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, m := range msgs {
		bodies = append(bodies, m.Body)
	}
	if strings.Join(bodies, ",") != "old,new" && strings.Join(bodies, ",") != "new,old" {
		t.Errorf("cached %v, want both", bodies)
	}
	select {
	case m := <-a.Messages():
		if m.Body != "new" {
			t.Errorf("streamed %q", m.Body)
		}
	case <-time.After(time.Second):
		t.Error("the live message was not streamed")
	}
}
