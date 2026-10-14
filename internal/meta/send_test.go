package meta

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// What Meta's answer to a send says: the message's real ID, or why it refused.
func TestSentID(t *testing.T) {
	t.Parallel()
	ok := &table.LSTable{LSReplaceOptimsiticMessage: []*table.LSReplaceOptimsiticMessage{
		{OfflineThreadingId: "11", MessageId: "mid.other"}, {OfflineThreadingId: "42", MessageId: "mid.mine"},
	}}
	if mid, err := sentID(ok, 42); err != nil || mid != "mid.mine" {
		t.Errorf("sentID = %q, %v", mid, err)
	}
	// A refusal says Meta's reason, so the person knows why.
	for name, tc := range map[string]struct {
		resp *table.LSTable
		why  string
	}{
		"no answer": {nil, "no answer"},
		"another's": {&table.LSTable{LSReplaceOptimsiticMessage: []*table.LSReplaceOptimsiticMessage{{OfflineThreadingId: "11", MessageId: "mid.x"}}}, "named no message"},
		"failed":    {&table.LSTable{LSMarkOptimisticMessageFailed: []*table.LSMarkOptimisticMessageFailed{{Message: "blocked"}}}, "blocked"},
		"task":      {&table.LSTable{LSHandleFailedTask: []*table.LSHandleFailedTask{{Message: "rate limited"}}}, "rate limited"},
	} {
		if _, err := sentID(tc.resp, 42); !errors.Is(err, errSendRefused) || !strings.Contains(err.Error(), tc.why) {
			t.Errorf("%s: sentID = %v, want a refusal saying %q", name, err, tc.why)
		}
	}
}

// A send kith cannot make says why at once: no connection, an edit, nothing to send,
// a reply to something that is not a Messenger message.
func TestSendsThatCannotGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	room := roomID(me, dana)
	if err := a.Send(ctx, room, domain.Draft{Body: "hi", Edits: "x"}); !errors.Is(err, errEditsNotYet) {
		t.Errorf("an edit = %v", err)
	}
	if err := a.Send(ctx, room, domain.Draft{Body: "  "}); !errors.Is(err, errEmptyMessage) {
		t.Errorf("nothing = %v", err)
	}
	if err := a.Send(ctx, "!room:matrix", domain.Draft{Body: "hi"}); !errors.Is(err, errNotAThread) {
		t.Errorf("not Messenger = %v", err)
	}
	if err := a.Send(ctx, roomID(999, dana), domain.Draft{Body: "hi"}); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("an account not connected = %v", err)
	}
	// The connection exists but has no client yet (still loading): not connected.
	if err := a.Send(ctx, room, domain.Draft{Body: "hi"}); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("before the connection loads = %v", err)
	}
	// An encrypted chat before the encrypted connection is up.
	a.take(ctx, c, encryptedThread())
	if err := a.Send(ctx, room, domain.Draft{Body: "hi"}); !errors.Is(err, errNoEncrypted) {
		t.Errorf("an encrypted chat before its connection = %v", err)
	}
	if _, _, err := replyParts(&domain.Message{ID: "$matrix", Sender: "@x:y"}); !errors.Is(err, errUnknownTarget) {
		t.Errorf("a reply to a Matrix message = %v", err)
	}
	if mid, sender, err := replyParts(&domain.Message{ID: eventID(me, dana, "mid.1"), Sender: personID(dana)}); err != nil || mid != "mid.1" || sender != dana {
		t.Errorf("replyParts = %q, %d, %v", mid, sender, err)
	}
	if content, _ := encryptedText("hello"); encryptedBody(content.GetPayload().GetContent()) != "hello" {
		t.Error("an encrypted text does not read back as itself")
	}
}

// The newest page is the cache's newest; an older page comes from the cache while it
// holds enough, and an encrypted chat's pages are only what the cache holds.
func TestHistoryPages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	var upserts []*table.LSUpsertMessage
	for i := range 5 {
		upserts = append(upserts, &table.LSUpsertMessage{ThreadKey: group, SenderId: eli, MessageId: "mid." + itoa(int64(i)), Text: "m", TimestampMs: int64(1000 * (i + 1))})
	}
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(group, table.GROUP_THREAD, "G", "inbox", 5000, 5000, 0)},
		LSUpsertMessage:          upserts,
		LSInsertNewMessageRange:  []*table.LSInsertNewMessageRange{{ThreadKey: group, MinTimestampMs: 1000, MaxTimestampMs: 5000}},
	})
	room := roomID(me, group)
	newest, err := a.Timeline(ctx, room, "", 2)
	if err != nil || len(newest.Messages) != 2 || newest.Next != string(eventID(me, group, "mid.3")) {
		t.Fatalf("newest = %+v, %v", newest, err)
	}
	older, err := a.Timeline(ctx, room, newest.Next, 2)
	if err != nil || len(older.Messages) != 2 || older.Messages[0].ID != eventID(me, group, "mid.1") || older.Next == "" {
		t.Errorf("older = %+v, %v", older, err)
	}
	// Past what is cached, with no connection to ask: what is left, then the end.
	last, err := a.Timeline(ctx, room, older.Next, 2)
	if err != nil || len(last.Messages) != 1 {
		t.Errorf("last = %+v, %v", last, err)
	}
	if end, err := a.Timeline(ctx, room, last.Next, 2); err != nil || len(end.Messages) != 0 || end.Next != "" {
		t.Errorf("past the beginning = %+v, %v", end, err)
	}
}

// A page request is woken by its thread's range in a later table, once that table is
// cached, and not by another thread's.
func TestAPageRequestWakesOnItsThreadsRange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, _, _ := cachedAdapter(t)
	a.take(ctx, c, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
		threadRow(group, table.GROUP_THREAD, "G", "inbox", 1, 1, 0), threadRow(901, table.GROUP_THREAD, "H", "inbox", 1, 1, 0),
	}})
	arrived := make(chan *table.LSInsertNewMessageRange, 1)
	c.applying.Lock()
	c.ranges[group] = arrived
	c.applying.Unlock()
	a.take(ctx, c, &table.LSTable{LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: 901, HasMoreBefore: true}}})
	select {
	case <-arrived:
		t.Fatal("another thread's range woke the request")
	default:
	}
	a.take(ctx, c, &table.LSTable{LSInsertNewMessageRange: []*table.LSInsertNewMessageRange{{ThreadKey: group, HasMoreBefore: true}}})
	select {
	case r := <-arrived:
		if !r.HasMoreBefore {
			t.Errorf("range = %+v", r)
		}
	case <-time.After(time.Second):
		t.Error("the thread's range did not wake the request")
	}
}
