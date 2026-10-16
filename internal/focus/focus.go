// Package focus raises the browser after a link is opened. Window managers usually
// refuse a background program's activation request, so it tries compositor IPC (sway,
// i3, Hyprland, KWin), then EWMH via wmctrl; macOS and Windows need nothing.
package focus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// classHolder is where the window class goes in a Focuser's argv.
const classHolder = "{class}"

// Focuser is one desktop's way of raising a window.
type Focuser struct {
	// Name labels the focuser in status lines and tests.
	Name string
	// Vars: any one set means this desktop is running. Checked before Command, since
	// swaymsg may be installed on an i3 machine.
	Vars []string
	// Command is looked up on $PATH; empty means no program is needed.
	Command string
	// Tries are argv lines attempted in order (sway: native app_id, then XWayland class).
	Tries [][]string
}

// known is the table, in the order detect searches it.
var known = []Focuser{
	{
		Name:    "sway",
		Vars:    []string{"SWAYSOCK"},
		Command: "swaymsg",
		Tries: [][]string{
			{"swaymsg", `[app_id="(?i)^` + classHolder + `$"] focus`},
			{"swaymsg", `[class="(?i)^` + classHolder + `$"] focus`},
		},
	},
	{
		Name:    "i3",
		Vars:    []string{"I3SOCK"},
		Command: "i3-msg",
		// i3 exits 2 when no window matches, so wait can retry.
		Tries: [][]string{{"i3-msg", `[class="(?i)^` + classHolder + `$"] focus`}},
	},
	{
		Name:    "hyprland",
		Vars:    []string{"HYPRLAND_INSTANCE_SIGNATURE"},
		Command: "hyprctl",
		Tries:   [][]string{{"hyprctl", "dispatch", "focuswindow", "class:(?i)^" + classHolder + "$"}},
	},
	{
		Name:    "kde",
		Vars:    []string{"KDE_FULL_SESSION"},
		Command: "kdotool",
		// Chaining search into the action is the form kdotool's README documents.
		Tries: [][]string{{"kdotool", "search", "--class", classHolder, "windowactivate", "%@"}},
	},
	{
		Name: "ewmh",
		// Fallback for any other X11 WM; wmctrl sees only managed windows.
		Vars:    []string{"DISPLAY"},
		Command: "wmctrl",
		Tries:   [][]string{{"wmctrl", "-x", "-a", classHolder}},
	},
}

// desktopOpener is the no-op for platforms where opening a URL already raises the browser.
var desktopOpener = Focuser{Name: "desktop"}

var (
	// ErrNoFocuser means nothing here can raise a window (e.g. GNOME on Wayland).
	ErrNoFocuser = errors.New("focus: nothing here can raise a window")
	// ErrNoWindow means the focuser found no window of that class.
	ErrNoWindow = errors.New("focus: no window of that class")
	// ErrNoBrowser means the default browser, and so its class, is unknown.
	ErrNoBrowser = errors.New("focus: no default browser on this desktop")
)

// run executes one command and reports only whether it worked.
func run(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is this package's own table with a window class substituted in; no shell
	return cmd.Run()                                      //nolint:wrapcheck // Raise wraps it with the focuser's name
}

// detect returns the focuser for this machine, or false when there is none.
func detect() (Focuser, bool) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return desktopOpener, true
	}
	for _, f := range known {
		if !f.present() {
			continue
		}
		if _, err := exec.LookPath(f.Command); err != nil {
			continue // probing: this focuser's tool is not installed
		}
		return f, true
	}
	return Focuser{}, false
}

// present reports whether this desktop is the one running.
func (f Focuser) present() bool {
	for _, name := range f.Vars {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			return true
		}
	}
	return false
}

// Raise brings the first window of this class forward, once.
func (f Focuser) Raise(ctx context.Context, class string) error {
	if len(f.Tries) == 0 {
		return nil
	}
	if class == "" && f.needsClass() {
		return ErrNoBrowser
	}
	var last error
	for _, try := range f.Tries {
		argv := make([]string, len(try))
		for i, arg := range try {
			argv[i] = strings.ReplaceAll(arg, classHolder, class)
		}
		if err := run(ctx, argv); err == nil {
			return nil
		} else if last == nil {
			last = err
		}
	}
	return fmt.Errorf("%w: %s: %w", ErrNoWindow, f.Name, last)
}

// custom is a user-configured focuser run by a shell: it takes no message input, and
// these tools' quoting does not fit an argv.
func custom(command string) Focuser {
	return Focuser{Name: "configured", Tries: [][]string{{"sh", "-c", command}}}
}

