package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// newWriter is a server that may write, with a ledger in a directory that goes away with
// the test.
func newWriter(t *testing.T, f *fake, scope domain.ModelScope, send []string) *server {
	t.Helper()
	ledger, err := agent.Open(filepath.Join(t.TempDir(), "sends.jsonl"))
	if err != nil {
		t.Fatalf("opening the ledger: %v", err)
	}
	return &server{
		backend: f, scope: scope,
		send: send, cooldown: time.Minute, ledger: ledger, client: "test-client",
	}
}

// entries is the ledger, read back.
func entries(t *testing.T, s *server) []agent.Entry {
	t.Helper()
	out, err := s.ledger.Entries()
	if err != nil {
		t.Fatalf("reading the ledger: %v", err)
	}
	return out
}

// A room the list names is posted to, and the answer says so.
func TestSendGoesOutWhenTheListSaysSo(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})

	if out["action"] != agent.Sent {
		t.Fatalf("action = %v, want %q", out["action"], agent.Sent)
	}
	if len(f.sent) != 1 || !strings.HasSuffix(f.sent[0].Body, "on my way") {
		t.Fatalf("sent = %+v, want the message", f.sent)
	}
	// The transaction ID is what makes a failed send safe to retry, so a send without one
	// is a send whose failure cannot be recovered from.
	if f.sent[0].TxnID == "" {
		t.Error("the send carried no transaction ID")
	}
	if len(f.drafts) != 0 {
		t.Errorf("a message that was sent also became a draft: %+v", f.drafts)
	}
	ledger := entries(t, s)
	if len(ledger) != 1 || ledger[0].Outcome != agent.Sent || ledger[0].Author != "test-client" {
		t.Fatalf("ledger = %+v, want one sent entry naming who wrote it", ledger)
	}
}

// Everywhere the list does not name is drafted — which is an answer, not a failure.
func TestUnlistedRoomIsDrafted(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"room:Somewhere else"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})

	if out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want %q", out["action"], agent.Drafted)
	}
	if len(f.sent) != 0 {
		t.Fatalf("a room outside [agent.write] send was posted to: %+v", f.sent)
	}
	held := f.drafts["!open:x"]
	if held.Body != "on my way" {
		t.Fatalf("draft = %q, want the words", held.Body)
	}
	// The provenance is the whole reason a draft is an acceptable answer: the composer
	// says who wrote it and when, so old words are visibly not a live thought.
	if held.Author != "test-client" || held.Updated.IsZero() {
		t.Errorf("draft author/when = %q/%v, want the client and a time", held.Author, held.Updated)
	}
	if reason, _ := out["reason"].(string); !strings.Contains(reason, "send") {
		t.Errorf("reason = %q, want it to name the list that decided", reason)
	}
}

// Reading is the weaker permission and it is asked first: a room the scope refuses cannot
// be written to however `send` names it.
func TestSendCannotOutrankTheReadScope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		scope domain.ModelScope
		room  string
	}{
		{"excluded by the deny list", domain.ModelScope{Only: []string{"group"}, Except: []string{"room:Standup"}}, "Standup"},
		{"missing from the allow list", domain.ModelScope{Only: []string{"room:Private chat"}}, "Standup"},
		{"encrypted while encrypted rooms are out", shareAll, "Private chat"},
		{"nothing shared for reading", domain.ModelScope{}, "Standup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := twoRooms()
			// The send list names everything, which is the point: it still loses.
			s := newWriter(t, f, tc.scope, []string{"room:Standup", "room:Private chat", "dm", "group"})
			said := callErr(t, s, "send_message", map[string]any{"room": tc.room, "text": "hello"})
			if len(f.sent) != 0 || len(f.drafts) != 0 {
				t.Fatalf("a refused room was written to: sent=%+v drafts=%+v", f.sent, f.drafts)
			}
			if !strings.Contains(said, "[agent.read]") {
				t.Errorf("refusal = %q, want it to name the setting that refused", said)
			}
			// A refused write is still an attempt somebody auditing wants to see.
			if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Outcome != agent.Refused {
				t.Errorf("ledger = %+v, want the refusal recorded", ledger)
			}
		})
	}
}

