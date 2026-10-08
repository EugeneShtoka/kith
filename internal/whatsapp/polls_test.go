package whatsapp

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hash is an answer as a vote names it.
func hash(answer string) []byte {
	sum := sha256.Sum256([]byte(answer))
	return sum[:]
}

// A poll creation message is the poll; a vote names answers by their hash, replaces
// the voter's last, and none takes it back; this account's own ballot is its choice.
func TestAPollCountsEachVotersLastVote(t *testing.T) {
	t.Parallel()
	created := &waE2E.Message{PollCreationMessageV3: &waE2E.PollCreationMessage{
		Name: new("Hike when?"), SelectableOptionsCount: new(uint32(1)),
		Options: []*waE2E.PollCreationMessage_Option{{OptionName: new("Saturday")}, {OptionName: new("Sunday")}},
	}}
	p := pollOf(created)
	if p == nil || p.Question != "Hike when?" || p.Multiple || len(p.Options) != 2 || p.Options[1].ID != "Sunday" {
		t.Fatalf("poll = %+v", p)
	}

	ctx := context.Background()
	account := Account{Name: "home", Digits: ownDigits}
	a, cache, store := offline(t, account)
	client := linkedClient(t, store, ownDigits)
	room := roomID(ownDigits, types.NewJID("120363000000000001", types.GroupServer))
	poll := domain.EventID(domain.NativeID(domain.ProtocolWhatsApp, ownDigits, "POLL1"))
	if err := cache.AddRooms(ctx, domain.AccountRooms(domain.ProtocolWhatsApp, ownDigits), []domain.Room{{ID: room, Name: "Hikers", Membership: domain.MembershipJoin}}); err != nil {
		t.Fatal(err)
	}
	if err := cache.SaveMessages(ctx, room, []domain.Message{{ID: poll, RoomID: room, Body: p.Summary(), Poll: p, Timestamp: time.Unix(1000, 0)}}); err != nil {
		t.Fatal(err)
	}
	dana := domain.NativePerson(domain.ProtocolWhatsApp, "447700900111@s.whatsapp.net")
	eli := domain.NativePerson(domain.ProtocolWhatsApp, "447700900222@s.whatsapp.net")
	me := domain.NativePerson(domain.ProtocolWhatsApp, ownDigits+"@s.whatsapp.net")

	a.cast(ctx, client, room, poll, dana, [][]byte{hash("Saturday")}, nil)
	a.cast(ctx, client, room, poll, eli, [][]byte{hash("Saturday")}, nil)
	a.cast(ctx, client, room, poll, dana, [][]byte{hash("Sunday")}, nil) // Dana changes her mind
	a.cast(ctx, client, room, poll, me, nil, []string{"Sunday"})
	a.cast(ctx, client, room, poll, eli, [][]byte{}, nil) // Eli takes his back

	got, _, _ := cache.MessageByID(ctx, room, poll)
	sat, sun := got.Poll.Options[0], got.Poll.Options[1]
	if sat.Votes != 0 || sun.Votes != 2 || !sun.Mine || sat.Mine || got.Poll.Voters != 2 {
		t.Errorf("tally = %+v, %d voters; want Sunday 2 with mine, Saturday 0", got.Poll.Options, got.Poll.Voters)
	}
}
