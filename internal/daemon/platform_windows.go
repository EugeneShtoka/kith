//go:build windows

package daemon

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

// shellCommand runs a user-configured command line through cmd.exe. CmdLine is set
// verbatim because cmd.exe ignores CommandLineToArgvW quoting; /d skips AutoRun and
// `/s /c "<line>"` strips exactly the outer quotes.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	comspec := os.Getenv("ComSpec")
	if comspec == "" {
		comspec = "cmd.exe"
	}
	cmd := exec.CommandContext(ctx, comspec) // #nosec G204,G702 -- command is user config; the payload goes on stdin, never interpolated
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:    `"` + comspec + `" /d /s /c "` + command + `"`,
		HideWindow: true,
	}
	return cmd
}

// graphicalSession is nil: a Windows session always has its desktop's clipboard.
func graphicalSession() error { return nil }

// restrictSocket is a no-op: a Windows AF_UNIX socket is authorized by ACL and
// inherits %LOCALAPPDATA%\kith's owner-only ACL.
func restrictSocket(string) error { return nil }

// secureDir is a no-op: %LOCALAPPDATA% is per user and its ACL is owner-only.
func secureDir(string) error { return nil }
