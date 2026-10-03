package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// refusingBackend fails every reaction and records what it was told to remember.
type refusingBackend struct {
	apitest.Nop
	err       error
	refusals  []domain.ReactionRefusal
	recorded  []string
	protocols []string
}

func (b *refusingBackend) SendReaction(context.Context, domain.RoomID, domain.EventID, string) error {
	return b.err
}

func (b *refusingBackend) ReactionRefusals(context.Context) ([]domain.ReactionRefusal, error) {
	return b.refusals, nil
}

func (b *refusingBackend) RecordReactionRefusal(_ context.Context, protocol, emoji string) error {
	b.protocols = append(b.protocols, protocol)
	b.recorded = append(b.recorded, emoji)
	return nil
}

func bridged(t *testing.T, b *refusingBackend) Model {
	t.Helper()
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Chat"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@telegram_12345:x", SenderName: "Ada", Body: "hi", Timestamp: at(1)},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false
	return m
}

// The room's network is read from its participants' ghost MXIDs.
func TestRoomProtocolFromParticipants(t *testing.T) {
	t.Parallel()

	m := bridged(t, &refusingBackend{})
	if got := m.roomProtocol(); !got.IsBridged() || !strings.EqualFold(got.String(), "telegram") {
		t.Errorf("roomProtocol = %q, want telegram", got)
	}

	native := sized(t, withRooms(t, newModel()))
	native = update(t, native, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@ada:example.org", Timestamp: at(1)},
	}}})
	if got := native.roomProtocol(); got.IsBridged() {
		t.Errorf("roomProtocol = %q for a native room, want Matrix", got)
	}
}

// A refusal on record keeps the emoji (any tone) out of the grid and quick palette.
func TestKnownRefusalsAreNotOffered(t *testing.T) {
	t.Parallel()

	b := &refusingBackend{refusals: []domain.ReactionRefusal{
		{Protocol: "Telegram", Emoji: "🫶", At: time.Now()},
	}}
	m := bridged(t, b)
	m = update(t, m, refusalsMsg{refusals: b.refusals})

	m, _ = m.openReactionPalette()
	for _, item := range m.picker.items {
		if bareEmoji(item.value) == "🫶" {
			t.Error("the grid still offers a reaction this network refused")
		}
	}
	if got := m.allowedReactions([]string{"👍", "🫶", "🎉"}); len(got) != 2 {
		t.Errorf("allowedReactions = %v, want the refused one dropped", got)
	}
	if got := m.allowedReactions([]string{"🫶\U0001F3FB"}); len(got) != 0 {
		t.Errorf("allowedReactions = %v, want the toned form refused too", got)
	}
}

// A native Matrix room is never filtered.
func TestNativeRoomsAreNotFiltered(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	m = update(t, m, refusalsMsg{refusals: []domain.ReactionRefusal{{Protocol: "Telegram", Emoji: "🫶"}}})
	if got := m.allowedReactions([]string{"👍", "🫶"}); len(got) != 2 {
		t.Errorf("allowedReactions = %v, want everything: this room is not bridged", got)
	}
}

// A failed send is learned and announced.
func TestFailedReactionIsLearnedAndAnnounced(t *testing.T) {
	t.Parallel()

	b := &refusingBackend{err: errUnsupported{}}
	m := bridged(t, b)

	m, _ = press(t, m, keyText("e"))    // the react prompt
	m, cmd := press(t, m, keyText("1")) // first palette slot: sends immediately
	if cmd == nil {
		t.Fatal("no send command")
	}
	m = deliver(t, m, cmd)

	if !strings.Contains(m.status(), "would not take") {
		t.Errorf("status = %q, want it to say the network refused it", m.status())
	}
	if len(m.glyphs.refused["Telegram"]) == 0 {
		t.Fatalf("nothing learned: %v", m.glyphs.refused)
	}
}

type errUnsupported struct{}

func (errUnsupported) Error() string { return "m.reaction not supported by this network" }
