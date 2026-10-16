package whatsapp

import (
	"context"
	"slices"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/readstate"
)

func TestTheReadPositionFromHistory(t *testing.T) {
	t.Parallel()
	at := func(s int) time.Time { return time.Unix(int64(1700000000+s), 0) }
	msgs := []domain.Message{{Timestamp: at(1)}, {Timestamp: at(2)}, {Timestamp: at(3)}}
	for unread, want := range map[int]time.Time{
		0: at(3), 1: at(2), 2: at(1), 3: at(1).Add(-time.Millisecond), 9: at(1).Add(-time.Millisecond),
	} {
		if got := readFromHistory(msgs, unread); !got.Equal(want) {
			t.Errorf("unread %d: read to %v, want %v", unread, got, want)
		}
	}
}

// historyFrom is a history chunk of Dana's messages at seconds 1, 2, 3.
func historyFrom(unread uint32) *events.HistorySync {
	var msgs []*waHistorySync.HistorySyncMsg
	for i, text := range []string{"one", "two", "three"} {
		msgs = append(msgs, &waHistorySync.HistorySyncMsg{Message: &waWeb.WebMessageInfo{
			Key: &waCommon.MessageKey{
				RemoteJID: new(pn(danaPhone).String()), FromMe: new(false), ID: new("3EB0H" + text),
			},
			Message:          &waE2E.Message{Conversation: new(text)},
			MessageTimestamp: new(uint64(1700000001 + i)),
		}})
	}
	return &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: new(pn(danaPhone).String()), Name: new("Dana Levi"), UnreadCount: new(unread), Messages: msgs,
	}}}}
}

// History is cached with room, name and read position, and nothing is streamed.
func TestHistoryIsCachedQuietly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	a.clients[ownDigits] = client
	var changed []domain.RoomID
	a.OnCached(nil, func(room domain.RoomID) { changed = append(changed, room) })

	a.onHistory(ctx, account, client, historyFrom(1))

	msgs, _ := cache.Messages(ctx, danaChat, 10)
	if len(msgs) != 3 {
		t.Fatalf("cached %d messages, want 3", len(msgs))
	}
	rooms, _ := a.Rooms(ctx)
	if len(rooms) != 1 || rooms[0].Name != "Dana Levi" || !rooms[0].IsDirect {
		t.Errorf("rooms = %+v, want the chat, named as history names it", rooms)
	}
	unread, _ := a.CachedUnread(ctx)
	if len(unread) != 1 || !unread[0].Counted || unread[0].Messages != 1 {
		t.Errorf("unread = %+v, want the one WhatsApp says is unread", unread)
	}
	select {
	case m := <-a.Messages():
		t.Errorf("history streamed %+v", m)
	default:
	}
	if len(changed) != 1 || changed[0] != danaChat {
		t.Errorf("word completion heard %v, want the chat rebuilt once", changed)
	}
}

