// Package launch opens a terminal to handle a desktop-clicked link when no kith is
// running: a new tab in a running terminal first, a new window if that exits non-zero.
package launch

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

// Terminal is how to start one program in a terminal: the argv that opens a tab in the
// running instance, and the argv that opens a new window.
type Terminal struct {
	Name   string
	Tab    []string
	Window []string
}

// known is the table, in the order it is searched when nothing is configured.
var known = []Terminal{
	{
		Name: "wezterm",
		// `cli spawn` talks to the running GUI over its own socket and exits non-zero
		// when there is none, which is exactly the signal the fallback wants.
		Tab:    []string{"wezterm", "cli", "spawn", "--"},
		Window: []string{"wezterm", "start", "--"},
	},
	{
		Name: "kitty",
		// `kitten @ launch` needs remote control enabled in kitty.conf; without it this
		// fails and the window opens instead, which is the right outcome and not worth
		// a configuration check.
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
		// gnome-terminal is a client of its own server, so `--tab` reaches the
		// running window and falls back to making one by itself.
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

// Lookup is how a terminal binary is found, replaced in tests. It is a variable
// rather than a parameter because every caller in this program wants the real one.
var Lookup = exec.LookPath

// start runs one command and waits only long enough to know it started. Replaced in
// tests, for the same reason Lookup is.
var start = func(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is this program's own table or the user's own config
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch: start %s: %w", argv[0], err)
	}
	// Waited on rather than left running: a tab command *is* a short-lived client of
	// the running terminal, and its exit code is the whole signal.
	return cmd.Wait()
}

// Detect returns the first terminal on this machine, or false when there is none.
func Detect() (Terminal, bool) {
	for _, term := range known {
		name := term.Name
		if len(term.Window) > 0 {
			name = term.Window[0]
		}
		if _, err := Lookup(name); err == nil {
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

// Run starts command in a terminal: a tab in a running one if it can, a new window
// if it cannot. It reports which of the two happened, for the caller to say.
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
var startDetached = func(ctx context.Context, argv []string) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is this program's own table or the user's own config
	// Cancel is cleared so the terminal outlives the context that started it: the
	// caller's context ends when the caller exits, which is immediately.
	cmd.Cancel = func() error { return nil }
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch: start %s: %w", argv[0], err)
	}
	// Released rather than waited on. The process is reparented to init when this one
	// exits, which is what a launcher is for.
	return nil
}
