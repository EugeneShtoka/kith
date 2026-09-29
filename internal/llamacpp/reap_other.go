//go:build !linux

package llamacpp

import "syscall"

// dieWithParent is a no-op off Linux: macOS has no parent-death signal, so a kill -9
// of the daemon leaves the server behind there (Windows uses the job object).
func dieWithParent(*syscall.SysProcAttr) {}
