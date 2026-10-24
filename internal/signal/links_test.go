package signal_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
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

// With no account connected, nothing is looked up.
func TestASignalLinkNeedsAConnectedAccount(t *testing.T) {
	r := newRig(t, "ok", "", "")
	if _, err := r.a.ResolveLink(context.Background(), "https://signal.me/#p/+15550100009"); err == nil {
		t.Error("a link was looked up with no account connected")
	}
}
