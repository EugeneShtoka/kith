package matrix

import (
	"reflect"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func TestPollStartMessage(t *testing.T) {
	t.Parallel()
	evt := &event.Event{
		ID:        id.EventID("$poll:x"),
		RoomID:    id.RoomID("!r:x"),
		Sender:    id.UserID("@ann:x"),
		Type:      event.EventUnstablePollStart,
		Timestamp: 1700000000000,
		Content: event.Content{Parsed: &event.PollStartEventContent{
			PollStart: event.PollStart{
				MaxSelections: 1,
				Question:      event.MSC1767Message{Text: "lunch?"},
				Answers: []event.PollOption{
					{ID: "0", MSC1767Message: event.MSC1767Message{Text: "pizza"}},
					{ID: "1", MSC1767Message: event.MSC1767Message{Text: "ramen"}},
				},
			},
		}},
	}
	msg, ok := (ids{}).pollStartMessage(evt)
	if !ok {
		t.Fatal("pollStartMessage returned ok=false")
	}
	if msg.ID != "$poll:x" || msg.RoomID != "!r:x" || msg.Sender != "@ann:x" {
		t.Errorf("message identity wrong: %+v", msg)
	}
	if msg.Poll == nil {
		t.Fatal("Poll was nil")
	}
	if msg.Poll.Question != "lunch?" || len(msg.Poll.Options) != 2 {
		t.Errorf("poll contents wrong: %+v", msg.Poll)
	}
	if msg.Poll.Multiple {
		t.Error("single-choice poll read as multiple")
	}
	if msg.Body != "📊 lunch?" {
		t.Errorf("body = %q, want 📊 lunch?", msg.Body)
	}
}

func TestPollStartMessageMultiple(t *testing.T) {
	t.Parallel()
	evt := &event.Event{
		ID:   id.EventID("$p:x"),
		Type: event.EventUnstablePollStart,
		Content: event.Content{Parsed: &event.PollStartEventContent{
			PollStart: event.PollStart{
				MaxSelections: 2,
				Question:      event.MSC1767Message{Text: "pick two"},
				Answers: []event.PollOption{
					{ID: "a", MSC1767Message: event.MSC1767Message{Text: "a"}},
					{ID: "b", MSC1767Message: event.MSC1767Message{Text: "b"}},
					{ID: "c", MSC1767Message: event.MSC1767Message{Text: "c"}},
				},
			},
		}},
	}
	msg, ok := (ids{}).pollStartMessage(evt)
	if !ok {
		t.Fatal("ok=false")
	}
	if !msg.Poll.Multiple {
		t.Error("multiple-choice poll read as single")
	}
}

func TestPollStartMessageUnparsedIsSkipped(t *testing.T) {
	t.Parallel()
	evt := &event.Event{Type: event.EventUnstablePollStart, Content: event.Content{}}
	if _, ok := (ids{}).pollStartMessage(evt); ok {
		t.Error("unparsed poll start was accepted")
	}
}

func TestApplyResponseSingleChoice(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{
		Options: []domain.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Ballots: map[string][]string{},
	}
	applyResponse(p, "@ann:x", []string{"a"}, 100, isSelfNone)
	if got := p.Ballots["@ann:x"]; !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("ann voted a, got %v", got)
	}
	// Later vote replaces the earlier one.
	applyResponse(p, "@ann:x", []string{"b"}, 200, isSelfNone)
	if got := p.Ballots["@ann:x"]; !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("ann changed to b, got %v", got)
	}
	// Single-choice: a response naming two answers keeps only the first valid one.
	applyResponse(p, "@bob:x", []string{"c", "a"}, 100, isSelfNone)
	if got := p.Ballots["@bob:x"]; !reflect.DeepEqual(got, []string{"c"}) {
		t.Errorf("single-choice overflow, got %v", got)
	}
	// Empty answers retracts.
	applyResponse(p, "@ann:x", nil, 300, isSelfNone)
	if _, held := p.Ballots["@ann:x"]; held {
		t.Error("empty answers did not retract ann's vote")
	}
}