// Others' live messages are unread, own ones not; a read on the phone clears and
// streams the count.
func TestUnreadFollowsMessagesAndReads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	a.clients[ownDigits] = client
	count := func() int {
		unread, _ := a.CachedUnread(ctx)
		for _, u := range unread {
			if u.RoomID == danaChat && u.Counted {
				return u.Messages
			}
		}
		return -1
	}
	drain := func() (last domain.Unread, n int) {
		for {
			select {
			case u := <-a.Unread():
				last, n = u, n+1
			default:
				return
			}
		}
	}

	a.onMessage(ctx, account, client, danaWrites("3EB0U1", "first"))
	a.onMessage(ctx, account, client, danaWrites("3EB0U2", "second"))
	if got := count(); got != 2 {
		t.Errorf("after two of hers, unread = %d", got)
	}
	if last, n := drain(); n == 0 || last.Messages != 2 {
		t.Errorf("streamed %d changes, last %+v", n, last)
	}

	a.onReceipt(ctx, account, &events.Receipt{
		MessageSource: types.MessageSource{Chat: pn(danaPhone), Sender: me.pn, IsFromMe: true},
		MessageIDs:    []types.MessageID{"3EB0U2"}, Timestamp: time.Now(), Type: types.ReceiptTypeReadSelf,
	})
	if got := count(); got != 0 {
		t.Errorf("after reading it on the phone, unread = %d", got)
	}
	if last, _ := drain(); last.Messages != 0 || !last.Counted {
		t.Errorf("the read was streamed as %+v", last)
	}
	// Read up to the first of two: the second stays unread though it arrived
	// before the receipt.
	fourth, fifth := danaWrites("3EB0U4", "fourth"), danaWrites("3EB0U5", "fifth")
	fourth.Info.Timestamp, fifth.Info.Timestamp = time.Now().Add(time.Second), time.Now().Add(2*time.Second)
	a.onMessage(ctx, account, client, fourth)
	a.onMessage(ctx, account, client, fifth)
	a.onReceipt(ctx, account, &events.Receipt{
		MessageSource: types.MessageSource{Chat: pn(danaPhone), Sender: me.pn, IsFromMe: true},
		// The receipt's own time would cover the fifth too.
		MessageIDs: []types.MessageID{"3EB0U4"}, Timestamp: time.Now().Add(5 * time.Second), Type: types.ReceiptTypeReadSelf,
	})
	if got := count(); got != 1 {
		t.Errorf("reading up to the fourth left %d unread, want the fifth", got)
	}
	// A late receipt for an older message does not move the position back.
	a.onReceipt(ctx, account, &events.Receipt{
		MessageSource: types.MessageSource{Chat: pn(danaPhone), Sender: me.pn, IsFromMe: true},
		MessageIDs:    []types.MessageID{"3EB0U1"}, Timestamp: time.Now().Add(-time.Hour), Type: types.ReceiptTypeReadSelf,
	})
	if got := count(); got != 1 {
		t.Errorf("a late receipt for an older message left %d unread, want still the fifth", got)
	}
	// Another person's read receipt does not mark ours read.
	third := danaWrites("3EB0U3", "third")
	third.Info.Timestamp = time.Now().Add(3 * time.Second)
	a.onMessage(ctx, account, client, third)
	a.onReceipt(ctx, account, &events.Receipt{
		MessageSource: types.MessageSource{Chat: pn(danaPhone), Sender: pn(danaPhone)},
		MessageIDs:    []types.MessageID{"3EB0U3"}, Timestamp: time.Now(), Type: types.ReceiptTypeRead,
	})
	if got := count(); got != 2 {
		t.Errorf("their read receipt changed our count to %d, want the fifth and third", got)
	}
}

// Typing is a set per room; own typing from another device is ignored.
func TestTypingIsWhoIsTypingNow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	typing := func(sender types.JID, state types.ChatPresence) {
		a.onTyping(ctx, account, client, &events.ChatPresence{
			MessageSource: types.MessageSource{Chat: pn(danaPhone), Sender: sender}, State: state,
		})
	}
	next := func() (domain.Activity, bool) {
		select {
		case act := <-a.Activity():
			return act, true
		default:
			return domain.Activity{}, false
		}
	}
	typing(pn(danaPhone), types.ChatPresenceComposing)
	if act, ok := next(); !ok || act.RoomID != danaChat || len(act.Typing) != 1 || act.Typing[0] != "whatsapp:"+danaPhone+"@s.whatsapp.net" {
		t.Errorf("typing = %+v (%v)", act, ok)
	}
	typing(pn(danaPhone), types.ChatPresenceComposing)
	if act, ok := next(); ok {
		t.Errorf("no change was streamed as %+v", act)
	}
	typing(me.pn, types.ChatPresenceComposing)
	if act, ok := next(); ok {
		t.Errorf("our own typing was streamed as %+v", act)
	}
	typing(pn(danaPhone), types.ChatPresencePaused)
	if act, ok := next(); !ok || len(act.Typing) != 0 {
		t.Errorf("after pausing = %+v (%v)", act, ok)
	}
}

