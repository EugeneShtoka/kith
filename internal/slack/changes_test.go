package slack

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Slack's reaction names and kith's emoji keys map both ways: a name to its emoji
// (toned as it says), the emoji back to a name Slack takes; a workspace's own emoji
// keeps its name.
func TestReactionNamesAndKeys(t *testing.T) {
	t.Parallel()
	for name, key := range map[string]string{
		"thumbsup": "👍", "+1": "👍", "+1::skin-tone-3": "👍\U0001F3FC", "partyparrot": ":partyparrot:",
	} {
		if got := reactionKey(name); got != key {
			t.Errorf("reactionKey(%q) = %q, want %q", name, got, key)
		}
	}
	for key, want := range map[string]string{"👍": "+1", "👍\U0001F3FC": "+1::skin-tone-3", ":partyparrot:": "partyparrot", "❤️": "heart"} {
		if got, ok := reactionName(key); !ok || got != want {
			t.Errorf("reactionName(%q) = %q, %v; want %q", key, got, ok, want)
		}
	}
}

// liveWorkspace is a workspace whose people are known, so nothing asks Slack.
func liveWorkspace(t *testing.T, a *Adapter) *workspace {
	t.Helper()
	w := newWorkspace(Account{Name: "work", Workspace: "acme"}, Credentials{Team: "T1", User: "U1"}, "Acme", nil, 0)
	for _, u := range []string{"U1", "U2", "U3"} {
		w.knowPerson(u, "person "+u)
	}
	if !a.adopt(w) {
		t.Fatal("adopt refused")
	}
	return w
}

