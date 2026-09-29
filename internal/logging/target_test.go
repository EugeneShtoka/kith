package logging

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]Target{
		"": TargetAuto, "auto": TargetAuto, "Journal": TargetJournal, "FILE": TargetFile,
	} {
		if got, err := ParseTarget(name); err != nil || got != want {
			t.Errorf("ParseTarget(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
	if got, err := ParseTarget("  journal\t"); err != nil || got != TargetJournal {
		t.Errorf("surrounding space not trimmed: %v, %v", got, err)
	}
	if _, err := ParseTarget("syslog"); err == nil || !strings.Contains(err.Error(), "syslog") {
		t.Errorf("ParseTarget(syslog) = %v, want an error naming it", err)
	}
}

func TestResolveTargetPrecedence(t *testing.T) {
	t.Setenv(EnvTarget, "")
	if got, _ := ResolveTarget("", ""); got != TargetAuto {
		t.Errorf("nothing set = %v, want auto", got)
	}
	if got, _ := ResolveTarget("", "file"); got != TargetFile {
		t.Errorf("config only = %v, want file", got)
	}
	t.Setenv(EnvTarget, "journal")
	if got, _ := ResolveTarget("", "file"); got != TargetJournal {
		t.Errorf("env over config = %v, want journal", got)
	}
	if got, _ := ResolveTarget("file", "auto"); got != TargetFile {
		t.Errorf("flag over env = %v, want file", got)
	}
	t.Setenv(EnvTarget, "bogus")
	if _, err := ResolveTarget("", "file"); err == nil {
		t.Error("a bad $KITH_LOG_TARGET must be reported, not ignored")
	}
}

func TestIdentifier(t *testing.T) {
	t.Parallel()
	if got := Identifier("kithd", ""); got != "kithd" {
		t.Errorf("no profile = %q", got)
	}
	if got := Identifier("kithd", "work"); got != "kithd-work" {
		t.Errorf("profile = %q", got)
	}
}

func TestOpenAutoPicksJournal(t *testing.T) {
	t.Parallel()
	f := &fakeJournal{}
	j := f.journal()
	file := filepath.Join(t.TempDir(), "x.log")
	sink, err := Open(Destination{Target: TargetAuto, Identifier: "kith-work", File: file, Journal: &j})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !sink.Journal || sink.Path != "" {
		t.Errorf("auto with a journal = %+v, want the journal", sink)
	}
	sink.Logger.Info("hi", "room", "!r:x")
	if got := f.last(t); got["SYSLOG_IDENTIFIER"] != "kith-work" || got["ROOM"] != "!r:x" {
		t.Errorf("entry = %v", got)
	}
	if err := sink.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the fallback file was created although the journal answered: %v", err)
	}
}

func TestOpenFallsBackToFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := (&fakeJournal{down: true}).journal()
	for _, tc := range []struct {
		target   Target
		wantNote bool
	}{{TargetAuto, false}, {TargetJournal, true}, {TargetFile, false}} {
		file := filepath.Join(dir, string(tc.target)+".log")
		sink, err := Open(Destination{Target: tc.target, Identifier: "x", File: file, Journal: &missing})
		if err != nil {
			t.Fatalf("%s: Open: %v", tc.target, err)
		}
		if sink.Journal || sink.Path != file {
			t.Errorf("%s: sink = %+v, want the file", tc.target, sink)
		}
		sink.Logger.Info("after")
		if err := sink.Close(); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(file)
		const marker = "log target is journal"
		note := strings.Contains(string(data), marker)
		if note != tc.wantNote || strings.Count(string(data), marker) > 1 {
			t.Errorf("%s: file %q; want note %v, once", tc.target, data, tc.wantNote)
		}
		if !strings.Contains(string(data), "msg=after") {
			t.Errorf("%s: file lacks the line: %q", tc.target, data)
		}
	}
}

// TargetFile never touches the journal, even when one is there.
func TestOpenFileIgnoresJournal(t *testing.T) {
	t.Parallel()
	f := &fakeJournal{}
	j := f.journal()
	file := filepath.Join(t.TempDir(), "x.log")
	sink, err := Open(Destination{Target: TargetFile, File: file, Journal: &j})
	if err != nil || sink.Journal {
		t.Fatalf("Open = %+v, %v; want the file", sink, err)
	}
	_ = sink.Close()
	if f.count() != 0 {
		t.Error("TargetFile wrote to the journal")
	}
}

func TestOpenErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := (&fakeJournal{down: true}).journal()
	_, err := Open(Destination{Target: TargetAuto, Journal: &missing})
	if !errors.Is(err, ErrNoJournal) {
		t.Errorf("no journal, no file = %v, want ErrNoJournal", err)
	}
	blocker := filepath.Join(dir, "blocker")
	if werr := os.WriteFile(blocker, nil, 0o600); werr != nil {
		t.Fatal(werr)
	}
	if _, err := Open(Destination{Target: TargetFile, File: filepath.Join(blocker, "x.log")}); err == nil {
		t.Error("Open on an unopenable file succeeded")
	}
	if err := (&Sink{}).Close(); err != nil {
		t.Errorf("zero Sink Close = %v", err)
	}
}

func TestUnderJournald(t *testing.T) {
	t.Setenv("JOURNAL_STREAM", "")
	if UnderJournald() {
		t.Error("no JOURNAL_STREAM counted as the journal")
	}
	t.Setenv("JOURNAL_STREAM", "garbage")
	if UnderJournald() {
		t.Error("an unparsable JOURNAL_STREAM counted as the journal")
	}
	// Inherited, but naming some other stream: not ours.
	t.Setenv("JOURNAL_STREAM", "1:1")
	if UnderJournald() {
		t.Error("a JOURNAL_STREAM naming another stream counted as the journal")
	}
}

// Where there is no journald the default journal says so, and auto picks the file.
func TestSystemJournalOpens(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "x.log")
	sink, err := Open(Destination{Target: TargetAuto, Identifier: "kith-test", File: file, Level: slog.LevelError + 100})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = sink.Close() }()
	if sink.Journal != SystemJournal.Available() {
		t.Errorf("sink.Journal = %v, SystemJournal.Available() = %v", sink.Journal, SystemJournal.Available())
	}
}
