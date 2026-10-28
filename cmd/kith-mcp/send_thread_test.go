package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// threaded is twoRooms with a thread at $root (two replies) and a main-timeline message
// in Standup, and a message in the other room.
func threaded() *fake {
	f := twoRooms()
	f.messages = map[domain.RoomID][]domain.Message{
		mx + "!open:x": {
			{ID: mx + "$root", RoomID: mx + "!open:x", Sender: "matrix:@dana:x", Body: "who takes the deploy?"},
			{ID: mx + "$main", RoomID: mx + "!open:x", Sender: "matrix:@sam:x", Body: "lunch at one"},
			{ID: mx + "$r1", RoomID: mx + "!open:x", Sender: "matrix:@sam:x", Body: "not me", ThreadRoot: mx + "$root"},
			{ID: mx + "$r2", RoomID: mx + "!open:x", Sender: "matrix:@dana:x", Body: "anyone?", ThreadRoot: mx + "$root", ReplyTo: mx + "$r1"},
		},
		mx + "!secret:x": {{ID: mx + "$elsewhere", RoomID: mx + "!secret:x", Sender: "matrix:@dana:x", Body: "private"}},
	}
	return f
}

// A thread is reached by its root or any reply; a reply to a threaded message lands in
// that thread.
func TestAMessageGoesWhereItIsAimed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		args            map[string]any
		thread, replyTo domain.EventID
	}{
		{"the main timeline", map[string]any{}, "", ""},
		{"a thread by its root", map[string]any{"thread": mx + "$root"}, mx + "$root", ""},
		{"a thread by one of its replies", map[string]any{"thread": mx + "$r2"}, mx + "$root", ""},
		{"a reply to a message in a thread", map[string]any{"reply_to": mx + "$r1"}, mx + "$root", mx + "$r1"},
		{"a reply in the main timeline", map[string]any{"reply_to": mx + "$main"}, "", mx + "$main"},
		{"the root answered in its thread", map[string]any{"thread": mx + "$root", "reply_to": mx + "$root"}, mx + "$root", mx + "$root"},
		{"a reply and its own thread", map[string]any{"thread": mx + "$root", "reply_to": mx + "$r2"}, mx + "$root", mx + "$r2"},
	} {
		f := threaded()
		s := newWriter(t, f, shareAll, []string{"space:Work"})
		c.args["room"], c.args["text"] = "Standup", "I will"
		if out := call(t, s, "send_message", c.args); out["action"] != agent.Sent {
			t.Fatalf("%s: action = %v, want sent", c.name, out["action"])
		}
		if len(f.sent) != 1 || f.sent[0].ThreadRoot != c.thread || f.sent[0].ReplyTo != c.replyTo {
			t.Errorf("%s: sent %+v, want thread %q reply %q", c.name, f.sent, c.thread, c.replyTo)
		}
	}
}

// A target not in that room is refused and nothing written; a room outside read scope
// is not found either.
func TestATargetMustBeAMessageOfThatRoom(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"another room's message", map[string]any{"reply_to": mx + "$elsewhere"}},
		{"another room's thread", map[string]any{"thread": mx + "$elsewhere"}},
		{"no such event", map[string]any{"thread": mx + "$nothing"}},
		{"a reply outside the thread named", map[string]any{"thread": mx + "$root", "reply_to": mx + "$main"}},
	} {
		f := threaded()
		s := newWriter(t, f, shareAllEncrypted, []string{"space:Work"})
		c.args["room"], c.args["text"] = "Standup", "I will"
		callErr(t, s, "send_message", c.args)
		if len(f.sent) != 0 || len(f.drafts) != 0 {
			t.Errorf("%s: something was written: sent %+v drafts %+v", c.name, f.sent, f.drafts)
		}
	}
}

// A drafted thread reply or reply takes its aim in an empty composer.
func TestAThreadReplyIsDraftedIntoItsThread(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args            map[string]any
		thread, replyTo domain.EventID
	}{
		{map[string]any{"thread": mx + "$r1"}, mx + "$root", ""},
		{map[string]any{"reply_to": mx + "$r2"}, mx + "$root", mx + "$r2"},
		{map[string]any{"reply_to": mx + "$main"}, "", mx + "$main"},
	} {
		f := threaded()
		s := newWriter(t, f, shareAll, nil) // no send list: every room is drafted
		c.args["room"], c.args["text"] = "Standup", "I will"
		if out := call(t, s, "send_message", c.args); out["action"] != agent.Drafted {
			t.Fatalf("%v: action = %v, want drafted", c.args, out["action"])
		}
		held := f.drafts[mx+"!open:x"]
		if held.Body != "I will" || held.ThreadRoot != c.thread || held.ReplyTo != c.replyTo {
			t.Errorf("%v: draft %+v, want it aimed at thread %q reply %q", c.args, held, c.thread, c.replyTo)
		}
		if len(f.sent) != 0 {
			t.Errorf("%v: sent %+v, want only a draft", c.args, f.sent)
		}
	}
}

