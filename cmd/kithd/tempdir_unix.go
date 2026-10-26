//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"syscall"
)

// sqliteTempDir is where SQLite's unix VFS puts temporary files: the first of these
// that is a directory it may write in (os_unix.c, unixTempFileDir).
func sqliteTempDir() string {
	const writeSearch = 0o3 // W_OK|X_OK, as SQLite asks
	for _, dir := range []string{os.Getenv("SQLITE_TMPDIR"), os.Getenv("TMPDIR"), "/var/tmp", "/usr/tmp", "/tmp"} {
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		if info, err := os.Stat(dir); err == nil && info.IsDir() && syscall.Access(dir, writeSearch) == nil {
			return dir
		}
	}
	return "."
}
