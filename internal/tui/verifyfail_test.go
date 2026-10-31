package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

var errNoDaemonReach = errors.New("accept verification: connection refused")

// failingVerify refuses both controls, as an unreachable daemon would (a restart, a
// dropped socket, a canceled context).
type failingVerify struct {
	apitest.Nop
}

func (failingVerify) AcceptVerification(context.Context, string) error { return errNoDaemonReach }

// verifyBackend records what the overlay asked of it; for an outgoing request a key
// that means nothing must reach nothing.
type verifyBackend struct {
	apitest.Nop
	mu   sync.Mutex
	took []string
}

func (b *verifyBackend) AcceptVerification(_ context.Context, txnID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.took = append(b.took, txnID)
	return nil
}

func (b *verifyBackend) accepted() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.took...)
}

func keyOf(r rune) tea.KeyPressMsg                             { return tea.KeyPressMsg{Code: r} }
func (failingVerify) ConfirmSAS(context.Context, string) error { return errNoDaemonReach }

// A control that never reached the daemon releases the overlay's waiting state.
func TestFailedVerifyControlReleasesTheKeyboard(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		stage domain.VerificationKind
		sas   []domain.SASEmoji
	}{
		"accept fails at the request stage": {stage: domain.VerificationRequested},
		"confirm fails at the SAS stage": {
			stage: domain.VerificationSAS,
			sas:   []domain.SASEmoji{{Glyph: "🐶", Name: "Dog"}},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			m := sized(t, withRooms(t, starterNew(failingVerify{}, config.Display{})))
			m = update(t, m, verifyMsg{v: domain.Verification{
				Kind: domain.VerificationRequested, TxnID: "t1", From: "matrix:@me:x", Device: "DEV",
			}})
			if tc.stage == domain.VerificationSAS {
				m = update(t, m, verifyMsg{v: domain.Verification{
					Kind: domain.VerificationSAS, TxnID: "t1", Emojis: tc.sas,
				}})
			}

			// The confirm key arms the wait and issues the control.
			m, cmd := press(t, m, keyText("y"))
			if !m.verify.waiting {
				t.Fatalf("expected the overlay to be waiting: %+v", m.verify)
			}
			if cmd == nil {
				t.Fatal("expected a command to be issued")
			}

			// Running it delivers verifyFailedMsg, which re-enters Update.
			m = settle(t, m, cmd)

			if m.verify.waiting {
				t.Error("waiting is still set after the control failed — the overlay is " +
					"stuck and the keyboard never comes back")
			}
			if m.st.event == "" {
				t.Errorf("status = %q, want it to name the failure", m.st.event)
			}

			// With waiting cleared the user can decline.
			m, _ = press(t, m, keyText("n"))
			if m.verify.active {
				t.Errorf("cancel should dismiss the overlay after a failure: %+v", m.verify)
			}
		})
	}
}

// Starting a verification raises a waiting overlay, so a fresh session need not be
// verified from another client.
func TestStartingAVerificationRaisesAWaitingOverlay(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.handleVerifyStarted(verifyStartedMsg{txnID: mx + "$txn"})
	m = next

	if !m.verify.active || !m.verify.ours || m.verify.txnID != mx+"$txn" {
		t.Fatalf("overlay = %+v, want an outgoing verification on that transaction", m.verify)
	}
	view := m.verifyView()
	// No [y]: the other device decides, so there is nothing to accept.
	if strings.Contains(view, "[y]") {
		t.Errorf("an outgoing request offers [y]:\n%s", view)
	}
	if !strings.Contains(view, "[n]") {
		t.Errorf("an outgoing request cannot be called off:\n%s", view)
	}
}

