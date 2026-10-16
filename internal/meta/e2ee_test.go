package meta

import (
	"context"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	mtypes "go.mau.fi/mautrix-meta/pkg/messagix/types"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
	"go.mau.fi/whatsmeow/proto/waMsgApplication"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const encryptedKey = int64(555) // an encrypted one-to-one thread's own key

// encryptedThread is a table with the encrypted one-to-one thread between you and
// dana, keyed by its own key as Meta keys it.
func encryptedThread() *table.LSTable {
	return &table.LSTable{
		LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor"}},
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			threadRow(encryptedKey, table.ENCRYPTED_OVER_WA_ONE_TO_ONE, "", "inbox", 10, 10, 0),
		},
		LSAddParticipantIdToGroupThread: []*table.LSAddParticipantIdToGroupThread{
			{ThreadKey: encryptedKey, ContactId: me}, {ThreadKey: encryptedKey, ContactId: dana},
		},
	}
}

// An encrypted one-to-one chat is keyed by its person, as its messages are addressed:
// from its participants, or from Meta's mapping; an older room under the thread's own
// key goes.
func TestEncryptedOneToOnesAreKeyedByTheirPerson(t *testing.T) {
	t.Parallel()
	in := newInbox(me)
	ch := in.apply(encryptedThread())
	if in.threads[dana] == nil || in.threads[encryptedKey] != nil {
		t.Fatalf("threads = %v", in.threads)
	}
	if r := in.room(in.threads[dana]); r.ID != roomID(me, dana) || r.Name != "Dana Mor" || !r.IsDirect {
		t.Errorf("room = %+v", r)
	}
	if len(ch.gone) != 1 || ch.gone[0] != encryptedKey {
		t.Errorf("gone = %v, want the thread's own key", ch.gone)
	}
	// A later row, read mark or delete under the thread's own key reaches the same room.
	in.apply(&table.LSTable{LSMarkThreadReadV2: []*table.LSMarkThreadReadV2{{ThreadKey: encryptedKey, LastReadWatermarkTimestampMs: 50}}})
	if in.threads[dana].readTo != 50 {
		t.Errorf("a read mark under the thread's key did not reach the room: %d", in.threads[dana].readTo)
	}

	// Meta's mapping says the same for a thread whose participants are not sent.
	in.apply(&table.LSTable{
		LSVerifyHybridThreadExists: []*table.LSVerifyHybridThreadExists{{ThreadKey: 556, ThreadJID: eli}},
		LSDeleteThenInsertThread:   []*table.LSDeleteThenInsertThread{threadRow(556, table.ENCRYPTED_OVER_WA_ONE_TO_ONE, "Eli", "inbox", 5, 5, 0)},
	})
	if in.threads[eli] == nil || in.threads[556] != nil {
		t.Errorf("the mapped thread: eli %+v, 556 %+v", in.threads[eli], in.threads[556])
	}
}

// fbMessage is an encrypted message from sender in chat.
func fbMessage(chat, sender types.JID, id string, at time.Time, content *waConsumerApplication.ConsumerApplication_Content) *events.FBMessage {
	return &events.FBMessage{
		Info: types.MessageInfo{
			MessageSource: types.MessageSource{Chat: chat, Sender: sender, IsFromMe: sender.User == itoa(me)},
			ID:            id, Timestamp: at,
		},
		Message: &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
			Payload: &waConsumerApplication.ConsumerApplication_Payload_Content{Content: content},
		}},
		FBApplication: &waMsgApplication.MessageApplication{},
	}
}

func textContent(text string) *waConsumerApplication.ConsumerApplication_Content {
	return &waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_MessageText{
		MessageText: &waCommon.MessageText{Text: &text},
	}}
}

func msgrJID(id int64) types.JID { return types.JID{User: itoa(id), Server: types.MessengerServer} }

