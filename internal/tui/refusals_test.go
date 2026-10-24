package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
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
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Chat"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@telegram_12345:x", SenderName: "Ada", Body: "hi", Timestamp: at(1)},
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
	native = update(t, native, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@ada:example.org", Timestamp: at(1)},
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

// toneRefusingBackend refuses every toned reaction and records each one sent.
type toneRefusingBackend struct {
	refusingBackend
	sent []string
}

func (b *toneRefusingBackend) SendReaction(_ context.Context, _ domain.RoomID, _ domain.EventID, key string) error {
	b.sent = append(b.sent, key)
	if withoutTone(key) != key {
		return errors.New("REACTION_INVALID")
	}
	return nil
}

// bridgedWhatsApp is a room bridged from a network that may take a toned reaction.
func bridgedWhatsApp(t *testing.T, b api.Backend) Model {
	t.Helper()
	m := update(t, starterNew(b, config.Display{}),
		roomsMsg{rooms: []domain.Room{{ID: mx + "!a:x", Name: "Chat"}}})
	m = sized(t, m)
	m, _ = m.selectRoom(m.filteredRooms()[0])
	m = update(t, m, timelineMsg{roomID: mx + "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: mx + "$1", RoomID: mx + "!a:x", Sender: "matrix:@whatsapp_15550100009:x", SenderName: "Ada", Body: "hi", Timestamp: at(1)},
	}}})
	m.focus, m.compose.insertMode = paneTimeline, false
	m.glyphs.skin = light
	return m
}

// A toned reaction the network refuses goes again without its tone, and from then on
// reactions there go plain; the plain emoji stays on offer.
func TestARefusedToneIsSentPlain(t *testing.T) {
	t.Parallel()
	b := &toneRefusingBackend{}
	m := bridgedWhatsApp(t, b)
	m, cmd := m.sendReaction(m.tone("👍"))
	m = deliverTwice(t, m, cmd)
	if len(b.sent) != 2 || b.sent[0] != "👍🏻" || b.sent[1] != "👍" {
		t.Fatalf("sent %q, want 👍🏻 then 👍", b.sent)
	}
	if !strings.Contains(m.status(), "sent 👍 instead") {
		t.Errorf("status = %q", m.status())
	}
	if m.refusedIn(m.roomProtocol(), "👍") {
		t.Error("a refused tone took the plain emoji off the palette")
	}
	m, cmd = m.sendReaction(m.tone("👋"))
	_ = deliver(t, m, cmd)
	if last := b.sent[len(b.sent)-1]; last != "👋" || len(b.sent) != 3 {
		t.Errorf("after a refused tone: sent %q, want 👋 alone, untoned", b.sent[2:])
	}
}

// A bridge that takes a toned reaction and then refuses it (the daemon records the
// refusal): the reaction goes again without its tone, once.
func TestATonedReactionABridgeRefusesLaterGoesPlain(t *testing.T) {
	t.Parallel()
	rb := &recordingReact{}
	m := bridgedWhatsApp(t, rb)
	m, cmd := m.sendReaction(m.tone("👍"))
	runCmd(t, cmd)
	if rb.key != "👍🏻" {
		t.Fatalf("first sent %q", rb.key)
	}
	refusals := []domain.ReactionRefusal{{Protocol: "WhatsApp", Emoji: "👍🏻", At: time.Now()}}
	m, cmd = m.handleRefusals(refusalsMsg{refusals: refusals})
	if cmd == nil {
		t.Fatal("a refused toned reaction was not sent again")
	}
	runCmd(t, cmd)
	if rb.key != "👍" {
		t.Errorf("resent %q, want 👍", rb.key)
	}
	if _, again := m.handleRefusals(refusalsMsg{refusals: refusals}); again != nil {
		t.Error("the same refusal resent the reaction a second time")
	}
}
