package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// queueStub is the daemon's send queue, as far as the client can see it.
type queueStub struct {
	added []domain.ScheduledMessage
	fail  error
}

func (q *queueStub) Schedule(_ context.Context, msg domain.ScheduledMessage) (string, error) {
	if q.fail != nil {
		return "", q.fail
	}
	q.added = append(q.added, msg)
	return "id-1", nil
}

func (q *queueStub) ScheduledMessages(context.Context) ([]domain.ScheduledMessage, error) {
	return q.added, nil
}

func (q *queueStub) CancelScheduled(context.Context, string) error { return nil }
func (q *queueStub) RetryScheduled(context.Context, string) error  { return nil }

// failing builds a client whose sends fail, with a queue behind it.
func failing(t *testing.T) (Model, *queueStub) {
	t.Helper()
	queue := &queueStub{}
	m := jumping(t).WithSchedules(queue)
	return openedFromList(t, m, "!a:x"), queue
}

func TestAFailedSendIsQueued(t *testing.T) {
	t.Parallel()

	m, queue := failing(t)
	m, cmd := routed(t, m, sentMsg{
		err:    errors.New("dial tcp: connection refused"),
		roomID: "!a:x",
		draft: domain.Draft{
			Body:     "the words that must not be lost",
			ReplyTo:  "$their-message",
			Mentions: []domain.Mention{{UserID: "@dana:x", Name: "Dana"}},
		},
	})
	m = settle(t, m, cmd)

	if len(queue.added) != 1 {
		t.Fatalf("the queue holds %d entries, want the failed send", len(queue.added))
	}
	held := queue.added[0]
	if held.Body != "the words that must not be lost" || held.RoomID != "!a:x" {
		t.Errorf("queued %+v, want the message that failed", held)
	}
	// Queued as composed: a dropped reply target or pill would change the message.
	if held.ReplyTo != "$their-message" {
		t.Errorf("queued reply target = %q, want it kept", held.ReplyTo)
	}
	if len(held.Mentions) != 1 || held.Mentions[0].UserID != "@dana:x" {
		t.Errorf("queued mentions = %+v, want Dana's pill kept", held.Mentions)
	}
	// Due now: the scheduler sends it at the next pass, and it counts as unsent, not scheduled.
	if !held.Immediate() {
		t.Error("the queued entry reads as scheduled for later, not as unsent")
	}
	if !strings.Contains(m.status(), "queued") {
		t.Errorf("status = %q, want it to say the message was queued", m.status())
	}
}

// When the queue can't take it either (no daemon), the words return as the room's draft.
func TestAnUnqueueableSendGoesBackToTheDraft(t *testing.T) {
	t.Parallel()

	m, queue := failing(t)
	queue.fail = errors.New("daemon is not listening")
	m, cmd := routed(t, m, sentMsg{
		err:    errors.New("connection refused"),
		roomID: "!a:x",
		draft:  domain.Draft{Body: "keep me", ReplyTo: "$theirs"},
	})
	m = settle(t, m, cmd)

	if m.compose.input != "keep me" {
		t.Errorf("composer holds %q, want the words back", m.compose.input)
	}
	if m.compose.replyTo != "$theirs" {
		t.Errorf("reply target = %q, want it back with the words", m.compose.replyTo)
	}
	if !strings.Contains(m.status(), "draft") {
		t.Errorf("status = %q, want it to say where the words went", m.status())
	}
}