// Encrypted messages land in their chat's room: new ones announced, edits in place,
// unsends marked, replies linked, your own reading the chat, and a chat never heard of
// made at once.
func TestEncryptedMessagesLandInTheirChatsRoom(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, announced := cachedAdapter(t)
	a.take(ctx, c, encryptedThread())
	room := roomID(me, dana)
	at := time.UnixMilli(100_000)

	a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), "AAA", at, textContent("hello")))
	got, ok, err := cache.MessageByID(ctx, room, eventID(me, dana, "AAA"))
	if err != nil || !ok || got.Body != "hello" || got.Sender != personID(dana) || got.SenderName != "Dana Mor" {
		t.Fatalf("cached %+v (%v, %v)", got, ok, err)
	}
	if len(*announced) != 1 {
		t.Errorf("announced %d", len(*announced))
	}

	reply := fbMessage(msgrJID(dana), msgrJID(dana), "BBB", at.Add(time.Second), textContent("and"))
	stanza := "AAA"
	reply.FBApplication = &waMsgApplication.MessageApplication{Metadata: &waMsgApplication.MessageApplication_Metadata{
		QuotedMessage: &waMsgApplication.MessageApplication_Metadata_QuotedMessage{StanzaID: &stanza},
	}}
	a.takeEncrypted(ctx, c, reply)
	if got, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, "BBB")); got.ReplyTo != eventID(me, dana, "AAA") {
		t.Errorf("reply to = %s", got.ReplyTo)
	}

	newText := "hello again"
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), "CCC", at.Add(time.Hour),
		&waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{
			EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
				Key: &waCommon.MessageKey{ID: &stanza}, Message: &waCommon.MessageText{Text: &newText},
			},
		}}))
	if got, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, "AAA")); got.Body != newText || !got.Edited || !got.Timestamp.Equal(at) {
		t.Errorf("after the edit: %+v", got)
	}

	revoke := fbMessage(msgrJID(dana), msgrJID(dana), "DDD", at.Add(2*time.Hour), nil)
	revoke.Message = &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{
			ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{
					Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: &waCommon.MessageKey{ID: &stanza}},
				},
			},
		},
	}}
	a.takeEncrypted(ctx, c, revoke)
	if got, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, "AAA")); !got.Redacted {
		t.Errorf("after the unsend: %+v", got)
	}

	// Your own message reads the chat.
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(me), "EEE", at.Add(3*time.Hour), textContent("bye")))
	if n, _, counted, err := cache.CountUnread(ctx, a.Me(), room); err != nil || !counted || n != 0 {
		t.Errorf("after writing: %d unread (counted %v, %v)", n, counted, err)
	}

	// A chat never heard of becomes a room at once.
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(eli), msgrJID(eli), "FFF", at, textContent("hi")))
	if got, ok, _ := cache.MessageByID(ctx, roomID(me, eli), eventID(me, eli, "FFF")); !ok || got.Body != "hi" {
		t.Errorf("the unknown chat's message: %+v (%v)", got, ok)
	}
	if rooms := cachedRooms(t, cache); len(rooms) != 2 {
		t.Errorf("rooms = %q, want dana's and eli's", rooms)
	}
}

