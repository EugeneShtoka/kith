package meta

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"
)

// encryptedRevoke is an encrypted unsend of message mid, as fbMessage's message.
func encryptedRevoke(mid string) *waConsumerApplication.ConsumerApplication {
	return &waConsumerApplication.ConsumerApplication{Payload: &waConsumerApplication.ConsumerApplication_Payload{
		Payload: &waConsumerApplication.ConsumerApplication_Payload_ApplicationData{
			ApplicationData: &waConsumerApplication.ConsumerApplication_ApplicationData{
				ApplicationContent: &waConsumerApplication.ConsumerApplication_ApplicationData_Revoke{
					Revoke: &waConsumerApplication.ConsumerApplication_RevokeMessage{Key: &waCommon.MessageKey{ID: &mid}},
				},
			},
		},
	}}
}

// An unsent message stays unsent whatever comes after, and nothing else unsends one:
// in any order of unsends in every form Meta sends them (a delete row, the message
// again marked unsent, a listing marked unsent, an encrypted revoke) and yours from
// kith, edits, listings of the message as it was, Meta rewriting it without unsending
// it, and an unsend that comes before its message does, with deleted messages kept or not. At every step a message is unsent
// exactly when one of its unsends has come, and its text is gone unless kept.
func TestAnUnsentMessageStaysUnsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const danaPlain, myPlain, danaEncrypted, myEncrypted, late = "mid.$dana1", "mid.$me1", "1000001", "1000002", "mid.$late"
	room := roomID(me, dana)
	author := map[string]int64{danaPlain: dana, myPlain: me, danaEncrypted: dana, myEncrypted: me, late: dana}
	mids := []string{danaPlain, myPlain, danaEncrypted, myEncrypted, late}
	for seed := range uint64(60) {
		r := rand.New(rand.NewPCG(seed, 9))
		keep := seed%2 == 1
		a, c, cache, _ := cachedAdapter(t)
		a.keepDeleted.Store(keep)
		a.take(ctx, c, &table.LSTable{
			LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0)},
			LSInsertMessage:          []*table.LSInsertMessage{live(dana, dana, danaPlain, "first", 10), live(dana, me, myPlain, "first", 11)},
		})
		a.take(ctx, c, &table.LSTable{LSMoveThreadToE2EECutoverFolder: []*table.LSMoveThreadToE2EECutoverFolder{{ThreadKey: dana}}})
		a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), danaEncrypted, time.UnixMilli(20), textContent("first")))
		a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(me), myEncrypted, time.UnixMilli(21), textContent("first")))
		unsent := map[string]bool{}
		listed := map[string]bool{danaPlain: true, myPlain: true, danaEncrypted: true, myEncrypted: true}
		var steps []string
		ids := int64(3000000)
		for i := range 20 {
			mid := mids[r.IntN(len(mids))]
			plain := plainMessage(mid)
			switch op := r.IntN(6); {
			case op == 0 && plain: // listed again, as it was
				steps = append(steps, "list "+mid)
				a.take(ctx, c, &table.LSTable{LSInsertMessage: []*table.LSInsertMessage{live(dana, author[mid], mid, "first", 30)}})
				listed[mid] = true
			case op == 1 && plain && listed[mid]: // edited by its author
				steps = append(steps, "edit "+mid)
				a.take(ctx, c, &table.LSTable{LSEditMessage: []*table.LSEditMessage{{MessageID: mid, Text: fmt.Sprint("edit ", i), EditCount: int64(i + 1)}}})
			case op == 1: // edited by its author, encrypted
				ids++
				steps = append(steps, "edit "+mid+" (encrypted)")
				a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(author[mid]), fmt.Sprint(ids), time.Now(), encryptedEdit(mid, fmt.Sprint("edit ", i))))
			case op == 2 && author[mid] == me: // yours from kith: what Redact does once it went
				steps = append(steps, "you unsend "+mid)
				a.unsent(ctx, c, room, eventID(me, dana, mid), personID(me), time.Now())
				unsent[mid] = true
			case plain && op == 2:
				steps = append(steps, "delete row "+mid)
				a.take(ctx, c, &table.LSTable{LSDeleteMessage: []*table.LSDeleteMessage{{ThreadKey: dana, MessageId: mid}}})
				unsent[mid] = true
			case plain && op == 3:
				steps = append(steps, "unsent row "+mid)
				a.take(ctx, c, &table.LSTable{LSDeleteThenInsertMessage: []*table.LSDeleteThenInsertMessage{
					{ThreadKey: dana, MessageId: mid, SenderId: author[mid], IsUnsent: true, UnsentTimestampMs: 40},
				}})
				unsent[mid] = true
			case plain && op == 4:
				steps = append(steps, "listed unsent "+mid)
				row := live(dana, author[mid], mid, "", 30)
				row.IsUnsent = true
				a.take(ctx, c, &table.LSTable{LSInsertMessage: []*table.LSInsertMessage{row}})
				unsent[mid] = true
			case plain && op == 5: // Meta rewriting the message, not unsending it
				steps = append(steps, "rewrite row "+mid)
				a.take(ctx, c, &table.LSTable{LSDeleteThenInsertMessage: []*table.LSDeleteThenInsertMessage{
					{ThreadKey: dana, MessageId: mid, SenderId: author[mid], Text: "first"},
				}})
			case !plain && op >= 3:
				ids++
				steps = append(steps, "revoke "+mid)
				m := fbMessage(msgrJID(dana), msgrJID(author[mid]), fmt.Sprint(ids), time.Now(), nil)
				m.Message = encryptedRevoke(mid)
				a.takeEncrypted(ctx, c, m)
				unsent[mid] = true
			default:
				continue
			}
			for _, m := range mids {
				got, ok, err := cache.MessageByID(ctx, room, eventID(me, dana, m))
				if err != nil {
					t.Fatal(err)
				}
				if !ok {
					if listed[m] && !unsent[m] {
						t.Fatalf("seed %d %v: %s is gone", seed, steps, m)
					}
					continue
				}
				if got.Redacted != unsent[m] {
					t.Fatalf("seed %d keep=%v %v: %s unsent = %v, want %v", seed, keep, steps, m, got.Redacted, unsent[m])
				}
				if got.Redacted && !keep && got.Body != "" {
					t.Fatalf("seed %d %v: unsent %s still shows %q", seed, steps, m, got.Body)
				}
			}
		}
	}
}