// Reading is local first: with nothing connected, the room is read here and the
// receipt owed (for WhatsApp, until the account connects); a resend that still cannot go
// keeps it owed. A room with nothing cached is skipped; typing is a courtesy.
func TestReadingWhileAwayIsKeptAndOwed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, _ := offline(t, Account{Name: "home", Digits: ownDigits})
	event := domain.EventID("whatsapp:" + ownDigits + "/3EB0")
	if err := a.MarkRead(ctx, danaChat, event, true); err != nil {
		t.Errorf("MarkRead with nothing connected = %v", err)
	}
	rows, err := cache.Unread(ctx)
	if err != nil {
		t.Fatal(err)
	}
	read := false
	for _, u := range rows {
		read = read || (u.RoomID == danaChat && u.ReadEvent == event)
	}
	if !read {
		t.Errorf("not read here: %+v", rows)
	}
	owed := a.reads.Take(ownDigits)
	if m := owed[danaChat]; m.event != event || !m.private {
		t.Fatalf("the receipt is not owed: %+v", owed)
	}
	a.reads.Add(danaChat, owed[danaChat])
	a.sendOwed(ctx, Account{Name: "home", Digits: ownDigits}) // still away: cannot go
	if again := a.reads.Take(ownDigits); again[danaChat].event != event {
		t.Errorf("a resend that could not go dropped the receipt: %+v", again)
	}
	if got, err := a.MarkRoomsRead(ctx, []domain.RoomID{"whatsapp:" + ownDigits + "/nothing@s.whatsapp.net"}, false); err != nil || got.Skipped != 1 {
		t.Errorf("MarkRoomsRead of a room with nothing cached = (%+v, %v), want it skipped", got, err)
	}
	if err := a.SendTyping(ctx, danaChat, true, time.Second); err != nil {
		t.Errorf("SendTyping with nothing connected = %v", err)
	}
}

// Owed receipts, by account: a later one replaces an earlier, never the reverse;
// settling an earlier one leaves a later owed; one account's are not another's.
func TestOwedReadReceipts(t *testing.T) {
	t.Parallel()
	o := readstate.NewOwed(laterReceipt)
	t0 := time.Unix(1000, 0)
	a, b := domain.RoomID("whatsapp:1/x@s.whatsapp.net"), domain.RoomID("whatsapp:2/y@s.whatsapp.net")
	o.Add(a, receipt{event: "e1", at: t0})
	o.Add(a, receipt{event: "e3", at: t0.Add(2 * time.Second)})
	o.Add(a, receipt{event: "e2", at: t0.Add(time.Second)})
	o.Add(b, receipt{event: "f1", at: t0})
	o.Settle(a, receipt{event: "e1", at: t0})
	if got := o.Take("1"); got[a].event != "e3" || len(got) != 1 {
		t.Errorf("account 1 owes %+v", got)
	}
	if got := o.Take("2"); got[b].event != "f1" {
		t.Errorf("account 2 owes %+v", got)
	}
}

// Timestamps are whole seconds and IDs unordered: history orders by msgOrderID,
// live messages by arrival, so a burst within a second keeps its send order.
func TestMessagesInOneSecondKeepTheOrderTheyWereSent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	a.clients[ownDigits] = client

	// IDs sorting against the order, in history's newest-first order.
	sent := []struct {
		id    string
		order uint64
	}{{"3EB0A3", 30}, {"3EB0B2", 20}, {"3EB0C1", 10}}
	var msgs []*waHistorySync.HistorySyncMsg
	for _, s := range sent {
		msgs = append(msgs, &waHistorySync.HistorySyncMsg{MsgOrderID: new(s.order), Message: &waWeb.WebMessageInfo{
			Key:              &waCommon.MessageKey{RemoteJID: new(pn(danaPhone).String()), FromMe: new(false), ID: new(s.id)},
			Message:          &waE2E.Message{Conversation: new(s.id)},
			MessageTimestamp: new(uint64(1700000000)),
		}})
	}
	a.onHistory(ctx, account, client, &events.HistorySync{Data: &waHistorySync.HistorySync{
		Conversations: []*waHistorySync.Conversation{{ID: new(pn(danaPhone).String()), Messages: msgs}},
	}})

	// Live, in one second, with IDs sorting against the order again.
	second := time.Now().Truncate(time.Second)
	for _, id := range []string{"3EB0Z", "3EB0Y", "3EB0X"} {
		e := danaWrites(id, id)
		e.Info.Timestamp = second
		a.onMessage(ctx, account, client, e)
	}

	got, err := cache.Messages(ctx, danaChat, 10)
	if err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for _, m := range got {
		bodies = append(bodies, m.Body)
	}
	if want := []string{"3EB0C1", "3EB0B2", "3EB0A3", "3EB0Z", "3EB0Y", "3EB0X"}; !slices.Equal(bodies, want) {
		t.Errorf("order = %v, want %v", bodies, want)
	}
}
