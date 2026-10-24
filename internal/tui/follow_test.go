package tui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Following a link to a place has three answers: go there, ask before joining, or
// open the switcher.

// linkMsg is a message carrying one address.
func linkMsg(body, html string) domain.Message {
	return domain.Message{ID: "$1", RoomID: "!a:x", Sender: "@her:x", Body: body, Format: richtext.FromMarkup(html)}
}

// A link to a room you are in is a move, not a question.
func TestFollowingALinkToAJoinedRoomOpensIt(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("see https://matrix.to/#/!b:x for details", "")})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if got.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x", got.openRoom)
	}
	if got.confirm.active() {
		t.Error("following a link to a room we are in asked a question")
	}
}

// A link to a message queues the jump until the timeline holds it, as a search hit does.
func TestFollowingALinkToAMessageQueuesTheJump(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("https://matrix.to/#/!b:x/$deep", "")})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if got.openRoom != "!b:x" {
		t.Fatalf("open room = %q, want !b:x", got.openRoom)
	}
	if got.jump.to != "$deep" {
		t.Errorf("jump.to = %q, want $deep", got.jump.to)
	}
}

// Joining is visible to everyone in the room, so a link to an unjoined room asks
// first, naming what it would join.
func TestFollowingALinkToARoomWeAreNotInAsksFirst(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("https://matrix.to/#/%23book-club:example.org?via=one.org", "")})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if got.confirm.action != pendingJoinPlace {
		t.Fatalf("confirm action = %v, want pendingJoinPlace", got.confirm.action)
	}
	if got.confirm.address != "#book-club:example.org" {
		t.Errorf("would join %q, want #book-club:example.org", got.confirm.address)
	}
	// The routing servers make a room on an unknown homeserver reachable, so they survive
	// the question.
	if len(got.confirm.via) != 1 || got.confirm.via[0] != "one.org" {
		t.Errorf("via = %+v, want [one.org]", got.confirm.via)
	}
	if q := got.confirmPrompt(); !strings.Contains(q, "#book-club:example.org") {
		t.Errorf("the question does not name the room: %q", q)
	}
}

// A link to a person opens the switcher listing their rooms instead of making a room.
func TestFollowingALinkToAPersonOpensTheSwitcher(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("matrix:u/alice:example.org said so", "")})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if got.picker.kind != pickerJump {
		t.Fatalf("picker = %v, want the switcher", got.picker.kind)
	}
	if got.picker.filter == "" {
		t.Error("the switcher opened unfiltered, so it did not follow anybody")
	}
	if got.confirm.active() {
		t.Error("following a person asked to join something")
	}
}

// A mention pill's address is in the markup, not the words, so the formatted body is read.
func TestFollowingFindsTheAddressInsideAPill(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg(
		"Alice said so",
		`<a href="https://matrix.to/#/@alice:example.org">Alice</a> said so`,
	)})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if got.picker.kind != pickerJump {
		t.Errorf("picker = %v, want the switcher — the pill's target was not read", got.picker.kind)
	}
}

// Several addresses ask which, by the one-or-ask rule.
func TestSeveralPlacesAsksWhich(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg(
		"https://matrix.to/#/!b:x and https://matrix.to/#/@alice:example.org", "")})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if got.picker.kind != pickerContext || got.picker.then != actFollowLink {
		t.Fatalf("picker = %v/%v, want the follow chooser", got.picker.kind, got.picker.then)
	}
	if len(got.picker.items) != 2 {
		t.Errorf("%d rows, want 2", len(got.picker.items))
	}
	// A known room is shown by name, not ID.
	if got.picker.items[0].label != "Bravo" {
		t.Errorf("first row = %q, want the room's name", got.picker.items[0].label)
	}
}

// A message with only web links says it has no Matrix link, not that the link is broken.
func TestFollowingSaysWhenThereIsNoMatrixLink(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	m = m.setMessages([]domain.Message{linkMsg("the docs are at https://example.org/docs", "")})
	m.timeline.selected = "$1"

	next, _ := m.takeContext(actFollowLink)
	got := next
	if !strings.Contains(got.status(), "Matrix") {
		t.Errorf("status = %q, want it to say nothing points into Matrix", got.status())
	}
	if got.picker.kind != pickerNone {
		t.Error("a message with no Matrix link opened a picker")
	}
}

