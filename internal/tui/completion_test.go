package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// memberBackend answers with a fixed membership and records what was sent.
type memberBackend struct {
	apitest.Nop
	members  []domain.Member
	sent     []domain.Draft
	refresh  int
	sendFail error
}

func (b *memberBackend) MentionCandidates(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return b.members, nil
}

func (b *memberBackend) Members(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return b.members, nil
}

func (b *memberBackend) RefreshMembers(context.Context, domain.RoomID) ([]domain.Member, error) {
	b.refresh++
	return b.members, nil
}

func (b *memberBackend) Send(_ context.Context, _ domain.RoomID, draft domain.Draft) error {
	b.sent = append(b.sent, draft)
	return b.sendFail
}

// composing returns a model in insert mode in a room with the given members.
func composing(t *testing.T, members ...domain.Member) (Model, *memberBackend) {
	t.Helper()
	return composingWith(t, config.Display{}, members...)
}

func composingWith(t *testing.T, display config.Display, members ...domain.Member) (Model, *memberBackend) {
	t.Helper()
	b := &memberBackend{members: members}
	m := update(t, New(context.Background(), b, display),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, _ := m.selectRoom(m.filteredRooms()[0])
	m = next
	m = update(t, m, membersMsg{roomID: "!a:x", members: members})
	m.focus = paneTimeline
	m.compose.insertMode = true
	m = m.clearStatus()
	return m, b
}

// typeInto types text into the composer one key at a time.
func typeInto(t *testing.T, m Model, text string) Model {
	t.Helper()
	for _, r := range text {
		m, _ = press(t, m, keyText(string(r)))
	}
	return m
}

var people = []domain.Member{
	{UserID: "@mike:x", DisplayName: "Michael Livingston"},
	{UserID: "@liv:x", DisplayName: "Liv Andersen"},
	{UserID: "@dana:x", DisplayName: "Dana Levi"},
	{UserID: "@wa:x", DisplayName: "Olivia Stone"},
}

// `@` after a space opens the popup listing the room.
func TestMentionPopupOpens(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "hi @")
	if !m.completion.active {
		t.Fatal("@ after a space should open the popup")
	}
	if m.completion.trigger != "@" {
		t.Errorf("trigger = %q", m.completion.trigger)
	}
	if len(m.completion.candidates) != len(people) {
		t.Errorf("candidates = %d, want the whole room", len(m.completion.candidates))
	}
	if m.compose.input != "hi @" {
		t.Errorf("input = %q — the trigger stays as typed text", m.compose.input)
	}
}

// A trigger mid-word is an email address, not a mention.
func TestMentionPopupNotOpenedMidWord(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "mail me at eugene@")
	if m.completion.active {
		t.Error("a mid-word @ should not open the popup")
	}
	if m.compose.input != "mail me at eugene@" {
		t.Errorf("input = %q", m.compose.input)
	}
	// At the very start of the composer it does open.
	m2, _ := composing(t, people...)
	m2 = typeInto(t, m2, "@")
	if !m2.completion.active {
		t.Error("@ at the start of the input should open the popup")
	}
}

// Typing narrows, and the ordering is the specified one: name-token prefixes first,
// substrings after.
func TestMentionPopupNarrows(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "@liv")

	labels := make([]string, 0, len(m.completion.candidates))
	for _, c := range m.completion.candidates {
		labels = append(labels, c.label)
	}
	// Michael Livingston and Liv Andersen match a name token; Olivia Stone only as
	// a substring; Dana Levi not at all.
	want := []string{"Michael Livingston", "Liv Andersen", "Olivia Stone"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Errorf("candidates = %v, want %v", labels, want)
	}
}

// A popup with nothing to offer closes and gives its keys back to typing.
func TestMentionPopupClosesWithNoCandidates(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "@zzz")
	if m.completion.active {
		t.Error("the popup should close when nothing matches")
	}
	if m.compose.input != "@zzz" {
		t.Errorf("input = %q — the typed text must survive", m.compose.input)
	}
	// And the keys it would have owned go back to typing.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.compose.input != "@zzz" {
		t.Errorf("input = %q after tab, want the text untouched", m.compose.input)
	}
}

// Whitespace ends a completion — names do not span words here.
func TestMentionPopupClosesOnSpace(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "@liv")
	if !m.completion.active {
		t.Fatal("setup")
	}
	m = typeInto(t, m, " ")
	if m.completion.active {
		t.Error("a space should close the popup")
	}
}

// Backspacing onto the trigger closes the popup.
func TestMentionPopupClosesOnBackspaceToTrigger(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "hi @li")
	if !m.completion.active {
		t.Fatal("setup")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	if !m.completion.active {
		t.Error("backspacing within the query should keep the popup open")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace}) // now on "@"
	if !m.completion.active {
		t.Error("backspacing to the bare trigger should keep the popup open")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace}) // trigger gone
	if m.completion.active {
		t.Error("backspacing past the trigger should close the popup")
	}
	if m.compose.input != "hi " {
		t.Errorf("input = %q", m.compose.input)
	}
}

