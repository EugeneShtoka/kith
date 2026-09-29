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

// Where the daemon logs, by how it was started and what the target says.
func TestDaemonLogChoice(t *testing.T) {
	t.Setenv(logging.EnvTarget, "")
	var sent int
	journalWith := func(up bool) logging.Journal {
		return logging.Journal{
			Available: func() bool { return up },
			Send: func(string, int, map[string]string) error {
				sent++
				return nil
			},
		}
	}
	for _, tc := range []struct {
		name          string
		target        string
		file          bool
		underJournald bool
		journalUp     bool
		want          string // journal, file or stderr
	}{
		{"systemd unit, auto", "", false, true, true, "stderr"},
		{"systemd unit, journal", "journal", false, true, true, "stderr"},
		{"systemd unit, file", "file", true, true, true, "file"},
		{"started by kith, journal up", "", true, false, true, "journal"},
		{"started by kith, no journal", "", true, false, false, "file"},
		{"started by kith, file", "file", true, false, true, "file"},
		{"terminal, auto", "", false, false, true, "stderr"},
		{"terminal, journal up", "journal", false, false, true, "journal"},
		{"terminal, journal down", "journal", false, false, false, "stderr"},
	} {
		sent = 0
		path := ""
		if tc.file {
			path = filepath.Join(t.TempDir(), "kithd.log")
		}
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stderr
		os.Stderr = w
		log, closeLog := newLogger(daemonLog{
			configured: tc.target, file: path, identifier: "kithd", level: slog.LevelInfo,
			underJournald: tc.underJournald, journal: journalWith(tc.journalUp),
		})
		log.Info("line")
		closeLog()
		os.Stderr = saved
		_ = w.Close()
		errOut, _ := io.ReadAll(r)
		onStderr := strings.Contains(string(errOut), "msg=line")
		inFile := false
		if path != "" {
			data, _ := os.ReadFile(path)
			inFile = strings.Contains(string(data), "msg=line")
		}
		got := "nowhere"
		switch {
		case onStderr:
			got = "stderr"
		case sent > 0:
			got = "journal"
		case inFile:
			got = "file"
		}
		if got != tc.want {
			t.Errorf("%s: logged to %s, want %s", tc.name, got, tc.want)
		}
	}
}
