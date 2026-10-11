package domain

import (
	"strconv"
	"strings"
)

// Poll is a message's poll and its votes as the network last reported them.
type Poll struct {
	// ID is the network's handle for the poll, named by its results updates.
	ID       string
	Question string
	Options  []PollOption
	// Multiple lets more than one answer be chosen; Closed takes no more votes; Quiz
	// has one right answer.
	Multiple, Closed, Quiz bool
	// Voters is how many have voted, when the network says.
	Voters int
	// Ballots is each voter's answer IDs, where the network reports individual votes
	// (WhatsApp); Retally counts them.
	Ballots map[string][]string `json:",omitempty"`
}

// Retally recounts votes, voters and Mine from Ballots.
func (p *Poll) Retally(selves func(voter string) bool) {
	count := map[string]int{}
	mine := map[string]bool{}
	voters := 0
	for voter, chosen := range p.Ballots {
		if len(chosen) == 0 {
			continue
		}
		voters++
		for _, id := range chosen {
			count[id]++
			if selves(voter) {
				mine[id] = true
			}
		}
	}
	for i := range p.Options {
		p.Options[i].Votes, p.Options[i].Mine = count[p.Options[i].ID], mine[p.Options[i].ID]
	}
	p.Voters = voters
}

// PollOption is one answer of a poll.
type PollOption struct {
	// ID is the network's handle for the answer, which a vote names.
	ID   string
	Text string
	// Votes is how many chose it; Mine whether this person did.
	Votes int
	Mine  bool
}

// Voted reports whether this person has voted.
func (p *Poll) Voted() bool {
	for _, o := range p.Options {
		if o.Mine {
			return true
		}
	}
	return false
}

// Summary is the question and answers on one line, for search, notifications and quotes.
func (p *Poll) Summary() string {
	answers := make([]string, len(p.Options))
	for i, o := range p.Options {
		answers[i] = o.Text
	}
	return "📊 " + p.Question + " (" + strings.Join(answers, " / ") + ")"
}

// Share is how much of the vote an answer has, in whole percent; 0 with no votes.
func (p *Poll) Share(o PollOption) int {
	total := 0
	for _, x := range p.Options {
		total += x.Votes
	}
	if total == 0 {
		return 0
	}
	return o.Votes * 100 / total
}

// Tally is an answer's votes as words: "3 votes · 25%".
func (p *Poll) Tally(o PollOption) string {
	word := "votes"
	if o.Votes == 1 {
		word = "vote"
	}
	return strconv.Itoa(o.Votes) + " " + word + " · " + strconv.Itoa(p.Share(o)) + "%"
}

// PollVoters is who chose what in a poll, as far as its network names them.
type PollVoters struct {
	Voters []PollVoter
	// Hidden is why the network names no one, PollNamed when it names them.
	Hidden PollHiding
}

// PollVoter is one person's vote in a poll: who, and the answers they chose by ID.
type PollVoter struct {
	ID string
	// Name is what the network calls them, "" where it gives no name.
	Name    string
	Options []string
}

// PollHiding is why a poll's network names none of its voters.
type PollHiding int

// The reasons a poll's voters go unnamed.
const (
	// PollNamed: the network names them.
	PollNamed PollHiding = iota
	// PollAnonymous: the poll was made anonymous.
	PollAnonymous
	// PollVoteFirst: the network names them only to those who have voted.
	PollVoteFirst
)
