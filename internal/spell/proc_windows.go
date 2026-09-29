//go:build windows

package spell

import "os/exec"

// ownGroup does nothing on Windows; die still closes the output pipe, so a child that
// outlives the engine cannot hold a reader.
func ownGroup(*exec.Cmd) {}

// killEngine kills the engine process.
func killEngine(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
