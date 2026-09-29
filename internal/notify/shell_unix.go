//go:build !windows

package notify

import (
	"context"
	"os/exec"
)

// shellCommand runs the user's notification hook through the platform shell.
func shellCommand(ctx context.Context, template string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", template) // #nosec G204,G702 -- template is user config; content passed via env, not interpolated
}