// Answering the join question carries the address through to the call.
func TestConfirmingAJoinJoinsWhatTheLinkNamed(t *testing.T) {
	t.Parallel()

	b := &membershipBackend{}
	m := withRooms(t, sized(t, starterNew(b, config.Display{})))
	m.confirm = confirmState{action: pendingJoinPlace, address: "#book-club:example.org", via: []string{"one.org"}}

	next, cmd := m.resolveConfirm(true)
	got := next
	if got.confirm.active() {
		t.Error("the question outlived its answer")
	}
	if cmd == nil {
		t.Fatal("confirming produced no join")
	}
	cmd() // the command is what calls the backend
	if len(b.joined) != 1 || b.joined[0] != "#book-club:example.org" {
		t.Fatalf("joined %+v, want [#book-club:example.org]", b.joined)
	}
	if len(b.joinedVia) != 1 || b.joinedVia[0] != "one.org" {
		t.Errorf("joined via %+v, want [one.org]", b.joinedVia)
	}
}

// A link handed over by the desktop while running moves the view and says so.
func TestAHandedOverLinkMovesAndSaysSo(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	next, _ := m.handleFollow(followMsg{uri: "https://matrix.to/#/!b:x"})
	got := next

	if got.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x", got.openRoom)
	}
	if !strings.Contains(got.status(), "Bravo") {
		t.Errorf("status = %q, want it to name where it went", got.status())
	}
}

// A handed-over non-link is reported instead of moving.
func TestAHandedOverNonLinkIsReported(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	next, _ := m.handleFollow(followMsg{uri: "https://example.org/nope"})
	got := next

	if got.openRoom == "!b:x" {
		t.Error("a non-link moved the client")
	}
	if !strings.Contains(got.status(), "not a Matrix or Telegram link") {
		t.Errorf("status = %q, want it to say what was wrong", got.status())
	}
}

// A tg:// link to a chat the Telegram account sees is a move to that chat.
func TestAHandedOverTelegramLinkOpensTheChat(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "telegram:42/-1000000000021", Name: "Novo Salsa", Handle: "novosalsa"},
		{ID: "!b:x", Name: "Bravo"},
	}})

	next, _ := m.handleFollow(followMsg{uri: "tg://resolve?domain=novosalsa&post=35"})
	if next.openRoom != "telegram:42/-1000000000021" {
		t.Errorf("open room = %q, want the chat matching @novosalsa", next.openRoom)
	}
	if next.jump.to != "telegram:42/-1000000000021/35" {
		t.Errorf("jump.to = %q, want the post as a kith event ID", next.jump.to)
	}
	if !strings.Contains(next.status(), "Novo Salsa") {
		t.Errorf("status = %q, want it to name the chat", next.status())
	}
}

// A tg:// link whose @username matches no joined chat says so instead of guessing.
func TestAHandedOverTelegramLinkToAnUnknownChatIsReported(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	next, _ := m.handleFollow(followMsg{uri: "tg://resolve?domain=novosalsa"})
	if next.openRoom == "telegram:42/-1000000000021" {
		t.Error("an unknown Telegram @username moved the client")
	}
	if !strings.Contains(next.status(), "no Telegram chat") {
		t.Errorf("status = %q, want it to say the chat was not found", next.status())
	}
}

// A tg://privatepost resolves by the channel's -100<id> native, so a shared private
// channel link opens its kith room.
func TestAHandedOverTelegramPrivateLinkOpensTheChannel(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel())
	m = update(t, m, roomsMsg{rooms: []domain.Room{
		{ID: "telegram:42/-1000000000021", Name: "Private channel"},
	}})

	next, _ := m.handleFollow(followMsg{uri: "tg://privatepost?channel=21&post=78"})
	if next.openRoom != "telegram:42/-1000000000021" {
		t.Errorf("open room = %q, want the private channel's room", next.openRoom)
	}
	if next.jump.to != "telegram:42/-1000000000021/78" {
		t.Errorf("jump.to = %q, want the post", next.jump.to)
	}
}

