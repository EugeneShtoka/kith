package domain

import (
	"strconv"
	"strings"
)

// Poll is a question a message asks, the answers to choose among, and how the votes
// stand as the network last said.
type Poll struct {
	// ID is the network's own handle for the poll, which its results updates name.
	ID       string
	Question string
	Options  []PollOption
	// Multiple lets more than one answer be chosen; Closed takes no more votes; Quiz
	// has one right answer.
	Multiple, Closed, Quiz bool
	// Voters is how many have voted, when the network says.
	Voters int
	// Ballots is each voter's answers by ID, where the network tells votes one by one
	// (WhatsApp): the votes are counted from them (Retally).
	Ballots map[string][]string `json:",omitempty"`
}

// Retally counts the votes from the ballots: each answer's votes, how many voted, and
// whether this person (any of selves) chose it.
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

// Summary is the poll as one line of words: its question and answers, for search,
// notifications and a quote.
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
