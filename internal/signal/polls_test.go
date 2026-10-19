package signal_test

import (
	"context"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const pollSent = 1700000000100

// asked is Dana's poll in the group, as it first arrives and as it arrives again. A poll
// has no body; this one has words only for the rig, which logs acks by them (kith keeps
// the poll's summary as its body either way).
func asked() *signalv1.EventsResponse {
	ev := inGroup(msg(groupID, dana, pollSent, "poll"))
	ev.GetMessage().Poll = &signalv1.Poll{Question: "Trail?", Multiple: true, Options: []string{"north", "south", "east"}}
	return ev
}

// answers is a vote's answer IDs as one string, "0,2".
func answers(indexes []uint32) string {
	var out []string
	for _, i := range indexes {
		out = append(out, fmt.Sprint(i))
	}
	return strings.Join(out, ",")
}

func vote(sender string, round uint32, options ...uint32) *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_Vote{Vote: &signalv1.Vote{
		Chat: groupID, Group: true, Sender: sender, TargetAuthor: dana, TargetSent: pollSent, Options: options, Round: round,
	}}}
}

// archived is the poll as the phone's history has it: open, with each voter's latest
// vote as the phone knew it, older or newer than what came live.
func archived() *signalv1.EventsResponse {
	ev := asked()
	ev.GetMessage().Poll.Ballots = []*signalv1.Ballot{
		{Voter: dana, Options: []uint32{0}, Round: 1},
		{Voter: ofer, Options: []uint32{1}, Round: 3},
	}
	return historyOf(groupID, true, 0, ev)
}

func ended() *signalv1.EventsResponse {
	return &signalv1.EventsResponse{Event: &signalv1.EventsResponse_PollEnd{PollEnd: &signalv1.PollEnd{
		Chat: groupID, Group: true, Sender: dana, TargetSent: pollSent,
	}}}
}

var hikers = domain.RoomID("signal:" + me + "/" + "-_8BAgMEBQ")

func (r *rig) poll() *domain.Poll {
	r.t.Helper()
	msg, ok, err := r.cache.MessageByID(context.Background(), hikers, domain.EventID(fmt.Sprintf("%s/%s.%d", hikers, dana, pollSent)))
	if err != nil || !ok || msg.Poll == nil {
		r.t.Fatalf("the poll is not cached: %+v, %v", msg, err)
	}
	return msg.Poll
}

// Whatever order a poll's votes arrive in after it, each voter's highest-numbered vote
// stands: a late older vote, a vote played twice, the poll arriving again with no votes
// and the phone's history with older and newer ones change nothing else; taking a vote
// back is a vote; the author's end closes it. The account's own vote (from its phone)
// counts as its own.
func TestAPollsVotesStandByTheirNumberWhateverTheirOrder(t *testing.T) {
	after := []*signalv1.EventsResponse{
		vote(dana, 1, 0), vote(dana, 2, 1, 2), vote(dana, 2, 1, 2),
		vote(ofer, 1, 2), vote(ofer, 2),
		vote(me, 1, 0),
		ended(), asked(), archived(),
	}
	orders := [][]int{
		{0, 1, 2, 3, 4, 5, 6, 7, 8}, // as sent
		{8, 7, 6, 5, 4, 3, 2, 1, 0}, // reversed
		{1, 0, 4, 3, 7, 8, 6, 5, 2}, // each voter's newer first, copies between
	}
	rnd := rand.New(rand.NewPCG(1, 2))
	for range 12 {
		orders = append(orders, rnd.Perm(len(after)))
	}
	want := map[string][]string{"signal:" + dana: {"1", "2"}, "signal:" + ofer: {"1"}, "signal:" + me: {"0"}}
	for _, order := range orders {
		events := []*signalv1.EventsResponse{asked()}
		for _, i := range order {
			events = append(events, after[i])
		}
		r := connected(t, events...)
		p := r.poll()
		if !maps.EqualFunc(p.Ballots, want, slices.Equal[[]string]) {
			t.Errorf("order %v: ballots %v, want %v", order, p.Ballots, want)
		}
		votes := []int{p.Options[0].Votes, p.Options[1].Votes, p.Options[2].Votes}
		if !slices.Equal(votes, []int{1, 2, 1}) || p.Voters != 3 || !p.Options[0].Mine || p.Options[1].Mine {
			t.Errorf("order %v: tally %v, voters %d, options %+v", order, votes, p.Voters, p.Options)
		}
		if !p.Closed {
			t.Errorf("order %v: the poll its author ended is open", order)
		}
	}
}