// A client started for a link waits for the room list: until it arrives, every room
// looks unjoined.
func TestAStartupLinkWaitsForTheRoomList(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel()).WithFollow("https://matrix.to/#/!b:x")

	// No rooms yet: the request stands instead of being spent on an empty list.
	if _, _, followed := m.followPendingLink(); followed {
		t.Fatal("the link was followed before the room list arrived")
	}
	if m.followAt == "" {
		t.Fatal("the pending link was cleared while it was still pending")
	}

	m = withRooms(t, m)
	if m.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x — the rooms arrived and nothing followed", m.openRoom)
	}
	if m.followAt != "" {
		t.Error("the link stayed pending after it was followed")
	}
}

// raising is a sized model whose [clipboard] raise_command is command.
func raising(t *testing.T, command string) Model {
	t.Helper()
	cfg := config.Config{Clipboard: config.Clipboard{RaiseCommand: command}}
	return sized(t, newModel().WithConfigFile("", cfg))
}

// raiseMarker is a raise command that writes its parent's PID to a file, and that file.
func raiseMarker(t *testing.T) (command, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "raised")
	return `echo "$PPID" > '` + marker + `'`, marker
}

// A handed-over link runs raise_command in this process, so it sees this window's
// environment; $PPID is kith.
func TestAHandedOverLinkRaisesTheWindow(t *testing.T) {
	t.Parallel()

	command, marker := raiseMarker(t)
	m := withRooms(t, raising(t, command))
	next, cmd := asModel(m.Update(followMsg{uri: "https://matrix.to/#/!b:x"}))
	raised, ok := msgOf[raisedMsg](t, cmd)
	if !ok {
		t.Fatal("a handed-over link did not run raise_command")
	}
	if raised.err != nil {
		t.Fatalf("raise_command failed: %v", raised.err)
	}
	if next.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x", next.openRoom)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("raise_command left no mark: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); strings.TrimSpace(string(got)) != want {
		t.Errorf("$PPID = %q, want kith's own PID %s", strings.TrimSpace(string(got)), want)
	}
}

// The link a client was started with does not raise: that terminal was just opened.
func TestAStartupLinkDoesNotRaise(t *testing.T) {
	t.Parallel()

	command, marker := raiseMarker(t)
	m := raising(t, command).WithFollow("https://matrix.to/#/!b:x")
	next, cmd := asModel(m.Update(roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
		{ID: "!b:x", Name: "Bravo", IsDirect: true},
	}}))
	if next.openRoom != "!b:x" {
		t.Fatalf("open room = %q, want !b:x — the startup link was not followed", next.openRoom)
	}
	if _, ok := msgOf[raisedMsg](t, cmd); ok {
		t.Error("the startup link ran raise_command")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the startup link ran raise_command")
	}
}

// With no raise_command, following a handed-over link runs nothing.
func TestNoRaiseCommandRunsNothing(t *testing.T) {
	t.Parallel()

	m := withRooms(t, raising(t, ""))
	_, cmd := asModel(m.Update(followMsg{uri: "https://matrix.to/#/!b:x"}))
	if _, ok := msgOf[raisedMsg](t, cmd); ok {
		t.Error("an empty raise_command ran")
	}
}

// A raise_command that fails says so; the link was still followed.
func TestAFailingRaiseCommandIsReported(t *testing.T) {
	t.Parallel()

	m := withRooms(t, raising(t, "exit 3"))
	next, cmd := asModel(m.Update(followMsg{uri: "https://matrix.to/#/!b:x"}))
	raised, ok := msgOf[raisedMsg](t, cmd)
	if !ok || raised.err == nil {
		t.Fatalf("raisedMsg = %+v, %v; want the failure", raised, ok)
	}
	next = update(t, next, raised)
	if next.openRoom != "!b:x" {
		t.Errorf("open room = %q, want !b:x", next.openRoom)
	}
	if !strings.Contains(next.status(), "could not raise this window") {
		t.Errorf("status = %q, want it to say the window was not raised", next.status())
	}
}
