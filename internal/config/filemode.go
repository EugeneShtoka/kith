package config

import (
	"fmt"
	"os"
	"runtime"
)

// OpenToOthers says what users other than the owner may do with the config file at
// path ("read", "write", "read and write"), or "" when nobody else can reach it. The
// file says what an assistant may read and send (see docs/mcp.md), so a copy another
// user can write widens that, and one they can read shows it. The app writes it 0600;
// a file made by hand keeps the umask's mode. Windows keeps no such bits: always "".
func OpenToOthers(path string) (string, error) {
	if runtime.GOOS == "windows" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("config: check the mode of %s: %w", path, err)
	}
	perm := info.Mode().Perm()
	read, write := perm&0o044 != 0, perm&0o022 != 0
	switch {
	case read && write:
		return "read and write", nil
	case write:
		return "write", nil
	case read:
		return "read", nil
	}
	return "", nil
}

// ModeWarning is the line to show when others can reach the config file, or "".
func ModeWarning(path string) string {
	open, err := OpenToOthers(path)
	if err != nil || open == "" {
		return ""
	}
	return fmt.Sprintf("other users can %s %s, which says what an assistant may read and send; run `chmod 600 %s`",
		open, path, path)
}
