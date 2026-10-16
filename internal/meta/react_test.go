package meta

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/table"
	"go.mau.fi/whatsmeow/proto/waCommon"
	"go.mau.fi/whatsmeow/proto/waConsumerApplication"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// encryptedReaction is an encrypted reaction (empty: none) on message mid.
func encryptedReaction(mid, key string) *waConsumerApplication.ConsumerApplication_Content {
	return &waConsumerApplication.ConsumerApplication_Content{Content: &waConsumerApplication.ConsumerApplication_Content_ReactionMessage{
		ReactionMessage: &waConsumerApplication.ConsumerApplication_ReactionMessage{Key: &waCommon.MessageKey{ID: &mid}, Text: &key},
	}}
}

// metaIDOf is a kith person ID's Meta ID.
func metaIDOf(t *testing.T, person string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(domain.ParseID(person).Native, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Reactions from every writer land as Messenger keeps them, one per person on a
// message, in any order: other people's, plain or encrypted, on both kinds of message
// in a room that holds both; yours, applied here at once, and Meta's echo of each, late
// and after a newer one of yours; your other device's. The cache ends as the last word
// of each person on each message, holds one reaction per person on a message at every
// step, and what is streamed builds the same view the cache holds.
func TestReactionsFromEveryWriterEndAsTheLastWord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const plainMid, encryptedMid = "mid.$plain1", "7516770863073460266"
	room := roomID(me, dana)
	keys := []string{"👍", "❤️", "😂", ""}
	people := []int64{me, dana}
	type slot struct {
		mid   string
		actor int64
	}
	for seed := range uint64(60) {
		r := rand.New(rand.NewPCG(seed, 3))
		a, c, cache, _ := cachedAdapter(t)
		// A one-to-one thread moved to encryption: its plain message, then an encrypted one.
		a.take(ctx, c, &table.LSTable{
			LSDeleteThenInsertContact: []*table.LSDeleteThenInsertContact{{Id: dana, Name: "Dana Mor"}},
			LSDeleteThenInsertThread:  []*table.LSDeleteThenInsertThread{threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0)},
			LSInsertMessage:           []*table.LSInsertMessage{live(dana, dana, plainMid, "plain", 10)},
		})
		a.take(ctx, c, &table.LSTable{LSMoveThreadToE2EECutoverFolder: []*table.LSMoveThreadToE2EECutoverFolder{{ThreadKey: dana}}})
		a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(dana), encryptedMid, time.UnixMilli(20), textContent("encrypted")))
		for len(a.reactions) > 0 {
			<-a.reactions
		}

		want := map[slot]string{}     // each person's last word
		streamed := map[slot]string{} // the view the stream builds
		var echoes []func()           // Meta's echoes of your plain reactions, in order
		var steps []string
		check := func() {
			t.Helper()
			for len(a.reactions) > 0 {
				u := <-a.reactions
				var mid string
				for _, s := range []string{plainMid, encryptedMid} {
					if u.Reaction.Target == eventID(me, dana, s) {
						mid = s
					}
				}
				k := slot{mid, metaIDOf(t, u.Reaction.Sender)}
				if u.Removed {
					if streamed[k] != u.Reaction.Key {
						t.Fatalf("seed %d %v: the stream took back %q, which it never showed (%q)", seed, steps, u.Reaction.Key, streamed[k])
					}
					delete(streamed, k)
				} else {
					if _, shown := streamed[k]; shown {
						t.Fatalf("seed %d %v: the stream showed a second reaction for one person", seed, steps)
					}
					streamed[k] = u.Reaction.Key
				}
			}
			got := map[slot]string{}
			rs, err := cache.Reactions(ctx, room)
			if err != nil {
				t.Fatal(err)
			}
			for _, x := range rs {
				actor := metaIDOf(t, x.Sender)
				mid := plainMid
				if x.Target == eventID(me, dana, encryptedMid) {
					mid = encryptedMid
				}
				if _, two := got[slot{mid, actor}]; two {
					t.Fatalf("seed %d %v: two reactions cached for one person on one message", seed, steps)
				}
				got[slot{mid, actor}] = x.Key
			}
			if !maps.Equal(got, streamed) {
				t.Fatalf("seed %d %v: cached %v, streamed %v", seed, steps, got, streamed)
			}
		}
		flush := func() { // Meta's echoes of yours, all of them, in order
			for _, echo := range echoes {
				echo()
			}
			echoes = nil
		}
		for range 14 {
			mid := []string{plainMid, encryptedMid}[r.IntN(2)]
			actor, key := people[r.IntN(2)], keys[r.IntN(len(keys))]
			op := r.IntN(4)
			if op == 1 && len(echoes) > 0 { // Meta echoes the oldest of yours
				steps = append(steps, "an echo")
				echoes[0]()
				echoes = echoes[1:]
				check() // an echo repeats what was sent; the last word is unchanged
				continue
			}
			switch {
			case op == 0 && actor == me: // yours from kith: applied now, echoed later if plain
				steps = append(steps, fmt.Sprintf("you %q on %s", key, mid))
				a.applyReaction(ctx, domain.Reaction{
					ID: reactionID(me, dana, mid, me), RoomID: room, Target: eventID(me, dana, mid), Sender: personID(me), Key: key,
				})
				if plainMessage(mid) {
					echoes = append(echoes, func() {
						a.take(ctx, c, &table.LSTable{LSUpsertReaction: []*table.LSUpsertReaction{{ThreadKey: dana, MessageId: mid, ActorId: me, Reaction: key}}})
					})
				}
			case plainMessage(mid):
				if actor == me {
					// Meta's stream is in order: your other device's word on a plain
					// message comes after the echoes of what you sent before it.
					flush()
				}
				steps = append(steps, fmt.Sprintf("%d %q on %s", actor, key, mid))
				tbl := &table.LSTable{LSUpsertReaction: []*table.LSUpsertReaction{{ThreadKey: dana, MessageId: mid, ActorId: actor, Reaction: key}}}
				if key == "" {
					tbl = &table.LSTable{LSDeleteReaction: []*table.LSDeleteReaction{{ThreadKey: dana, MessageId: mid, ActorId: actor}}}
				}
				a.take(ctx, c, tbl)
			default: // over the encrypted connection: Dana's, or yours from another device
				steps = append(steps, fmt.Sprintf("%d %q on %s (encrypted)", actor, key, mid))
				a.takeEncrypted(ctx, c, fbMessage(msgrJID(dana), msgrJID(actor), fmt.Sprint("R", r.Int()), time.UnixMilli(30), encryptedReaction(mid, key)))
			}
			if key == "" {
				delete(want, slot{mid, actor})
			} else {
				want[slot{mid, actor}] = key
			}
			check()
		}
		flush()
		check()
		if !maps.Equal(streamed, want) {
			t.Fatalf("seed %d %v: after every echo, cached %v, want each person's last word %v", seed, steps, streamed, want)
		}
	}
}