// Voting sends the answers' places, numbered after the account's last vote, and counts
// at once; a second answer in a one-answer poll, an answer it lacks and an ended poll
// are refused; who chose what names each voter.
func TestVotingInAPoll(t *testing.T) {
	single := asked()
	single.GetMessage().Poll.Multiple = false
	r := connected(t, single, vote(me, 4, 2), vote(dana, 1, 0))
	ctx := context.Background()
	id := domain.EventID(fmt.Sprintf("%s/%s.%d", hikers, dana, pollSent))
	if err := r.a.VotePoll(ctx, hikers, id, []string{"0", "1"}); err == nil {
		t.Error("two answers went in a one-answer poll")
	}
	if err := r.a.VotePoll(ctx, hikers, id, []string{"7"}); err == nil {
		t.Error("an answer the poll lacks went")
	}
	if err := r.a.VotePoll(ctx, hikers, id, []string{"1"}); err != nil {
		t.Fatal(err)
	}
	var sent *signalv1.Vote
	b, _ := os.ReadFile(r.sent)
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.SendRequest{}
		if protojson.Unmarshal([]byte(line), req) == nil && req.GetVote() != nil {
			sent = req.GetVote()
		}
	}
	if sent == nil || sent.GetTargetAuthor() != dana || sent.GetTargetSent() != pollSent || !slices.Equal(sent.GetOptions(), []uint32{1}) || sent.GetRound() != 5 {
		t.Fatalf("sent %v, want answer 1 to Dana's poll, numbered 5", sent)
	}
	if p := r.poll(); !p.Options[1].Mine || p.Options[2].Mine || p.Options[1].Votes != 1 {
		t.Errorf("the vote is not counted at once: %+v", p.Options)
	}
	voters, err := r.a.PollVoters(ctx, hikers, id)
	if err != nil || len(voters.Voters) != 2 || voters.Voters[0].ID != "signal:"+me || !slices.Equal(voters.Voters[1].Options, []string{"0"}) {
		t.Errorf("voters %+v, %v", voters, err)
	}
	// Two votes at once (each reads the same last number) still go numbered one after
	// the other, and the higher-numbered one is what counts.
	var wg sync.WaitGroup
	for _, answer := range []string{"0", "2"} {
		wg.Go(func() {
			if err := r.a.VotePoll(ctx, hikers, id, []string{answer}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b, _ = os.ReadFile(r.sent)
	answerOf := map[uint32]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.SendRequest{}
		if protojson.Unmarshal([]byte(line), req) == nil && req.GetVote() != nil {
			answerOf[req.GetVote().GetRound()] = answers(req.GetVote().GetOptions())
		}
	}
	if !slices.Equal(slices.Sorted(maps.Keys(answerOf)), []uint32{5, 6, 7}) {
		t.Fatalf("own votes numbered %v, want 5, 6, 7", slices.Sorted(maps.Keys(answerOf)))
	}
	if got := strings.Join(r.poll().Ballots["signal:"+me], ","); got != answerOf[7] {
		t.Errorf("counted %v, want the vote numbered 7 (%s)", got, answerOf[7])
	}

	r2 := connected(t, asked(), ended())
	if err := r2.a.VotePoll(ctx, hikers, id, []string{"0"}); err == nil {
		t.Error("a vote went in an ended poll")
	}
}
