package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// copyTimeout bounds the clipboard command, which is waited for: a tool that hangs
// (a vanished X server) must not hold the notifier's goroutine.
const copyTimeout = 5 * time.Second

// clipboard writes text through the user's configured command (wl-copy, pbcopy,
// xclip...). The daemon has no terminal, so OSC 52 is not an option.
type clipboard struct{ command string }

// available reports why this clipboard cannot be written, or nil when it can.
// A systemd --user unit does not inherit WAYLAND_DISPLAY, and wl-copy then exits
// having copied nothing, silently — so check first and say what is wrong.
func (c clipboard) available() error {
	if c.command == "" {
		return errors.New("no `[clipboard] command` is set, and a daemon has no terminal to copy through")
	}
	return graphicalSession()
}

// copy pipes text to the clipboard command on stdin — never argv, since the text
// came out of somebody else's message — and waits for it.
func (c clipboard) copy(ctx context.Context, text string) error {
	if err := c.available(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	cmd := shellCommand(ctx, c.command)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clipboard command %q: %w", c.command, err)
	}
	return nil
}