// The cooldown rests a room: a second message inside the window waits in the composer.
func TestCooldownDraftsTheSecondMessage(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	call(t, s, "send_message", map[string]any{"room": "Standup", "text": "first"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "second"})

	if out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want the second message drafted", out["action"])
	}
	if len(f.sent) != 1 {
		t.Fatalf("sent %d messages, want the cooldown to have stopped the second", len(f.sent))
	}
	if reason, _ := out["reason"].(string); !strings.Contains(reason, "cooldown") {
		t.Errorf("reason = %q, want it to name the cooldown", reason)
	}
	// Nothing is lost when the rail bites: the words are in the composer.
	if !strings.Contains(f.drafts["!open:x"].Body, "second") {
		t.Errorf("draft = %q, want the message the cooldown held back", f.drafts["!open:x"].Body)
	}
}

// A cooldown of zero is off, which is a thing somebody can ask for out loud.
func TestCooldownOffSendsBoth(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	s.cooldown = 0
	call(t, s, "send_message", map[string]any{"room": "Standup", "text": "first"})
	call(t, s, "send_message", map[string]any{"room": "Standup", "text": "second"})
	if len(f.sent) != 2 {
		t.Fatalf("sent %d messages, want both", len(f.sent))
	}
}

// A draft is appended to, never over: the person's words come first and survive.
func TestDraftIsAppendedNotOverwritten(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.drafts = map[domain.RoomID]domain.StoredDraft{
		"!open:x": {RoomID: "!open:x", Body: "half a thought", Caret: 4},
	}
	s := newWriter(t, f, shareAll, nil)
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "the rest of it"})

	want := "half a thought\n\nthe rest of it"
	if got := f.drafts["!open:x"].Body; got != want {
		t.Fatalf("draft = %q, want %q", got, want)
	}
	if reason, _ := out["reason"].(string); !strings.Contains(reason, "appended") {
		t.Errorf("reason = %q, want it to say the words were appended", reason)
	}
}

// Whatever happens to the words, they reach the backend with short dashes: a long dash
// is an en dash, and hyphens and en dashes stay. A draft the person already had keeps
// their dashes as they typed them.
func TestLongDashesAreWrittenShort(t *testing.T) {
	t.Parallel()

	const text, want = "soon \u2014 or later \u2015 well-ish \u2013 ok", "soon \u2013 or later \u2013 well-ish \u2013 ok"
	mine := "mine \u2014 as typed"
	cases := []struct {
		name    string
		send    []string
		fail    bool
		drafted string
		outcome string
		wrote   func(*fake) string
	}{
		{"sent", []string{"space:Work"}, false, "", agent.Sent, func(f *fake) string {
			return strings.TrimPrefix(f.sent[0].Body, "!open:x ") // The fake stamps the room.
		}},
		{"queued", []string{"space:Work"}, true, "", agent.Queued, func(f *fake) string { return f.queued[0].Body }},
		{"drafted", nil, false, "", agent.Drafted, func(f *fake) string { return f.drafts["!open:x"].Body }},
		{"appended", nil, false, mine, agent.Drafted, func(f *fake) string {
			return strings.TrimPrefix(f.drafts["!open:x"].Body, mine+"\n\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			f := twoRooms()
			if c.fail {
				f.sendErr = errNoServer
			}
			if c.drafted != "" {
				f.drafts = map[domain.RoomID]domain.StoredDraft{"!open:x": {RoomID: "!open:x", Body: c.drafted}}
			}
			s := newWriter(t, f, shareAll, c.send)
			out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": text})

			if out["action"] != c.outcome {
				t.Fatalf("action = %v, want %q", out["action"], c.outcome)
			}
			if got := c.wrote(f); got != want {
				t.Errorf("wrote %q, want %q", got, want)
			}
			if c.drafted != "" && !strings.HasPrefix(f.drafts["!open:x"].Body, mine+"\n\n") {
				t.Errorf("their draft changed: %q", f.drafts["!open:x"].Body)
			}
			if ledger := entries(t, s); len(ledger) != 1 || ledger[0].Text != want {
				t.Errorf("ledger = %+v, want what was written", ledger)
			}
		})
	}
}

// An edit in progress is the case the rule allows a refusal for: there is no safe merge
// into somebody's correction of a message the room has already read.
func TestEditInProgressIsRefused(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.drafts = map[domain.RoomID]domain.StoredDraft{
		"!open:x": {RoomID: "!open:x", Body: "fixing this", Editing: "$1"},
	}
	s := newWriter(t, f, shareAll, nil)
	said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hello"})

	if !strings.Contains(said, "edit") {
		t.Errorf("refusal = %q, want it to say why", said)
	}
	if f.drafts["!open:x"].Body != "fixing this" {
		t.Errorf("the edit in progress was changed: %q", f.drafts["!open:x"].Body)
	}
}

// A draft nobody is sending is where an append loop would end up, so there is a floor.
func TestAnEndlessDraftIsRefused(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.drafts = map[domain.RoomID]domain.StoredDraft{
		"!open:x": {RoomID: "!open:x", Body: strings.Repeat("x", draftCap+1)},
	}
	s := newWriter(t, f, shareAll, nil)
	if said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "more"}); said == "" {
		t.Fatal("an unbounded draft was appended to")
	}
}