// An unsend kith cannot make says why and unsends nothing: someone else's message, one
// not cached, an encrypted one before encrypted chats connect, a plain one with the
// connection still loading.
func TestUnsendsThatCannotGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0)},
		LSInsertMessage:          []*table.LSInsertMessage{live(dana, dana, "mid.$hers", "hers", 10), live(dana, me, "mid.$mine", "mine", 11)},
	})
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(me), "777", time.UnixMilli(20), textContent("mine too")))
	room := roomID(me, dana)
	for _, tc := range []struct {
		name   string
		target string
		want   error
	}{
		{"someone else's", "mid.$hers", errNotYours},
		{"not cached", "mid.$gone", errNotCached},
		{"plain, still loading", "mid.$mine", errNotConnected},
		{"encrypted, not connected", "777", errNoEncrypted},
	} {
		if err := a.Redact(ctx, room, eventID(me, dana, tc.target), ""); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if err := a.Redact(ctx, "!room:example.org", "$e", ""); !errors.Is(err, errNotAThread) {
		t.Errorf("another network's room: %v", err)
	}
	for _, mid := range []string{"mid.$hers", "mid.$mine", "777"} {
		if m, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, mid)); m.Redacted {
			t.Errorf("%s unsent by an unsend that did not go", mid)
		}
	}
}
