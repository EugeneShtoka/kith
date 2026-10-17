//go:build !linux

package signal

import "os/exec"

// dieWithKithd leaves it to the heartbeat: without Linux's parent-death signal, a
// helper whose kithd died ends when it stops hearing from it (within 10 s).
func dieWithKithd(*exec.Cmd) {}