// A send the homeserver will not take goes into the daemon's queue under the same
// transaction ID, which is what makes retrying it safe.
func TestFailedSendIsQueuedUnderTheSameID(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.sendErr = errNoServer
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})

	if out["action"] != agent.Queued {
		t.Fatalf("action = %v, want %q", out["action"], agent.Queued)
	}
	if len(f.queued) != 1 || f.queued[0].TxnID == "" {
		t.Fatalf("queued = %+v, want one message carrying its transaction ID", f.queued)
	}
	// A queued message is on its way, so the rail counts it: a loop that could bypass the
	// cooldown by failing would be the worst kind.
	if _, ever := agent.LastOut(entries(t, s), "!open:x"); !ever {
		t.Error("a queued message was not counted by the cooldown")
	}
}

// When the queue will not take it either, the words come back as a draft — the same last
// resort the client itself falls back to.
func TestUnqueueableSendBecomesADraft(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.sendErr, f.queueErr = errNoServer, errNoServer
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})

	if out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want the words kept as a draft", out["action"])
	}
	if !strings.Contains(f.drafts["!open:x"].Body, "on my way") {
		t.Fatalf("draft = %q, want the message", f.drafts["!open:x"].Body)
	}
}

// The cooldown is read out of the ledger, so a ledger that cannot be written is a rail
// that cannot count — and an unrecordable send is refused rather than made.
func TestNothingIsSentThatCannotBeRecorded(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newWriter(t, f, shareAll, []string{"space:Work"})
	// A directory where the file should be: openable for neither append nor create.
	path := filepath.Join(t.TempDir(), "sends.jsonl")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("blocking the ledger: %v", err)
	}
	blocked, err := agent.Open(path)
	if err != nil {
		t.Fatalf("opening the ledger: %v", err)
	}
	s.ledger = blocked
	said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})

	if len(f.sent) != 0 {
		t.Fatalf("a message went out with no way to record it: %+v", f.sent)
	}
	if !strings.Contains(said, "recorded") {
		t.Errorf("refusal = %q, want it to say the ledger is the reason", said)
	}
}

// A server with no ledger at all refuses to write, and says so rather than failing
// halfway through.
func TestNoLedgerMeansNoWriting(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	s := newServer(f, shareAll)
	s.send = []string{"space:Work"}
	if said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "hi"}); !strings.Contains(said, "ledger") {
		t.Errorf("refusal = %q, want it to name the ledger", said)
	}
	if len(f.sent) != 0 || len(f.drafts) != 0 {
		t.Error("a server with no ledger wrote something")
	}
}

