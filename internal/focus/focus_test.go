package focus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// env replaces the environment reader with a fixed map.
func env(t *testing.T, vars map[string]string) {
	t.Helper()
	real := lookupEnv
	lookupEnv = func(name string) (string, bool) {
		value, ok := vars[name]
		return value, ok
	}
	t.Cleanup(func() { lookupEnv = real })
}

// installed replaces the $PATH lookup with a fixed set of programs.
func installed(t *testing.T, programs ...string) {
	t.Helper()
	have := map[string]bool{}
	for _, p := range programs {
		have[p] = true
	}
	real := lookPath
	lookPath = func(name string) (string, error) {
		if have[name] {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("not installed: " + name)
	}
	t.Cleanup(func() { lookPath = real })
}

// record swaps the runner for one that records each argv and answers with errs in
// order, repeating the last.
func record(t *testing.T, errs ...error) *[][]string {
	t.Helper()
	var ran [][]string
	real := run
	run = func(_ context.Context, argv []string) error {
		ran = append(ran, argv)
		switch {
		case len(errs) == 0:
			return nil
		case len(ran) <= len(errs):
			return errs[len(ran)-1]
		default:
			return errs[len(errs)-1]
		}
	}
	t.Cleanup(func() { run = real })
	return &ran
}

// noNap makes wait's retries instant.
func noNap(t *testing.T) {
	t.Helper()
	real := nap
	nap = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { nap = real })
}

// The desktop is chosen by its environment variable, not by what is installed.
func TestTheRunningDesktopWinsOverTheInstalledOne(t *testing.T) {
	env(t, map[string]string{"I3SOCK": "/run/user/1000/i3/ipc-socket.2532", "DISPLAY": ":0"})
	installed(t, "swaymsg", "i3-msg", "wmctrl")

	f, ok := detect()

	if !ok || f.Name != "i3" {
		t.Fatalf("detect() = %q, %v; want i3", f.Name, ok)
	}
}

// A desktop whose tool is missing falls through to wmctrl.
func TestAMissingToolFallsThroughToTheGenericRung(t *testing.T) {
	env(t, map[string]string{"I3SOCK": "/run/user/1000/i3/ipc-socket.2532", "DISPLAY": ":0"})
	installed(t, "wmctrl")

	f, ok := detect()

	if !ok || f.Name != "ewmh" {
		t.Fatalf("detect() = %q, %v; want ewmh", f.Name, ok)
	}
}

func TestNoFocuserIsAnAnswer(t *testing.T) {
	env(t, map[string]string{"WAYLAND_DISPLAY": "wayland-0", "XDG_CURRENT_DESKTOP": "GNOME"})
	installed(t)

	if f, ok := detect(); ok {
		t.Fatalf("detect() = %q, true; want nothing", f.Name)
	}
}

// The class is substituted inside the criteria string, not appended to the argv.
func TestTheClassIsSubstitutedIntoTheCriteria(t *testing.T) {
	ran := record(t)

	if err := known[1].Raise(context.Background(), "vivaldi-stable"); err != nil {
		t.Fatalf("Raise() = %v", err)
	}

	want := []string{"i3-msg", `[class="(?i)^vivaldi-stable$"] focus`}
	if len(*ran) != 1 || !reflect.DeepEqual((*ran)[0], want) {
		t.Errorf("ran %+v\nwant %+v", *ran, want)
	}
}

// sway tries app_id then class: the browser may be native Wayland or XWayland.
func TestASecondFormIsTriedWhenTheFirstFindsNothing(t *testing.T) {
	ran := record(t, errors.New("no window"), nil)

	if err := known[0].Raise(context.Background(), "firefox"); err != nil {
		t.Fatalf("Raise() = %v", err)
	}

	if len(*ran) != 2 {
		t.Fatalf("ran %d commands, want 2: %+v", len(*ran), *ran)
	}
	if !strings.Contains((*ran)[0][1], "app_id") || !strings.Contains((*ran)[1][1], "class") {
		t.Errorf("wrong order: %+v", *ran)
	}
}

// Every form failing is ErrNoWindow, which wait retries on.
func TestEveryFormFailingIsNoWindow(t *testing.T) {
	record(t, errors.New("exit status 2"))

	err := known[1].Raise(context.Background(), "vivaldi-stable")

	if !errors.Is(err, ErrNoWindow) {
		t.Fatalf("Raise() = %v; want ErrNoWindow", err)
	}
}

// Where opening already raises the browser, nothing runs and Raise succeeds.
func TestTheDesktopOpenerRunsNothing(t *testing.T) {
	ran := record(t)

	if err := desktopOpener.Raise(context.Background(), ""); err != nil {
		t.Fatalf("Raise() = %v", err)
	}

	if len(*ran) != 0 {
		t.Errorf("ran %+v; want nothing", *ran)
	}
}

// On a cold start the first attempts find no window and a later one succeeds.
func TestWaitRetriesUntilTheWindowExists(t *testing.T) {
	noNap(t)
	ran := record(t, errors.New("no window"), errors.New("no window"), nil)

	if err := wait(context.Background(), known[1], "vivaldi-stable"); err != nil {
		t.Fatalf("wait() = %v", err)
	}

	if len(*ran) != 3 {
		t.Errorf("gave up after %d attempts, want 3", len(*ran))
	}
}

func TestWaitGivesUp(t *testing.T) {
	noNap(t)
	record(t, errors.New("no window"))

	err := wait(context.Background(), known[1], "vivaldi-stable")

	if !errors.Is(err, ErrNoWindow) {
		t.Fatalf("wait() = %v; want ErrNoWindow", err)
	}
}

// An unknown browser returns at once instead of spending the retry budget.
func TestNotKnowingTheBrowserDoesNotWait(t *testing.T) {
	noNap(t)
	ran := record(t)

	err := wait(context.Background(), known[1], "")

	if !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("wait() = %v; want ErrNoBrowser", err)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %+v; want nothing", *ran)
	}
}

// A configured command names its own window, so no browser lookup happens.
func TestAConfiguredCommandNeedsNoBrowserLookup(t *testing.T) {
	browser = browserCache{}
	t.Cleanup(func() { browser = browserCache{} })
	installed(t) // no xdg-settings, no xdg-mime: a lookup here would fail
	ran := record(t)

	if err := Go(context.Background(), `i3-msg '[class="Vivaldi-stable"] focus'`); err != nil {
		t.Fatalf("Go() = %v", err)
	}

	want := []string{"sh", "-c", `i3-msg '[class="Vivaldi-stable"] focus'`}
	if len(*ran) != 1 || !reflect.DeepEqual((*ran)[0], want) {
		t.Errorf("ran %+v\nwant %+v", *ran, want)
	}
}

// StartupWMClass is preferred over the file name.
func TestTheDesktopEntryNamesTheWindowClass(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "vivaldi-stable.desktop", "[Desktop Entry]\nName=Vivaldi\nStartupWMClass=Vivaldi-stable\n")
	env(t, map[string]string{"XDG_DATA_HOME": dir, "XDG_DATA_DIRS": dir})

	if class := startupClass("vivaldi-stable.desktop"); class != "Vivaldi-stable" {
		t.Errorf("startupClass() = %q; want Vivaldi-stable", class)
	}
}

func TestAHyphenatedIDMayBeADirectory(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, filepath.Join(dir, "applications", "kde"), "konqueror.desktop", "[Desktop Entry]\nStartupWMClass=konqueror\n")
	env(t, map[string]string{"XDG_DATA_HOME": dir, "XDG_DATA_DIRS": ""})

	if class := startupClass("kde-konqueror.desktop"); class != "konqueror" {
		t.Errorf("startupClass() = %q; want konqueror", class)
	}
}

// Keys under a [Desktop Action …] header belong to the action and are ignored.
func TestOnlyTheFirstGroupIsRead(t *testing.T) {
	file := "[Desktop Entry]\nName=Firefox\n\n[Desktop Action new-private-window]\nStartupWMClass=wrong\n"

	if class := entryValue(file, "StartupWMClass"); class != "" {
		t.Errorf("entryValue() = %q; want nothing", class)
	}
}

// Without StartupWMClass the desktop file's name is the class, as for most browsers.
func TestTheFileNameIsTheFallbackClass(t *testing.T) {
	browser = browserCache{}
	t.Cleanup(func() { browser = browserCache{} })
	dir := t.TempDir()
	writeEntry(t, dir, "firefox.desktop", "[Desktop Entry]\nName=Firefox\n")
	env(t, map[string]string{"XDG_DATA_HOME": dir, "XDG_DATA_DIRS": dir})
	installed(t, "xdg-settings")
	answer(t, "firefox.desktop\n")

	class, err := browserClass(context.Background())

	if err != nil || class != "firefox" {
		t.Fatalf("browserClass() = %q, %v; want firefox", class, err)
	}
}

// No way to query the default browser is ErrNoBrowser, distinct from ErrNoWindow.
func TestNoDefaultBrowserIsItsOwnError(t *testing.T) {
	browser = browserCache{}
	t.Cleanup(func() { browser = browserCache{} })
	installed(t)

	if _, err := browserClass(context.Background()); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("browserClass() = %v; want ErrNoBrowser", err)
	}
}

// answer makes the command-output reader always return line.
func answer(t *testing.T, line string) {
	t.Helper()
	real := output
	output = func(context.Context, []string) (string, error) { return strings.TrimSpace(line), nil }
	t.Cleanup(func() { output = real })
}

// writeEntry writes a desktop file where the lookup finds it.
func writeEntry(t *testing.T, dir, name, body string) {
	t.Helper()
	if filepath.Base(dir) != "kde" {
		dir = filepath.Join(dir, "applications")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
