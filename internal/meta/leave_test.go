package meta

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A chat you left stays gone until something newer than your leaving comes, in any
// order of: pages of the inbox and of messages fetched before you left, Meta asking
// whether the thread exists, Meta's own delete, and new messages, plain and encrypted,
// for a plain one-to-one, a plain group and an encrypted chat Meta keys by its own
// thread key. At every step a chat is cached exactly when it is shown.
func TestALeftChatStaysGoneUntilSomethingNewer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const groupKey, encryptedKey = int64(300), int64(555)
	chats := []int64{dana, groupKey, eli}
	for seed := range uint64(60) {
		r := rand.New(rand.NewPCG(seed, 11))
		a, c, cache, _ := cachedAdapter(t)
		a.take(ctx, c, &table.LSTable{
			LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor"}, {Id: eli, Name: "Eli"}},
			LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
				threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0),
				threadRow(groupKey, table.GROUP_THREAD, "Board", "inbox", 10, 10, 0),
				threadRow(encryptedKey, table.ENCRYPTED_OVER_WA_ONE_TO_ONE, "", "inbox", 10, 10, 0),
			},
			LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{
				{ThreadKey: encryptedKey, ContactId: me}, {ThreadKey: encryptedKey, ContactId: eli},
			},
		})
		shown := map[int64]bool{dana: true, groupKey: true, eli: true}
		leftAt := map[int64]int64{}
		metaKey := map[int64]int64{dana: dana, groupKey: groupKey, eli: encryptedKey}
		var steps []string
		now := time.Now().UnixMilli()
		for i := range 16 {
			key := chats[r.IntN(len(chats))]
			before := leftAt[key] - 1 - int64(r.IntN(1000)) // something from before you left
			if leftAt[key] == 0 {
				before = 5
			}
			switch r.IntN(6) {
			case 0: // you leave it: what LeaveRoom does once Meta took it
				now += 1000
				steps = append(steps, fmt.Sprint("leave ", key))
				a.left(ctx, c, key, time.UnixMilli(now))
				leftAt[key], shown[key] = now, false
			case 1: // a page of the inbox from before
				steps = append(steps, fmt.Sprint("old page with ", key))
				kind := map[int64]table.ThreadType{dana: table.ONE_TO_ONE, groupKey: table.GROUP_THREAD, eli: table.ENCRYPTED_OVER_WA_ONE_TO_ONE}[key]
				a.take(ctx, c, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(metaKey[key], kind, "", "inbox", before, before, 0)}})
			case 2: // Meta asking whether it exists
				steps = append(steps, fmt.Sprint("verify ", key))
				a.take(ctx, c, &table.LSTable{LSVerifyThreadExists: []*table.LSVerifyThreadExists{{ThreadKey: metaKey[key], ThreadType: table.GROUP_THREAD, FolderName: "inbox"}}})
			case 3: // an old message, fetched before
				steps = append(steps, fmt.Sprint("old message in ", key))
				if key == eli {
					a.takeEncrypted(ctx, c, fbMessage(msgrJID(eli), msgrJID(eli), fmt.Sprint("old", i), time.UnixMilli(before), textContent("old")))
				} else {
					a.take(ctx, c, &table.LSTable{LSInsertMessage: []*table.LSInsertMessage{live(key, dana, fmt.Sprint("mid.$old", i), "old", before)}})
				}
			case 4: // Meta's own delete
				steps = append(steps, fmt.Sprint("Meta deletes ", key))
				at := time.Now().UnixMilli()
				a.take(ctx, c, &table.LSTable{LSDeleteThread: []*table.LSDeleteThread{{ThreadKey: metaKey[key]}}})
				leftAt[key], shown[key] = max(leftAt[key], at), false
			default: // a new message
				now += 1000
				steps = append(steps, fmt.Sprint("new message in ", key))
				if key == eli {
					a.takeEncrypted(ctx, c, fbMessage(msgrJID(eli), msgrJID(eli), fmt.Sprint("new", i), time.UnixMilli(now), textContent("new")))
				} else {
					a.take(ctx, c, &table.LSTable{LSInsertMessage: []*table.LSInsertMessage{live(key, dana, fmt.Sprint("mid.$new", i), "new", now)}})
				}
				shown[key] = true
				delete(leftAt, key)
			}
			rooms, err := cache.Rooms(ctx)
			if err != nil {
				t.Fatal(err)
			}
			cached := map[domain.RoomID]bool{}
			for i := range rooms {
				cached[rooms[i].ID] = true
			}
			for _, k := range chats {
				if cached[roomID(me, k)] != shown[k] {
					t.Fatalf("seed %d %v: %d cached = %v, want %v", seed, steps, k, cached[roomID(me, k)], shown[k])
				}
			}
			if cached[roomID(me, encryptedKey)] {
				t.Fatalf("seed %d %v: the encrypted chat came back under its thread's own key", seed, steps)
			}
		}
	}
}

// Leaving that cannot go says why and leaves nothing.
func TestLeavingThatCannotGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0)}})
	if err := a.LeaveRoom(ctx, roomID(me, dana)); !errors.Is(err, errNotConnected) {
		t.Errorf("with the connection still loading: %v", err)
	}
	if err := a.LeaveRoom(ctx, roomID(me+1, dana)); !errors.Is(err, errNotConnected) {
		t.Errorf("as an account not signed in: %v", err)
	}
	if err := a.LeaveRoom(ctx, "!room:example.org"); !errors.Is(err, errNotAThread) {
		t.Errorf("another network's room: %v", err)
	}
	if rooms := cachedRooms(t, cache); len(rooms) != 1 {
		t.Errorf("rooms after leaving that did not go: %q", rooms)
	}
}
