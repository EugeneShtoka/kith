package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/proto/waWeb"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// danaWrites is Dana's message to us, addressed as WhatsApp does now (by LID).
func danaWrites(id, text string) *events.Message {
	return &events.Message{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: lid(danaLID), Sender: lid(danaLID), SenderAlt: pn(danaPhone)},
			ID:            id, PushName: "Dana", Timestamp: time.Now(),
		},
		Message: &waE2E.Message{Conversation: new(text)},
	}
}

var danaChat = domain.RoomID("whatsapp:" + ownDigits + "/" + danaPhone + "@s.whatsapp.net")

// A direct chat's first message makes it a room named after the person, with them
// in it; the message is cached, counted into word completion and streamed.
func TestADirectChatBeginsWithItsFirstMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	var heard []domain.EventID
	a.OnCached(func(m domain.Message) { heard = append(heard, m.ID) }, nil)

	a.onMessage(ctx, account, client, danaWrites("3EB0A", "hello"))

	rooms, _ := a.Rooms(ctx)
	if len(rooms) != 1 || rooms[0].ID != danaChat || !rooms[0].IsDirect || rooms[0].Name != "Dana" {
		t.Fatalf("rooms = %+v, want the chat with Dana, named after her", rooms)
	}
	if members, _ := cache.Members(ctx, danaChat, 0); len(members) != 1 || members[0].UserID != "whatsapp:"+danaPhone+"@s.whatsapp.net" {
		t.Errorf("members = %+v", members)
	}
	msgs, _ := cache.Messages(ctx, danaChat, 10)
	if len(msgs) != 1 || msgs[0].Body != "hello" || msgs[0].SenderName != "Dana" {
		t.Errorf("cached = %+v", msgs)
	}
	if len(heard) != 1 {
		t.Errorf("word completion heard %v", heard)
	}
	select {
	case m := <-a.Messages():
		if m.ID != "whatsapp:"+ownDigits+"/3EB0A" {
			t.Errorf("streamed %+v", m)
		}
	default:
		t.Error("nothing was streamed")
	}
}