func TestApplyResponseMultipleChoice(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{
		Multiple: true,
		Options:  []domain.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Ballots:  map[string][]string{},
	}
	applyResponse(p, "@ann:x", []string{"a", "b"}, 100, isSelfNone)
	if got := p.Ballots["@ann:x"]; !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("ann picked two, got %v", got)
	}
	// Duplicates and unknowns are ignored.
	applyResponse(p, "@bob:x", []string{"a", "a", "z", "c"}, 100, isSelfNone)
	if got := p.Ballots["@bob:x"]; !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("bob deduped+unknown, got %v", got)
	}
}

func TestApplyResponseRetallies(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{
		Options: []domain.PollOption{{ID: "a"}, {ID: "b"}},
		Ballots: map[string][]string{},
	}
	applyResponse(p, "@ann:x", []string{"a"}, 100, func(id string) bool { return id == "@ann:x" })
	applyResponse(p, "@bob:x", []string{"a"}, 100, func(id string) bool { return id == "@ann:x" })
	applyResponse(p, "@cat:x", []string{"b"}, 100, func(id string) bool { return id == "@ann:x" })
	if p.Voters != 3 {
		t.Errorf("Voters = %d, want 3", p.Voters)
	}
	if p.Options[0].Votes != 2 || p.Options[1].Votes != 1 {
		t.Errorf("votes = %+v", p.Options)
	}
	if !p.Options[0].Mine {
		t.Error("ann's a-vote was not counted as mine")
	}
	if p.Options[1].Mine {
		t.Error("b was counted as mine without any own vote")
	}
}

func TestApplyResponseIgnoresStaleRound(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{
		Options: []domain.PollOption{{ID: "a"}, {ID: "b"}},
		Ballots: map[string][]string{},
		Rounds:  map[string]uint32{},
	}
	applyResponse(p, "@ann:x", []string{"b"}, 300, isSelfNone)
	// A stale backfill arrives for ann with an older round: the kept b must survive.
	applyResponse(p, "@ann:x", []string{"a"}, 100, isSelfNone)
	if got := p.Ballots["@ann:x"]; !reflect.DeepEqual(got, []string{"b"}) {
		t.Errorf("stale round should not overwrite; got %v", got)
	}
	if p.Rounds["@ann:x"] != 300 {
		t.Errorf("round regressed to %d", p.Rounds["@ann:x"])
	}
}

