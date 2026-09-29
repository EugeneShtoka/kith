//go:build windows

package llamacpp

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestMain lets the test binary stand in for a long-running child: re-executed with
// the variable set, it sleeps instead of testing. Windows has no `sleep` to borrow.
func TestMain(m *testing.M) {
	if os.Getenv("KITH_LLAMACPP_SLEEPER") == "1" {
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// sleeper starts the stand-in child, wired the way start() wires llama-server.
func sleeper(t *testing.T) (*exec.Cmd, tree) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.CommandContext(context.Background(), self, "-test.run=^$")
	cmd.Env = append(os.Environ(), "KITH_LLAMACPP_SLEEPER=1")
	cmd.SysProcAttr = groupAttr()
	if startErr := cmd.Start(); startErr != nil {
		t.Fatalf("spawn: %v", startErr)
	}
	tr := adopt(cmd)
	if tr.job == nil {
		_ = cmd.Process.Kill()
		t.Fatal("adopt() made no job object; stop would fall back to killing the process alone")
	}
	return cmd, tr
}

// Closing the last handle to the job kills what is in it. That is the dieWithParent
// guarantee: when the daemon dies however it dies, the kernel closes its handles.
func TestClosingTheJobKillsTheServer(t *testing.T) {
	cmd, tr := sleeper(t)
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	tr.release()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the process outlived the last handle to its kill-on-close job")
	}
}

// The job object is made, and stopping it ends the process at once — the Windows
// counterpart of exit_test.go's TestStopReturnsWhenTheProcessDies.
func TestStopEndsTheJob(t *testing.T) {
	cmd, tr := sleeper(t)
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); tr.release(); close(exited) }()
	srv := &server{cmd: cmd, tree: tr, exited: exited, now: time.Now}

	began := time.Now()
	if stopErr := srv.stop(); stopErr != nil {
		t.Fatalf("stop() error = %v", stopErr)
	}
	if took := time.Since(began); took > time.Second {
		t.Errorf("stop() took %s; TerminateJobObject should end the process at once", took)
	}
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("the process outlived stop()")
	}
	// A second stop, after the reaper closed the job, is a stop that worked.
	if stopErr := srv.stop(); stopErr != nil {
		t.Errorf("second stop() error = %v", stopErr)
	}
}