// A listing of groups keeps the account's direct chats, and any room a message came
// to after the listing was fetched; it still sweeps a group the account left.
func TestAListingSweepsOnlyWhatItShould(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	left := domain.RoomID("whatsapp:" + ownDigits + "/1203LEFT@g.us")
	joined := domain.RoomID("whatsapp:" + ownDigits + "/1203NEW@g.us")
	kept := domain.RoomID("whatsapp:" + ownDigits + "/1203KEPT@g.us")
	if err := a.saveListing(ctx, account, groupListing{rooms: []domain.Room{{ID: left}, {ID: kept}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	a.onMessage(ctx, account, client, danaWrites("3EB0B", "hi"))
	fetched := time.Now()
	time.Sleep(time.Millisecond)
	// A group's first message arrives after this listing was fetched.
	newGroup := danaWrites("3EB0C", "welcome")
	newGroup.Info.Chat, newGroup.Info.IsGroup = types.NewJID("1203NEW", types.GroupServer), true
	a.onMessage(ctx, account, client, newGroup)

	if err := a.saveListing(ctx, account, groupListing{rooms: []domain.Room{{ID: kept}}}, fetched); err != nil {
		t.Fatal(err)
	}
	rooms, _ := a.Rooms(ctx)
	has := map[domain.RoomID]bool{}
	for _, r := range rooms {
		has[r.ID] = true
	}
	if !has[danaChat] || !has[joined] || !has[kept] || has[left] {
		t.Errorf("rooms after the listing = %v; want the chat, the new group and the kept one, not the one left", has)
	}
	if msgs, _ := cache.Messages(ctx, joined, 10); len(msgs) != 1 {
		t.Errorf("the new group's message = %v", msgs)
	}
}

// Messages arriving while listings are written: none is lost with its room, whatever
// the interleaving. A listing is what WhatsApp would answer: every group joined
// before it was fetched, none joined after (the case the sweep must not take).
func TestNoMessageIsSweptWithItsRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(60) {
		rng := rand.New(rand.NewPCG(seed, 9))
		account := Account{Name: "bg", Digits: ownDigits}
		a, cache, store := offline(t, account)
		client := linkedClient(t, store, ownDigits)
		var world sync.Mutex
		var joined []domain.Room // groups the account is in, as WhatsApp knows them
		listing := func() ([]domain.Room, time.Time) {
			world.Lock()
			defer world.Unlock()
			return append([]domain.Room(nil), joined...), time.Now()
		}
		var wg sync.WaitGroup
		var sent []domain.RoomID
		for i := range 20 {
			e := danaWrites(fmt.Sprintf("3EB0%d", i), "m")
			room := danaChat
			if rng.IntN(2) == 0 {
				e.Info.Chat, e.Info.IsGroup = types.NewJID(fmt.Sprintf("1203%d", i), types.GroupServer), true
				room = roomID(ownDigits, e.Info.Chat)
				world.Lock()
				joined = append(joined, domain.Room{ID: room})
				world.Unlock()
			}
			sent = append(sent, room)
			if rng.IntN(3) == 0 {
				wg.Go(func() { a.onHistory(ctx, account, client, historyOf(e)) }) // a history chunk
			} else {
				wg.Go(func() { a.onMessage(ctx, account, client, e) })
			}
			if rng.IntN(2) == 0 {
				rooms, fetched := listing()
				wg.Go(func() { _ = a.saveListing(ctx, account, groupListing{rooms: rooms}, fetched) })
			}
		}
		wg.Wait()
		for _, room := range sent {
			if msgs, _ := cache.Messages(ctx, room, 50); len(msgs) == 0 {
				t.Fatalf("seed %d: %s lost its message to a listing", seed, room)
			}
		}
	}
}

// historyOf is a live message as a history chunk carries it.
func historyOf(e *events.Message) *events.HistorySync {
	key := &waCommon.MessageKey{RemoteJID: new(e.Info.Chat.String()), FromMe: new(e.Info.IsFromMe), ID: new(e.Info.ID)}
	if e.Info.IsGroup {
		key.Participant = new(e.Info.Sender.String())
	}
	return &events.HistorySync{Data: &waHistorySync.HistorySync{Conversations: []*waHistorySync.Conversation{{
		ID: new(e.Info.Chat.String()),
		Messages: []*waHistorySync.HistorySyncMsg{{Message: &waWeb.WebMessageInfo{
			Key: key, Message: e.Message, MessageTimestamp: new(uint64(e.Info.Timestamp.Unix())),
		}}},
	}}}}
}

// Sending refuses what it cannot do, and an account that is not connected.
func TestSendRefusesWhatItCannot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, _, _ := offline(t, Account{Name: "bg", Digits: ownDigits})
	if err := a.Send(ctx, danaChat, domain.Draft{Body: "x"}); !errors.Is(err, api.ErrNetworkOff) {
		t.Errorf("sending with nothing connected = %v, want ErrNetworkOff", err)
	}
}

// A mention is written as WhatsApp writes it and names the person; a reply quotes
// what it answers; a retried send keeps its ID.
func TestAnOutgoingMessageAsWhatsAppTakesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, cache, store := offline(t, Account{Name: "bg", Digits: ownDigits})
	client := linkedClient(t, store, ownDigits)
	draft := domain.Draft{Body: "Dana Levi, see this", Mentions: []domain.Mention{
		{UserID: "whatsapp:" + danaPhone + "@s.whatsapp.net", Name: "Dana Levi"},
		{UserID: "@dana:matrix.org", Name: "nowhere"}, // not in the text, not WhatsApp's
	}}
	text, mentioned := mentionText(draft)
	if text != "@"+danaPhone+", see this" || len(mentioned) != 1 || mentioned[0] != pn(danaPhone) {
		t.Errorf("mentionText = %q, %v", text, mentioned)
	}
	plain, _ := a.outgoing(ctx, ownDigits, danaChat, "hi", nil, "")
	if plain.GetConversation() != "hi" || plain.GetExtendedTextMessage() != nil {
		t.Errorf("a plain message = %v", plain)
	}

	original := domain.Message{ID: "whatsapp:" + ownDigits + "/3EB0Q", RoomID: danaChat, Sender: "whatsapp:" + danaPhone + "@s.whatsapp.net", Body: "lunch?"}
	if err := cache.SaveMessages(ctx, danaChat, []domain.Message{original}); err != nil {
		t.Fatal(err)
	}
	reply, replyTo := a.outgoing(ctx, ownDigits, danaChat, text, mentioned, original.ID)
	ctxInfo := reply.GetExtendedTextMessage().GetContextInfo()
	if replyTo != original.ID || ctxInfo.GetStanzaID() != "3EB0Q" || ctxInfo.GetParticipant() != pn(danaPhone).String() ||
		ctxInfo.GetQuotedMessage().GetConversation() != "lunch?" || len(ctxInfo.GetMentionedJID()) != 1 {
		t.Errorf("a reply = %v (replyTo %q)", reply, replyTo)
	}
	if _, gone := a.outgoing(ctx, ownDigits, danaChat, "x", nil, "whatsapp:"+ownDigits+"/3EB0GONE"); gone != "" {
		t.Errorf("a reply to a message not in the cache still says it replies (%q)", gone)
	}

	first := a.messageIDFor(client, "kith-txn-1")
	if again := a.messageIDFor(client, "kith-txn-1"); again != first {
		t.Errorf("a retry went out as %s, the first try as %s", again, first)
	}
	if other := a.messageIDFor(client, "kith-txn-2"); other == first {
		t.Error("two sends shared an ID")
	}
}