// The key does nothing either: the overlay owns the keyboard, so a [y] can't fall
// through to the pane behind it.
func TestConfirmDoesNothingOnOurOwnRequest(t *testing.T) {
	t.Parallel()

	b := &verifyBackend{}
	m := sized(t, withRooms(t, newModel()))
	m.backend = b
	next, _ := m.handleVerifyStarted(verifyStartedMsg{txnID: mx + "$txn"})
	m = next

	after, cmd := m.handleVerifyKey(keyOf('y'))
	m = after
	if cmd != nil {
		cmd()
	}
	if got := b.accepted(); len(got) != 0 {
		t.Errorf("accepting our own request reached the backend: %v", got)
	}
	if !m.verify.active {
		t.Error("the overlay was dismissed by a key that means nothing here")
	}
}

// The emoji arrive on the incoming verification path, so the outgoing flow needs
// nothing of its own past the first move.
func TestOurOwnRequestShowsTheEmojiWhenTheyArrive(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.handleVerifyStarted(verifyStartedMsg{txnID: mx + "$txn"})
	m = next

	sas := domain.Verification{
		Kind:  domain.VerificationSAS,
		TxnID: mx + "$txn",
		Emojis: []domain.SASEmoji{
			{Glyph: "🐶", Name: "Dog"}, {Glyph: "🌍", Name: "Globe"},
		},
	}
	after, _ := m.handleVerify(verifyMsg{v: sas})
	m = after
	if m.verify.stage != domain.VerificationSAS || len(m.verify.emojis) != 2 {
		t.Fatalf("overlay = %+v, want the emoji", m.verify)
	}
	if view := m.verifyView(); !strings.Contains(view, "Dog") {
		t.Errorf("the emoji step does not ask:\n%s", view)
	}
}

func TestStartingWithoutEncryptionSaysSo(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.handleVerifyStarted(verifyStartedMsg{err: api.ErrNoEncryption})
	m = next
	if m.verify.active {
		t.Error("an overlay was raised for a verification that never started")
	}
	if m.status() == "" {
		t.Errorf("status = %q, want it to name what is missing", m.status())
	}
}

// A step answered in another window (or another TUI) waits here too, so the overlay
// does not offer an answer kithd would refuse; an acceptance arriving after the emoji
// leaves them to answer, and a step of another verification changes nothing.
func TestAStepAnsweredElsewhereWaitsHere(t *testing.T) {
	t.Parallel()

	request := domain.Verification{Kind: domain.VerificationRequested, TxnID: "t1", From: "matrix:@me:x", Device: "DEV"}
	sas := domain.Verification{Kind: domain.VerificationSAS, TxnID: "t1", Emojis: []domain.SASEmoji{{Glyph: "🐶", Name: "Dog"}}}
	tests := map[string]struct {
		steps   []domain.Verification
		waiting bool
	}{
		"accepted elsewhere": {
			steps:   []domain.Verification{request, {Kind: domain.VerificationAccepted, TxnID: "t1"}},
			waiting: true,
		},
		"confirmed elsewhere": {
			steps:   []domain.Verification{request, sas, {Kind: domain.VerificationConfirmed, TxnID: "t1"}},
			waiting: true,
		},
		"the acceptance arrives after the emoji": {
			steps: []domain.Verification{request, sas, {Kind: domain.VerificationAccepted, TxnID: "t1"}},
		},
		"another verification's answer": {
			steps: []domain.Verification{request, {Kind: domain.VerificationAccepted, TxnID: "t2"}},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			b := &verifyBackend{}
			m := sized(t, withRooms(t, newModel()))
			m.backend = b
			for _, v := range tc.steps {
				m, _ = m.handleVerify(verifyMsg{v: v})
			}
			if m.verify.waiting != tc.waiting {
				t.Fatalf("waiting = %t, want %t: %+v", m.verify.waiting, tc.waiting, m.verify)
			}
			m, cmd := m.handleVerifyKey(keyOf('y'))
			if cmd != nil {
				cmd()
			}
			if got := b.accepted(); tc.waiting && len(got) != 0 {
				t.Errorf("a step answered elsewhere was answered again: %v", got)
			}
			if !m.verify.active {
				t.Error("the overlay was dismissed")
			}
		})
	}
}