// A failed send for a room since left goes to that room's draft, not the one on screen.
func TestAnUnqueueableSendReachesTheRightRoom(t *testing.T) {
	t.Parallel()

	m, queue := failing(t)
	queue.fail = errors.New("no daemon")
	m = openedFromList(t, m, "!ops:x") // moved on before the failure lands
	m, cmd := routed(t, m, sentMsg{
		err:    errors.New("connection refused"),
		roomID: "!a:x",
		draft:  domain.Draft{Body: "for Alpha"},
	})
	m = settle(t, m, cmd)

	if m.compose.input != "" {
		t.Errorf("the open room's composer holds %q, want it untouched", m.compose.input)
	}
	if !m.hasDraft("!a:x") {
		t.Fatal("the words did not reach the room they were written for")
	}
	if _, ok := findGroup(m.rail.groups, draftsGroupKey); !ok {
		t.Error("the Drafts group does not show the room now holding words")
	}
	back := openedFromList(t, m, "!a:x")
	if back.compose.input != "for Alpha" {
		t.Errorf("Alpha's draft = %q, want the unsent words", back.compose.input)
	}
}

// Text typed since the failure is kept too, visibly separated.
func TestAnUnqueueableSendKeepsWhatWasTypedSince(t *testing.T) {
	t.Parallel()

	m, queue := failing(t)
	queue.fail = errors.New("no daemon")
	m = typeInto(t, m, "typed while it was failing")
	m, cmd := routed(t, m, sentMsg{
		err:    errors.New("connection refused"),
		roomID: "!a:x",
		draft:  domain.Draft{Body: "the earlier message"},
	})
	m = settle(t, m, cmd)

	if !strings.Contains(m.compose.input, "the earlier message") ||
		!strings.Contains(m.compose.input, "typed while it was failing") {
		t.Errorf("composer = %q, want both kept", m.compose.input)
	}
	if strings.Index(m.compose.input, "the earlier message") >
		strings.Index(m.compose.input, "typed while it was failing") {
		t.Errorf("composer = %q, want the earlier message first", m.compose.input)
	}
}

// An edit is not queued: a late replacement is worse than a failed one.
func TestAFailedEditIsNotQueued(t *testing.T) {
	t.Parallel()

	m, queue := failing(t)
	m, cmd := routed(t, m, sentMsg{
		err:    errors.New("connection refused"),
		roomID: "!a:x",
		draft:  domain.Draft{Body: "corrected", Edits: "$original"},
	})
	m = settle(t, m, cmd)

	if len(queue.added) != 0 {
		t.Errorf("queued %+v, want an edit left alone", queue.added)
	}
	if !strings.Contains(m.status(), "send failed") {
		t.Errorf("status = %q, want the failure reported", m.status())
	}
}

// routed routes a message through Update and returns its command, so a test can run
// the follow-up as the event loop would.
func routed(t *testing.T, m Model, msg any) (Model, tea.Cmd) {
	t.Helper()
	mdl, cmd := asModel(m.Update(msg))
	got := mdl
	return got, cmd
}

// "Send failed" doesn't mean "was not sent", so a retry keeps the transaction ID.
func TestAQueuedRetryKeepsTheTransactionID(t *testing.T) {
	t.Parallel()

	m, queue := failing(t)
	m, cmd := routed(t, m, sentMsg{
		err:    errors.New("connection reset by peer"),
		roomID: "!a:x",
		draft:  domain.Draft{Body: "sent, maybe", TxnID: "kith-abc123"},
	})
	settle(t, m, cmd)

	if len(queue.added) != 1 {
		t.Fatalf("queued %d messages, want the failed send", len(queue.added))
	}
	if queue.added[0].TxnID != "kith-abc123" {
		t.Errorf("queued transaction ID = %q, want the one the send used — a fresh one "+
			"would deliver a second copy of a message that may already have arrived",
			queue.added[0].TxnID)
	}
}

// Every composed message gets one, or the retry has nothing to carry.
func TestEveryComposedMessageCarriesATransactionID(t *testing.T) {
	t.Parallel()

	first, second := newTxnID(), newTxnID()
	if first == "" || second == "" {
		t.Fatal("no transaction ID was generated")
	}
	if first == second {
		t.Error("two sends got the same transaction ID — the homeserver would drop one")
	}
	if !strings.HasPrefix(first, "kith-") {
		t.Errorf("id = %q, want it recognizable in a homeserver log", first)
	}
}
