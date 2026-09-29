//go:build windows

package notify

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// shellCommand runs the user's notification hook through cmd.exe, so the variables read
// as %KITH_TITLE% (or $env:KITH_TITLE in a `powershell -Command` hook).
func shellCommand(ctx context.Context, template string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, comspec) // #nosec G204,G702 -- template is user config; content passed via env, not interpolated
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:    `"` + comspec + `" /d /s /c "` + template + `"`,
		HideWindow: true,
	}
	return cmd
}
