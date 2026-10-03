package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// warden records which membership call was made, against whom.
type warden struct {
	apitest.Nop
	invited, kicked, banned, unbanned string
	err                               error
}

func (w *warden) InviteUser(_ context.Context, _ domain.RoomID, userID string) error {
	w.invited = userID
	return w.err
}

func (w *warden) KickUser(_ context.Context, _ domain.RoomID, userID, _ string) error {
	w.kicked = userID
	return w.err
}

func (w *warden) BanUser(_ context.Context, _ domain.RoomID, userID, _ string) error {
	w.banned = userID
	return w.err
}

func (w *warden) UnbanUser(_ context.Context, _ domain.RoomID, userID string) error {
	w.unbanned = userID
	return w.err
}

func (w *warden) Members(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}, nil
}

func (w *warden) MentionCandidates(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}, nil
}

func warding(t *testing.T) (Model, *warden) {
	t.Helper()
	w := &warden{}
	m := update(t, starterNew(w, config.Display{}), roomsMsg{rooms: []domain.Room{
		{ID: "!a:x", Name: "Alpha"},
	}})
	m = sized(t, m.clearStatus())
	m.me = "@me:x"
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, homeGroupKey)
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	// The people list is built from the members the room-open load fetches, so a test
	// that opens it has to have them.
	m = update(t, m, membersMsg{roomID: "!a:x", members: []domain.Member{
		{UserID: "@dana:x", DisplayName: "Dana"},
	}})
	return m.clearStatus(), w
}

// Inviting prompts for an MXID and sends it.
func TestInviteAsksForAnMXIDAndSendsIt(t *testing.T) {
	t.Parallel()

	m, w := warding(t)
	m, _ = press(t, m, keyText("i"))
	if !m.prompt.active() {
		t.Fatal("i in the room list opened no prompt")
	}
	if !strings.Contains(m.prompt.label(), "invite") {
		t.Errorf("prompt label = %q, want it to say invite", m.prompt.label())
	}
	next, cmd := m.submitInvite("@dana:x")
	mdl := next
	_ = deliver(t, mdl, cmd)
	if w.invited != "@dana:x" {
		t.Errorf("invited %q, want @dana:x", w.invited)
	}
}

// An MXID without a server part never leaves the client.
func TestInviteRefusesAMalformedMXID(t *testing.T) {
	t.Parallel()

	m, w := warding(t)
	m, _ = press(t, m, keyText("i"))
	next, cmd := m.submitInvite("@dana")
	mdl := next
	_ = deliver(t, mdl, cmd)
	if w.invited != "" {
		t.Errorf("invited %q, want nothing sent for a malformed MXID", w.invited)
	}
}

// Kick and ban ask first, naming the person, and act only on yes.
func TestKickAndBanAskFirst(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		key     string
		word    string
		got     func(*warden) string
		pending pendingAction
	}{
		{"r", "remove", func(w *warden) string { return w.kicked }, pendingKick},
		{"b", "ban", func(w *warden) string { return w.banned }, pendingBan},
	} {
		m, w := warding(t)
		m, cmd := press(t, m, keyText("p")) // the people list
		m = deliver(t, m, cmd)
		if !m.picker.active() {
			t.Fatalf("%s: p opened no people picker", tc.key)
		}
		m, _ = press(t, m, keyText(tc.key))
		if m.confirm.action != tc.pending {
			t.Fatalf("%s: pending = %v, want a question first", tc.key, m.confirm.action)
		}
		if !strings.Contains(m.confirmPrompt(), tc.word) || !strings.Contains(m.confirmPrompt(), "Dana") {
			t.Errorf("%s: question = %q, want it to name the person and the verb", tc.key, m.confirmPrompt())
		}
		if tc.got(w) != "" {
			t.Errorf("%s: acted before the question was answered", tc.key)
		}
		next, cmd := m.resolveConfirm(true)
		mdl := next
		_ = deliver(t, mdl, cmd)
		if tc.got(w) != "@dana:x" {
			t.Errorf("%s: acted on %q, want @dana:x", tc.key, tc.got(w))
		}
	}
}

// The daemon's refusal (power needed vs held) is shown as written.
func TestARefusalIsShownAsWritten(t *testing.T) {
	t.Parallel()

	m, w := warding(t)
	w.err = errors.New("matrix: not enough power in this room: you need power 50 to ban, and you have 0")
	m, _ = press(t, m, keyText("i")) // the prompt is what captures the room
	next, cmd := m.submitInvite("@dana:x")
	mdl := next
	mdl = deliver(t, mdl, cmd)
	if got := mdl.status(); !strings.Contains(got, "power 50") {
		t.Errorf("status = %q, want the refusal's own words", got)
	}
}
