package signal_test

import (
	"context"
	"slices"
	"testing"
	"time"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

func edit(chat, sender string, target, sent uint64, body string) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Edit{Edit: &signalv1.Edit{
		Chat: chat, Sender: sender, Target: target, Sent: sent, Body: body,
	}}}
}

func react(chat, sender, emoji string, remove bool, author string, sent uint64) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Reaction{Reaction: &signalv1.Reaction{
		Chat: chat, Sender: sender, Emoji: emoji, Remove: remove, TargetAuthor: author, TargetSent: sent,
	}}}
}

func del(chat, sender string, target, sent uint64) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Delete{Delete: &signalv1.Delete{
		Chat: chat, Sender: sender, Target: target, Sent: sent,
	}}}
}

func (r *rig) reactions(room domain.RoomID) []domain.Reaction {
	rs, err := r.cache.Reactions(context.Background(), room)
	if err != nil {
		r.t.Fatal(err)
	}
	return rs
}

// An edit replaces its message's text under the message's own ID; a reaction naming the
// edit's time (as Signal's does) lands on the message; a second reaction replaces the
// sender's first, and a removal takes it off.
func TestEditsAndReactions(t *testing.T) {
	r := connected(t,
		msg(dana, dana, 1700000000100, "lunch at 1?"),
		edit(dana, dana, 1700000000100, 1700000000200, "lunch at 2?"),
		react(dana, me, "👍", false, dana, 1700000000200),
		react(dana, ofer, "🎉", false, dana, 1700000000200),
		react(dana, me, "❤️", false, dana, 1700000000200),
		react(dana, ofer, "🎉", true, dana, 1700000000200),
	)
	msgs := r.messages(direct(dana))
	if len(msgs) != 1 || msgs[0].Body != "lunch at 2?" || !msgs[0].Edited {
		t.Fatalf("after the edit: %+v", msgs)
	}
	original := domain.EventID(string(direct(dana)) + "/" + dana + ".1700000000100")
	if msgs[0].ID != original {
		t.Errorf("the edited message is %s, want %s", msgs[0].ID, original)
	}
	rs := r.reactions(direct(dana))
	if len(rs) != 1 || rs[0].Key != "❤️" || rs[0].Sender != "signal:"+me || rs[0].Target != original {
		t.Errorf("reactions = %+v, want my ❤️ on the message", rs)
	}
	// Clients hear each change: my 👍 taken off as my ❤️ replaced it, Ofer's 🎉 off.
	var stream []string
	for len(r.a.Reactions()) > 0 {
		u := <-r.a.Reactions()
		sign := "+"
		if u.Removed {
			sign = "-"
		}
		stream = append(stream, sign+u.Reaction.Key)
	}
	if want := []string{"+👍", "+🎉", "-👍", "+❤️", "-🎉"}; !slices.Equal(stream, want) {
		t.Errorf("reaction stream = %v, want %v", stream, want)
	}
}

// A deletion for everyone marks the message deleted; one that comes before its message
// keeps the message deleted when it arrives.
func TestDeletions(t *testing.T) {
	r := connected(t,
		msg(dana, dana, 1700000000100, "oops"),
		del(dana, dana, 1700000000100, 1700000000150),
		del(dana, dana, 1700000000300, 1700000000350),
		msg(dana, dana, 1700000000300, "deleted before it came"),
	)
	msgs := r.messages(direct(dana))
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", msgs)
	}
	for _, m := range msgs {
		if !m.Redacted || m.Body != "" {
			t.Errorf("%s is not deleted: %+v", m.ID, m)
		}
	}
}

// A chat read on the phone (Signal names only the message's author and time) is read
// in kith up to there.
func TestAChatReadOnThePhoneIsRead(t *testing.T) {
	read := &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Read{Read: &signalv1.Read{
		Messages: []*signalv1.ReadMessage{{Author: dana, Sent: 1700000000200}},
	}}}
	r := connected(t,
		msg(dana, dana, 1700000000100, "one"),
		msg(dana, dana, 1700000000200, "two"),
		msg(dana, dana, 1700000000300, "three"),
		read,
	)
	n, _, counted, err := r.cache.CountUnread(context.Background(), r.a.Me(), direct(dana))
	if err != nil {
		t.Fatal(err)
	}
	if !counted || n != 1 {
		t.Errorf("unread = %d (counted %v), want only three", n, counted)
	}
}

// Typing shows who types until they stop.
func TestTypingShowsWhoTypes(t *testing.T) {
	typing := func(started bool) *signalv1.EventsResponse {
		return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Typing{Typing: &signalv1.Typing{
			Chat: dana, Sender: dana, Started: started,
		}}}
	}
	r := newRigPlaying(t, "ok", me, me, []*signalv1.EventsResponse{typing(true), typing(false)})
	var seen [][]string
	deadline := time.After(10 * time.Second)
	for len(seen) < 2 {
		select {
		case act := <-r.a.Activity():
			if act.RoomID == direct(dana) {
				seen = append(seen, act.Typing)
			}
		case <-deadline:
			t.Fatalf("typing seen: %v", seen)
		}
	}
	if len(seen[0]) != 1 || seen[0][0] != "signal:"+dana || len(seen[1]) != 0 {
		t.Errorf("typing = %v, want Dana then nobody", seen)
	}
}

// Each edit names the version it replaces, so a second edit names the first edit's time
// (as Signal's apps send it), not the message's: every edit still lands on the message.
func TestEditsOfEditsLandOnTheMessage(t *testing.T) {
	r := connected(t,
		msg(dana, dana, 1700000000100, "one"),
		edit(dana, dana, 1700000000100, 1700000000200, "two"),
		edit(dana, dana, 1700000000200, 1700000000300, "three"),
		edit(dana, dana, 1700000000300, 1700000000400, "four"),
		react(dana, me, "👍", false, dana, 1700000000400),
	)
	msgs := r.messages(direct(dana))
	if len(msgs) != 1 || msgs[0].Body != "four" {
		t.Fatalf("messages = %+v, want one, reading four", msgs)
	}
	if rs := r.reactions(direct(dana)); len(rs) != 1 || rs[0].Target != msgs[0].ID {
		t.Errorf("reactions = %+v, want one on the message", rs)
	}
}

// A sender's reaction is one per message, whichever version of it each names: reacting
// again after an edit (naming the edit) replaces the first.
func TestOneReactionPerSenderAcrossEdits(t *testing.T) {
	r := connected(t,
		msg(dana, dana, 1700000000100, "one"),
		react(dana, me, "👍", false, dana, 1700000000100),
		edit(dana, dana, 1700000000100, 1700000000200, "two"),
		react(dana, me, "❤️", false, dana, 1700000000200),
	)
	if rs := r.reactions(direct(dana)); len(rs) != 1 || rs[0].Key != "❤️" {
		t.Errorf("reactions = %+v, want only my ❤️", rs)
	}
}
