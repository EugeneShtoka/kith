package logging

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]slog.Level{
		"": slog.LevelInfo, "info": slog.LevelInfo, "DEBUG": slog.LevelDebug,
		"Warn": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError,
	} {
		got, err := ParseLevel(name)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v", name, got, err, want)
		}
	}
	if got, err := ParseLevel("  warn\t"); err != nil || got != slog.LevelWarn {
		t.Errorf("surrounding space not trimmed: %v, %v", got, err)
	}
	if _, err := ParseLevel("loud"); err == nil || !strings.Contains(err.Error(), "loud") {
		t.Errorf("ParseLevel(loud) = %v, want an error naming it", err)
	}
}

func TestResolvePrecedence(t *testing.T) {
	t.Setenv(EnvLevel, "")
	if got, _ := Resolve("", ""); got != slog.LevelInfo {
		t.Errorf("nothing set = %v, want info", got)
	}
	if got, _ := Resolve("", "error"); got != slog.LevelError {
		t.Errorf("config only = %v, want error", got)
	}
	t.Setenv(EnvLevel, "warn")
	if got, _ := Resolve("", "error"); got != slog.LevelWarn {
		t.Errorf("env over config = %v, want warn", got)
	}
	if got, _ := Resolve("debug", "error"); got != slog.LevelDebug {
		t.Errorf("flag over env = %v, want debug", got)
	}
	t.Setenv(EnvLevel, "nonsense")
	if _, err := Resolve("", "info"); err == nil {
		t.Error("a bad $KITH_LOG_LEVEL must be reported, not ignored")
	}
}

func TestNewLevelAndTime(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := New(&buf, Options{Level: slog.LevelWarn, NoTime: true})
	log.Info("quiet")
	log.Warn("mark room read failed", "room", "!r:x", "err", errors.New("M_BAD_JSON"))
	out := buf.String()
	if strings.Contains(out, "quiet") {
		t.Errorf("info written at warn level: %q", out)
	}
	if strings.Contains(out, "time=") {
		t.Errorf("NoTime still wrote a time: %q", out)
	}
	for _, want := range []string{"level=WARN", `msg="mark room read failed"`, "room=!r:x", "err=M_BAD_JSON"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}

	buf.Reset()
	New(&buf, Options{}).Info("stamped")
	if !strings.Contains(buf.String(), "time=") {
		t.Errorf("default options dropped the time: %q", buf.String())
	}
}

type stringer string

func (s stringer) String() string { return string(s) }

// No secret survives into the log, whatever shape the value has.
func TestSecretsAreScrubbed(t *testing.T) {
	t.Parallel()
	// Built at runtime so the secret scanner does not flag a fixture.
	token := "syt_" + strings.Repeat("x", 20)
	var buf bytes.Buffer
	log := New(&buf, Options{Level: slog.LevelDebug})
	reqErr := fmt.Errorf(`Put "https://hs.example/_matrix/client/v3/rooms/!r/receipt?access_token=%s": `+
		`request failed; Authorization: Bearer %s`, token, token)
	log.Warn("mark room read failed", "err", reqErr)
	log.Warn("body "+`{"password":"hunter2","type":"m.login.password"}`, "detail", stringer("api_key="+token))
	log.Warn("grouped", slog.Group("req", "hdr", "Bearer "+token, "raw", []byte("token: "+token)))
	log.Warn("bare " + token)
	out := buf.String()
	for _, secret := range []string{token, "hunter2", "xxxxxxxxxx"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked into the log:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, Redacted) || !strings.Contains(out, "receipt") {
		t.Errorf("scrubbing removed too much or nothing:\n%s", out)
	}
}

func TestFileRotates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state", FileName)
	lf, err := OpenFile(path, 64)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	line := strings.Repeat("a", 39) + "\n" // 40 bytes: two do not fit in 64
	for i := range 3 {
		if _, werr := lf.Write([]byte(line)); werr != nil {
			t.Fatalf("write %d: %v", i, werr)
		}
	}
	if cerr := lf.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
	current, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(current) != line || string(old) != line {
		t.Errorf("after 3 writes: current %q, old %q; want one line each", current, old)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("log file mode = %v, %v; want 0600", info, err)
	}
	if _, werr := lf.Write([]byte("x")); !errors.Is(werr, os.ErrClosed) {
		t.Errorf("write after Close = %v, want os.ErrClosed", werr)
	}
	if cerr := lf.Close(); cerr != nil {
		t.Errorf("second Close = %v", cerr)
	}
}

// Reopening appends and counts what is already there toward the cap.
func TestFileReopenKeepsSize(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(strings.Repeat("b", 60)), 0o600); err != nil {
		t.Fatal(err)
	}
	lf, err := OpenFile(path, 64)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() { _ = lf.Close() }()
	if lf.Path() != path {
		t.Errorf("Path = %q", lf.Path())
	}
	if _, werr := lf.Write([]byte("0123456789\n")); werr != nil {
		t.Fatal(werr)
	}
	if old, _ := os.ReadFile(path + ".1"); len(old) != 60 {
		t.Errorf("existing 60 bytes were not rotated out: %q", old)
	}
}

func TestHelpers(t *testing.T) {
	t.Parallel()
	if OrDiscard(nil) == nil || OrDiscard(Discard()) == nil {
		t.Error("OrDiscard returned nil")
	}
	Discard().Error("nothing") // must not panic
}

// A value under a secret's name is redacted whole, whatever it looks like: the value
// patterns cannot recognize "abc" as a password. Other keys keep their values.
func TestSecretKeysAreRedactedWhatever(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := New(&buf, Options{Level: slog.LevelDebug})
	log.Info("login", "password", "plainword", "Access-Token", "abc123", "api_key", 42,
		slog.Group("req", "token", "opaque"), "key", "👍", "room", "!r:x")
	out := buf.String()
	for _, secret := range []string{"plainword", "abc123", "=42", "opaque"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q under a secret key leaked:\n%s", secret, out)
		}
	}
	for _, kept := range []string{"👍", "!r:x"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was redacted, but its key names no secret:\n%s", kept, out)
		}
	}
}
