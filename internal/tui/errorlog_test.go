package tui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/logging"
)

// bufferLog is a debug-level logger writing to a buffer, as the client's log file would.
func bufferLog() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return logging.New(&buf, logging.Options{Level: slog.LevelDebug, NoTime: true}), &buf
}

// A refused mark-read says why on the status line and in the log: "could not mark X
// read" alone hid a homeserver rejecting every receipt for hours.
func TestMarkReadRefusalShowsAndLogsTheReason(t *testing.T) {
	t.Parallel()

	const reason = "daemon: mark rooms read: M_BAD_JSON (HTTP 400): Invalid JSON"
	m, b := reading(t)
	log, buf := bufferLog()
	m = m.WithLogger(log)
	b.result = domain.ReadResult{Failed: 1, FirstError: reason}
	m.focus = paneRooms
	m.rail.cursor = indexOfGroup(m.rail.groups, "home")
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m, cmd := press(t, next, keyText("m"))
	if cmd == nil {
		t.Fatal("m sent no command")
	}
	msg, ok := msgOf[markedReadMsg](t, cmd)
	if !ok {
		t.Fatal("the command produced no markedReadMsg")
	}
	if msg.result.FirstError != reason {
		t.Errorf("the command dropped the reason: %+v", msg.result)
	}
	m = update(t, m, msg)
	if want := "could not mark Alpha read: " + reason; m.status() != want {
		t.Errorf("status = %q, want %q", m.status(), want)
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "M_BAD_JSON") {
		t.Errorf("log = %q, want a warn line with the reason", out)
	}
}

// A call that failed outright is shown and logged with its error.
func TestMarkReadErrorShowsAndLogsTheReason(t *testing.T) {
	t.Parallel()

	m, _ := reading(t)
	log, buf := bufferLog()
	m = update(t, m.WithLogger(log), markedReadMsg{label: "Work", rooms: 3, err: errors.New("daemon: socket gone")})
	if !strings.Contains(m.status(), "socket gone") {
		t.Errorf("status = %q, want the reason", m.status())
	}
	if !strings.Contains(buf.String(), "socket gone") {
		t.Errorf("log = %q, want the reason", buf.String())
	}
}

// A group's refused rooms carry the first reason too.
func TestReadStatusNamesTheFirstRefusal(t *testing.T) {
	t.Parallel()

	got := readStatus(markedReadMsg{label: "Work", rooms: 5,
		result: domain.ReadResult{Marked: 4, Failed: 1, FirstError: "M_FORBIDDEN"}})
	if !strings.Contains(got, "1 room refused: M_FORBIDDEN") {
		t.Errorf("readStatus() = %q, want the refusal's reason", got)
	}
}

// sayErr puts the reason on screen and a warn line in the log; nil says the text alone.
func TestSayErrShowsAndLogs(t *testing.T) {
	t.Parallel()

	log, buf := bufferLog()
	m := newModel().WithLogger(log)
	m = m.sayErr("could not delete it", errors.New("M_FORBIDDEN"), "room", "!r:x")
	if m.status() != "could not delete it: M_FORBIDDEN" {
		t.Errorf("status = %q", m.status())
	}
	for _, want := range []string{"level=WARN", `msg="could not delete it"`, "err=M_FORBIDDEN", "room=!r:x"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("log %q lacks %q", buf.String(), want)
		}
	}
	buf.Reset()
	if m = m.sayErr("nothing wrong", nil); m.status() != "nothing wrong" || buf.Len() != 0 {
		t.Errorf("nil err: status %q, log %q", m.status(), buf.String())
	}
}

// fire reports nothing on screen but logs a failing write under its op.
func TestFireLogsAFailure(t *testing.T) {
	t.Parallel()

	log, buf := bufferLog()
	cmd := fire(context.Background(), log, "save draft", func(context.Context) error {
		return errors.New("database is locked")
	})
	if msg := cmd(); msg != nil {
		t.Errorf("fire returned %v, want nil", msg)
	}
	if out := buf.String(); !strings.Contains(out, "level=WARN") || !strings.Contains(out, "op=\"save draft\"") ||
		!strings.Contains(out, "database is locked") {
		t.Errorf("log = %q, want a warn line naming the op and the error", out)
	}

	buf.Reset()
	fire(context.Background(), log, "ok", func(context.Context) error { return nil })()
	if buf.Len() != 0 {
		t.Errorf("a success was logged: %q", buf.String())
	}

	// Quitting cancels in-flight writes: debug, not warn.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fire(ctx, log, "send typing", func(ctx context.Context) error { return ctx.Err() })()
	if out := buf.String(); !strings.Contains(out, "level=DEBUG") {
		t.Errorf("a canceled write logged as %q, want debug", out)
	}
}

// A background read that fails is logged, not dropped.
func TestBackgroundReadFailureIsLogged(t *testing.T) {
	t.Parallel()

	log, buf := bufferLog()
	m := newModel().WithLogger(log)
	_ = update(t, m, spacesMsg{err: errors.New("daemon: spaces: cache closed")})
	if !strings.Contains(buf.String(), "load spaces") || !strings.Contains(buf.String(), "cache closed") {
		t.Errorf("log = %q, want the failed read", buf.String())
	}
}
