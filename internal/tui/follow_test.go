package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// Following a link to a place, and the three answers it has — go there, ask before
// joining, or hand the question to the switcher.

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

// A message names a message, and the jump is queued for when the timeline holds it —
// the same mechanism a search hit uses.
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

// A room this account is not in is an invitation to join, and joining is visible to
// everybody already there — so it asks, naming what it would join.
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
	// The routing servers are what make a room on an unknown homeserver reachable at
	// all, so they have to survive the question.
	if len(got.confirm.via) != 1 || got.confirm.via[0] != "one.org" {
		t.Errorf("via = %+v, want [one.org]", got.confirm.via)
	}
	if q := got.confirmPrompt(); !strings.Contains(q, "#book-club:example.org") {
		t.Errorf("the question does not name the room: %q", q)
	}
}

// A person is the one case with more than one right answer, so it opens the screen that
// lists them all rather than making a room.
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

// The address in a mention pill is in the markup, not in the words — which is why this
// reads the formatted body.
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

// Several addresses ask which, the same one-or-ask rule the other link keys follow.
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
	// The room this client knows is named, not spelled: a column of room IDs is a
	// column nobody can choose between.
	if got.picker.items[0].label != "Bravo" {
		t.Errorf("first row = %q, want the room's name", got.picker.items[0].label)
	}
}

// A message full of web links has nothing for this key, and says so in words that do
// not read as "that link is broken".
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
	m := withRooms(t, sized(t, New(context.Background(), b, config.Display{})))
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

// A link handed over by the desktop while the client is running: it moves, and it says
// so.
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

// Something that is not a link says so rather than moving.
func TestAHandedOverNonLinkIsReported(t *testing.T) {
	t.Parallel()

	m := withRooms(t, sized(t, newModel()))
	next, _ := m.handleFollow(followMsg{uri: "https://example.org/nope"})
	got := next

	if got.openRoom == "!b:x" {
		t.Error("a non-link moved the client")
	}
	if !strings.Contains(got.status(), "not a Matrix link") {
		t.Errorf("status = %q, want it to say what was wrong", got.status())
	}
}

// A client started for a link waits for the room list before acting on it: "am I in
// that room" is the first question following one asks, and the answer is no for every
// room until the list arrives.
func TestAStartupLinkWaitsForTheRoomList(t *testing.T) {
	t.Parallel()

	m := sized(t, newModel()).WithFollow("https://matrix.to/#/!b:x")

	// No rooms yet: the request stands rather than being spent against an empty list.
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
