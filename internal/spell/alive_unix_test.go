//go:build !windows

package spell

import "syscall"

// processAlive reports whether pid still runs (signal 0 checks without sending).
func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
