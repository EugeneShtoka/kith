// Package launch opens a terminal to handle a desktop-clicked link when no kith is
// running: a new tab in a running terminal first, a new window if that exits non-zero.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// Terminal holds the argv prefixes that open a program in a new tab of a running
// instance and in a new window.
type Terminal struct {
	Name   string
	Tab    []string
	Window []string
}

// known is searched in order when nothing is configured.
var known = []Terminal{
	{
		Name: "wezterm",
		// `cli spawn` exits non-zero when no GUI is running, triggering the fallback.
		Tab:    []string{"wezterm", "cli", "spawn", "--"},
		Window: []string{"wezterm", "start", "--"},
	},
	{
		Name: "kitty",
		// `kitten @ launch` needs remote control in kitty.conf; without it the window
		// fallback is fine.
		Tab:    []string{"kitten", "@", "launch", "--type=tab"},
		Window: []string{"kitty"},
	},
	{
		Name:   "konsole",
		Tab:    []string{"konsole", "--new-tab", "-e"},
		Window: []string{"konsole", "-e"},
	},
	{
		Name: "gnome-terminal",
		// `--tab` reaches the running server and creates a window itself if none.
		Tab:    []string{"gnome-terminal", "--tab", "--"},
		Window: []string{"gnome-terminal", "--"},
	},
	{Name: "foot", Window: []string{"foot"}},
	{Name: "alacritty", Window: []string{"alacritty", "-e"}},
	{Name: "ghostty", Window: []string{"ghostty", "-e"}},
	{Name: "xterm", Window: []string{"xterm", "-e"}},
}

// ErrNoTerminal is returned when nothing on this machine can be started.
var ErrNoTerminal = errors.New("launch: no terminal found")

// start runs a short-lived command and returns its exit status.
func start(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is this program's own table or the user's own config
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch: start %s: %w", argv[0], err)
	}
	// A tab command is a short-lived client; its exit code is the signal.
	return cmd.Wait() //nolint:wrapcheck // its exit code is all Run reads
}

// Detect returns the first terminal on this machine, or false when there is none.
func Detect() (Terminal, bool) {
	for _, term := range known {
		name := term.Name
		if len(term.Window) > 0 {
			name = term.Window[0]
		}
		if _, err := exec.LookPath(name); err == nil {
			return term, true
		}
	}
	return Terminal{}, false
}

// Named returns the known terminal with this name.
func Named(name string) (Terminal, bool) {
	for _, term := range known {
		if term.Name == name {
			return term, true
		}
	}
	return Terminal{}, false
}

// Run starts command in a tab of a running terminal, else a new window, and reports
// which ("tab" or "window").
func Run(ctx context.Context, term Terminal, command []string) (opened string, err error) {
	if len(command) == 0 {
		return "", errors.New("launch: nothing to run")
	}
	if len(term.Tab) > 0 {
		if tabErr := start(ctx, append(append([]string{}, term.Tab...), command...)); tabErr == nil {
			return "tab", nil
		}
	}
	if len(term.Window) == 0 {
		return "", fmt.Errorf("launch: %s could not open a tab and has no window command: %w", term.Name, ErrNoTerminal)
	}
	if winErr := startDetached(ctx, append(append([]string{}, term.Window...), command...)); winErr != nil {
		return "", winErr
	}
	return "window", nil
}

// startDetached starts a command and does not wait for it.
func startDetached(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is this program's own table or the user's own config
	// A no-op Cancel lets the terminal outlive the caller's context.
	cmd.Cancel = func() error { return nil }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch: start %s: %w", argv[0], err)
	}
	// Not waited on: it is reparented to init when this process exits.
	return nil
}
