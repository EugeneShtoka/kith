package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// /export writes the open room, or the open thread, to a Markdown file
// (domain.ExportMarkdown): all of what the cache holds, or the span asked for, as
// /summary reads one ("7d", "yesterday", "2026-09-18", 500). It goes into
// [storage] export_dir, or where the person says: a folder, or a file ending in .md.
// People are named as you know them, whole; times as you have them written.

// exportPage is how many cached messages one read back fetches.
const exportPage = 2000

// exportedMsg is an export written, or why not.
type exportedMsg struct {
	path  string
	count int
	err   error
}

// openExport starts an export of room, or of its open thread.
func (m Model) openExport(room domain.Room, arg string) (Model, tea.Cmd) {
	spanArg, where := splitExportArg(arg)
	span, err := domain.ParseSummarySpan(spanArg, time.Now())
	if err != nil {
		return m.sayErr("/export", err), nil
	}
	dir, file, err := m.exportTarget(where)
	if err != nil {
		return m.sayErr("/export", err), nil
	}
	of := domain.ExportOf{
		Title:   m.roomName(room),
		Network: domain.ParseID(string(room.ID)).Network.String(),
		Span:    span.Said,
		At:      time.Now(),
	}
	thread := m.thread.root
	if thread != "" {
		of.Title += " — " + m.threadTitle()
	}
	ctx, backend, people, clock := m.ctx, m.backend, m.people(), m.prefs.clock
	return m.say("exporting " + of.Title + "…"), func() tea.Msg {
		msgs, err := cachedHistory(ctx, backend, room.ID, span)
		if err != nil {
			return exportedMsg{err: err}
		}
		if thread != "" {
			msgs = domain.ThreadMessages(msgs, thread)
		}
		msgs = domain.MessagesIn(msgs, span.Since, span.Count)
		if len(msgs) == 0 {
			return exportedMsg{err: fmt.Errorf("nothing cached to export%s", spanSaid(span))}
		}
		reactions, err := backend.CachedReactions(ctx, room.ID)
		if err != nil {
			return exportedMsg{err: err}
		}
		md := domain.ExportMarkdown(of, msgs, reactions, people.Name, clock)
		name := file
		if name == "" {
			name = exportFileName(of)
		}
		path, err := writeDownload(dir, name, []byte(md))
		return exportedMsg{path: path, count: len(msgs), err: err}
	}
}

// cachedHistory is everything the cache holds of a room, oldest first, read back a page
// at a time; it stops once it has gone past the span asked for.
func cachedHistory(ctx context.Context, backend api.Backend, room domain.RoomID, span domain.SummarySpan) ([]domain.Message, error) {
	msgs, err := backend.CachedTimeline(ctx, room)
	if err != nil {
		return nil, fmt.Errorf("read the room: %w", err)
	}
	for len(msgs) > 0 {
		if span.Count > 0 && len(msgs) >= span.Count {
			break
		}
		if !span.Since.IsZero() && msgs[0].Timestamp.Before(span.Since) {
			break
		}
		page, err := backend.MessagesAround(ctx, room, msgs[0].ID, exportPage, 0)
		if err != nil {
			return nil, fmt.Errorf("read the room further back: %w", err)
		}
		// The page ends with the message asked about, which msgs already holds.
		if len(page) > 0 && page[len(page)-1].ID == msgs[0].ID {
			page = page[:len(page)-1]
		}
		if len(page) == 0 {
			break
		}
		msgs = append(page, msgs...)
	}
	return msgs, nil
}

// splitExportArg is /export's argument as a span and a place: the place is the first
// word that is a path ("~/…", "/…", "./…") to the end, so a folder may have spaces in
// its name, and the span is what comes before it.
func splitExportArg(arg string) (span, where string) {
	words := strings.Fields(arg)
	for i, w := range words {
		at := strings.Index(arg, w) // a word of arg's own, so it is there
		if at >= 0 && (strings.HasPrefix(w, "~") || strings.HasPrefix(w, "/") || strings.HasPrefix(w, ".")) {
			return strings.Join(words[:i], " "), strings.TrimSpace(arg[at:])
		}
	}
	return strings.Join(words, " "), ""
}

// exportTarget is the folder an export goes into, and its file name when the person
// gave one (a path ending in .md); with no place given, [storage] export_dir.
func (m Model) exportTarget(where string) (dir, file string, err error) {
	if where == "" {
		dirs, derr := setup.StorageDirs(m.conf.base)
		if derr != nil {
			return "", "", derr
		}
		return dirs.ExportDir, "", nil
	}
	path, err := expandHome(where)
	if err != nil {
		return "", "", err
	}
	if strings.EqualFold(filepath.Ext(path), ".md") {
		return filepath.Dir(path), filepath.Base(path), nil
	}
	return path, "", nil
}

// spanSaid is " in <span>" for a span asked for, "" for none.
func spanSaid(span domain.SummarySpan) string {
	if span.Said == "" {
		return ""
	}
	return " in " + span.Said
}

// exportFileName is "<title> <date>.md", with nothing in it a path would read.
func exportFileName(of domain.ExportOf) string {
	title := strings.NewReplacer("/", "-", "\\", "-", "\x00", "").Replace(strings.TrimSpace(of.Title))
	if title == "" {
		title = "conversation"
	}
	return title + " " + of.At.Format("2006-01-02") + ".md"
}

// handleExported says where the export went.
func (m Model) handleExported(msg exportedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("export failed", msg.err), nil
	}
	return m.say(fmt.Sprintf("exported %d %s to %s", msg.count, plural(msg.count, "message", "messages"), msg.path)), nil
}
