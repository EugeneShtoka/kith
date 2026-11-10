package whatsapp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waSyncAction"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// markedChat is a chat's unread as a test sees it: in the cache, and as last streamed.
type markedChat struct {
	marked   bool
	messages int
}

// unreadOf is room's unread in the cache; ok is false when the cache holds no row.
func unreadOf(t *testing.T, a *Adapter, room domain.RoomID) (markedChat, bool) {
	t.Helper()
	rows, err := a.CachedUnread(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range rows {
		if u.RoomID == room {
			return markedChat{marked: u.Marked, messages: u.Messages}, true
		}
	}
	return markedChat{}, false
}

// lastStreamed drains the unread stream, returning room's last change.
func lastStreamed(a *Adapter, room domain.RoomID) (markedChat, bool) {
	var last markedChat
	seen := false
	for {
		select {
		case u := <-a.Unread():
			if u.RoomID == room {
				last, seen = markedChat{marked: u.Marked, messages: u.Messages}, true
			}
		default:
			return last, seen
		}
	}
}

// phoneMarks is the phone marking a chat unread, or read through the message last.
func phoneMarks(chat types.JID, read bool, last *events.Message, at time.Time) *events.MarkChatAsRead {
	action := &waSyncAction.MarkChatAsReadAction{Read: new(read)}
	if last != nil {
		ts := last.Info.Timestamp.Unix()
		action.MessageRange = &waSyncAction.SyncActionMessageRange{
			LastMessageTimestamp: new(ts),
			Messages: []*waSyncAction.SyncActionMessage{{
				Key: &waCommon.MessageKey{RemoteJID: new(chat.String()), ID: new(last.Info.ID)}, Timestamp: new(ts),
			}},
		}
	}
	return &events.MarkChatAsRead{JID: chat, Timestamp: at, Action: action}
}

// The unread mark is one state written by the phone and by kith, apart from reading.
// Every sequence of up to three steps from a chat with one unread message, against a
// model: the phone marking unread (+) or read (-, which reads through its newest
// message), Dana writing (m), the phone reading her latest (r), kith marking unread
// (K) or taking the mark away (k, which reads the chat), and kith reading the chat
// (o). The phone addresses the chat by LID, then number, in turn. With WhatsApp out of
// reach, kith's marks stay owed: the last one made, unless the phone changed the mark
// since. The cache and the stream agree with the model after every sequence.
func TestTheUnreadMarkFollowsThePhoneAndKith(t *testing.T) {
	t.Parallel()
	const steps = "+-mrKko"
	type model struct {
		marked bool
		count  int
		owed   string // "", "unread" or "read"
	}
	run := func(t *testing.T, seq string) {
		ctx := context.Background()
		account := Account{Name: "home", Digits: ownDigits}
		a, _, store := offline(t, account)
		client := linkedClient(t, store, ownDigits)
		if err := client.Store.LIDs.PutLIDMapping(ctx, lid(danaLID), pn(danaPhone)); err != nil {
			t.Fatal(err)
		}
		a.clients[ownDigits] = client // linked, never connected: nothing reaches WhatsApp
		base := time.Now().Truncate(time.Second).Add(-time.Hour)
		latest := danaWrites("3EB0M0", "hello")
		latest.Info.Timestamp = base
		a.onMessage(ctx, account, client, latest)
		want := model{count: 1}
		for i, step := range seq {
			chat := lid(danaLID)
			if i%2 == 1 {
				chat = pn(danaPhone)
			}
			switch step {
			case '+':
				a.handle(account, client, phoneMarks(chat, false, nil, time.Now()))
				want.marked, want.owed = true, ""
			case '-':
				a.handle(account, client, phoneMarks(chat, true, latest, time.Now()))
				want.marked, want.count, want.owed = false, 0, ""
			case 'm':
				latest = danaWrites(fmt.Sprintf("3EB0M%d", i+1), "more")
				latest.Info.Timestamp = base.Add(time.Duration(i+1) * time.Second)
				a.onMessage(ctx, account, client, latest)
				want.count++
			case 'r':
				a.onReceipt(ctx, account, &events.Receipt{
					MessageSource: types.MessageSource{Chat: chat, Sender: me.pn, IsFromMe: true},
					MessageIDs:    []types.MessageID{latest.Info.ID}, Timestamp: time.Now(), Type: types.ReceiptTypeReadSelf,
				})
				want.count = 0
			case 'K':
				if err := a.MarkRoomUnread(ctx, danaChat, true); err != nil {
					t.Fatalf("MarkRoomUnread(true) = %v", err)
				}
				want.marked, want.owed = true, "unread"
			case 'k', 'o':
				var err error
				if step == 'k' {
					err = a.MarkRoomUnread(ctx, danaChat, false)
				} else {
					err = a.MarkRead(ctx, danaChat, domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, ownDigits, latest.Info.ID)), true)
				}
				if err != nil {
					t.Fatalf("%c = %v", step, err)
				}
				if want.marked {
					want.owed = "read"
				}
				want.marked, want.count = false, 0
			}
		}
		got, ok := unreadOf(t, a, danaChat)
		if !ok || got != (markedChat{marked: want.marked, messages: want.count}) {
			t.Errorf("cached %+v (%v), want marked=%v and %d unread", got, ok, want.marked, want.count)
		}
		if streamed, ok := lastStreamed(a, danaChat); !ok || streamed != got {
			t.Errorf("streamed %+v (%v), cached %+v", streamed, ok, got)
		}
		owed, held := a.marks.Take(ownDigits)[danaChat]
		switch {
		case want.owed == "" && held:
			t.Errorf("owes WhatsApp %+v, want nothing", owed)
		case want.owed == "unread" && (!held || !owed.unread), want.owed == "read" && (!held || owed.unread):
			t.Errorf("owes WhatsApp %+v (%v), want it marked %s", owed, held, want.owed)
		}
	}
	var walk func(seq string)
	walk = func(seq string) {
		if seq != "" {
			t.Run(seq, func(t *testing.T) {
				t.Parallel()
				run(t, seq)
			})
		}
		if len(seq) < 3 {
			for _, s := range steps {
				walk(seq + string(s))
			}
		}
	}
	walk("")
}

