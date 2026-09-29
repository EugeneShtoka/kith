package tui

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A window whose seat another took saves the draft typed and not yet saved, then
// quits, the way quit does.
func TestAWindowSteppingAsideSavesItsDraftFirst(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " unsaved")

	m, cmd := asModel(m.Update(seatLostMsg{}))
	if !m.link.seatLost {
		t.Fatal("the window does not know it lost the seat")
	}
	quit := false
	for _, msg := range msgsOf(t, cmd) {
		if _, ok := msg.(tea.QuitMsg); ok {
			quit = true
		}
		var next tea.Cmd
		m, next = asModel(m.Update(msg))
		for _, more := range msgsOf(t, next) {
			if _, ok := more.(tea.QuitMsg); ok {
				quit = true
			}
		}
	}
	if got := store.body("!a:x"); got != "hi unsaved" {
		t.Errorf("stored %q, want the unsaved words saved before quitting", got)
	}
	if !quit {
		t.Error("the window did not quit")
	}
}

// seatlessDrafts refuses every write: another window has the seat.
type seatlessDrafts struct{ *sharedDrafts }

func (seatlessDrafts) ReplaceDraft(context.Context, domain.StoredDraft, domain.StoredDraft) (bool, error) {
	return false, api.ErrSeatTaken
}

// A save refused because another window has the seat is not "maybe written", and the
// window steps aside as if it had been told.
func TestASaveRefusedForTheSeatStepsAside(t *testing.T) {
	t.Parallel()
	m, store := sharing(t)
	m.backend = seatlessDrafts{store}
	m.compose.caret = caret{owner: fieldComposer, at: len(m.compose.input)}
	m = typeInto(t, m, " more")
	m, save := tickCmd(t, m)
	m = settle(t, m, save)
	if !m.link.seatLost {
		t.Error("a window whose saves the seat refuses does not step aside")
	}
	if n := len(m.draftSync.unsure["!a:x"]); n != 0 {
		t.Errorf("%d refused write(s) kept as maybe-written", n)
	}
	if m.draftSync.retry["!a:x"] {
		t.Error("a refused write is to be retried")
	}
}
