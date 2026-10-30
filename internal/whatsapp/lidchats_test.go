package whatsapp

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Ruth never shows her number (a hidden one, a username): her chat stays on her LID.
const ruthLID = "100000000000007"

var (
	danaLIDChat = domain.RoomID("whatsapp:" + ownDigits + "/" + danaLID + "@lid")
	ruthChat    = domain.RoomID("whatsapp:" + ownDigits + "/" + ruthLID + "@lid")
)

// What happens next to the account, in TestAChatKnownByItsLIDJoinsItsNumbers.
type lidStep int

const (
	underLID   lidStep = iota // Dana writes, addressed by her LID alone
	withNumber                // Dana writes by LID with her number beside it; the store keeps the pair, as whatsmeow does
	byNumber                  // Dana writes by her number alone
	fromMe                    // the account writes to Dana's LID, her number beside it once known
	learned                   // the store learns Dana's number with no message (a contacts sync)
	reconnect                 // the account connects again
	historyLID                // a history chunk of Dana's chat, by her LID
	readOpen                  // a window on the chat it opened earlier marks it read
	opens                     // a window opens the chat Dana's last message is in
	listed                    // a listing of the account's groups lands, racing the next steps
	stranger                  // Ruth writes, by LID
	lidSteps
)

// lidMessage is a direct message in the chat with chat, sent by sender (alt beside
// it), or by the account.
func lidMessage(id string, chat, sender, alt types.JID, mine bool) *events.Message {
	info := types.MessageInfo{
		MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: mine},
		ID:            id, Timestamp: time.Now(),
	}
	if mine {
		info.RecipientAlt = alt
	} else {
		info.SenderAlt = alt
	}
	return &events.Message{Info: info, Message: &waE2E.Message{Conversation: new("m")}}
}

// TestAChatKnownByItsLIDJoinsItsNumbers delivers Dana's chat in every order: by LID
// before her number is known, by number, both at once, from history; the number learned
// with a message or silently, then a reconnect; a window still on the LID's chat
// marking it read; listings racing it all; and Ruth, whose number never shows. After
// every step each of Dana's messages is held once; once her number is known and the
// account has heard it named beside her LID (or reconnected), her LID's chat is gone,
// everything in her number's, and nothing writes the LID's chat back.
func TestAChatKnownByItsLIDJoinsItsNumbers(t *testing.T) {
	t.Parallel()
	for seed := range uint64(40) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			r := rand.New(rand.NewPCG(seed, 47))
			ctx := context.Background()
			account := Account{Name: "home", Digits: ownDigits}
			a, cache, store := offline(t, account)
			client := linkedClient(t, store, ownDigits)
			a.clients[ownDigits] = client
			var listings sync.WaitGroup
			defer listings.Wait()
			var (
				known, merged, filedByLID bool
				sent                      []domain.EventID
				ruth                      int
				window                    domain.RoomID
			)
			learn := func() {
				if err := client.Store.LIDs.PutLIDMapping(ctx, lid(danaLID), pn(danaPhone)); err != nil {
					t.Fatal(err)
				}
				known = true
			}
			for i := range 24 {
				step := lidStep(r.IntN(int(lidSteps)))
				id := fmt.Sprintf("3EB0L%02d", i)
				switch step {
				case underLID:
					a.handle(account, client, lidMessage(id, lid(danaLID), lid(danaLID), types.EmptyJID, false))
				case withNumber:
					learn()
					a.handle(account, client, lidMessage(id, lid(danaLID), lid(danaLID), pn(danaPhone), false))
				case byNumber:
					a.handle(account, client, lidMessage(id, pn(danaPhone), pn(danaPhone), types.EmptyJID, false))
				case fromMe:
					alt := types.EmptyJID
					if known {
						alt = pn(danaPhone)
					}
					a.handle(account, client, lidMessage(id, lid(danaLID), pn(ownDigits), alt, true))
				case learned:
					learn()
				case reconnect:
					a.handle(account, client, &events.Connected{})
				case historyLID:
					a.handle(account, client, historyOf(lidMessage(id, lid(danaLID), lid(danaLID), types.EmptyJID, false)))
				case readOpen:
					if window != "" {
						if err := a.MarkRead(ctx, window, sent[len(sent)-1], true); err != nil {
							t.Fatalf("MarkRead(%s) = %v", window, err)
						}
					}
				case opens:
					if len(sent) > 0 {
						window = roomOf(t, cache, sent[len(sent)-1])
					}
				case listed:
					fetched := time.Now()
					listings.Go(func() { _ = a.saveListing(ctx, account, groupListing{}, fetched) })
				case stranger:
					a.handle(account, client, lidMessage(id, lid(ruthLID), lid(ruthLID), types.EmptyJID, false))
					ruth++
				case lidSteps:
				}
				// The account hears the LID named with the number known: by a message, or
				// on reconnecting while the LID's chat is held.
				switch step {
				case underLID, withNumber, fromMe, historyLID:
					merged = merged || known
					filedByLID = filedByLID || !known
				case reconnect:
					merged = merged || (known && filedByLID)
				case byNumber, learned, readOpen, opens, listed, stranger, lidSteps:
				}
				if step != learned && step != reconnect && step != readOpen && step != opens && step != listed && step != stranger {
					sent = append(sent, domain.EventID("whatsapp:"+ownDigits+"/"+id))
				}
				if step == listed {
					continue // it races the next step, checked after both
				}
				listings.Wait()
				checkDanasChat(t, a, cache, sent, merged)
				if msgs, _ := cache.Messages(ctx, ruthChat, 100); len(msgs) != ruth {
					t.Fatalf("step %d: Ruth's chat holds %d messages, want her %d, by her LID", i, len(msgs), ruth)
				}
			}
			listings.Wait()
			if merged != slices.ContainsFunc(rooms(t, a), func(r domain.Room) bool {
				return r.ID == danaChat && slices.Equal(r.Merged, []domain.RoomID{danaLIDChat})
			}) {
				t.Errorf("Dana's number's chat lists her LID's as merged: %v, want %v", !merged, merged)
			}
		})
	}
}

