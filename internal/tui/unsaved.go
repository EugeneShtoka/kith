package tui

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Drafts are saved as they are typed (draftSaveDelay after a pause, draftSaveEvery at
// the latest while typing goes on), when a room is left, and on quit. What the program
// holds unsaved when it ends is written once more; what the daemon does not take then
// (it is down, or another window has the seat) is gone: at most the last few seconds
// of typing.

// exitFlushWait bounds the drafts' last write at exit, when the daemon may be gone.
const exitFlushWait = 3 * time.Second

// flushDrafts writes every draft this client holds that is not stored yet, once the
// program has ended: every way out passes here (quit, the interrupt key, a signal, the
// terminal closing), and only quit waits for the saves itself. A save still on the wire
// is abandoned and written again; its version is one this client may have written, so
// the retry does not take it for another writer's. It answers the rooms it could not
// write. ctx must outlive the program's own.
func (m Model) flushDrafts(ctx context.Context) []domain.RoomID {
	m.ctx = ctx
	m.draftSync.inflight, m.draftSync.dirty = nil, nil
	rooms := map[domain.RoomID]bool{}
	for _, keys := range []func(yield func(domain.RoomID) bool){
		maps.Keys(m.drafts), maps.Keys(m.draftSync.bases), maps.Keys(m.draftSync.retry),
		maps.Keys(m.draftSync.unsure), slices.Values(m.draftSync.pending),
	} {
		for room := range keys {
			rooms[room] = true
		}
	}
	if m.openRoom != "" {
		rooms[m.openRoom] = true
	}
	var lost []domain.RoomID
	for _, room := range slices.Sorted(maps.Keys(rooms)) {
		var save tea.Cmd
		m, save = m.requestSave(room)
		if save == nil {
			continue
		}
		if saved, ok := save().(draftSavedMsg); !ok || saved.err != nil {
			lost = append(lost, room)
		}
	}
	return lost
}

// flushAtExit writes the drafts still unsaved when the program ends, and says so when
// the daemon would not take some.
func flushAtExit(last Model, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), exitFlushWait)
	defer cancel()
	if lost := last.flushDrafts(ctx); len(lost) > 0 {
		log.Warn("drafts not saved at exit", "drafts", len(lost))
		fmt.Fprintf(os.Stderr, "kith: the last words typed in %d draft(s) were not saved: the daemon did not take them\n", len(lost))
	}
}