// No room reads as plain while it may hold encrypted messages: in any order of plain
// threads of every kind, encrypted threads, a move to encryption and encrypted
// messages, every cached room, a room not heard of and, after a restart, every room
// before Meta's first table is encrypted.
func TestNoRoomIsToldPlain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kinds := []table.ThreadType{
		table.UNKNOWN_THREAD_TYPE, table.ONE_TO_ONE, table.GROUP_THREAD, table.TINCAN_ONE_TO_ONE,
		table.CARRIER_MESSAGING_ONE_TO_ONE, table.ENCRYPTED_OVER_WA_ONE_TO_ONE,
		table.ENCRYPTED_OVER_WA_GROUP, table.COMMUNITY_GROUP,
	}
	keys := []int64{dana, eli, 300}
	steps := []func(r *rand.Rand, a *Adapter, c *conn){
		func(r *rand.Rand, a *Adapter, c *conn) { // a plain or encrypted thread row
			key := keys[r.IntN(len(keys))]
			a.take(ctx, c, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
				threadRow(key, kinds[r.IntN(len(kinds))], "", "inbox", 10, 0, 0),
			}})
		},
		func(_ *rand.Rand, a *Adapter, c *conn) { a.take(ctx, c, encryptedThread()) },
		func(r *rand.Rand, a *Adapter, c *conn) { // a thread moved to encryption
			a.take(ctx, c, &table.LSTable{LSMoveThreadToE2EECutoverFolder: []*table.LSMoveThreadToE2EECutoverFolder{
				{ThreadKey: keys[r.IntN(len(keys))]},
			}})
		},
		func(r *rand.Rand, a *Adapter, c *conn) { // an encrypted message, maybe in a plain thread's room
			key := keys[r.IntN(2)]
			a.takeEncrypted(ctx, c, fbMessage(msgrJID(key), msgrJID(key), fmt.Sprint("M", r.Int()), time.UnixMilli(100_000), textContent("hi")))
		},
	}
	allEncrypted := func(t *testing.T, a *Adapter, cache *db.Cache, when string) {
		t.Helper()
		rooms, err := cache.Rooms(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := []domain.RoomID{roomID(me, 999)}
		for i := range rooms {
			ids = append(ids, rooms[i].ID)
		}
		got, err := a.RoomEncryption(ctx, ids)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			if !got[id] {
				t.Fatalf("%s: %s reads as plain", when, id)
			}
		}
	}
	for seed := range uint64(30) {
		r := rand.New(rand.NewPCG(seed, 1))
		a, c, cache, _ := cachedAdapter(t)
		for i := range 8 {
			steps[r.IntN(len(steps))](r, a, c)
			allEncrypted(t, a, cache, fmt.Sprintf("seed %d step %d", seed, i))
		}
		allEncrypted(t, New(cache, newSecrets(), nil, nil, nil), cache, fmt.Sprintf("seed %d after a restart", seed))
		// After a restart, a plain row can come before anything says the room's thread
		// moved to encryption.
		again := New(cache, newSecrets(), nil, nil, nil)
		again.UseConfig(ctx, withAccounts("fb"))
		c2 := again.newConn(Account{Name: "fb"}, aSession(me, "x"), nil)
		again.adopt(c2)
		again.take(ctx, c2, &table.LSTable{LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 0, 0),
		}})
		allEncrypted(t, again, cache, fmt.Sprintf("seed %d after a restart's first plain row", seed))
	}
}

// What an encrypted message shows for what it carries.
func TestEncryptedBodies(t *testing.T) {
	t.Parallel()
	caption := "look"
	name := "plan.pdf"
	for _, tc := range []struct {
		content *waConsumerApplication.ConsumerApplication_Content
		want    string
	}{
		{textContent("hi"), "hi"},
		{&waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_ImageMessage{
			ImageMessage: &waConsumerApplication.ConsumerApplication_ImageMessage{Caption: &waCommon.MessageText{Text: &caption}},
		}}, "look\n[photo]"},
		{&waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_DocumentMessage{
			DocumentMessage: &waConsumerApplication.ConsumerApplication_DocumentMessage{FileName: &name},
		}}, "[file: plan.pdf]"},
		{&waConsumerApplication.ConsumerApplication_Content{}, ""},
		{nil, ""},
	} {
		if got := encryptedBody(tc.content); got != tc.want {
			t.Errorf("encryptedBody = %q, want %q", got, tc.want)
		}
	}
}