// The name a client gives itself is what a draft and a ledger line are attributed to, and
// it is somebody else's text: one line, bounded, no control characters.
func TestClientNameIsTakenAsText(t *testing.T) {
	t.Parallel()

	s := newWriter(t, twoRooms(), shareAll, nil)
	s.client = ""
	if who := s.whoever(); who != domain.DraftAgent {
		t.Errorf("a client that named itself nothing = %q, want %q", who, domain.DraftAgent)
	}
	s.nameClient("claude\ncode\r", "1.2.3")
	if who := s.whoever(); who != "claude code 1.2.3" {
		t.Errorf("name = %q, want the lines folded into one", who)
	}
	s.nameClient(strings.Repeat("n", 200), "")
	if who := s.whoever(); len([]rune(who)) != clientNameCap {
		t.Errorf("name is %d runes, want it capped at %d", len([]rune(who)), clientNameCap)
	}
}

// errNoServer stands in for the homeserver being unreachable, which is the failure the
// send ladder exists for.
var errNoServer = errTest("no route to host")

type errTest string

func (e errTest) Error() string { return string(e) }

// A write to a withheld room and a write to a room that does not exist look the same
// to the assistant: the same answer, and the same work behind it (one ledger entry
// each), so neither the words nor the time tell them apart. The user still sees which.
func TestAWithheldRoomAndAMissingOneAreRefusedAlike(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	// "Private chat" is encrypted, so [agent.read] withholds it.
	s := newWriter(t, f, shareAll, []string{"dm", "group"})
	hidden := callErr(t, s, "send_message", map[string]any{"room": "Private chat", "text": "psst"})
	missing := callErr(t, s, "send_message", map[string]any{"room": "No Such Room", "text": "psst"})
	if strings.ReplaceAll(hidden, "Private chat", "X") != strings.ReplaceAll(missing, "No Such Room", "X") {
		t.Errorf("the refusals differ:\n withheld: %s\n missing:  %s", hidden, missing)
	}
	ledger := entries(t, s)
	if len(ledger) != 2 {
		t.Fatalf("ledger has %d entries, want one per refused write: %+v", len(ledger), ledger)
	}
	if ledger[0].Room != "!secret:x" || ledger[1].Room != "" || ledger[1].Name != "No Such Room" {
		t.Errorf("ledger = %+v, want the withheld room by ID and the missing one by the name asked", ledger)
	}
}

// The TUI saves what the person types a moment after they type it. An append built on
// the draft read before that save must not overwrite it: it is rebuilt on theirs.
func TestAnAppendKeepsWordsTypedWhileItWasBuilt(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.drafts = map[domain.RoomID]domain.StoredDraft{
		"!open:x": {RoomID: "!open:x", Body: "hi", Updated: time.UnixMilli(1000)},
	}
	f.beforeReplace = func() {
		f.drafts["!open:x"] = domain.StoredDraft{RoomID: "!open:x", Body: "hi there, quick question", Updated: time.UnixMilli(2000)}
	}
	s := newWriter(t, f, shareAll, []string{"room:Private chat"}) // Standup is not on the send list
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "the report is attached"})
	if out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want a draft", out["action"])
	}
	if got, want := f.drafts["!open:x"].Body, "hi there, quick question\n\nthe report is attached"; got != want {
		t.Errorf("draft = %q, want the words typed meanwhile kept, then the append: %q", got, want)
	}
}

