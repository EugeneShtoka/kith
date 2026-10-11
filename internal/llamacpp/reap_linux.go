package llamacpp

import "syscall"

// dieWithParent has the kernel kill the child when the forking thread exits (a backstop
// for kill -9, OOM or a panic). It is per thread, hence start's runtime.LockOSThread.
func dieWithParent(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGKILL }
