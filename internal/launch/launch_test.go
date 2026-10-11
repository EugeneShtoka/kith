package launch

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// record swaps both runners for recorders; the tab runner returns tabErr.
func record(t *testing.T, tabErr error) (tab, window *[]string) {
	t.Helper()
	var ranTab, ranWindow []string
	realStart, realDetached := start, startDetached
	start = func(_ context.Context, argv []string) error {
		ranTab = argv
		return tabErr
	}
	startDetached = func(_ context.Context, argv []string) error {
		ranWindow = argv
		return nil
	}
	t.Cleanup(func() { start, startDetached = realStart, realDetached })
	return &ranTab, &ranWindow
}

// A running terminal takes the tab, and nothing else is started.
func TestATabIsTriedFirst(t *testing.T) {
	tab, window := record(t, nil)

	opened, err := Run(context.Background(), Terminal{
		Name: "wezterm", Tab: []string{"wezterm", "cli", "spawn", "--"}, Window: []string{"wezterm", "start", "--"},
	}, []string{"/bin/kith", "--follow", "matrix:r/x:example.org"})

	if err != nil || opened != "tab" {
		t.Fatalf("Run() = %q, %v; want a tab", opened, err)
	}
	want := []string{"wezterm", "cli", "spawn", "--", "/bin/kith", "--follow", "matrix:r/x:example.org"}
	if !reflect.DeepEqual(*tab, want) {
		t.Errorf("ran %+v\nwant %+v", *tab, want)
	}
	if len(*window) != 0 {
		t.Errorf("a window was opened as well: %+v", *window)
	}
}

// When the tab command fails, a window opens.
func TestAFailedTabOpensAWindow(t *testing.T) {
	tab, window := record(t, errors.New("no wezterm gui running"))

	opened, err := Run(context.Background(), Terminal{
		Name: "wezterm", Tab: []string{"wezterm", "cli", "spawn", "--"}, Window: []string{"wezterm", "start", "--"},
	}, []string{"/bin/kith"})

	if err != nil || opened != "window" {
		t.Fatalf("Run() = %q, %v; want a window", opened, err)
	}
	if len(*tab) == 0 {
		t.Error("the tab was never attempted")
	}
	want := []string{"wezterm", "start", "--", "/bin/kith"}
	if !reflect.DeepEqual(*window, want) {
		t.Errorf("ran %+v\nwant %+v", *window, want)
	}
}

func TestATerminalWithNoTabCommandOpensAWindow(t *testing.T) {
	tab, window := record(t, errors.New("should not be called"))

	opened, err := Run(context.Background(), Terminal{Name: "foot", Window: []string{"foot"}}, []string{"/bin/kith"})
	if err != nil || opened != "window" {
		t.Fatalf("Run() = %q, %v; want a window", opened, err)
	}
	if len(*tab) != 0 {
		t.Errorf("a tab was attempted on a terminal with no tab command: %+v", *tab)
	}
	if len(*window) == 0 {
		t.Error("nothing was opened")
	}
}

// An empty command is refused rather than opening a bare terminal.
func TestNothingToRunIsRefused(t *testing.T) {
	record(t, nil)
	if _, err := Run(context.Background(), Terminal{Name: "foot", Window: []string{"foot"}}, nil); err == nil {
		t.Error("Run with no command succeeded")
	}
}

// Detect takes the first terminal present; tab-capable ones come first in the table.
func TestDetectPrefersATerminalThatCanTab(t *testing.T) {
	realLookup := Lookup
	t.Cleanup(func() { Lookup = realLookup })

	Lookup = func(name string) (string, error) {
		if name == "xterm" || name == "kitty" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	got, ok := Detect()
	if !ok {
		t.Fatal("Detect found nothing with two terminals present")
	}
	if got.Name != "kitty" {
		t.Errorf("Detect() = %q, want kitty — it can put a tab in a running terminal", got.Name)
	}

	Lookup = func(string) (string, error) { return "", errors.New("not found") }
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