// A draft the assistant could not write is recorded like any refused write, whatever
// stopped it: the composer mid-edit, a draft already over the cap, or a draft that
// kept changing under every attempt. The user reads the ledger to see what was tried.
func TestADraftThatCouldNotBeWrittenIsInTheLedger(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		setup func(f *fake)
	}{
		{"mid-edit", func(f *fake) {
			f.drafts = map[domain.RoomID]domain.StoredDraft{
				"!open:x": {RoomID: "!open:x", Body: "fixed", Editing: "$m", EditSaved: "hi"},
			}
		}},
		{"over the cap", func(f *fake) {
			f.drafts = map[domain.RoomID]domain.StoredDraft{
				"!open:x": {RoomID: "!open:x", Body: strings.Repeat("x", draftCap+1)},
			}
		}},
		{"always changing", func(f *fake) {
			n := 0
			var typing func()
			typing = func() {
				n++
				f.store(domain.StoredDraft{RoomID: "!open:x", Body: strings.Repeat("y", n), Updated: time.UnixMilli(int64(n))})
				f.beforeReplace = typing
			}
			f.beforeReplace = typing
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := twoRooms()
			tc.setup(f)
			s := newWriter(t, f, shareAll, nil)
			if said := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "more"}); said == "" {
				t.Fatal("the draft was written")
			}
			ledger := entries(t, s)
			if len(ledger) != 1 || ledger[0].Outcome != agent.Refused || ledger[0].Room != "!open:x" || ledger[0].Text != "more" {
				t.Errorf("ledger = %+v, want the one refused attempt, with its words", ledger)
			}
		})
	}
}

// replyLost commits every draft write and then reports an error, as when the daemon
// restarts after COMMIT; commits false only reports the error.
type replyLost struct {
	*fake
	commits bool
}

func (r replyLost) ReplaceDraft(ctx context.Context, draft, over domain.StoredDraft) (bool, error) {
	if r.commits {
		if _, err := r.fake.ReplaceDraft(ctx, draft, over); err != nil {
			return false, err
		}
	}
	return false, errors.New("unavailable: connection reset")
}

// An append that landed with its answer lost is reported as drafted, and not appended
// again: the words are in the draft once.
func TestADraftWhoseAnswerIsLostIsWrittenOnce(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	f.store(domain.StoredDraft{RoomID: "!open:x", Body: "hi", Updated: time.UnixMilli(1000)})
	s := newWriter(t, f, shareAll, nil)
	s.backend = replyLost{fake: f, commits: true}
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})
	if out["action"] != agent.Drafted {
		t.Fatalf("action = %v, want %q", out["action"], agent.Drafted)
	}
	if got := f.drafts["!open:x"].Body; got != "hi\n\non my way" {
		t.Fatalf("draft = %q, want the words once", got)
	}
}

// A write that did not land is still the error it was.
func TestADraftThatDidNotLandIsAnError(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	s := newWriter(t, f, shareAll, nil)
	s.backend = replyLost{fake: f}
	if got := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"}); !strings.Contains(got, "connection reset") {
		t.Fatalf("send_message said %q, want the write's error", got)
	}
}

// The cap counts the addition: a draft just under it cannot grow past it in one go.
func TestTheDraftCapCountsTheAddition(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	f.store(domain.StoredDraft{RoomID: "!open:x", Body: strings.Repeat("x", draftCap-1)})
	s := newWriter(t, f, shareAll, nil)
	if got := callErr(t, s, "send_message", map[string]any{"room": "Standup", "text": "more words"}); !strings.Contains(got, "with this addition") {
		t.Fatalf("send_message said %q, want the cap refusal", got)
	}
}

func TestLanded(t *testing.T) {
	t.Parallel()
	wrote := domain.StoredDraft{Body: "hi\n\non my way", Author: "bot", Updated: time.UnixMilli(5)}
	for _, c := range []struct {
		stored domain.StoredDraft
		want   bool
	}{
		{wrote, true},
		{domain.StoredDraft{Body: wrote.Body, Author: "bot", Updated: time.UnixMilli(6)}, false},
		{domain.StoredDraft{Body: wrote.Body + " and more"}, true},
		{domain.StoredDraft{Body: wrote.Body + "\n\nlater"}, true},
		{domain.StoredDraft{Body: wrote.Body + "s"}, false},
		{domain.StoredDraft{Body: "hi"}, false},
	} {
		if got := landed(c.stored, wrote); got != c.want {
			t.Errorf("landed(%q) = %v, want %v", c.stored.Body, got, c.want)
		}
	}
}

