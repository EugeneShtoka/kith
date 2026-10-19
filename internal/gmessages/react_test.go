package gmessages

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"go.mau.fi/mautrix-gmessages/pkg/libgm/gmproto"
	"google.golang.org/protobuf/proto"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// reactionSet is reactions as a key per participant, or per reaction ID once cached.
type reactionSet map[string]string

// withReactions is a copy of m carrying rs, by participant.
func withReactions(m *gmproto.Message, rs reactionSet) *gmproto.Message {
	out, _ := proto.Clone(m).(*gmproto.Message)
	byKey := map[string][]string{}
	for pid, key := range rs {
		byKey[key] = append(byKey[key], pid)
	}
	for key, pids := range byKey {
		slices.Sort(pids)
		out.Reactions = append(out.Reactions, &gmproto.ReactionEntry{Data: gmproto.MakeReactionData(key), ParticipantIDs: pids})
	}
	return out
}

// cachedReactions is target's cached reactions, by reaction ID.
func cachedReactions(t *testing.T, cache *db.Cache, room domain.RoomID, target domain.EventID) reactionSet {
	t.Helper()
	rs, err := cache.Reactions(context.Background(), room)
	if err != nil {
		t.Fatal(err)
	}
	out := reactionSet{}
	for _, r := range rs {
		if r.Target == target {
			out[string(r.ID)] = r.Key
		}
	}
	return out
}

// byID is rs, by participant, as reaction IDs on a message.
func byID(conversation, message string, rs reactionSet) reactionSet {
	out := reactionSet{}
	for pid, key := range rs {
		out[string(reactionID(self, conversation, message, pid))] = key
	}
	return out
}

// A message's reactions change on the phone while kith takes live copies and listings
// in every order, a listing taken after newer live copies included: the cache ends as
// the copy the phone had last says; a listing asked for before a change kith already
// took never undoes it.
func TestReactionsAreTheNewestCopys(t *testing.T) {
	t.Parallel()

	keys := []string{"", "👍", "❤️", "😂"}
	for seed := range uint64(120) {
		rng := rand.New(rand.NewPCG(seed, 11))
		a, c, cache, _ := cachedAdapter(t)
		ctx := context.Background()
		a.takeConversations(ctx, c, []*gmproto.Conversation{active(group, "G", true, 10, false, pMe, pDana, pEli)})
		base := text(group, "m1", "2", "pizza?", 1000, gmproto.MessageStatusType_INCOMING_COMPLETE)
		start := time.Now()
		type copyOf struct {
			rs reactionSet
			at time.Time
		}
		var phone reactionSet // as the phone has them now
		var asked []copyOf    // listings asked for, not taken yet
		var newest time.Time
		want := reactionSet{}
		var log []string
		take := func(v copyOf, live bool) {
			a.takeMessagesAt(ctx, c, []*gmproto.Message{withReactions(base, v.rs)}, live, v.at)
			if !v.at.Before(newest) {
				newest, want = v.at, v.rs
			}
		}
		for step := range 12 {
			at := start.Add(time.Duration(step) * time.Second)
			switch rng.IntN(3) {
			case 0: // the reactions change on the phone, and its live copy comes
				phone = reactionSet{}
				for _, pid := range []string{"1", "2", "3"} {
					if k := keys[rng.IntN(len(keys))]; k != "" {
						phone[pid] = k
					}
				}
				take(copyOf{phone, at}, true)
				log = append(log, fmt.Sprintf("live %v", phone))
			case 1: // a listing is asked for
				asked = append(asked, copyOf{maps.Clone(phone), at})
				log = append(log, "asked")
			default: // a listing asked for earlier is taken
				if len(asked) == 0 {
					continue
				}
				i := rng.IntN(len(asked))
				take(asked[i], false)
				log = append(log, fmt.Sprintf("listed %v", asked[i].rs))
				asked = slices.Delete(asked, i, i+1)
			}
		}
		got := cachedReactions(t, cache, roomID(self, group), eventID(self, group, "m1"))
		if wantIDs := byID(group, "m1", want); !maps.Equal(got, wantIDs) {
			t.Fatalf("seed %d: cached %v, want %v\n%v", seed, got, wantIDs, log)
		}
	}
}

// A reaction sent is kept at once, replacing the account's own (one each) and leaving
// others'; taking it off leaves the others; a listing asked for before it does not
// bring the old one back. A reaction is not a message.
func TestAReactionSentIsKeptAtOnce(t *testing.T) {
	t.Parallel()

	a, c, cache, _ := cachedAdapter(t)
	ctx := context.Background()
	dm := active(danaDM, "", false, 10, false, pMe, pDana)
	dm.DefaultOutgoingID = "1"
	a.takeConversations(ctx, c, []*gmproto.Conversation{dm})
	asked := time.Now()
	msg := text(danaDM, "m1", "2", "pizza?", 1000, gmproto.MessageStatusType_INCOMING_COMPLETE)
	a.takeMessagesAt(ctx, c, []*gmproto.Message{withReactions(msg, reactionSet{"2": "😂"})}, true, asked)
	room, target := roomID(self, danaDM), eventID(self, danaDM, "m1")
	expect := func(when string, rs reactionSet) {
		t.Helper()
		if got, want := cachedReactions(t, cache, room, target), byID(danaDM, "m1", rs); !maps.Equal(got, want) {
			t.Errorf("%s: %v, want %v", when, got, want)
		}
	}

	a.keepOwnReaction(ctx, c, danaDM, "m1", "1", "👍", gmproto.SendReactionRequest_ADD)
	expect("after adding", reactionSet{"1": "👍", "2": "😂"})
	a.keepOwnReaction(ctx, c, danaDM, "m1", "1", "❤️", gmproto.SendReactionRequest_SWITCH)
	expect("after switching", reactionSet{"1": "❤️", "2": "😂"})
	a.takeMessagesAt(ctx, c, []*gmproto.Message{withReactions(msg, reactionSet{"1": "👍", "2": "😂"})}, false, asked)
	expect("after an older listing", reactionSet{"1": "❤️", "2": "😂"})
	a.keepOwnReaction(ctx, c, danaDM, "m1", "1", "❤️", gmproto.SendReactionRequest_REMOVE)
	expect("after taking it off", reactionSet{"2": "😂"})
	if _, ok := messageOf(reactionID(self, danaDM, "m1", "1")); ok {
		t.Error("a reaction reads as a message")
	}
}
