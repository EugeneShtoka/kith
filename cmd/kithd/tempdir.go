package main

import "os"

// sqliteTempDir is where the cache's SQLite (ncruces' VFS) puts temporary files, on
// every system: $SQLITE_TMPDIR, else Go's temporary directory ($TMPDIR, else /tmp).
func sqliteTempDir() string {
	if dir := os.Getenv("SQLITE_TMPDIR"); dir != "" {
		return dir
	}
	return os.TempDir()
}
