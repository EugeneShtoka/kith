package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// `kith export` writes every room to its own Markdown file, as /export writes one:
// the same span, names and file names. A model with no terminal loads what naming reads
// (the config, the rooms, the phone book, who is me, the people rooms are named after)
// the way a running client does.

// Exported is one room's export: where it went and how many messages it holds.
type Exported struct {
	Room  string
	Path  string
	Count int
}

// ExportAll writes each of the account's rooms with cached messages in span to dir, and
// says each as it is written. A room that fails is told in the error; the rest still go.
func ExportAll(ctx context.Context, backend api.Backend, cfg config.Config, span domain.SummarySpan, dir string,
	said func(Exported),
) error {
	m, err := exporting(ctx, backend, cfg)
	if err != nil {
		return err
	}
	at := time.Now()
	var failed []error
	for i := range m.rooms.joined {
		room := &m.rooms.joined[i]
		of := domain.ExportOf{
			Title:   m.roomName(*room),
			Network: domain.ParseID(string(room.ID)).Network.String(),
			Span:    span.Said,
			At:      at,
		}
		done := exportRoom(ctx, backend, room.ID, "", of, span, m.people(), m.prefs.clock, dir, "")
		switch {
		case errors.Is(done.err, errNothingToExport):
		case done.err != nil:
			failed = append(failed, fmt.Errorf("%s: %w", of.Title, done.err))
		default:
			said(Exported{Room: of.Title, Path: done.path, Count: done.count})
		}
		if ctx.Err() != nil {
			return fmt.Errorf("export stopped: %w", ctx.Err())
		}
	}
	return errors.Join(failed...)
}

// exporting is a model with cfg's display (your names, your clock) and what naming
// reads loaded.
func exporting(ctx context.Context, backend api.Backend, cfg config.Config) (Model, error) {
	m := New(ctx, backend, cfg.Display, config.DeviceDisplay{}) // an export reads no device setting
	rooms, err := backend.Rooms(ctx)
	if err != nil {
		return Model{}, fmt.Errorf("list the rooms: %w", err)
	}
	m.rooms = m.rooms.withJoined(rooms)
	dir, err := backend.Directory(ctx)
	if err != nil {
		return Model{}, fmt.Errorf("read the phone book: %w", err)
	}
	m.dir = directoryState{dir: dir}
	if m.selves, err = backend.Selves(ctx); err != nil {
		return Model{}, fmt.Errorf("read who you are: %w", err)
	}
	m, load := m.refreshPeers()
	if load != nil {
		if peers, ok := load().(peerMembersMsg); ok {
			m.peers.members = peers.members
		}
	}
	return m.withPeerAliases(), nil
}
