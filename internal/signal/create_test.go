package signal_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A connected account offers a group. Making one asks the helper with Signal people by
// account and anyone else by number, and keeps its room at once, named; the people it
// could not add are named back. A name too long, or another kind, is refused.
func TestAGroupIsMadeWithThePeopleAskedFor(t *testing.T) {
	r := connected(t, msg(dana, dana, 1700000000100, "hi"))
	ctx := context.Background()
	offers, err := r.a.ChatOffers(ctx)
	if err != nil || len(offers) != 1 || offers[0].Kind != domain.ChatGroup {
		t.Fatalf("offers %+v, %v; want one group", offers, err)
	}
	spec := domain.NewRoom{
		On: offers[0].On, Kind: domain.ChatGroup, Name: " Hikers ",
		Invite: []string{
			"signal:" + dana, "tel:+972500000001", "whatsapp:972500000002@s.whatsapp.net",
			"tel:+15550000000", "telegram:123",
		},
	}
	room, err := r.a.CreateRoom(ctx, spec)
	if err == nil || !strings.Contains(err.Error(), "+15550000000") || !strings.Contains(err.Error(), "telegram:123") {
		t.Errorf("err %v, want the number Signal does not know and the Telegram person named", err)
	}
	if got, ok := r.room(room); !ok || got.Name != "Hikers" {
		t.Errorf("room %s: %+v, %v; want it kept at once as Hikers", room, got, ok)
	}
	b, _ := os.ReadFile(r.sent)
	var asked *signalv1.CreateGroupRequest
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.CreateGroupRequest{}
		if protojson.Unmarshal([]byte(line), req) == nil && req.GetTitle() != "" {
			asked = req
		}
	}
	if asked == nil || asked.GetAccount() != me || !slices.Equal(asked.GetMembers(), []string{dana}) ||
		!slices.Equal(asked.GetNumbers(), []string{"+972500000001", "+972500000002", "+15550000000"}) {
		t.Errorf("asked %v", asked)
	}

	long := spec
	long.Name = strings.Repeat("x", 33)
	if _, err := r.a.CreateRoom(ctx, long); err == nil {
		t.Error("a 33-character name was taken")
	}
	channel := spec
	channel.Kind = domain.ChatChannel
	if _, err := r.a.CreateRoom(ctx, channel); err == nil {
		t.Error("a channel was made")
	}
}
