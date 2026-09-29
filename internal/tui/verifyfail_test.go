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

// failingVerify refuses both controls, which is what the daemon being unreachable looks
// like from here — a restart mid-flow, a dropped socket, a canceled context.
type failingVerify struct {
	apitest.Nop
}

func (failingVerify) AcceptVerification(context.Context, string) error { return errNoDaemonReach }

// verifyBackend records what the overlay asked of it, which for an outgoing request is
// the whole of what there is to observe: the key that means nothing must reach nothing.
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

// keyOf is one keypress of a letter.
func keyOf(r rune) tea.KeyPressMsg                             { return tea.KeyPressMsg{Code: r} }
func (failingVerify) ConfirmSAS(context.Context, string) error { return errNoDaemonReach }

// A control that never reached the daemon must release the overlay's waiting state.
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

			m := sized(t, withRooms(t, New(context.Background(), failingVerify{}, config.Display{})))
			m = update(t, m, verifyMsg{v: domain.Verification{
				Kind: domain.VerificationRequested, TxnID: "t1", From: "@me:x", Device: "DEV",
			}})
			if tc.stage == domain.VerificationSAS {
				m = update(t, m, verifyMsg{v: domain.Verification{
					Kind: domain.VerificationSAS, TxnID: "t1", Emojis: tc.sas,
				}})
			}

			// Pressing the confirm key arms the wait and issues the control.
			m, cmd := press(t, m, keyText("y"))
			if !m.verify.waiting {
				t.Fatalf("expected the overlay to be waiting: %+v", m.verify)
			}
			if cmd == nil {
				t.Fatal("expected a command to be issued")
			}

			// Running it delivers verifyFailedMsg, which is what has to arrive: before
			// this existed the command returned nil and Update was never re-entered.
			m = settle(t, m, cmd)

			if m.verify.waiting {
				t.Error("waiting is still set after the control failed — the overlay is " +
					"stuck and the keyboard never comes back")
			}
			if !strings.Contains(m.st.event, "verification failed") {
				t.Errorf("status = %q, want it to name the failure", m.st.event)
			}

			// The point of clearing waiting: the user can now decline.
			m, _ = press(t, m, keyText("n"))
			if m.verify.active {
				t.Errorf("cancel should dismiss the overlay after a failure: %+v", m.verify)
			}
		})
	}
}

// Starting a verification is the direction that was missing: everything after the first
// move already worked, and a fresh session had to be verified from another client.
func TestStartingAVerificationRaisesAWaitingOverlay(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.handleVerifyStarted(verifyStartedMsg{txnID: "$txn"})
	m = next

	if !m.verify.active || !m.verify.ours || m.verify.txnID != "$txn" {
		t.Fatalf("overlay = %+v, want an outgoing verification on that transaction", m.verify)
	}
	view := m.verifyView()
	if !strings.Contains(view, "Accept it there") {
		t.Errorf("the overlay does not say where to accept it:\n%s", view)
	}
	// **No [y].** There is nothing to accept — the other device is the one deciding —
	// and offering the key would be offering to answer ourselves.
	if strings.Contains(view, "[y]") {
		t.Errorf("an outgoing request offers [y]:\n%s", view)
	}
	if !strings.Contains(view, "[n]") {
		t.Errorf("an outgoing request cannot be called off:\n%s", view)
	}
}

// And the key does nothing either, because the overlay owns the keyboard: a [y] that
// fell through would reach the pane behind it.
func TestConfirmDoesNothingOnOurOwnRequest(t *testing.T) {
	t.Parallel()

	b := &verifyBackend{}
	m := sized(t, withRooms(t, newModel()))
	m.backend = b
	next, _ := m.handleVerifyStarted(verifyStartedMsg{txnID: "$txn"})
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

// The emoji arrive on the same path an incoming verification takes, so the outgoing
// flow needs nothing of its own past the first move.
func TestOurOwnRequestShowsTheEmojiWhenTheyArrive(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.handleVerifyStarted(verifyStartedMsg{txnID: "$txn"})
	m = next

	sas := domain.Verification{
		Kind:  domain.VerificationSAS,
		TxnID: "$txn",
		Emojis: []domain.SASEmoji{
			{Glyph: "🐶", Name: "Dog"}, {Glyph: "🌍", Name: "Globe"},
		},
	}
	after, _ := m.handleVerify(verifyMsg{v: sas})
	m = after
	if m.verify.stage != domain.VerificationSAS || len(m.verify.emojis) != 2 {
		t.Fatalf("overlay = %+v, want the emoji", m.verify)
	}
	if view := m.verifyView(); !strings.Contains(view, "Do they match?") {
		t.Errorf("the emoji step does not ask:\n%s", view)
	}
}

// A session with no crypto machine has nothing to prove and nothing to prove it with.
func TestStartingWithoutEncryptionSaysSo(t *testing.T) {
	t.Parallel()

	m := sized(t, withRooms(t, newModel()))
	next, _ := m.handleVerifyStarted(verifyStartedMsg{err: api.ErrNoEncryption})
	m = next
	if m.verify.active {
		t.Error("an overlay was raised for a verification that never started")
	}
	if !strings.Contains(m.status(), "encryption") {
		t.Errorf("status = %q, want it to name what is missing", m.status())
	}
}
