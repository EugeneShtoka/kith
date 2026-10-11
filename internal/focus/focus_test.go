package focus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

// env sets the desktop variables focus reads: these, and every other one empty.
func env(t *testing.T, vars map[string]string) {
	t.Helper()
	for _, f := range known {
		for _, name := range f.Vars {
			t.Setenv(name, "")
		}
	}
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_DATA_DIRS", "")
	for name, value := range vars {
		t.Setenv(name, value)
	}
}

// bin is a PATH of stand-in programs that record each run.
type bin struct {
	t   *testing.T
	dir string
}

// installed makes PATH hold only stand-ins for programs. Each appends its name and
// arguments as one line to a shared log, prints what prints set, and exits as exits set.
func installed(t *testing.T, programs ...string) *bin {
	t.Helper()
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		t.Skip("opening a link raises the browser here; the stand-ins are shell scripts")
	}
	b := &bin{t: t, dir: t.TempDir()}
	for _, name := range programs {
		script := "#!/bin/sh\n" +
			"d='" + b.dir + "'\n" +
			"{ printf '%s' '" + name + "'; for a in \"$@\"; do printf '\\037%s' \"$a\"; done; printf '\\n'; } >>\"$d/runs\"\n" +
			"n=0; [ -f \"$d/count\" ] && read -r n <\"$d/count\"; n=$((n+1)); echo $n >\"$d/count\"\n" +
			"[ -f \"$d/out\" ] && while IFS= read -r l; do printf '%s\\n' \"$l\"; done <\"$d/out\"\n" +
			"s=0; i=0\n" +
			"[ -f \"$d/exit\" ] && while read -r v; do i=$((i+1)); s=$v; [ $i -ge $n ] && break; done <\"$d/exit\"\n" +
			"exit $s\n"
		if err := os.WriteFile(filepath.Join(b.dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", b.dir)
	return b
}

// exits sets the status of the n-th run of any stand-in, the last repeating.
func (b *bin) exits(statuses ...int) *bin {
	b.t.Helper()
	var lines strings.Builder
	for _, s := range statuses {
		lines.WriteString(strconv.Itoa(s) + "\n")
	}
	b.write("exit", lines.String())
	return b
}

// prints sets what every stand-in writes to stdout.
func (b *bin) prints(out string) *bin {
	b.t.Helper()
	b.write("out", out)
	return b
}

func (b *bin) write(name, body string) {
	b.t.Helper()
	if err := os.WriteFile(filepath.Join(b.dir, name), []byte(body), 0o600); err != nil {
		b.t.Fatal(err)
	}
}

// runs is every argv run so far, in order.
func (b *bin) runs() [][]string {
	b.t.Helper()
	data, err := os.ReadFile(filepath.Join(b.dir, "runs"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		b.t.Fatal(err)
	}
	var out [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(data), "\n"), "\n") {
		out = append(out, strings.Split(line, "\x1f"))
	}
	return out
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
	b := installed(t, "i3-msg")

	if err := known[1].Raise(context.Background(), "vivaldi-stable"); err != nil {
		t.Fatalf("Raise() = %v", err)
	}

	want := [][]string{{"i3-msg", `[class="(?i)^vivaldi-stable$"] focus`}}
	if got := b.runs(); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %+v\nwant %+v", got, want)
	}
}

// sway tries app_id then class: the browser may be native Wayland or XWayland.
func TestASecondFormIsTriedWhenTheFirstFindsNothing(t *testing.T) {
	b := installed(t, "swaymsg").exits(1, 0)

	if err := known[0].Raise(context.Background(), "firefox"); err != nil {
		t.Fatalf("Raise() = %v", err)
	}

	ran := b.runs()
	if len(ran) != 2 {
		t.Fatalf("ran %d commands, want 2: %+v", len(ran), ran)
	}
	if !strings.Contains(ran[0][1], "app_id") || !strings.Contains(ran[1][1], "class") {
		t.Errorf("wrong order: %+v", ran)
	}
}

// Every form failing is ErrNoWindow, which wait retries on.
func TestEveryFormFailingIsNoWindow(t *testing.T) {
	installed(t, "i3-msg").exits(2)

	err := known[1].Raise(context.Background(), "vivaldi-stable")

	if !errors.Is(err, ErrNoWindow) {
		t.Fatalf("Raise() = %v; want ErrNoWindow", err)
	}
}

// Where opening already raises the browser, nothing runs and Raise succeeds.
func TestTheDesktopOpenerRunsNothing(t *testing.T) {
	b := installed(t, "i3-msg", "swaymsg", "hyprctl", "kdotool", "wmctrl", "sh")

	if err := desktopOpener.Raise(context.Background(), ""); err != nil {
		t.Fatalf("Raise() = %v", err)
	}

	if got := b.runs(); len(got) != 0 {
		t.Errorf("ran %+v; want nothing", got)
	}
}

// On a cold start the first attempts find no window and a later one succeeds. The
// bubble's clock makes the waits between them free.
func TestWaitRetriesUntilTheWindowExists(t *testing.T) {
	b := installed(t, "i3-msg").exits(1, 1, 0)

	synctest.Test(t, func(t *testing.T) {
		began := time.Now()
		if err := wait(context.Background(), known[1], "vivaldi-stable"); err != nil {
			t.Fatalf("wait() = %v", err)
		}
		if waited := time.Since(began); waited != 2*interval {
			t.Errorf("waited %s, want two intervals", waited)
		}
	})

	if got := len(b.runs()); got != 3 {
		t.Errorf("gave up after %d attempts, want 3", got)
	}
}

func TestWaitGivesUp(t *testing.T) {
	b := installed(t, "i3-msg").exits(1)

	synctest.Test(t, func(t *testing.T) {
		err := wait(context.Background(), known[1], "vivaldi-stable")

		if !errors.Is(err, ErrNoWindow) {
			t.Fatalf("wait() = %v; want ErrNoWindow", err)
		}
	})

	if got := len(b.runs()); got != attempts {
		t.Errorf("tried %d times, want %d", got, attempts)
	}
}

// An unknown browser returns at once instead of spending the retry budget.
func TestNotKnowingTheBrowserDoesNotWait(t *testing.T) {
	b := installed(t, "i3-msg")

	err := wait(context.Background(), known[1], "")

	if !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("wait() = %v; want ErrNoBrowser", err)
	}
	if got := b.runs(); len(got) != 0 {
		t.Errorf("ran %+v; want nothing", got)
	}
}