func TestPollRoundBounds(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		in   int64
		want uint32
	}{
		"zero":                 {0, 1},
		"negative":             {-1, 1},
		"sub-second":           {500, 1},
		"ordinary":             {1700000000000, 1700000000},
		"overflows to max":     {int64(^uint32(0))*1000 + 2000, ^uint32(0)},
		"exactly max fits":     {int64(^uint32(0)) * 1000, ^uint32(0)},
		"second-boundary +999": {1699999999999, 1699999999},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := pollRound(tc.in); got != tc.want {
				t.Errorf("pollRound(%d) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestClonePollIndependent(t *testing.T) {
	t.Parallel()
	src := &domain.Poll{
		Options: []domain.PollOption{{ID: "a", Votes: 1}},
		Ballots: map[string][]string{"@ann:x": {"a"}},
		Rounds:  map[string]uint32{"@ann:x": 5},
	}
	out := clonePoll(src)
	out.Ballots["@bob:x"] = []string{"a"}
	out.Rounds["@bob:x"] = 7
	out.Options[0].Votes = 99
	if _, held := src.Ballots["@bob:x"]; held {
		t.Error("clone's Ballots write reached the source")
	}
	if _, held := src.Rounds["@bob:x"]; held {
		t.Error("clone's Rounds write reached the source")
	}
	if src.Options[0].Votes != 1 {
		t.Error("clone's Options write reached the source")
	}
}

func TestApplyEventsLatestWinsAndEndStops(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{Options: []domain.PollOption{{ID: "a"}, {ID: "b"}}, Ballots: map[string][]string{}}
	starter := "@ann:x"
	events := []*event.Event{
		pollResponseEvent("$r1", "@bob:x", 100, []string{"a"}),
		pollResponseEvent("$r2", "@bob:x", 300, []string{"b"}),
		pollEndEvent("$end", starter, 200), // ends at 200: bob's 300 is dropped
	}
	(ids{}).applyEvents(p, starter, events, isSelfNone)
	if got := p.Ballots["@bob:x"]; !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("bob's a (pre-end) should win, got %v", got)
	}
	if !p.Closed {
		t.Error("poll should be closed")
	}
}

func TestApplyEventsEndRequiresStarter(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{Options: []domain.PollOption{{ID: "a"}}, Ballots: map[string][]string{}}
	events := []*event.Event{pollEndEvent("$x", "@mallory:x", 100)}
	(ids{}).applyEvents(p, "@ann:x", events, isSelfNone)
	if p.Closed {
		t.Error("a non-starter's end closed the poll")
	}
}

func TestPollValidAnswersPreservesOrder(t *testing.T) {
	t.Parallel()
	p := &domain.Poll{Options: []domain.PollOption{{ID: "a"}, {ID: "b"}, {ID: "c"}}}
	got := pollValidAnswers(p, []string{"c", "b", "a"}, 3)
	if !reflect.DeepEqual(got, []string{"c", "b", "a"}) {
		t.Errorf("order lost, got %v", got)
	}
}

func TestPollBackfillClaimDedupes(t *testing.T) {
	t.Parallel()
	var b pollBackfill
	if !b.claim("$a") {
		t.Error("first claim should succeed")
	}
	if b.claim("$a") {
		t.Error("second claim for same id should be refused")
	}
	if !b.claim("$b") {
		t.Error("different id should claim")
	}
}

func TestPollResponseTargetFromParsed(t *testing.T) {
	t.Parallel()
	evt := &event.Event{Content: event.Content{Parsed: &event.PollResponseEventContent{
		RelatesTo: event.RelatesTo{Type: event.RelReference, EventID: id.EventID("$start")},
	}}}
	if got := (ids{}).pollResponseTarget(evt); got != "$start" {
		t.Errorf("target = %q, want $start", got)
	}
}

func TestPollResponseTargetFromRaw(t *testing.T) {
	t.Parallel()
	evt := &event.Event{Content: event.Content{Raw: map[string]any{
		"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$start"},
	}}}
	if got := (ids{}).pollResponseTarget(evt); got != "$start" {
		t.Errorf("target = %q, want $start", got)
	}
}

// isSelfNone is the "no one is self" predicate for pure-logic tests.
func isSelfNone(string) bool { return false }

// pollResponseEvent is a minimal m.poll.response for applyEvents tests.
func pollResponseEvent(evtID, sender string, ts int64, answers []string) *event.Event {
	return &event.Event{
		ID:        id.EventID(evtID),
		Sender:    id.UserID(sender),
		Type:      event.EventUnstablePollResponse,
		Timestamp: ts,
		Content: event.Content{Parsed: &event.PollResponseEventContent{
			RelatesTo: event.RelatesTo{Type: event.RelReference, EventID: id.EventID("$start")},
			Response:  event.PollResponse{Answers: answers},
		}},
	}
}

// pollEndEvent is a minimal m.poll.end for applyEvents tests. It carries raw content
// since mautrix registers no decoded type for it.
func pollEndEvent(evtID, sender string, ts int64) *event.Event {
	return &event.Event{
		ID:        id.EventID(evtID),
		Sender:    id.UserID(sender),
		Type:      event.EventUnstablePollEnd,
		Timestamp: ts,
		Content: event.Content{Raw: map[string]any{
			"m.relates_to": map[string]any{"rel_type": "m.reference", "event_id": "$start"},
		}},
	}
}