// A read receipt names the newest message by someone else at or before the read.
func TestReceiptTarget(t *testing.T) {
	t.Parallel()
	msg := func(sender int64, id string, at int64, redacted bool) domain.Message {
		return domain.Message{ID: eventID(me, dana, id), Sender: personID(sender), Timestamp: time.UnixMilli(at), Redacted: redacted}
	}
	msgs := []domain.Message{
		msg(dana, "A", 10, false), msg(dana, "B", 20, false), msg(dana, "C", 30, true),
		msg(me, "D", 40, false), msg(dana, "E", 50, false),
	}
	if mid, sender, ok := receiptTarget(msgs, me, 45); !ok || mid != "B" || sender != itoa(dana) {
		t.Errorf("receiptTarget(45) = %q, %q, %v; want B by dana", mid, sender, ok)
	}
	if _, _, ok := receiptTarget(msgs, me, 5); ok {
		t.Error("a read before every message names one")
	}
}

// The device kith registers is kept in the store and found again on the next run; one
// the store lost is registered afresh.
func TestTheDeviceIsKeptAndFoundAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	devices, err := OpenStore(ctx, filepath.Join(t.TempDir(), "messenger.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devices.Close() })
	a := New(nil, newSecrets(), devices, nil, nil)
	s := aSession(me, "x")
	client := a.clientFor(s)

	// A first device is new; registering picks its Facebook UUID when the sign-in
	// suggested none.
	dev, fresh, err := a.device(ctx, client, s)
	if err != nil || !fresh || dev == nil {
		t.Fatalf("a first device: fresh %v, %v", fresh, err)
	}
	s.Native = &mtypes.NativeSession{DeviceID: uuid.New()}
	if native, _, _ := a.device(ctx, client, s); native.FacebookUUID != s.Native.DeviceID {
		t.Errorf("an Android sign-in's device ID was not used: %v", native.FacebookUUID)
	}
	s.Native = nil
	// What registering does: Meta names the device, and it is saved.
	dev.ID = &types.JID{User: itoa(me), Device: 7, Server: types.MessengerServer}
	// What registering fills in, at the lengths the store checks.
	dev.Account = &waAdv.ADVSignedDeviceIdentity{
		Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64),
	}
	if err = dev.Save(ctx); err != nil {
		t.Fatal(err)
	}
	s.WADevice = 7
	again, fresh, err := a.device(ctx, client, s)
	if err != nil || fresh || again.ID.Device != 7 {
		t.Errorf("the kept device: fresh %v, %+v, %v", fresh, again.ID, err)
	}
	s.WADevice = 9
	if _, fresh, err := a.device(ctx, client, s); err != nil || !fresh {
		t.Errorf("a device the store lost: fresh %v, %v", fresh, err)
	}
}

// Reading a chat on another device moves its read position here; other receipts and
// events change nothing, and without a store encrypted chats stay off quietly.
func TestEncryptedEventsBesidesMessages(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, encryptedThread())
	room := roomID(me, dana)
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), "AAA", time.UnixMilli(100_000), textContent("hi")))
	unread := func() int {
		n, _, _, err := cache.CountUnread(ctx, a.Me(), room)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	a.onEncrypted(c, &events.Receipt{
		MessageSource: types.MessageSource{Chat: msgrJID(dana)}, Type: types.ReceiptTypeRead, Timestamp: time.UnixMilli(200_000),
	})
	if n := unread(); n != 1 {
		t.Errorf("their receipt moved your read position: %d unread", n)
	}
	a.onEncrypted(c, &events.Receipt{
		MessageSource: types.MessageSource{Chat: msgrJID(dana)}, Type: types.ReceiptTypeReadSelf, Timestamp: time.UnixMilli(200_000),
	})
	if n := unread(); n != 0 {
		t.Errorf("after reading on another device: %d unread", n)
	}
	a.onEncrypted(c, &events.Connected{})
	a.onEncrypted(c, "something else")
	a.connectE2EE(ctx, c) // no store: logged, nothing else
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.e2ee != nil || c.e2eeBusy {
		t.Errorf("without a store: e2ee %v, busy %v", c.e2ee, c.e2eeBusy)
	}
}