// Mention candidates are ranked as Matrix's are: who spoke last leads.
func TestMentionCandidatesLeadWithWhoSpoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := Account{Name: "bg", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	group := domain.RoomID("whatsapp:" + ownDigits + "/1203@g.us")
	if err := a.saveListing(ctx, account, groupListing{rooms: []domain.Room{{ID: group}}, members: map[domain.RoomID][]domain.Member{group: {
		{UserID: "whatsapp:111@s.whatsapp.net", DisplayName: "Aaron"},
		{UserID: "whatsapp:" + danaPhone + "@s.whatsapp.net", DisplayName: "Dana"},
	}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	e := danaWrites("3EB0R", "hi all")
	e.Info.Chat, e.Info.IsGroup = types.NewJID("1203", types.GroupServer), true
	a.onMessage(ctx, account, client, e)
	_ = cache
	got, err := a.MentionCandidates(ctx, group, 0)
	if err != nil || len(got) != 2 || got[0].DisplayName != "Dana" {
		t.Errorf("candidates = (%+v, %v), want Dana, who just spoke, first", got, err)
	}
}

// A changed account list takes effect without a restart: a removed account is let go.
func TestAccountsChangeWithoutARestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bg, il := Account{Name: "bg", Digits: ownDigits}, Account{Name: "il", Digits: "972500000099"}
	a, _, store := offline(t, bg, il)
	a.clients[ownDigits] = linkedClient(t, store, ownDigits)
	a.UseAccounts(ctx, []Account{il})
	if got := a.connected(); len(got) != 0 {
		t.Errorf("connected after bg was removed = %v", got)
	}
	if got := a.accountsNow(); len(got) != 1 || got[0] != il {
		t.Errorf("accounts = %v", got)
	}
	if _, err := a.PairWhatsApp(ctx, "bg", func(string) error { return nil }); !errors.Is(err, errNoAccount) {
		t.Errorf("pairing a removed account = %v", err)
	}
}
