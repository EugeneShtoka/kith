//go:build !windows

package notify

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// shellCommand runs the user's notification hook through the platform shell.
func shellCommand(ctx context.Context, template string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", template) // #nosec G204,G702 -- template is user config; content passed via env, not interpolated
}

// graphicalSession reports why there is no clipboard to write to, or nil.
func graphicalSession() error {
	if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
		return errors.New("no graphical session in kith-notify's environment " +
			"(try `systemctl --user import-environment WAYLAND_DISPLAY`)")
	}
	return nil
}