// A mark is kept for a chat kith has not seen yet, group or direct, and stays when its
// first message arrives.
func TestAMarkBeforeTheChatIsKept(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	a.clients[ownDigits] = client
	if err := client.Store.LIDs.PutLIDMapping(ctx, lid(danaLID), pn(danaPhone)); err != nil {
		t.Fatal(err)
	}
	a.handle(account, client, phoneMarks(group("1203"), false, nil, time.Now()))
	a.handle(account, client, phoneMarks(lid(danaLID), false, nil, time.Now()))
	for _, room := range []domain.RoomID{waRoom, danaChat} {
		if got, _ := unreadOf(t, a, room); !got.marked {
			t.Errorf("%s: %+v, want it marked", room, got)
		}
	}
	a.onMessage(ctx, account, client, danaWrites("3EB0F1", "first"))
	if got, _ := unreadOf(t, a, danaChat); !got.marked || got.messages != 1 {
		t.Errorf("after her first message: %+v, want marked with it unread", got)
	}
}

// A mark the phone made before one owed from here does not undo it here: the owed one
// stands, and goes when WhatsApp is in reach.
func TestAnOlderMarkFromThePhoneLeavesTheOneOwed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, _ := offline(t, account)
	if err := a.MarkRoomUnread(ctx, danaChat, true); err != nil {
		t.Fatal(err)
	}
	a.handle(account, nil, phoneMarks(pn(danaPhone), true, nil, time.Now().Add(-time.Minute)))
	if got, _ := unreadOf(t, a, danaChat); !got.marked {
		t.Errorf("an older mark from the phone undid the one owed: %+v", got)
	}
	a.sendOwed(ctx, account) // still out of reach: it cannot go
	if owed := a.marks.Take(ownDigits)[danaChat]; !owed.unread {
		t.Errorf("owed %+v, want the mark", owed)
	}
}

// A chat marked unread when kith was linked is marked from the history chunk that
// first shows it; a later chunk does not mark it again once it was read.
func TestAMarkFromHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	a.clients[ownDigits] = client
	chunk := historyFrom(0)
	chunk.Data.Conversations[0].MarkedAsUnread = new(true)
	a.onHistory(ctx, account, client, chunk)
	if got, _ := unreadOf(t, a, danaChat); !got.marked || got.messages != 0 {
		t.Errorf("after history: %+v, want marked with nothing unread", got)
	}
	a.handle(account, client, phoneMarks(pn(danaPhone), true, nil, time.Now()))
	a.onHistory(ctx, account, client, chunk)
	if got, _ := unreadOf(t, a, danaChat); got.marked {
		t.Errorf("a later chunk marked the chat read since: %+v", got)
	}
}
