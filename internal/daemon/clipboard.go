package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// copyTimeout bounds the clipboard command: a hung tool (vanished X server) must not
// hold the notifier's goroutine.
const copyTimeout = 5 * time.Second

// clipboard writes text through the configured command (wl-copy, pbcopy, xclip...);
// the daemon has no terminal for OSC 52.
type clipboard struct{ command string }

// available reports why this clipboard cannot be written, or nil. A systemd --user
// unit may lack WAYLAND_DISPLAY, and wl-copy then silently copies nothing.
func (c clipboard) available() error {
	if c.command == "" {
		return errors.New("no `[clipboard] command` is set, and a daemon has no terminal to copy through")
	}
	return graphicalSession()
}

// copy pipes text to the clipboard command on stdin, never argv (the text came from
// someone else's message), and waits for it.
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
