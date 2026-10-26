package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// copyTimeout bounds the clipboard command: a hung tool (vanished X server) must not
// hold the notification it announces.
const copyTimeout = 5 * time.Second

// Clipboard writes text through the configured command (wl-copy, pbcopy, xclip...);
// kith-notify has no terminal for OSC 52.
type Clipboard struct{ Command string }

// Available reports why this clipboard cannot be written, or nil. A systemd --user
// unit may lack WAYLAND_DISPLAY, and wl-copy then silently copies nothing.
func (c Clipboard) Available() error {
	if c.Command == "" {
		return errors.New("no `[clipboard] command` is set, and kith-notify has no terminal to copy through")
	}
	return graphicalSession()
}

// Copy pipes text to the clipboard command on stdin, never argv (the text came from
// someone else's message), and waits for it.
func (c Clipboard) Copy(ctx context.Context, text string) error {
	if err := c.Available(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, copyTimeout)
	defer cancel()
	cmd := shellCommand(ctx, c.Command)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("clipboard command %q: %w", c.Command, err)
	}
	return nil
}
