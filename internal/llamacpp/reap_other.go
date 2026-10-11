//go:build !linux

package llamacpp

import "syscall"

// dieWithParent is a no-op off Linux: macOS has no parent-death signal, and Windows
// uses the job object.
func dieWithParent(*syscall.SysProcAttr) {}