// Accepting replaces exactly the trigger and query, and records the mention.
func TestMentionAcceptInsertsAndRecords(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "hi @liv")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})

	if m.compose.input != "hi Michael Livingston " {
		t.Errorf("input = %q, want the name and a trailing space", m.compose.input)
	}
	if m.completion.active {
		t.Error("accepting should close the popup")
	}
	if len(m.compose.drafted) != 1 || m.compose.drafted[0].UserID != "@mike:x" || m.compose.drafted[0].Name != "Michael Livingston" {
		t.Errorf("drafted = %+v, want the selected person recorded", m.compose.drafted)
	}
}

// The popup's keys walk it and wrap, and enter accepts as well as tab.
func TestMentionPopupNavigation(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "@")
	if m.completion.cursor != 0 {
		t.Fatalf("cursor = %d", m.completion.cursor)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.completion.cursor != 1 {
		t.Errorf("down: cursor = %d, want 1", m.completion.cursor)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyUp})
	if want := len(people) - 1; m.completion.cursor != want {
		t.Errorf("up from the top should wrap to %d, got %d", want, m.completion.cursor)
	}
	// ctrl+n/ctrl+p are the readline convention and work too.
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.completion.cursor != 0 {
		t.Errorf("ctrl+n from the end should wrap to 0, got %d", m.completion.cursor)
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if want := len(people) - 1; m.completion.cursor != want {
		t.Errorf("ctrl+p should wrap back to %d, got %d", want, m.completion.cursor)
	}
	// enter accepts rather than sending while the popup is up.
	m2, b2 := composing(t, people...)
	m2 = typeInto(t, m2, "@dana")
	m2, _ = press(t, m2, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(b2.sent) != 0 {
		t.Error("enter should accept the candidate, not send the message")
	}
	if m2.compose.input != "Dana Levi " {
		t.Errorf("input = %q", m2.compose.input)
	}
}

// ctrl+n navigates the popup but still means do-not-disturb everywhere else.
func TestCompletionShadowsGlobalKeysOnlyWhileOpen(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = m.WithNotifications(&fakeNotifications{})
	m = typeInto(t, m, "@")
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if m.picker.active() {
		t.Error("ctrl+n should navigate the popup, not open the DND chooser, while it is open")
	}
	// With the popup closed it means what it always means — even mid-compose.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape}) // dismiss the popup
	if m.completion.active {
		t.Fatal("esc should dismiss the popup")
	}
	m, _ = press(t, m, tea.KeyPressMsg{Code: 'n', Mod: tea.ModCtrl})
	if !m.picker.active() {
		t.Error("ctrl+n should ask what to silence once the popup is closed")
	}
}

// The first esc dismisses the popup; the second leaves insert mode.
func TestCompletionEscapeDismissesPopupOnly(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "hi @liv")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.completion.active {
		t.Error("esc should dismiss the popup")
	}
	if !m.compose.insertMode {
		t.Error("esc should not also leave insert mode")
	}
	if m.compose.input != "hi @liv" {
		t.Errorf("input = %q — dismissing leaves the text as typed", m.compose.input)
	}
	// A second esc leaves insert mode, as it always did.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.compose.insertMode {
		t.Error("the second esc should leave insert mode")
	}
}

// The whole point: the sent message carries a real Matrix mention.
func TestSendCarriesMention(t *testing.T) {
	t.Parallel()

	m, b := composing(t, people...)
	m = typeInto(t, m, "@dana")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	m = typeInto(t, m, "ping")
	m, cmd := press(t, m, sendKey())
	if cmd == nil {
		t.Fatal("sending produced no command")
	}
	m = deliver(t, m, cmd)

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts", len(b.sent))
	}
	draft := b.sent[0]
	if draft.Body != "Dana Levi ping" {
		t.Errorf("Body = %q", draft.Body)
	}
	live := draft.LiveMentions()
	if len(live) != 1 || live[0].UserID != "@dana:x" {
		t.Errorf("mentions = %+v, want dana", live)
	}
	// And the composer's mention list is cleared for the next message.
	if len(m.compose.drafted) != 0 {
		t.Errorf("drafted = %+v, want cleared after sending", m.compose.drafted)
	}
}

