package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// voteBackend records votes and answers who voted.
type voteBackend struct {
	apitest.Nop
	mu     sync.Mutex
	votes  [][]string
	voters domain.PollVoters
}

func (b *voteBackend) PollVoters(context.Context, domain.RoomID, domain.EventID) (domain.PollVoters, error) {
	return b.voters, nil
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
// opens to be read, and votes nothing.
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
	if !m.picker.active() || slices.ContainsFunc(m.picker.all, func(i pickerItem) bool { return i.value == voteTakeBack }) {
		t.Fatalf("a closed poll: picker %v %+v, want its answers alone", m.picker.active(), m.picker.all)
	}
	m, cmd = m.acceptPick()
	if cmd != nil || !strings.Contains(m.status(), "closed") {
		t.Errorf("voting in a closed poll: cmd %v, said %q", cmd != nil, m.status())
	}
	b.mu.Lock()
	if len(b.votes) != 3 {
		t.Errorf("a closed poll took a vote: %q", b.votes)
	}
	b.mu.Unlock()
}

// The timeline draws a poll's counts alone; the vote key reads who voted and says
// under the answers who chose each: this person as "you" first, others as the sender
// column would or, for someone kith does not know, as the network called them, and
// how many more the network did not name. An answer nobody chose adds nothing.
func TestThePollPickerNamesWhoChoseEachAnswer(t *testing.T) {
	t.Parallel()
	p := hikePoll()
	p.Options = append(p.Options, domain.PollOption{ID: "mon", Text: "Monday"})
	b := &voteBackend{voters: domain.PollVoters{Voters: []domain.PollVoter{
		{ID: "@me:x", Options: []string{"sat"}},
		{ID: "@eli:x", Options: []string{"sat", "sun"}},
		{ID: "@zoe:x", Name: "Zoe Stranger", Options: []string{"sun"}},
	}}}
	p.Options[1].Votes = 2
	m := withPoll(t, b, p)
	m.selves = []string{"@me:x"}
	msg, _ := m.selectedMessage()
	if body, _ := m.plainBody(msg, nil); strings.Count(body, "\n") != len(p.Options) {
		t.Errorf("the timeline names voters:\n%s", body)
	}

	m, cmd := m.openVote()
	if got := notes(m); !slices.Contains(got, "reading who voted…") {
		t.Errorf("before the answer, notes = %q", got)
	}
	read, ok := msgOf[pollVotersMsg](t, cmd)
	if !ok {
		t.Fatal("opening the poll read no voters")
	}
	m = update(t, m, read)
	want := []string{"", "Saturday: you, eli, 1 more", "Sunday: Zoe Stranger, eli"}
	if got := notes(m); !slices.Equal(got, want) {
		t.Errorf("notes = %q, want %q", got, want)
	}
}

// Why no one is named is said; an answer for another poll, or once the picker has
// closed, is not kept.
func TestThePollPickerSaysWhyNoOneIsNamed(t *testing.T) {
	t.Parallel()
	for hidden, want := range map[domain.PollHiding]string{
		domain.PollAnonymous: "an anonymous poll: no one who voted is named",
		domain.PollVoteFirst: "who voted is shown once you have voted",
	} {
		m := withPoll(t, &voteBackend{voters: domain.PollVoters{Hidden: hidden}}, hikePoll())
		m, cmd := m.openVote()
		read, _ := msgOf[pollVotersMsg](t, cmd)
		m = update(t, m, read)
		if got := notes(m); !slices.Contains(got, want) {
			t.Errorf("hidden %v: notes = %q, want %q", hidden, got, want)
		}
	}

	b := &voteBackend{voters: domain.PollVoters{Voters: []domain.PollVoter{{ID: "@eli:x", Options: []string{"sat"}}}}}
	m := withPoll(t, b, hikePoll())
	m, cmd := m.openVote()
	read, _ := msgOf[pollVotersMsg](t, cmd)
	stale := read
	stale.event = "$other"
	if m = update(t, m, stale); m.voters.read {
		t.Error("an answer for another poll was kept")
	}
	m = m.closePicker()
	if m = update(t, m, read); m.voters.read {
		t.Error("an answer after the picker closed was kept")
	}
}

// notes is what the open picker says beneath its list, plain.
func notes(m Model) []string {
	var out []string
	for _, line := range m.pickerNotes(m.pickerWidth()) {
		out = append(out, strings.TrimRight(stripIsolates(ansi.Strip(line)), " "))
	}
	return out
}
