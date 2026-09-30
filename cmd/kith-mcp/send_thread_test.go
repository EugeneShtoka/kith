package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// threaded is twoRooms with a conversation in Standup: a thread rooted at $root with
// two replies, a main-timeline message, and a message in the other room.
func threaded() *fake {
	f := twoRooms()
	f.messages = map[domain.RoomID][]domain.Message{
		"!open:x": {
			{ID: "$root", RoomID: "!open:x", Sender: "@dana:x", Body: "who takes the deploy?"},
			{ID: "$main", RoomID: "!open:x", Sender: "@sam:x", Body: "lunch at one"},
			{ID: "$r1", RoomID: "!open:x", Sender: "@sam:x", Body: "not me", ThreadRoot: "$root"},
			{ID: "$r2", RoomID: "!open:x", Sender: "@dana:x", Body: "anyone?", ThreadRoot: "$root", ReplyTo: "$r1"},
		},
		"!secret:x": {{ID: "$elsewhere", RoomID: "!secret:x", Sender: "@dana:x", Body: "private"}},
	}
	return f
}

// Where a message goes: the thread by its root or any reply in it, and a reply to a
// message in a thread lands in that thread.
func TestAMessageGoesWhereItIsAimed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name            string
		args            map[string]any
		thread, replyTo domain.EventID
	}{
		{"the main timeline", map[string]any{}, "", ""},
		{"a thread by its root", map[string]any{"thread": "$root"}, "$root", ""},
		{"a thread by one of its replies", map[string]any{"thread": "$r2"}, "$root", ""},
		{"a reply to a message in a thread", map[string]any{"reply_to": "$r1"}, "$root", "$r1"},
		{"a reply in the main timeline", map[string]any{"reply_to": "$main"}, "", "$main"},
		{"the root answered in its thread", map[string]any{"thread": "$root", "reply_to": "$root"}, "$root", "$root"},
		{"a reply and its own thread", map[string]any{"thread": "$root", "reply_to": "$r2"}, "$root", "$r2"},
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

// A target that is not a message of that room is refused, and nothing is written: a
// room outside the read scope is not found either, so nothing is said about it.
func TestATargetMustBeAMessageOfThatRoom(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"another room's message", map[string]any{"reply_to": "$elsewhere"}, "not a message in this room"},
		{"another room's thread", map[string]any{"thread": "$elsewhere"}, "not a message in this room"},
		{"no such event", map[string]any{"thread": "$nothing"}, "not a message in this room"},
		{"a reply outside the thread named", map[string]any{"thread": "$root", "reply_to": "$main"}, "not in that thread"},
	} {
		f := threaded()
		s := newWriter(t, f, shareAllEncrypted, []string{"space:Work"})
		c.args["room"], c.args["text"] = "Standup", "I will"
		if got := callErr(t, s, "send_message", c.args); !strings.Contains(got, c.want) {
			t.Errorf("%s: %q, want it to say %q", c.name, got, c.want)
		}
		if len(f.sent) != 0 || len(f.drafts) != 0 {
			t.Errorf("%s: something was written: sent %+v drafts %+v", c.name, f.sent, f.drafts)
		}
	}
}

// A thread or reply the policy drafts is drafted aimed there: in an empty composer the
// draft takes the thread and the reply target.
func TestAThreadReplyIsDraftedIntoItsThread(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		args            map[string]any
		thread, replyTo domain.EventID
	}{
		{map[string]any{"thread": "$r1"}, "$root", ""},
		{map[string]any{"reply_to": "$r2"}, "$root", "$r2"},
		{map[string]any{"reply_to": "$main"}, "", "$main"},
	} {
		f := threaded()
		s := newWriter(t, f, shareAll, nil) // no send list: every room is drafted
		c.args["room"], c.args["text"] = "Standup", "I will"
		if out := call(t, s, "send_message", c.args); out["action"] != agent.Drafted {
			t.Fatalf("%v: action = %v, want drafted", c.args, out["action"])
		}
		held := f.drafts["!open:x"]
		if held.Body != "I will" || held.ThreadRoot != c.thread || held.ReplyTo != c.replyTo {
			t.Errorf("%v: draft %+v, want it aimed at thread %q reply %q", c.args, held, c.thread, c.replyTo)
		}
		if len(f.sent) != 0 {
			t.Errorf("%v: sent %+v, want only a draft", c.args, f.sent)
		}
	}
}

