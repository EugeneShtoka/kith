package launch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// installed makes PATH hold only stand-ins for programs, each exiting with its status.
// Every run appends its name and arguments as one line to the returned log.
func installed(t *testing.T, programs map[string]int) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-ins are shell scripts")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "runs")
	for name, status := range programs {
		script := "#!/bin/sh\n" +
			"{ printf '%s' " + name + "; for a in \"$@\"; do printf '\\037%s' \"$a\"; done; printf '\\n'; } >>'" + log + "'\n" +
			"exit " + strconv.Itoa(status) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return log
}

// runs reads the complete lines of log back as argvs.
func runs(t *testing.T, log string) [][]string {
	t.Helper()
	data, err := os.ReadFile(log) // #nosec G304 -- the test's own temp file
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line != "" {
			out = append(out, strings.Split(line, "\x1f"))
		}
	}
	// The last line may still be being written.
	if !strings.HasSuffix(string(data), "\n") && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out
}

// awaitRuns waits up to within for n runs: a window is started and not waited on.
func awaitRuns(t *testing.T, log string, n int, within time.Duration) [][]string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		got := runs(t, log)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var wezterm = Terminal{Name: "wezterm", Tab: []string{"wezterm", "cli", "spawn", "--"}, Window: []string{"wezterm", "start", "--"}}

// A running terminal takes the tab, and nothing else is started.
func TestATabIsTriedFirst(t *testing.T) {
	log := installed(t, map[string]int{"wezterm": 0, "kith": 0})

	opened, err := Run(context.Background(), wezterm, []string{"kith", "--follow", "matrix:r/x:example.org"})

	if err != nil || opened != "tab" {
		t.Fatalf("Run() = %q, %v; want a tab", opened, err)
	}
	want := [][]string{{"wezterm", "cli", "spawn", "--", "kith", "--follow", "matrix:r/x:example.org"}}
	// A window would be started, not waited on: give it time to show.
	if got := awaitRuns(t, log, 2, 300*time.Millisecond); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %+v\nwant %+v", got, want)
	}
}

// When the tab command fails, a window opens.
func TestAFailedTabOpensAWindow(t *testing.T) {
	log := installed(t, map[string]int{"wezterm": 1, "kith": 0})

	opened, err := Run(context.Background(), wezterm, []string{"kith"})

	if err != nil || opened != "window" {
		t.Fatalf("Run() = %q, %v; want a window", opened, err)
	}
	want := [][]string{{"wezterm", "cli", "spawn", "--", "kith"}, {"wezterm", "start", "--", "kith"}}
	if got := awaitRuns(t, log, 2, 5*time.Second); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %+v\nwant %+v", got, want)
	}
}

func TestATerminalWithNoTabCommandOpensAWindow(t *testing.T) {
	log := installed(t, map[string]int{"foot": 0, "kith": 0})

	opened, err := Run(context.Background(), Terminal{Name: "foot", Window: []string{"foot"}}, []string{"kith"})
	if err != nil || opened != "window" {
		t.Fatalf("Run() = %q, %v; want a window", opened, err)
	}
	want := [][]string{{"foot", "kith"}}
	if got := awaitRuns(t, log, 1, 5*time.Second); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %+v\nwant %+v", got, want)
	}
}

// An empty command is refused rather than opening a bare terminal.
func TestNothingToRunIsRefused(t *testing.T) {
	log := installed(t, map[string]int{"foot": 0})
	if _, err := Run(context.Background(), Terminal{Name: "foot", Window: []string{"foot"}}, nil); err == nil {
		t.Error("Run with no command succeeded")
	}
	if got := runs(t, log); len(got) != 0 {
		t.Errorf("ran %+v; want nothing", got)
	}
}

// Detect takes the first terminal present; tab-capable ones come first in the table.
func TestDetectPrefersATerminalThatCanTab(t *testing.T) {
	installed(t, map[string]int{"xterm": 0, "kitty": 0})
	got, ok := Detect()
	if !ok {
		t.Fatal("Detect found nothing with two terminals present")
	}
	if got.Name != "kitty" {
		t.Errorf("Detect() = %q, want kitty — it can put a tab in a running terminal", got.Name)
	}

	installed(t, nil)
	if _, found := Detect(); found {
		t.Error("Detect found a terminal on a machine with none")
	}
}

func TestNamedResolvesAConfiguredTerminal(t *testing.T) {
	got, ok := Named("konsole")
	if !ok || len(got.Tab) == 0 {
		t.Fatalf("Named(konsole) = %+v, %v", got, ok)
	}
	if _, found := Named("not-a-terminal"); found {
		t.Error("Named answered for a terminal nobody has heard of")
	}
}
