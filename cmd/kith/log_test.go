package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/logging"
)

// captureStderr runs f with os.Stderr redirected, returning what was written.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = saved }()
	f()
	_ = w.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// Whatever the target and whether or not there is a journal, the client's log never
// reaches the terminal the TUI draws on.
func TestClientLogNeverWritesStderr(t *testing.T) {
	saved := slog.Default()
	t.Cleanup(func() { slog.SetDefault(saved) })
	var sent []string
	up := logging.Journal{
		Available: func() bool { return true },
		Send: func(message string, _ int, _ map[string]string) error {
			sent = append(sent, message)
			return nil
		},
	}
	down := logging.Journal{
		Available: func() bool { return false },
		Send:      up.Send,
	}
	for _, tc := range []struct {
		name    string
		target  logging.Target
		journal logging.Journal
		file    bool
	}{
		{"auto with journal", logging.TargetAuto, up, false},
		{"auto without journal", logging.TargetAuto, down, true},
		{"journal without journal", logging.TargetJournal, down, true},
		{"file", logging.TargetFile, up, true},
	} {
		sent = nil
		file := filepath.Join(t.TempDir(), "state", logging.FileName)
		out := captureStderr(t, func() {
			log, closeLog := openLogTo(logging.Destination{
				Target: tc.target, Identifier: "kith", File: file, Journal: &tc.journal, Level: slog.LevelDebug,
			})
			log.Warn("a warning", "room", "!r:x")
			slog.Error("through the default") // what a library logging via slog does
			closeLog()
		})
		if out != "" {
			t.Errorf("%s: wrote to stderr: %q", tc.name, out)
		}
		data, _ := os.ReadFile(file)
		if tc.file != strings.Contains(string(data), "a warning") || tc.file == (len(sent) == 2) {
			t.Errorf("%s: file %q, journal %q; want the file: %v", tc.name, data, sent, tc.file)
		}
	}
}