// A composer holding a draft keeps its aim: words for another thread, for the main
// timeline, or answering another message are refused and recorded, and the draft is
// untouched; words for the same place are appended.
func TestADraftAimedElsewhereIsNotAddedTo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		held domain.StoredDraft
		args map[string]any
		ok   bool
	}{
		{"a thread draft, the main timeline", domain.StoredDraft{Body: "mine", ThreadRoot: "$root"}, map[string]any{}, false},
		{"a main draft, a thread", domain.StoredDraft{Body: "mine"}, map[string]any{"thread": "$root"}, false},
		{"a reply draft, another reply", domain.StoredDraft{Body: "mine", ReplyTo: "$main"}, map[string]any{"reply_to": "$root"}, false},
		{"a thread draft, the same thread", domain.StoredDraft{Body: "mine", ThreadRoot: "$root"}, map[string]any{"thread": "$r2"}, true},
		{"a reply draft, no reply of its own", domain.StoredDraft{Body: "mine", ReplyTo: "$main"}, map[string]any{}, true},
	} {
		f := threaded()
		c.held.RoomID = "!open:x"
		f.store(c.held)
		s := newWriter(t, f, shareAll, nil)
		c.args["room"], c.args["text"] = "Standup", "I will"
		if !c.ok {
			if got := callErr(t, s, "send_message", c.args); !strings.Contains(got, "aimed elsewhere") {
				t.Errorf("%s: %q, want the aim refusal", c.name, got)
			}
			if held := f.drafts["!open:x"]; held.Body != "mine" || held.ThreadRoot != c.held.ThreadRoot || held.ReplyTo != c.held.ReplyTo {
				t.Errorf("%s: the draft changed to %+v", c.name, held)
			}
			if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
				t.Errorf("%s: ledger %+v, want the refusal recorded", c.name, ledger)
			}
			continue
		}
		call(t, s, "send_message", c.args)
		if held := f.drafts["!open:x"]; held.Body != "mine\n\nI will" || held.ThreadRoot != c.held.ThreadRoot || held.ReplyTo != c.held.ReplyTo {
			t.Errorf("%s: draft %+v, want the words appended and the aim kept", c.name, held)
		}
	}
}

// A send that fails goes to the queue still aimed at its thread; one the queue will
// not take either is drafted, aimed there too.
func TestAQueuedThreadReplyKeepsItsThread(t *testing.T) {
	t.Parallel()
	f := threaded()
	f.sendErr = errors.New("homeserver down")
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	if out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "I will", "reply_to": "$r1"}); out["action"] != agent.Queued {
		t.Fatalf("action = %v, want queued", out["action"])
	}
	if len(f.queued) != 1 || f.queued[0].ThreadRoot != "$root" || f.queued[0].ReplyTo != "$r1" {
		t.Errorf("queued %+v, want it aimed at $r1 in $root", f.queued)
	}

	f = threaded()
	f.sendErr, f.queueErr = errors.New("homeserver down"), errors.New("queue full")
	s = newWriter(t, f, shareAll, []string{"space:Work"})
	if out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "I will", "thread": "$root"}); out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want drafted", out["action"])
	}
	if held := f.drafts["!open:x"]; held.ThreadRoot != "$root" {
		t.Errorf("draft %+v, want it in the thread", held)
	}
}

// Reading shows where each message is, and one thread can be read alone.
func TestReadRoomShowsAndReadsThreads(t *testing.T) {
	t.Parallel()
	s := newServer(threaded(), shareAll)
	all := call(t, s, "read_room", map[string]any{"room": "Standup"})
	msgs, _ := all["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("read %d messages, want 4", len(msgs))
	}
	r2 := msgs[3].(map[string]any)
	if r2["thread"] != "$root" || r2["reply_to"] != "$r1" {
		t.Errorf("a reply in a thread reads as %v, want its thread and what it answers", r2)
	}
	for _, via := range []string{"$root", "$r1"} {
		one := call(t, s, "read_room", map[string]any{"room": "Standup", "thread": via})
		got, _ := one["messages"].([]any)
		var ids []string
		for _, m := range got {
			ids = append(ids, m.(map[string]any)["event_id"].(string))
		}
		if strings.Join(ids, ",") != "$root,$r1,$r2" {
			t.Errorf("thread via %s = %v, want the root and its replies", via, ids)
		}
	}
	if got := callErr(t, s, "read_room", map[string]any{"room": "Standup", "thread": "$elsewhere"}); !strings.Contains(got, "not a message in this room") {
		t.Errorf("another room's thread: %q", got)
	}
}
