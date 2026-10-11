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

// Drafts are saved after a pause (draftSaveDelay), at least every draftSaveEvery while
// typing, when a room is left, and on quit. At exit unsaved drafts are written once
// more; what the daemon doesn't take then (down, or another window has the seat) is
// lost: at most the last few seconds of typing.

// exitFlushWait bounds the drafts' last write at exit, when the daemon may be gone.
const exitFlushWait = 3 * time.Second

// flushDrafts writes every unstored draft once the program has ended; every exit path
// passes here, and only quit waits for its saves. A save still in flight is written
// again; its version may be this client's own, so the retry doesn't mistake it for
// another writer's. Returns the rooms it couldn't write. ctx must outlive the program's.
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

// flushAtExit writes the drafts still unsaved at exit, reporting those the daemon refused.
func flushAtExit(last Model, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), exitFlushWait)
	defer cancel()
	if lost := last.flushDrafts(ctx); len(lost) > 0 {
		log.Warn("drafts not saved at exit", "drafts", len(lost))
		fmt.Fprintf(os.Stderr, "kith: the last words typed in %d draft(s) were not saved: the daemon did not take them\n", len(lost))
	}
}
