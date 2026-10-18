package whatsapp

import (
	"context"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// danaWritesWithTimer is Dana's message in a chat whose messages disappear after timer.
func danaWritesWithTimer(id, text string, timer time.Duration) *events.Message {
	e := danaWrites(id, "")
	expiration := uint32(timer / time.Second)
	e.Message = &waE2E.Message{ExtendedTextMessage: &waE2E.ExtendedTextMessage{
		Text: new(text), ContextInfo: &waE2E.ContextInfo{Expiration: &expiration},
	}}
	return e
}

// sentTimer is the timer kith gives a message it sends to room.
func sentTimer(t *testing.T, a *Adapter, room domain.RoomID, msg *waE2E.Message) time.Duration {
	t.Helper()
	a.withTimer(context.Background(), room, msg)
	return timerOf(msg)
}

// What kith sends in a chat carries the timer its messages arrive with, text and
// files alike, until a change of timer turns it off; a message without one leaves it.
func TestASendCarriesItsChatsTimer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	week := 7 * 24 * time.Hour

	if got := sentTimer(t, a, danaChat, &waE2E.Message{Conversation: new("hi")}); got != 0 {
		t.Fatalf("before any timer: %v", got)
	}
	a.onMessage(ctx, account, client, danaWritesWithTimer("3EB0T1", "hi", week))
	a.onMessage(ctx, account, client, danaWrites("3EB0T2", "from an old phone"))
	text := &waE2E.Message{Conversation: new("hello")}
	if got := sentTimer(t, a, danaChat, text); got != week || text.GetExtendedTextMessage().GetText() != "hello" {
		t.Errorf("text: timer %v, message %+v; want a week, the same words", got, text)
	}
	picture, _ := fileMessage("shot.png", []byte("x"), "")
	if got := sentTimer(t, a, danaChat, picture); got != week {
		t.Errorf("a picture: timer %v, want a week", got)
	}

	off := danaChanges("3EB0T3", &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
		Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum(), EphemeralExpiration: new(uint32(0)),
	}})
	a.onMessage(ctx, account, client, off)
	if got := sentTimer(t, a, danaChat, &waE2E.Message{Conversation: new("bye")}); got != 0 {
		t.Errorf("after the timer was turned off: %v", got)
	}
}

// A direct chat's timer comes with its history; a group's with its listing, which
// also turns it off.
func TestATimerIsLearnedFromHistoryAndListing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, _, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	day := 24 * time.Hour

	history := historyOf(danaWrites("3EB0H", "old"))
	history.Data.Conversations[0].EphemeralExpiration = new(uint32(day / time.Second))
	a.onHistory(ctx, account, client, history)
	// History names no number for Dana, so her chat is the one by her LID.
	if got := sentTimer(t, a, roomID(ownDigits, lid(danaLID)), &waE2E.Message{Conversation: new("x")}); got != day {
		t.Errorf("from history: %v, want a day", got)
	}

	group := &types.GroupInfo{JID: types.NewJID("1203", types.GroupServer)}
	group.IsEphemeral, group.DisappearingTimer = true, uint32(day/time.Second)
	listing := groupListing{rooms: []domain.Room{{ID: waRoom}}, timers: groupTimers(ownDigits, []*types.GroupInfo{group})}
	if err := a.saveListing(ctx, account, listing, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := sentTimer(t, a, waRoom, &waE2E.Message{Conversation: new("x")}); got != day {
		t.Errorf("a listed group: %v, want a day", got)
	}
	group.IsEphemeral = false
	listing.timers = groupTimers(ownDigits, []*types.GroupInfo{group})
	if err := a.saveListing(ctx, account, listing, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := sentTimer(t, a, waRoom, &waE2E.Message{Conversation: new("x")}); got != 0 {
		t.Errorf("a group listed without one: %v", got)
	}
}
