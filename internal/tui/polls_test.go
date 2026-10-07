package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// voteBackend records votes.
type voteBackend struct {
	apitest.Nop
	mu    sync.Mutex
	votes [][]string
}

func (b *voteBackend) VotePoll(_ context.Context, _ domain.RoomID, _ domain.EventID, options []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.votes = append(b.votes, options)
	return nil
}

// withPoll is a model whose room holds one poll message, selected.
func withPoll(t *testing.T, b *voteBackend, p domain.Poll) Model {
	t.Helper()
	m := sized(t, withRooms(t, starterNew(b, config.Display{})))
	m.focus = paneTimeline
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$poll", RoomID: "!a:x", Sender: "@dana:x", Timestamp: at(1), Body: p.Summary(), Poll: &p},
	}}})
	m.timeline.selected = "$poll"
	return m
}

func hikePoll() domain.Poll {
	return domain.Poll{Question: "Hike when?", Options: []domain.PollOption{
		{ID: "sat", Text: "Saturday", Votes: 3, Mine: true}, {ID: "sun", Text: "Sunday", Votes: 1},
	}}
}

// A poll draws its question and each answer with its votes, this person's marked.
func TestAPollDrawsItsAnswersAndTheVote(t *testing.T) {
	t.Parallel()
	p := hikePoll()
	body := pollBody(&p)
	for _, want := range []string{"📊 Hike when?", "● Saturday — 3 votes · 75%", "○ Sunday — 1 vote · 25%"} {
		if !strings.Contains(body, want) {
			t.Errorf("poll body lacks %q:\n%s", want, body)
		}
	}
	p.Multiple = true
	if !strings.Contains(pollBody(&p), "(choose any)") {
		t.Error("a poll taking several answers does not say so")
	}
}

// The vote key offers the answers: one is chosen and voted; a voted poll offers taking
// the vote back; a poll taking several answers votes what is ticked; a closed poll
// says so and offers nothing.
func TestVotingInAPoll(t *testing.T) {
	t.Parallel()
	b := &voteBackend{}
	m := withPoll(t, b, hikePoll())
	m, _ = m.openVote()
	if m.picker.kind != pickerVote || len(m.picker.all) != 3 || m.picker.all[2].value != voteTakeBack {
		t.Fatalf("picker = %v %+v, want the answers and taking the vote back", m.picker.kind, m.picker.all)
	}
	m.picker.cursor = 1 // Sunday
	next, cmd := m.acceptPick()
	if cmd == nil {
		t.Fatal("choosing an answer sent no vote")
	}
	cmd()
	m = next
	m, _ = m.openVote()
	m.picker.cursor = 2 // take back
	_, cmd = m.acceptPick()
	cmd()

	multi := hikePoll()
	multi.Multiple = true
	m = withPoll(t, b, multi)
	m, _ = m.openVote()
	if m.picker.kind != pickerVoteMulti || !m.picker.ticked("sat") || m.picker.ticked("sun") {
		t.Fatalf("multi picker = %v, ticked %v", m.picker.kind, m.picker.checked)
	}
	m.picker.checked = map[string]bool{"sat": true, "sun": true}
	_, cmd = m.acceptPick()
	cmd()

	b.mu.Lock()
	want := [][]string{{"sun"}, nil, {"sat", "sun"}}
	if !slices.EqualFunc(b.votes, want, slices.Equal[[]string]) {
		t.Errorf("votes = %q, want %q", b.votes, want)
	}
	b.mu.Unlock()

	closed := hikePoll()
	closed.Closed = true
	m = withPoll(t, b, closed)
	m, _ = m.openVote()
	if m.picker.active() || !strings.Contains(m.status(), "closed") {
		t.Errorf("a closed poll: picker %v, said %q", m.picker.active(), m.status())
	}
}
