package telegram

import (
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/telegram/message/peer"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
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

// Who voted is read of the poll's message page by page, every answer a voter chose
// kept, users and channels named as Telegram calls them; an anonymous poll is not
// asked, and a poll naming its voters only to those who voted says so.
func TestAPollsVotersAreRead(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		public bool
		denied string
		want   domain.PollVoters
	}{
		"public": {public: true, want: domain.PollVoters{Voters: []domain.PollVoter{
			{ID: personID(7), Name: "Dana", Options: []string{optionID([]byte{0}), optionID([]byte{1})}},
			{ID: personID(8), Name: "Eli Stone", Options: []string{optionID([]byte{1})}},
			{ID: personID(-(channelMark + 30)), Name: "Hiking Club", Options: []string{optionID([]byte{0})}},
		}}},
		"anonymous":  {want: domain.PollVoters{Hidden: domain.PollAnonymous}},
		"vote first": {public: true, denied: "POLL_VOTE_REQUIRED", want: domain.PollVoters{Hidden: domain.PollVoteFirst}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFakeTelegram(t)
			f.serveUpdates(&updatesOf{pts: 1})
			d := f.cluster.Dispatch(2, "dc2")
			poll := hike()
			poll.Poll.PublicVoters = c.public
			d.HandleFunc(tg.MessagesGetMessagesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
				return s.SendResult(r, &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{
					ID: 5, PeerID: &tg.PeerChat{ChatID: 11}, Date: 1000, Media: poll, FromID: &tg.PeerUser{UserID: 7},
				}}, Users: []tg.UserClass{dana}})
			})
			var mu sync.Mutex
			asked := 0
			d.HandleFunc(tg.MessagesGetPollVotesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
				var req tg.MessagesGetPollVotesRequest
				if err := req.Decode(r.Buf); err != nil {
					return err
				}
				mu.Lock()
				asked++
				mu.Unlock()
				if c.denied != "" {
					return s.SendErr(r, tgerr.New(403, c.denied))
				}
				if req.Offset == "" {
					return s.SendResult(r, &tg.MessagesVotesList{Count: 3, NextOffset: "page2", Votes: []tg.MessagePeerVoteClass{
						&tg.MessagePeerVoteMultiple{Peer: &tg.PeerUser{UserID: 7}, Options: [][]byte{{0}, {1}}},
						&tg.MessagePeerVote{Peer: &tg.PeerUser{UserID: 8}, Option: []byte{1}},
					}, Users: []tg.UserClass{dana, &tg.User{ID: 8, FirstName: "Eli", LastName: "Stone"}}})
				}
				return s.SendResult(r, &tg.MessagesVotesList{Count: 3, Votes: []tg.MessagePeerVoteClass{
					&tg.MessagePeerVote{Peer: &tg.PeerChannel{ChannelID: 30}, Option: []byte{0}},
					&tg.MessagePeerVoteInputOption{Peer: &tg.PeerUser{UserID: 9}},
				}, Chats: []tg.ChatClass{&tg.Channel{ID: 30, AccessHash: 300, Title: "Hiking Club", Photo: &tg.ChatPhotoEmpty{}}}})
			})
			a, _ := loggedInWithStore(t, f, openStore(t))
			room := roomID(42, -11)
			got, err := a.PollVoters(t.Context(), room, domain.EventID(string(room)+"/5"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("voters = %+v\nwant %+v", got, c.want)
			}
			mu.Lock()
			defer mu.Unlock()
			if !c.public && asked != 0 {
				t.Errorf("an anonymous poll's voters were asked for %d times", asked)
			}
		})
	}
}