// A held draft keeps its aim: words for elsewhere are refused and recorded, the draft
// untouched; words for the same place append.
func TestADraftAimedElsewhereIsNotAddedTo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		held domain.StoredDraft
		args map[string]any
		ok   bool
	}{
		{"a thread draft, the main timeline", domain.StoredDraft{Body: "mine", ThreadRoot: mx + "$root"}, map[string]any{}, false},
		{"a main draft, a thread", domain.StoredDraft{Body: "mine"}, map[string]any{"thread": mx + "$root"}, false},
		{"a reply draft, another reply", domain.StoredDraft{Body: "mine", ReplyTo: mx + "$main"}, map[string]any{"reply_to": mx + "$root"}, false},
		{"a thread draft, the same thread", domain.StoredDraft{Body: "mine", ThreadRoot: mx + "$root"}, map[string]any{"thread": mx + "$r2"}, true},
		{"a reply draft, no reply of its own", domain.StoredDraft{Body: "mine", ReplyTo: mx + "$main"}, map[string]any{}, true},
	} {
		f := threaded()
		c.held.RoomID = mx + "!open:x"
		f.store(c.held)
		s := newWriter(t, f, shareAll, nil)
		c.args["room"], c.args["text"] = "Standup", "I will"
		if !c.ok {
			callErr(t, s, "send_message", c.args)
			if held := f.drafts[mx+"!open:x"]; held.Body != "mine" || held.ThreadRoot != c.held.ThreadRoot || held.ReplyTo != c.held.ReplyTo {
				t.Errorf("%s: the draft changed to %+v", c.name, held)
			}
			if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
				t.Errorf("%s: ledger %+v, want the refusal recorded", c.name, ledger)
			}
			continue
		}
		call(t, s, "send_message", c.args)
		if held := f.drafts[mx+"!open:x"]; held.Body != "mine\n\nI will" || held.ThreadRoot != c.held.ThreadRoot || held.ReplyTo != c.held.ReplyTo {
			t.Errorf("%s: draft %+v, want the words appended and the aim kept", c.name, held)
		}
	}
}

// A failed send is queued still aimed at its thread; if the queue refuses, the draft is
// aimed there too.
func TestAQueuedThreadReplyKeepsItsThread(t *testing.T) {
	t.Parallel()
	f := threaded()
	f.sendErr = errors.New("homeserver down")
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	if out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "I will", "reply_to": mx + "$r1"}); out["action"] != agent.Queued {
		t.Fatalf("action = %v, want queued", out["action"])
	}
	if len(f.queued) != 1 || f.queued[0].ThreadRoot != mx+"$root" || f.queued[0].ReplyTo != mx+"$r1" {
		t.Errorf("queued %+v, want it aimed at $r1 in $root", f.queued)
	}

	f = threaded()
	f.sendErr, f.queueErr = errors.New("homeserver down"), errors.New("queue full")
	s = newWriter(t, f, shareAll, []string{"space:Work"})
	if out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "I will", "thread": mx + "$root"}); out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want drafted", out["action"])
	}
	if held := f.drafts[mx+"!open:x"]; held.ThreadRoot != mx+"$root" {
		t.Errorf("draft %+v, want it in the thread", held)
	}
}

// read_room shows each message's place, and one thread can be read alone.
func TestReadRoomShowsAndReadsThreads(t *testing.T) {
	t.Parallel()
	s := newServer(threaded(), shareAll)
	all := call(t, s, "read_room", map[string]any{"room": "Standup"})
	msgs, _ := all["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("read %d messages, want 4", len(msgs))
	}
	r2 := msgs[3].(map[string]any)
	if r2["thread"] != mx+"$root" || r2["reply_to"] != mx+"$r1" {
		t.Errorf("a reply in a thread reads as %v, want its thread and what it answers", r2)
	}
	for _, via := range []string{mx + "$root", mx + "$r1"} {
		one := call(t, s, "read_room", map[string]any{"room": "Standup", "thread": via})
		got, _ := one["messages"].([]any)
		var ids []string
		for _, m := range got {
			ids = append(ids, m.(map[string]any)["event_id"].(string))
		}
		if strings.Join(ids, ",") != mx+"$root,"+mx+"$r1,"+mx+"$r2" {
			t.Errorf("thread via %s = %v, want the root and its replies", via, ids)
		}
	}
	callErr(t, s, "read_room", map[string]any{"room": "Standup", "thread": mx + "$elsewhere"})
}