// A message is edited live, reacted to and taken back (live, or while kith was not
// connected), and read in history pages fetched before or after each change, written
// in any order, then once more at the end. Whatever the order: the message shows its
// newest edit, a deletion sticks, and its reactions are Slack's — a page read before a
// reaction was taken back never brings it back, and the last page takes back one that
// went while nobody was listening.
func TestLiveChangesAndHistoryPagesAgree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for seed := range uint64(60) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, 3))
			a, _ := cached(t, Account{Name: "work", Workspace: "acme"})
			w := liveWorkspace(t, a)
			const ts = "100.000001"
			target := messageID("T1", "C1", ts)
			// The truth, as Slack holds it.
			body, version, deleted := "v0", 0, false
			reactions := map[string]bool{} // user/name
			snapshot := func() slackgo.Message {
				m := slackgo.Message{Msg: slackgo.Msg{User: "U2", Text: body, Timestamp: ts}}
				if version > 0 {
					m.Edited = &slackgo.Edited{Timestamp: fmt.Sprintf("%d.000000", 100+version)}
				}
				byName := map[string][]string{}
				for k := range reactions {
					user, name, _ := cut(k)
					byName[name] = append(byName[name], user)
				}
				for name, users := range byName {
					slices.Sort(users)
					m.Reactions = append(m.Reactions, slackgo.ItemReaction{Name: name, Users: users, Count: len(users)})
				}
				return m
			}
			type page struct {
				msg     slackgo.Message
				fetched time.Time
			}
			var pending []page
			// Half the runs change nothing while kith is away: then the cache must hold
			// Slack's state after every step, not only once a fresh page lands.
			offline := seed%2 == 1
			check := func(step int) {
				t.Helper()
				msg, found, err := a.cache.MessageByID(ctx, roomID("T1", "C1"), target)
				if err != nil {
					t.Fatal(err)
				}
				if !offline && !deleted && version > 0 && (!found || msg.Body != body) {
					t.Fatalf("step %d: cache shows %q, want the newest edit %q", step, msg.Body, body)
				}
				if !offline && !deleted {
					if got, want := cachedReactions(t, a), liveReactions(reactions); fmt.Sprint(got) != fmt.Sprint(want) {
						t.Fatalf("step %d: reactions cached %v, want the live %v", step, got, want)
					}
				}
			}
			for step := range 30 {
				kind := rng.IntN(6)
				if kind == 5 && !offline {
					kind = 2
				}
				switch kind {
				case 0: // a live edit
					if deleted {
						continue
					}
					version++
					body = fmt.Sprintf("v%d", version)
					sub := slackgo.Msg{User: "U2", Text: body, Timestamp: ts, Edited: &slackgo.Edited{Timestamp: fmt.Sprintf("%d.000000", 100+version)}}
					a.onEdited(ctx, w, &slackgo.MessageEvent{Msg: slackgo.Msg{Channel: "C1", SubType: "message_changed", Timestamp: fmt.Sprintf("%d.5", 100+step)}, SubMessage: &sub})
				case 1: // a live reaction, added or taken back
					user, name := []string{"U1", "U2", "U3"}[rng.IntN(3)], []string{"thumbsup", "heart"}[rng.IntN(2)]
					k := user + "/" + name
					reactions[k] = !reactions[k]
					if !reactions[k] {
						delete(reactions, k)
					}
					ev := slackgo.ReactionEvent{User: user, Reaction: name, Item: slackgo.ReactionItem{Type: "message", Channel: "C1", Timestamp: ts}}
					a.onReaction(ctx, w, ev, reactions[k])
				case 2: // a history page fetched now
					pending = append(pending, page{snapshot(), time.Now()})
				case 3: // the oldest page fetched is written
					if len(pending) == 0 {
						continue
					}
					p := pending[0]
					pending = pending[1:]
					a.cachePage(ctx, w, "C1", []slackgo.Message{p.msg}, p.fetched)
				case 5: // a change while kith was not connected: no live event
					if rng.IntN(2) == 0 && !deleted { // an edit, which only a later page brings
						version++
						body = fmt.Sprintf("v%d", version)
						continue
					}
					user, name := []string{"U1", "U2", "U3"}[rng.IntN(3)], []string{"thumbsup", "heart"}[rng.IntN(2)]
					k := user + "/" + name
					if reactions[k] {
						delete(reactions, k)
					} else {
						reactions[k] = true
					}
				case 4: // rarely, a deletion
					if rng.IntN(6) == 0 && !deleted {
						deleted = true
						a.onDeleted(ctx, w, &slackgo.MessageEvent{Msg: slackgo.Msg{Channel: "C1", SubType: "message_deleted", DeletedTimestamp: ts, Timestamp: "999.0"}})
					}
				}
				check(step)
			}
			// Every page still in flight lands too, then one read now, after everything.
			for _, p := range pending {
				a.cachePage(ctx, w, "C1", []slackgo.Message{p.msg}, p.fetched)
			}
			a.cachePage(ctx, w, "C1", []slackgo.Message{snapshot()}, time.Now())
			msg, found, err := a.cache.MessageByID(ctx, roomID("T1", "C1"), target)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case deleted && found && !msg.Redacted:
				t.Errorf("deleted, but the cache shows %+v", msg)
			case !deleted && version > 0 && (!found || msg.Body != body):
				t.Errorf("cache shows %q (found %v), want the newest edit %q", msg.Body, found, body)
			}
			if deleted {
				return
			}
			if got, want := cachedReactions(t, a), liveReactions(reactions); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("reactions cached %v, want Slack's %v", got, want)
			}
		})
	}
}

// cachedReactions are the cached reactions in C1 as user/name, sorted.
func cachedReactions(t *testing.T, a *Adapter) []string {
	t.Helper()
	rs, err := a.cache.Reactions(context.Background(), roomID("T1", "C1"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range rs {
		name, _ := reactionName(r.Key)
		got[domain.ParseID(r.Sender).Native[len("T1."):]+"/"+name] = true
	}
	return sortedKeys(got)
}

// liveReactions are Slack's reactions as user/name, each name as kith spells it back.
func liveReactions(reactions map[string]bool) []string {
	want := map[string]bool{}
	for k := range reactions {
		user, name, _ := cut(k)
		canonical, _ := reactionName(reactionKey(name))
		want[user+"/"+canonical] = true
	}
	return sortedKeys(want)
}

func cut(k string) (string, string, bool) {
	for i := range k {
		if k[i] == '/' {
			return k[:i], k[i+1:], true
		}
	}
	return k, "", false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
