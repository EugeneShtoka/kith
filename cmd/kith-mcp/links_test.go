package main

import (
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Each message carries its link: its network's by default, kith's own with
// `[agent] links = "kith"`.
func TestEachMessageCarriesItsLink(t *testing.T) {
	t.Parallel()

	f := twoRooms()
	f.messages[mx+"!open:x"] = []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!open:x", Sender: "matrix:@dana:x", Body: "morning", Timestamp: time.Unix(1_700_000_000, 0)},
	}
	for _, tc := range []struct {
		kith bool
		want string
	}{
		{false, "https://matrix.to/#/%21open:x/$1"},
		{true, domain.KithLink(mx+"!open:x", mx+"$1")},
	} {
		s := newServer(f, shareAllEncrypted)
		s.kithLinks = tc.kith
		msgs, _ := call(t, s, "read_room", map[string]any{"room": "Standup"})["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("read_room gave %d messages, want 1", len(msgs))
		}
		if got, _ := msgs[0].(map[string]any)["link"].(string); got != tc.want {
			t.Errorf("kith links %v: link = %q, want %q", tc.kith, got, tc.want)
		}
	}
}

const (
	opsRoom = domain.RoomID("slack:T1/C1")
	opsMsg  = domain.EventID("slack:T1/C1/1700000000.000100")
	opsLink = "https://acme.slack.com/archives/C1/p1700000000000100"
)

// slackRoom is one Slack channel with one message, in a workspace at acme.slack.com.
func slackRoom() *fake {
	return &fake{
		rooms:    []domain.Room{{ID: opsRoom, Name: "Ops"}},
		messages: map[domain.RoomID][]domain.Message{opsRoom: {{ID: opsMsg, RoomID: opsRoom, Sender: "slack:T1.U1", Body: "deploy", Timestamp: time.Unix(1_700_000_000, 0)}}},
		hits:     []domain.SearchHit{{RoomID: opsRoom, EventID: opsMsg, Sender: "slack:T1.U1", Snippet: "deploy", Timestamp: time.Unix(1_700_000_000, 0)}},
		accounts: []domain.Account{{Network: domain.ProtocolSlack, On: domain.AccountRooms(domain.ProtocolSlack, "T1"), Name: "work", Web: "acme.slack.com"}},
	}
}

// links is the link on each message or hit a tool answered with.
func links(t *testing.T, out map[string]any, key string) []string {
	t.Helper()
	items, _ := out[key].([]any)
	var got []string
	for _, item := range items {
		m, _ := item.(map[string]any)
		link, _ := m["link"].(string)
		got = append(got, link)
	}
	return got
}

// A Slack message links under its workspace's address in read_room, read_around and
// search_messages alike, each the first call of its server.
func TestSlackMessagesLinkUnderTheirWorkspace(t *testing.T) {
	t.Parallel()
	first := func(tool, key string, args map[string]any) []string {
		return links(t, call(t, newServer(slackRoom(), shareAll), tool, args), key)
	}
	for tool, got := range map[string][]string{
		"read_room":       first("read_room", "messages", map[string]any{"room": "Ops"}),
		"read_around":     first("read_around", "messages", map[string]any{"room": "Ops", "event": string(opsMsg)}),
		"search_messages": first("search_messages", "hits", map[string]any{"query": "deploy"}),
	} {
		if len(got) != 1 || got[0] != opsLink {
			t.Errorf("%s links %v, want [%s]", tool, got, opsLink)
		}
	}
}

// The workspaces' addresses are asked once and kept; a Slack account not known yet (not
// connected when asked) links by team ID and is asked about again, until it is known.
func TestWorkspaceAddressesAreAskedOnceAndKept(t *testing.T) {
	t.Parallel()
	f := slackRoom()
	connected := f.accounts
	f.accounts = nil
	s := newServer(f, shareAll)
	read := func() []string { return links(t, call(t, s, "read_room", map[string]any{"room": "Ops"}), "messages") }

	if got := read(); len(got) != 1 || got[0] != "https://app.slack.com/client/T1/C1/p1700000000000100" {
		t.Errorf("before the workspace connects, read_room links %v, want the team-ID link", got)
	}
	f.accounts = connected
	for range 3 {
		if got := read(); len(got) != 1 || got[0] != opsLink {
			t.Errorf("read_room links %v, want [%s]", got, opsLink)
		}
	}
	if f.accountsCalls != 2 {
		t.Errorf("the accounts were asked %d times, want twice: once unknown, once found", f.accountsCalls)
	}
}
