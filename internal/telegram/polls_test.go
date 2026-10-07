package telegram

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgtest"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hike is a poll of group -11: Saturday or Sunday.
func hike() *tg.MessageMediaPoll {
	return &tg.MessageMediaPoll{
		Poll: tg.Poll{ID: 777, Question: tg.TextWithEntities{Text: "Hike when?"}, Answers: []tg.PollAnswerClass{
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Saturday"}, Option: []byte{0}},
			&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Sunday"}, Option: []byte{1}},
		}},
		Results: tg.PollResults{TotalVoters: 3, Results: []tg.PollAnswerVoters{
			{Option: []byte{0}, Voters: 2, Chosen: true}, {Option: []byte{1}, Voters: 1},
		}},
	}
}

// A poll message carries its poll: question, answers, votes and this account's
// choice; a results update rewrites it, keeping the choice when the update does not
// say it (min); a vote names the answers chosen, and its answer's results are kept.
func TestAPollIsReadUpdatedAndVotedIn(t *testing.T) {
	t.Parallel()
	ent := peer.NewEntities(map[int64]*tg.User{7: dana}, nil, nil)
	msg, ok := incoming(42, &tg.Message{ID: 5, PeerID: &tg.PeerChat{ChatID: 11}, Media: hike(), Date: 1000, FromID: &tg.PeerUser{UserID: 7}}, ent)
	if !ok || msg.Poll == nil {
		t.Fatalf("incoming = %+v, %v", msg, ok)
	}
	p := msg.Poll
	if p.Question != "Hike when?" || len(p.Options) != 2 || p.Options[0].Text != "Saturday" || p.Options[0].Votes != 2 ||
		!p.Options[0].Mine || p.Options[1].Mine || p.Voters != 3 || msg.Body != p.Summary() {
		t.Fatalf("poll = %+v, body %q", p, msg.Body)
	}

	f := newFakeTelegram(t)
	room := roomID(42, -11)
	var mu sync.Mutex
	var voted [][]byte
	f.cluster.Dispatch(2, "dc2").HandleFunc(tg.MessagesSendVoteRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesSendVoteRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		mu.Lock()
		voted = req.Options
		mu.Unlock()
		return s.SendResult(r, &tg.Updates{Date: 2000, Updates: []tg.UpdateClass{&tg.UpdateMessagePoll{
			Peer: &tg.PeerChat{ChatID: 11}, MsgID: 5, PollID: 777,
			Results: tg.PollResults{TotalVoters: 3, Results: []tg.PollAnswerVoters{{Option: []byte{0}, Voters: 1}, {Option: []byte{1}, Voters: 2, Chosen: true}}},
		}}})
	})
	a, cache := loggedInWithStore(t, f, openStore(t))
	ctx := t.Context()
	if err := cache.AddRooms(ctx, domain.AccountRooms(domain.ProtocolTelegram, "42"), []domain.Room{{ID: room, Name: "Hikers", Membership: domain.MembershipJoin}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, room, []domain.Message{msg}); err != nil {
		t.Fatal(err)
	}
	cached := func() *domain.Poll {
		got, _, _ := cache.MessageByID(ctx, room, msg.ID)
		return got.Poll
	}

	// Min, and with the poll itself again: the choice is not in it either.
	minUpdate := &tg.UpdateMessagePoll{PollID: 777, Results: tg.PollResults{Min: true, TotalVoters: 4, Results: []tg.PollAnswerVoters{
		{Option: []byte{0}, Voters: 3}, {Option: []byte{1}, Voters: 1},
	}}}
	minUpdate.SetPoll(hike().Poll)
	a.pollUpdated(ctx, 42, minUpdate)
	if got := cached(); got.Options[0].Votes != 3 || !got.Options[0].Mine || got.Voters != 4 {
		t.Errorf("after a min update: %+v, want the votes moved and the choice kept", got)
	}

	if err := a.VotePoll(ctx, room, msg.ID, []string{p.Options[1].ID}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if !slices.EqualFunc(voted, [][]byte{{1}}, slices.Equal[[]byte]) {
		t.Errorf("voted %v, want Sunday's option", voted)
	}
	mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := cached()
		if got.Options[1].Mine && !got.Options[0].Mine && got.Options[1].Votes == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after voting: %+v, want Sunday chosen", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