// rooms is the adapter's rooms.
func rooms(t *testing.T, a *Adapter) []domain.Room {
	t.Helper()
	list, err := a.Rooms(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// roomOf is the room a cached message is in.
func roomOf(t *testing.T, cache *db.Cache, id domain.EventID) domain.RoomID {
	t.Helper()
	for _, room := range []domain.RoomID{danaLIDChat, danaChat} {
		if _, ok, err := cache.MessageByID(context.Background(), room, id); err != nil {
			t.Fatal(err)
		} else if ok {
			return room
		}
	}
	t.Fatalf("%s is in neither of Dana's chats", id)
	return ""
}

// checkDanasChat checks each of sent is held once, in Dana's number's chat once merged
// and with no LID chat listed or written back.
func checkDanasChat(t *testing.T, a *Adapter, cache *db.Cache, sent []domain.EventID, merged bool) {
	t.Helper()
	ctx := context.Background()
	for _, id := range sent {
		held := 0
		for _, room := range []domain.RoomID{danaLIDChat, danaChat} {
			if _, ok, err := cache.MessageByID(ctx, room, id); err != nil {
				t.Fatal(err)
			} else if ok {
				held++
				if merged && room == danaLIDChat {
					t.Fatalf("%s is still in Dana's LID chat after the merge", id)
				}
			}
		}
		if held != 1 {
			t.Fatalf("%s is held %d times, want once", id, held)
		}
	}
	if !merged {
		return
	}
	rooms, err := a.Rooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(rooms, func(r domain.Room) bool { return r.ID == danaLIDChat }) {
		t.Fatalf("Dana's LID chat is still listed after the merge")
	}
	unread, err := cache.Unread(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(unread, func(u domain.Unread) bool { return u.RoomID == danaLIDChat }) {
		t.Fatalf("a read wrote Dana's LID chat back after the merge")
	}
}
