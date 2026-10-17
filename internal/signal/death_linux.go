package signal

import (
	"os/exec"
	"syscall"
)

// dieWithKithd has the kernel kill the helper when kithd dies, however it dies: a
// helper kithd no longer hears must not keep running until its heartbeat lapses.
func dieWithKithd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
