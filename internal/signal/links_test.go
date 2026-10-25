package signal_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"slices"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	signalv1 "github.com/EugeneShtoka/kith/internal/api/signal/v1"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A signal.me link is the chat with the person Signal finds behind it: one the
// account has already, or one to write in, named by the number or username it was
// found by (a username before a number), which keeps that name once the first message
// makes the chat. A number or link Signal knows nobody for is no chat.
func TestASignalLinkIsTheChatWithWhoeverItNames(t *testing.T) {
	r := connected(t, named(msg(dana, dana, 1700000000100, "hi"), "Dana", "Dana"))
	ctx := context.Background()
	usernameLink := "https://signal.me/#eu/" + base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 48))

	for _, c := range []struct {
		link string
		want domain.Room
	}{
		{"https://signal.me/#p/+15550100002", domain.Room{ID: direct(dana), Name: "Dana"}},
		{"sgnl://signal.me/#p/+15550100009", domain.Room{ID: direct(ofer), Name: "+15550100009"}},
		{usernameLink, domain.Room{ID: direct(ofer), Name: "ofer.42"}}, // a username names better than a number
	} {
		target, err := r.a.ResolveLink(ctx, c.link)
		if err != nil || target.Join || target.Room.ID != c.want.ID || target.Room.Name != c.want.Name || !target.Room.IsDirect {
			t.Errorf("%s: (%+v, %v), want the direct chat %s named %q", c.link, target, err, c.want.ID, c.want.Name)
		}
	}
	if _, err := r.a.ResolveLink(ctx, "https://signal.me/#p/+15550100077"); err == nil {
		t.Error("a number nobody has was a chat")
	}
	if _, err := r.a.ResolveLink(ctx, "https://t.me/novosalsa"); err == nil {
		t.Error("a Telegram link was looked up on Signal")
	}

	if err := r.a.Send(ctx, direct(ofer), domain.Draft{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.room(direct(ofer)); !ok || got.Name != "ofer.42" {
		t.Errorf("after the first message, the chat is %+v, %v; want it kept by the name it was found by", got, ok)
	}

	b, _ := os.ReadFile(r.sent)
	var asked []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.FindPersonRequest{}
		if protojson.Unmarshal([]byte(line), req) == nil && req.GetBy() != nil {
			if req.GetAccount() != me {
				t.Errorf("looked up on %q, want the connected account", req.GetAccount())
			}
			asked = append(asked, req.GetNumber()+string(req.GetUsernameLink()[:min(1, len(req.GetUsernameLink()))]))
		}
	}
	if len(asked) != 4 {
		t.Errorf("asked Signal %q, want each Signal link once", asked)
	}
}

// A signal.group link is the group Signal says it is to: one to join, said how big
// and whether an admin approves who joins, or the account's already. Joining keeps the
// group's room at once; where an admin approves, joining only asks, and says so, as
// does a link to a group the account asked to join already.
func TestASignalInviteIsTheGroupToJoin(t *testing.T) {
	r := connected(t)
	ctx := context.Background()
	invite := func(in string) string {
		return "https://signal.group/#" + base64.RawURLEncoding.EncodeToString([]byte(in))
	}
	group := func(id string) domain.RoomID {
		raw, _ := base64.StdEncoding.DecodeString(id)
		return domain.RoomID("signal:" + me + "/" + base64.RawURLEncoding.EncodeToString(raw))
	}

	for _, c := range []struct {
		invite string
		want   domain.LinkTarget
	}{
		{"open", domain.LinkTarget{Room: domain.Room{ID: group(joinedGroup), Name: "Book club"}, Join: true, About: "group, 12 members"}},
		{"approval", domain.LinkTarget{Room: domain.Room{ID: group(joinedGroup), Name: "Quiet room"}, Join: true, About: "group, 3 members; an admin approves who joins"}},
		{"mine", domain.LinkTarget{Room: domain.Room{ID: group(madeGroup), Name: "Hikers"}}},
	} {
		got, err := r.a.ResolveLink(ctx, invite(c.invite))
		if err != nil || got.Join != c.want.Join || got.About != c.want.About || got.Room.ID != c.want.Room.ID ||
			got.Room.Name != c.want.Room.Name || got.Room.IsDirect {
			t.Errorf("%s: (%+v, %v), want %+v", c.invite, got, err, c.want)
		}
	}
	if _, err := r.a.ResolveLink(ctx, invite("asked")); err == nil || !strings.Contains(err.Error(), "asked to join") {
		t.Errorf("a group asked to join already: %v, want it said", err)
	}
	if _, err := r.a.ResolveLink(ctx, invite("reset")); err == nil {
		t.Error("a reset link was a group")
	}

	room, err := r.a.JoinLink(ctx, invite("open"))
	if err != nil || room != group(joinedGroup) {
		t.Errorf("joining Book club: (%s, %v), want %s", room, err, group(joinedGroup))
	}
	if got, ok := r.room(room); !ok || got.Name != "Book club" || got.IsDirect {
		t.Errorf("after joining, the room is %+v, %v; want Book club kept at once", got, ok)
	}
	if _, err := r.a.JoinLink(ctx, invite("approval")); err == nil || !strings.Contains(err.Error(), "asked to join") {
		t.Errorf("joining where an admin approves: %v, want it said that it only asked", err)
	}
	if _, err := r.a.JoinLink(ctx, "https://signal.me/#p/+15550100009"); err == nil {
		t.Error("a link to a person was joined")
	}

	b, _ := os.ReadFile(r.sent)
	var asked []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		req := &signalv1.JoinGroupRequest{} // GroupByInviteRequest's fields are the same
		if protojson.Unmarshal([]byte(line), req) == nil && len(req.GetInvite()) > 0 {
			if req.GetAccount() != me {
				t.Errorf("asked on %q, want the connected account", req.GetAccount())
			}
			asked = append(asked, string(req.GetInvite()))
		}
	}
	if want := []string{"open", "approval", "mine", "asked", "reset", "open", "approval"}; !slices.Equal(asked, want) {
		t.Errorf("asked Signal about %q, want %q", asked, want)
	}
}

// With no account connected, nothing is looked up.
func TestASignalLinkNeedsAConnectedAccount(t *testing.T) {
	r := newRig(t, "ok", "", "")
	if _, err := r.a.ResolveLink(context.Background(), "https://signal.me/#p/+15550100009"); err == nil {
		t.Error("a link was looked up with no account connected")
	}
}
