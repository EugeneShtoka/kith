package slack

import (
	"errors"
	"slices"
	"testing"

	slackgo "github.com/slack-go/slack"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func conversation(id string, set func(*slackgo.Channel)) slackgo.Channel {
	c := slackgo.Channel{}
	c.ID = id
	set(&c)
	return c
}

// A workspace's conversations become rooms: channels by name, a DM by the person it
// is with (who is its member), a group DM by its people's handles; archived ones and
// DMs with deleted people are left out. The workspace is the space holding them all.
func TestAWorkspaceListsAsRoomsInOneSpace(t *testing.T) {
	t.Parallel()
	const team = "T0000000001"
	l := listed(team, "Acme", []slackgo.Channel{
		conversation("C0000000002", func(c *slackgo.Channel) { c.Name = "general"; c.Topic.Value = "Company-wide" }),
		conversation("G0000000003", func(c *slackgo.Channel) { c.Name = "secret-plans"; c.IsPrivate = true }),
		conversation("D0000000004", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U0000000005" }),
		conversation("G0000000006", func(c *slackgo.Channel) { c.IsMpIM = true; c.Name = "mpdm-dana--sam--lee-1" }),
		conversation("C0000000007", func(c *slackgo.Channel) { c.Name = "old"; c.IsArchived = true }),
		conversation("D0000000008", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U0000000009"; c.IsUserDeleted = true }),
	}, map[string]string{"U0000000005": "Dana Levi"})

	byName := map[string]domain.Room{}
	for _, r := range l.rooms {
		byName[r.Name] = r
	}
	if len(l.rooms) != 4 {
		t.Fatalf("rooms = %+v, want four (archived and deleted left out)", l.rooms)
	}
	if r := byName["general"]; r.ID != "slack:T0000000001/C0000000002" || r.Topic != "Company-wide" || r.IsDirect {
		t.Errorf("channel = %+v", r)
	}
	dm := byName["Dana Levi"]
	if !dm.IsDirect || dm.ID != "slack:T0000000001/D0000000004" {
		t.Errorf("DM = %+v", dm)
	}
	if m := l.members[dm.ID]; len(m) != 1 || m[0].UserID != "slack:T0000000001.U0000000005" || m[0].DisplayName != "Dana Levi" {
		t.Errorf("DM members = %+v", m)
	}
	if _, ok := byName["dana, sam, lee"]; !ok {
		t.Errorf("group DM not named by its people: %+v", l.rooms)
	}
	if l.space.ID != "slack:T0000000001/T0000000001" || l.space.Name != "Acme" || !l.space.Original || l.space.Bridge != domain.ProtocolSlack {
		t.Errorf("space = %+v", l.space)
	}
	if len(l.space.Children) != 4 || !slices.IsSorted(l.space.Children) {
		t.Errorf("space children = %v", l.space.Children)
	}
	for _, r := range l.rooms {
		if domain.OwnerOf(r.ID) != domain.AccountRooms(domain.ProtocolSlack, team) || !domain.IsRoomID(string(r.ID)) {
			t.Errorf("%s is not one of the workspace's rooms", r.ID)
		}
	}
	if domain.OwnerOf(domain.RoomID(l.space.ID)) != domain.AccountRooms(domain.ProtocolSlack, team) {
		t.Errorf("the space %s is not the workspace's", l.space.ID)
	}
	if got := dmPartners([]slackgo.Channel{
		conversation("D1", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U1" }),
		conversation("D2", func(c *slackgo.Channel) { c.IsIM = true; c.User = "U1" }),
		conversation("C1", func(c *slackgo.Channel) { c.Name = "x" }),
	}); !slices.Equal(got, []string{"U1"}) {
		t.Errorf("dmPartners = %v", got)
	}
}

// A person reads by the name they chose, else their full name, else their handle.
func TestUserNames(t *testing.T) {
	t.Parallel()
	u := slackgo.User{Name: "dlevi", RealName: "Dana Levi"}
	if got := userName(u); got != "Dana Levi" {
		t.Errorf("no display name = %q", got)
	}
	u.Profile.DisplayName = " Dana "
	if got := userName(u); got != "Dana" {
		t.Errorf("display name = %q", got)
	}
	if got := userName(slackgo.User{Name: "dlevi"}); got != "dlevi" {
		t.Errorf("handle alone = %q", got)
	}
}

// A sign-in counts only for the workspace the account names.
func TestASignInMustBeToTheAccountsWorkspace(t *testing.T) {
	t.Parallel()
	account := Account{Name: "work", Workspace: "acme"}
	if err := sameWorkspace(account, "https://Acme.slack.com/"); err != nil {
		t.Errorf("the workspace itself: %v", err)
	}
	if err := sameWorkspace(account, "https://other.slack.com/"); !errors.Is(err, errOtherWorkspace) {
		t.Errorf("another workspace: %v", err)
	}
	if err := sameWorkspace(account, ""); err == nil {
		t.Error("no address was accepted")
	}
}

// Slack refusing the session is signed out; anything else is not.
func TestASessionSlackRefusedIsSignedOut(t *testing.T) {
	t.Parallel()
	if !isSignedOut(slackgo.SlackErrorResponse{Err: "invalid_auth"}) {
		t.Error("invalid_auth is not signed out")
	}
	if isSignedOut(errors.New("dial tcp: no route")) || isSignedOut(slackgo.SlackErrorResponse{Err: "ratelimited"}) {
		t.Error("an outage reads as signed out")
	}
}
