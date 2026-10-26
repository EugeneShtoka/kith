//go:build windows

package main

import "os"

// sqliteTempDir is where SQLite's Windows VFS puts temporary files (GetTempPath).
func sqliteTempDir() string { return os.TempDir() }