// A reaction or typing that cannot go says why and changes nothing here.
func TestReactionsAndTypingThatCannotGo(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a, c, cache, _ := cachedAdapter(t)
	a.take(ctx, c, &table.LSTable{
		LSDeleteThenInsertThread: []*table.LSDeleteThenInsertThread{
			threadRow(dana, table.ONE_TO_ONE, "", "inbox", 10, 10, 0),
			threadRow(eli, table.ENCRYPTED_OVER_WA_ONE_TO_ONE, "", "inbox", 10, 10, 0),
		},
		LSInsertMessage: []*table.LSInsertMessage{live(dana, dana, "mid.$p", "plain", 10)},
	})
	a.takeEncrypted(ctx, c, fbMessage(msgrJID(eli), msgrJID(eli), "777", time.UnixMilli(20), textContent("encrypted")))
	for _, tc := range []struct {
		name   string
		room   domain.RoomID
		target domain.EventID
		want   error
	}{
		{"a plain message with the connection still loading", roomID(me, dana), eventID(me, dana, "mid.$p"), errNotConnected},
		{"an encrypted message before encrypted chats connect", roomID(me, eli), eventID(me, eli, "777"), errNoEncrypted},
		{"a message not cached", roomID(me, dana), eventID(me, dana, "mid.$gone"), errNotCached},
		{"another network's room", "!room:example.org", "$e", errNotAThread},
	} {
		if err := a.SendReaction(ctx, tc.room, tc.target, "👍"); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	for _, room := range []domain.RoomID{roomID(me, dana), roomID(me, eli)} {
		if rs, err := cache.Reactions(ctx, room); err != nil || len(rs) != 0 {
			t.Errorf("a reaction that did not go is cached in %s: %v (%v)", room, rs, err)
		}
	}
	if len(a.reactions) != 0 {
		t.Errorf("a reaction that did not go was streamed")
	}
	if err := a.SendTyping(ctx, roomID(me, dana), true, 0); !errors.Is(err, errNotConnected) {
		t.Errorf("typing with the connection still loading: %v", err)
	}
	if err := a.SendTyping(ctx, roomID(me, eli), true, 0); !errors.Is(err, errNoEncrypted) {
		t.Errorf("typing before encrypted chats connect: %v", err)
	}
	if err := a.SendTyping(ctx, roomID(me, 999), true, 0); err != nil {
		t.Errorf("typing in a thread not heard of: %v, want nothing said", err)
	}
	if err := a.SendTyping(ctx, roomID(me+1, dana), true, 0); !errors.Is(err, errNotConnected) {
		t.Errorf("typing as an account not signed in: %v", err)
	}
}
