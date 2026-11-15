package main

import (
	"slices"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func poll(multiple bool, mine ...string) *domain.Poll {
	p := &domain.Poll{Multiple: multiple}
	for _, id := range []string{"a", "b", "c"} {
		p.Options = append(p.Options, domain.PollOption{ID: id, Text: id, Mine: slices.Contains(mine, id)})
	}
	return p
}

// An open poll's buttons are its answers, then Vote where several may be ticked, or Take
// my vote back once one answer is chosen; a closed one has none.
func TestAPollsButtons(t *testing.T) {
	for _, tc := range []struct {
		what string
		p    *domain.Poll
		want []int
	}{
		{"one answer, not voted", poll(false), []int{0, 1, 2}},
		{"one answer, voted", poll(false, "b"), []int{0, 1, 2, pollBack}},
		{"several", poll(true), []int{0, 1, 2, pollVote}},
		{"closed", &domain.Poll{Closed: true, Options: poll(false, "a").Options}, nil},
	} {
		if got := pollButtons(tc.p); !slices.Equal(got, tc.want) {
			t.Errorf("%s: buttons %v, want %v", tc.what, got, tc.want)
		}
	}
}

// One answer is voted by pressing it; several are ticked, starting from this person's
// own, and voted together, none ticked taking the vote back; Vote with nothing ticked or
// chosen sends nothing.
func TestPressingAPoll(t *testing.T) {
	press := func(s *pollState, p *domain.Poll, b int, want []string, wantSend bool) {
		t.Helper()
		got, send := s.press(p, b)
		if send != wantSend || !slices.Equal(got, want) {
			t.Errorf("press %d: %v sent %v, want %v sent %v", b, got, send, want, wantSend)
		}
	}
	var s pollState
	press(&s, poll(false), 1, []string{"b"}, true)
	press(&s, poll(false, "b"), pollBack, nil, true)

	several := poll(true, "a")
	press(&s, several, pollVote, []string{"a"}, true) // as it stands
	press(&s, several, 2, nil, false)
	press(&s, several, 0, nil, false)
	if !s.ticked(several, "c") || s.ticked(several, "a") {
		t.Errorf("ticks %v, want c alone", s.ticks)
	}
	press(&s, several, pollVote, []string{"c"}, true)
	if s.ticks != nil {
		t.Errorf("voted: ticks %v kept, want this person's own again", s.ticks)
	}
	press(&s, several, 0, nil, false) // a unticked: none
	press(&s, several, pollVote, nil, true)

	press(&s, poll(true), pollVote, nil, false)
}
