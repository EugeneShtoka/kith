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

	"github.com/EugeneShtoka/kith/internal/domain"
)

// encryptedEdit is an encrypted edit of message mid to text.
func encryptedEdit(mid, text string) *waConsumerApplication.ConsumerApplication_Content {
	return &waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_EditMessage{
		EditMessage: &waConsumerApplication.ConsumerApplication_EditMessage{
			Key: &waCommon.MessageKey{ID: &mid}, Message: &waCommon.MessageText{Text: &text},
		},
	}}
}

// Edits from every writer show as their author's last word, in any order: Dana's on her
// plain and encrypted messages; yours from kith, applied here at once, with Meta's echo
// of a plain one late, after a newer one of yours; your other device's; and a listing
// of the message that comes again with an older text. At every step each message keeps
// its place, a plain one shows its edit with Meta's highest count so far (a late echo
// never undoes a newer edit), and an encrypted one its newest.
func TestEditsFromEveryWriterEndAsTheLastWord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const danaPlain, myPlain, danaEncrypted, myEncrypted = "mid.$dana1", "mid.$me1", "1000001", "1000002"
	room := roomID(me, dana)
	author := map[string]int64{danaPlain: dana, myPlain: me, danaEncrypted: dana, myEncrypted: me}
	for seed := range uint64(60) {
		r := rand.New(rand.NewPCG(seed, 5))
		a, c, cache, _ := cachedAdapter(t)
		a.take(ctx, c, &table.LSTable{
			LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor"}},
			LSDeleteThenInsertThread:  []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0)},
			LSInsertMessage:           []*table.LSInsertMessage{live(dana, dana, danaPlain, "first", 10), live(dana, me, myPlain, "first", 11)},
		})
		a.take(ctx, c, &table.LSTable{LSMoveThreadToE2EECutoverFolder: []*table.LSMoveThreadToE2EECutoverFolder{{ThreadKey: dana}}})
		a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), danaEncrypted, time.UnixMilli(20), textContent("first")))
		a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(me), myEncrypted, time.UnixMilli(21), textContent("first")))
		placed := map[string]time.Time{}
		for mid := range author {
			m, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, mid))
			placed[mid] = m.Timestamp
		}

		want := map[string]string{danaPlain: "first", myPlain: "first", danaEncrypted: "first", myEncrypted: "first"}
		count := map[string]int64{}   // a plain message's edits so far, as Meta counts them
		said := map[string][]string{} // every text a plain message has had
		var echoes []func()           // Meta's echoes of your plain edits, in order
		var steps []string
		ids := int64(2000000)
		for mid := range author {
			said[mid] = []string{"first"}
		}
		flush := func() {
			for _, echo := range echoes {
				echo()
			}
			echoes = nil
		}
		check := func() {
			t.Helper()
			for mid, text := range want {
				m, ok, err := cache.MessageByID(ctx, room, eventID(me, dana, mid))
				if err != nil || !ok {
					t.Fatalf("seed %d %v: %s is gone (%v)", seed, steps, mid, err)
				}
				if !m.Timestamp.Equal(placed[mid]) {
					t.Fatalf("seed %d %v: %s moved from %v to %v", seed, steps, mid, placed[mid], m.Timestamp)
				}
				if m.Body != text {
					t.Fatalf("seed %d %v: %s shows %q, want its author's last word %q", seed, steps, mid, m.Body, text)
				}
			}
		}
		for i := range 40 {
			mid := []string{danaPlain, myPlain, danaEncrypted, myEncrypted}[r.IntN(4)]
			text := fmt.Sprint("edit ", i)
			op := r.IntN(4)
			switch {
			case op == 0 && len(echoes) > 0: // Meta echoes the oldest of yours
				steps = append(steps, "an echo")
				echoes[0]()
				echoes = echoes[1:]
				check()
				continue
			case op == 1 && plainMessage(mid): // the message listed again, with a text it had
				old := said[mid][r.IntN(len(said[mid]))]
				var n int64
				for j, s := range said[mid] {
					if s == old {
						n = int64(j)
					}
				}
				steps = append(steps, fmt.Sprintf("%s listed with %q", mid, old))
				row := live(dana, author[mid], mid, old, placed[mid].UnixMilli())
				row.EditCount = n
				a.take(ctx, c, &table.LSTable{LSInsertMessage: []*table.LSInsertMessage{row}})
				check()
				continue
			case op == 2 && author[mid] == me && plainMessage(mid): // yours from kith
				count[mid]++
				n := count[mid]
				steps = append(steps, fmt.Sprintf("you edit %s to %q", mid, text))
				// What sendEdit does once Meta's answer names the count.
				m, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, mid))
				m.Body, m.Edited, m.RevisionID = text, true, editRevision(m.ID, n)
				a.edited(ctx, c, m)
				said[mid], want[mid] = append(said[mid], text), text
				echoes = append(echoes, func() {
					a.take(ctx, c, &table.LSTable{LSEditMessage: []*table.LSEditMessage{{MessageID: mid, Text: text, EditCount: n}}})
				})
			case op == 2 && author[mid] == me: // yours from kith, encrypted: no echo
				ids++
				steps = append(steps, fmt.Sprintf("you edit %s to %q", mid, text))
				m, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, mid))
				m.Body, m.Edited, m.EditedAt, m.RevisionID = text, true, time.Now(), eventID(me, dana, fmt.Sprint(ids))
				a.edited(ctx, c, m)
			case plainMessage(mid):
				if author[mid] == me {
					flush() // Meta's stream is in order: your other device's comes after
				}
				count[mid]++
				steps = append(steps, fmt.Sprintf("%d edits %s to %q", author[mid], mid, text))
				a.take(ctx, c, &table.LSTable{LSEditMessage: []*table.LSEditMessage{{MessageID: mid, Text: text, EditCount: count[mid]}}})
				said[mid], want[mid] = append(said[mid], text), text
			default: // an encrypted edit from its author's device, dated as sent
				ids++
				steps = append(steps, fmt.Sprintf("%d edits %s to %q (encrypted)", author[mid], mid, text))
				a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(author[mid]), fmt.Sprint(ids), time.Now(), encryptedEdit(mid, text)))
			}
			if !plainMessage(mid) {
				want[mid] = text
			}
			check()
		}
		flush()
		check()
	}
}

// An edit kith cannot make says why and changes nothing: someone else's message, one
// not cached, an encrypted one before encrypted chats connect, a plain one with the
// connection still loading.
func TestEditsThatCannotGo(t *testing.T) {
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
		if err := a.Send(ctx, room, domain.Draft{Body: "changed", Edits: eventID(me, dana, tc.target)}); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	for _, mid := range []string{"mid.$hers", "mid.$mine", "777"} {
		if m, _, _ := cache.MessageByID(ctx, room, eventID(me, dana, mid)); m.Edited || m.Body == "changed" {
			t.Errorf("%s changed by an edit that did not go: %+v", mid, m)
		}
	}
}
