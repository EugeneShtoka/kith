package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// personBackend records the room VerifyPerson was asked about.
type personBackend struct {
	apitest.Nop
	asked chan domain.RoomID
}

func (b personBackend) VerifyPerson(_ context.Context, room domain.RoomID) (string, error) {
	b.asked <- room
	return mx + "$request", nil
}

// /verify-person asks about the room written in and waits for its person, saying so.
func TestVerifyAsksTheRoomsPerson(t *testing.T) {
	t.Parallel()
	b := personBackend{asked: make(chan domain.RoomID, 1)}
	m := sized(t, withRooms(t, newModel()))
	m.backend = b

	_, m, cmd := m.composerCommand("/verify-person", roomByName(t, m, mx+"!a:x"))
	if cmd == nil {
		t.Fatalf("nothing asked; said %q", m.status())
	}
	started, ok := cmd().(verifyStartedMsg)
	if !ok {
		t.Fatal("the command did not start a verification")
	}
	m, _ = m.handleVerifyStarted(started)
	if got := <-b.asked; got != mx+"!a:x" {
		t.Errorf("asked about %s, want the room written in", got)
	}
	if m.compose.input != "" {
		t.Errorf("the composer kept %q", m.compose.input)
	}
	if !m.verify.active || !m.verify.ours || !m.verify.person || m.verify.txnID != mx+"$request" {
		t.Fatalf("overlay = %+v, want our request to a person", m.verify)
	}
	if view := m.verifyView(); !strings.Contains(view, "Asked them") || strings.Contains(view, "[y]") {
		t.Errorf("view:\n%s", view)
	}

	m, _ = m.handleVerify(verifyMsg{v: domain.Verification{Kind: domain.VerificationDone, TxnID: mx + "$request"}})
	if m.verify.active || m.status() != "✓ verified each other" {
		t.Errorf("after done: overlay %+v, said %q", m.verify, m.status())
	}
}

// A person asking to verify you is told apart from one of your own sessions.
func TestAPersonsRequestSaysItIsForYou(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		person bool
		want   string
	}{{true, "wants to verify you."}, {false, "wants to verify this session."}} {
		m := sized(t, withRooms(t, newModel()))
		m, _ = m.handleVerify(verifyMsg{v: domain.Verification{
			Kind: domain.VerificationRequested, TxnID: "t", From: "matrix:@ann:x", Device: "D", Person: c.person,
		}})
		if view := m.verifyView(); !strings.Contains(view, c.want) {
			t.Errorf("person %v: view lacks %q:\n%s", c.person, c.want, view)
		}
	}
}
