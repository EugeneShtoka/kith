package slack

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// A channel is made under a name Slack takes, public or private as asked, and the
// workspace's people are invited; someone of another workspace is named in the
// partial failure, and a kind Slack does not make is refused.
func TestAChannelIsMadeOnSlack(t *testing.T) {
	t.Parallel()
	f, client := newFakeSlack(t)
	f.on("conversations.create", func(form map[string]string) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "C9", "name": form["name"]}}
	})
	f.on("conversations.invite", func(map[string]string) any {
		return map[string]any{"ok": true, "channel": map[string]any{"id": "C9"}}
	})
	a, _ := connectedTo(t, client)
	ctx := t.Context()
	on := domain.AccountRooms(domain.ProtocolSlack, "T1")
	room, err := a.CreateRoom(ctx, domain.NewRoom{
		Name: "  Trip — Plans 2026! ", On: on, Kind: domain.ChatPrivateChannel,
		Invite: []string{personID("T1", "U2"), personID("T1", "U3"), personID("T2", "U4")},
	})
	if room != roomID("T1", "C9") {
		t.Errorf("room = %q", room)
	}
	if err == nil || !strings.Contains(err.Error(), personID("T2", "U4")) {
		t.Errorf("err = %v, want the other workspace's person named", err)
	}
	created := f.calls("conversations.create")
	if len(created) != 1 || created[0]["name"] != "trip-plans-2026" || created[0]["is_private"] != "true" {
		t.Errorf("created %v, want a private trip-plans-2026", created)
	}
	if invited := f.calls("conversations.invite"); len(invited) != 1 || invited[0]["users"] != "U2,U3" || invited[0]["channel"] != "C9" {
		t.Errorf("invited %v, want U2 and U3 into C9", invited)
	}
	if _, err := a.CreateRoom(ctx, domain.NewRoom{Name: "x", On: on, Kind: domain.ChatForum}); err == nil {
		t.Error("a forum was made on Slack")
	}
}

// A channel's name is lower case, a hyphen for each run of anything else, and at most
// what Slack takes.
func TestAChannelNameIsWhatSlackTakes(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"Trip Plans":            "trip-plans",
		"--Hello,  World--":     "hello-world",
		"snake_case":            "snake_case",
		"Привет мир":            "привет-мир",
		"!!!":                   "",
		strings.Repeat("a", 90): strings.Repeat("a", 80),
	} {
		if got := channelName(in); got != want {
			t.Errorf("channelName(%q) = %q, want %q", in, got, want)
		}
	}
}