// A configured command names its own window, so no browser lookup happens. No test
// fills the browser cache, so a lookup here would run xdg-settings.
func TestAConfiguredCommandNeedsNoBrowserLookup(t *testing.T) {
	b := installed(t, "sh", "xdg-settings", "xdg-mime").prints("firefox.desktop\n")

	if err := Go(context.Background(), `i3-msg '[class="Vivaldi-stable"] focus'`); err != nil {
		t.Fatalf("Go() = %v", err)
	}

	want := [][]string{{"sh", "-c", `i3-msg '[class="Vivaldi-stable"] focus'`}}
	if got := b.runs(); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %+v\nwant %+v", got, want)
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
	dir := t.TempDir()
	writeEntry(t, dir, "firefox.desktop", "[Desktop Entry]\nName=Firefox\n")
	env(t, map[string]string{"XDG_DATA_HOME": dir, "XDG_DATA_DIRS": dir})
	b := installed(t, "xdg-settings").prints("firefox.desktop\n")

	class, err := readBrowser(context.Background())

	if err != nil || class != "firefox" {
		t.Fatalf("readBrowser() = %q, %v; want firefox", class, err)
	}
	want := [][]string{{"xdg-settings", "get", "default-web-browser"}}
	if got := b.runs(); !reflect.DeepEqual(got, want) {
		t.Errorf("ran %+v\nwant %+v", got, want)
	}
}

// No way to query the default browser is ErrNoBrowser, distinct from ErrNoWindow.
func TestNoDefaultBrowserIsItsOwnError(t *testing.T) {
	installed(t)

	if _, err := readBrowser(context.Background()); !errors.Is(err, ErrNoBrowser) {
		t.Fatalf("readBrowser() = %v; want ErrNoBrowser", err)
	}
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