// needsClass reports whether the argv has a class placeholder; a configured command
// names its own window.
func (f Focuser) needsClass() bool {
	for _, try := range f.Tries {
		for _, arg := range try {
			if strings.Contains(arg, classHolder) {
				return true
			}
		}
	}
	return false
}

// Go picks a focuser, finds the browser's class if needed, and waits for its window.
func Go(ctx context.Context, configured string) error {
	focuser := custom(configured)
	if configured == "" {
		detected, ok := detect()
		if !ok {
			return ErrNoFocuser
		}
		focuser = detected
	}
	var class string
	if focuser.needsClass() {
		var err error
		if class, err = browserClass(ctx); err != nil {
			return err
		}
	}
	return wait(ctx, focuser, class)
}

// patience is how long a browser gets to show its window; interval is the retry period.
const (
	patience = 8 * time.Second
	interval = 200 * time.Millisecond
	attempts = int(patience / interval)
)

// nap sleeps for d, or until ctx ends.
func nap(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // wait only asks whether the nap was cut short
	case <-timer.C:
		return nil
	}
}

// wait raises the window, retrying until a cold-started browser has made one.
func wait(ctx context.Context, f Focuser, class string) error {
	var err error
	for try := range attempts {
		if err = f.Raise(ctx, class); err == nil {
			return nil
		}
		if errors.Is(err, ErrNoBrowser) || ctx.Err() != nil || try == attempts-1 {
			return err
		}
		if sleepErr := nap(ctx, interval); sleepErr != nil {
			return err
		}
	}
	return err
}

// browser caches the default browser's window class for the process lifetime.
var browser browserCache

type browserCache struct {
	once  sync.Once
	class string
	err   error
}

// browserClass is the window class of the desktop's default browser.
func browserClass(ctx context.Context) (string, error) {
	browser.once.Do(func() { browser.class, browser.err = readBrowser(ctx) })
	return browser.class, browser.err
}

// browserQueries ask for the default browser, in order.
var browserQueries = [][]string{
	{"xdg-settings", "get", "default-web-browser"},
	{"xdg-mime", "query", "default", "x-scheme-handler/https"},
}

// output runs a command and returns its first line.
func output(ctx context.Context, argv []string) (string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is this package's own table; no shell, no outside input
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("focus: %s: %w", argv[0], err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	return strings.TrimSpace(line), nil
}

// readBrowser does the work browserClass caches.
func readBrowser(ctx context.Context) (string, error) {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		// The no-op focuser takes no class; "" rather than an error keeps it silent.
		return "", nil
	}
	var desktop string
	for _, query := range browserQueries {
		if _, err := exec.LookPath(query[0]); err != nil {
			continue // probing: this query's tool is not installed
		}
		if line, err := output(ctx, query); err == nil && strings.HasSuffix(line, ".desktop") {
			desktop = line
			break
		}
	}
	if desktop == "" {
		return "", ErrNoBrowser
	}
	if class := startupClass(desktop); class != "" {
		return class, nil
	}
	return strings.TrimSuffix(desktop, ".desktop"), nil
}

// startupClass reads StartupWMClass out of a desktop entry, or "" when the file
// cannot be found or does not declare one.
func startupClass(desktop string) string {
	for _, dir := range applicationDirs() {
		for _, rel := range []string{desktop, strings.Replace(desktop, "-", string(os.PathSeparator), 1)} {
			data, err := os.ReadFile(filepath.Join(dir, rel)) // #nosec G304 -- a .desktop file under the XDG application directories
			if err != nil {
				continue // probing each applications dir; most do not have it
			}
			if class := entryValue(string(data), "StartupWMClass"); class != "" {
				return class
			}
		}
	}
	return ""
}

// entryValue reads one `Key=value` out of a desktop entry's first group.
func entryValue(file, key string) string {
	for line := range strings.SplitSeq(file, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && !strings.EqualFold(line, "[Desktop Entry]") {
			return ""
		}
		if value, found := strings.CutPrefix(line, key+"="); found {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// applicationDirs lists desktop-entry dirs in XDG precedence: user first, then system.
func applicationDirs() []string {
	var dirs []string
	home, ok := os.LookupEnv("XDG_DATA_HOME")
	if !ok || home == "" {
		if userHome, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(userHome, ".local", "share")
		}
	}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, "applications"))
	}
	system, ok := os.LookupEnv("XDG_DATA_DIRS")
	if !ok || system == "" {
		system = "/usr/local/share:/usr/share"
	}
	for _, dir := range filepath.SplitList(system) {
		if dir != "" {
			dirs = append(dirs, filepath.Join(dir, "applications"))
		}
	}
	return dirs
}