// A name typed over must not notify the person it no longer names.
func TestSendDropsMentionEditedAway(t *testing.T) {
	t.Parallel()

	m, b := composing(t, people...)
	m = typeInto(t, m, "@dana")
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	// Delete the inserted name again.
	for range len("Dana Levi ") {
		m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	m = typeInto(t, m, "never mind")
	_, cmd := press(t, m, sendKey())
	if cmd == nil {
		t.Fatal("sending produced no command")
	}
	_ = deliver(t, m, cmd)

	if len(b.sent) != 1 {
		t.Fatalf("sent %d drafts", len(b.sent))
	}
	if live := b.sent[0].LiveMentions(); len(live) != 0 {
		t.Errorf("mentions = %+v, want none — the body no longer names them", live)
	}
}

// A configured identity merges several accounts into one row.
func TestMentionPopupIsIdentityMergeAware(t *testing.T) {
	t.Parallel()

	display := config.Display{Identities: []config.Identity{{
		Alias: "Dana",
		IDs:   []string{"@dana:x", "@whatsapp_dana:x", "@telegram_dana:x"},
	}}}
	m, _ := composingWith(t, display,
		domain.Member{UserID: "@dana:x", DisplayName: "Dana Levi"},
		domain.Member{UserID: "@whatsapp_dana:x", DisplayName: "Dana L (WA)"},
		domain.Member{UserID: "@telegram_dana:x", DisplayName: "dana_tg"},
		domain.Member{UserID: "@other:x", DisplayName: "Danielle"},
	)
	m = typeInto(t, m, "@dan")

	labels := []string{}
	for _, c := range m.completion.candidates {
		labels = append(labels, c.label)
	}
	if len(labels) != 2 {
		t.Fatalf("candidates = %v, want one row for the merged person plus Danielle", labels)
	}
	if labels[0] != "Dana" {
		t.Errorf("the merged row should use the configured alias, got %q", labels[0])
	}
	// Accepting inserts the alias and notifies an account that is in this room.
	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeyTab})
	if len(m.compose.drafted) != 1 {
		t.Fatalf("drafted = %+v", m.compose.drafted)
	}
	if m.compose.drafted[0].Name != "Dana" {
		t.Errorf("inserted name = %q, want the alias", m.compose.drafted[0].Name)
	}
	inRoom := map[string]bool{"@dana:x": true, "@whatsapp_dana:x": true, "@telegram_dana:x": true}
	if !inRoom[m.compose.drafted[0].UserID] {
		t.Errorf("notified %q, which is not one of this room's accounts for that person", m.compose.drafted[0].UserID)
	}
}

// Two people with the same display name are disambiguated, and a bridged account
// says which network it is on.
func TestMentionPopupRowDetails(t *testing.T) {
	t.Parallel()

	m, _ := composing(t,
		domain.Member{UserID: "@a1:x", DisplayName: "Alex Kim"},
		domain.Member{UserID: "@a2:x", DisplayName: "Alex Kim"},
		domain.Member{UserID: "@whatsapp_99:x", DisplayName: "Bridged Bob"},
	)
	m = typeInto(t, m, "@")

	details := map[string]string{}
	for _, c := range m.completion.candidates {
		details[c.userID] = c.detail
	}
	if details["@a1:x"] != "@a1:x" || details["@a2:x"] != "@a2:x" {
		t.Errorf("duplicate names should carry their MXIDs, got %v", details)
	}
	if details["@whatsapp_99:x"] != "WhatsApp" {
		t.Errorf("a bridged account should name its network, got %q", details["@whatsapp_99:x"])
	}
}

// The popup renders with the composer still on screen.
func TestMentionPopupRenders(t *testing.T) {
	t.Parallel()

	m, _ := composing(t, people...)
	m = typeInto(t, m, "@")
	body := stripStyles(m.View().Content)
	for _, want := range []string{"Michael Livingston", "Liv Andersen", "[INSERT]"} {
		if !strings.Contains(body, want) {
			t.Errorf("the pane should show %q", want)
		}
	}
}

// A long candidate list is capped, and says how many it is not showing.
func TestMentionPopupCapsAndCounts(t *testing.T) {
	t.Parallel()

	many := make([]domain.Member, 0, 30)
	for i := range 30 {
		many = append(many, domain.Member{
			UserID:      "@u" + string(rune('a'+i%26)) + string(rune('0'+i/26)) + ":x",
			DisplayName: "Person " + string(rune('a'+i%26)) + string(rune('0'+i/26)),
		})
	}
	m, _ := composing(t, many...)
	m = typeInto(t, m, "@")
	rows := m.completionLines(60)
	if len(rows) != completionRows+1 {
		t.Fatalf("popup rendered %d rows, want %d plus a footer", len(rows), completionRows)
	}
	if !strings.Contains(stripStyles(rows[len(rows)-1]), "more") {
		t.Errorf("the last row should count what is hidden, got %q", stripStyles(rows[len(rows)-1]))
	}
}

// Members load with the room so the popup opens on the first keystroke, and the
// network refresh follows it.
func TestMembersLoadOnRoomOpen(t *testing.T) {
	t.Parallel()

	b := &memberBackend{members: people}
	m := update(t, New(context.Background(), b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}})
	m = sized(t, m)
	next, cmd := m.selectRoom(m.filteredRooms()[0])
	m = next
	if cmd == nil {
		t.Fatal("opening a room should load its members")
	}
	if m.timeline.members != nil {
		t.Error("members should start empty and arrive by message")
	}
	m = update(t, m, membersMsg{roomID: "!a:x", members: people})
	if len(m.timeline.members) != len(people) {
		t.Errorf("members = %d, want the ranked list", len(m.timeline.members))
	}
	// A result for a room the user has moved on from is dropped.
	m = update(t, m, membersMsg{roomID: "!elsewhere:x", members: nil})
	if len(m.timeline.members) != len(people) {
		t.Error("another room's members should not replace the open room's")
	}
}