// A failed send tells the assistant what happened, not the daemon's internals: the
// error text stays in kith-mcp's log.
func TestAFailedSendKeepsTheDaemonErrorOutOfTheReason(t *testing.T) {
	t.Parallel()
	f := twoRooms()
	f.sendErr = errors.New("dial unix /run/user/1000/kith/secret.sock: connection refused")
	s := newWriter(t, f, shareAll, everything)
	s.cooldown = 0
	out := call(t, s, "send_message", map[string]any{"room": "Standup", "text": "on my way"})
	body := fmt.Sprint(out)
	for _, ledgered := range entries(t, s) {
		body += ledgered.Reason
	}
	if strings.Contains(body, "secret.sock") || strings.Contains(body, "dial unix") {
		t.Fatalf("the daemon's error reached the assistant: %s", body)
	}
	if out["action"] != agent.Queued {
		t.Errorf("action = %v, want queued", out["action"])
	}
}

// Two rooms shown under one name are refused, not guessed: a DM peer can take any
// display name (a DM is named after its peer), and the first exact match would post
// there unreviewed.
func TestAnExactNameHeldByTwoRoomsIsRefused(t *testing.T) {
	t.Parallel()
	f := &fake{rooms: []domain.Room{
		{ID: "!impostor:x", Name: "Notes", IsDirect: true},
		{ID: "!notes:x", Name: "Notes"},
	}}
	s := newWriter(t, f, domain.ModelScope{Only: []string{"dm", "group"}}, []string{"room:Notes"})
	s.cooldown = 0
	raw, err := json.Marshal(writeArgs{Room: "Notes", Text: "my salary is ..."})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.sendMessage(withCallMemo(context.Background()), raw); err == nil || !strings.Contains(err.Error(), "matches 2 rooms") {
		t.Fatalf("sendMessage = %v, want it refused as ambiguous", err)
	}
	if len(f.sent) != 0 {
		t.Errorf("sent %+v, want nothing", f.sent)
	}
}

// gatedSend holds each send until the test lets it through.
type gatedSend struct {
	*fake
	arrived chan struct{}
	release chan struct{}
}

func (g gatedSend) Send(ctx context.Context, room domain.RoomID, d domain.Draft) error {
	g.arrived <- struct{}{}
	<-g.release
	return g.fake.Send(ctx, room, d)
}

// Two assistant sessions share one ledger, and so one cooldown: while one decides and
// sends, the other waits, then finds the send recorded and leaves its words as a draft.
func TestTwoSessionsShareOneCooldown(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sends.jsonl")
	arrived, release := make(chan struct{}, 2), make(chan struct{})
	session := func() *server {
		ledger, err := agent.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		return &server{
			backend: gatedSend{fake: scopeRooms(), arrived: arrived, release: release},
			scope:   shareAllEncrypted, send: []string{"group"},
			cooldown: time.Hour, ledger: ledger, client: "test",
		}
	}
	raw, err := json.Marshal(writeArgs{Room: "Standup", Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	outcomes := make(chan string, 2)
	for _, s := range []*server{session(), session()} {
		go func() {
			out, err := s.sendMessage(withCallMemo(context.Background()), raw)
			if err != nil {
				outcomes <- "error: " + err.Error()
				return
			}
			outcomes <- out.(map[string]any)["action"].(string)
		}()
	}
	<-arrived // one session is sending
	select {
	case <-arrived:
		t.Fatal("both sessions reached the send inside one cooldown")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	got := []string{<-outcomes, <-outcomes}
	slices.Sort(got)
	if want := []string{agent.Drafted, agent.Sent}; !slices.Equal(got, want) {
		t.Errorf("outcomes %v, want one sent and one drafted", got)
	}
}
