//go:build windows

package llamacpp

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// On Windows the group is a KILL_ON_JOB_CLOSE job object: only this daemon holds the
// handle, so the kernel kills the server when the daemon dies (the PR_SET_PDEATHSIG
// equivalent). Without a console there is no graceful stop. Plain syscall because this
// package imports only the standard library.

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW        = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob      = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject      = kernel32.NewProc("TerminateJobObject")
)

const (
	createNewProcessGroup        = 0x00000200 // CREATE_NEW_PROCESS_GROUP
	createNoWindow               = 0x08000000 // CREATE_NO_WINDOW
	processSetQuota              = 0x0100     // PROCESS_SET_QUOTA
	processTerminate             = 0x0001     // PROCESS_TERMINATE
	jobObjectLimitKillOnJobClose = 0x00002000 // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	jobObjectExtendedLimitInfo   = 9          // JobObjectExtendedLimitInformation
)

// C layouts of the JOBOBJECT_* structs, as golang.org/x/sys/windows declares them.
type jobBasicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimit struct {
	BasicLimitInformation jobBasicLimit
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// tree is the server's job; nil if none could be made, then only the process is killed.
type tree struct{ job *job }

// job is a job handle shared by stop and the reaper; the mutex keeps a closed (and
// possibly reused) handle from being terminated.
type job struct {
	mu     sync.Mutex
	handle syscall.Handle
	closed bool
}

// groupAttr keeps the child off this process's console and out of its Ctrl-C group.
func groupAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | createNoWindow, HideWindow: true}
}

// adopt puts the running process into a fresh kill-on-close job.
func adopt(cmd *exec.Cmd) tree {
	// Best effort: without the job, stop() still works; only a crash could orphan it.
	h, err := newKillOnCloseJob()
	if err != nil {
		return tree{}
	}
	// #nosec G115 -- a Windows pid is a DWORD
	proc, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = syscall.CloseHandle(h)
		return tree{}
	}
	defer func() { _ = syscall.CloseHandle(proc) }()
	if r, _, _ := procAssignProcessToJob.Call(uintptr(h), uintptr(proc)); r == 0 {
		_ = syscall.CloseHandle(h)
		return tree{}
	}
	return tree{job: &job{handle: h}}
}

func newKillOnCloseJob() (syscall.Handle, error) {
	r, _, callErr := procCreateJobObjectW.Call(0, 0)
	if r == 0 {
		return 0, fmt.Errorf("llamacpp: CreateJobObject: %w", callErr)
	}
	h := syscall.Handle(r)
	var info jobExtendedLimit
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	// #nosec G103 -- the documented way to pass a struct to a Win32 call; info outlives it
	ok, _, callErr := procSetInformationJobObject.Call(uintptr(h), jobObjectExtendedLimitInfo,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	if ok == 0 {
		_ = syscall.CloseHandle(h)
		return 0, fmt.Errorf("llamacpp: SetInformationJobObject: %w", callErr)
	}
	return h, nil
}

// release closes the job after reaping, killing any orphan still in it.
func (t tree) release() {
	if t.job == nil {
		return
	}
	t.job.mu.Lock()
	defer t.job.mu.Unlock()
	if !t.job.closed {
		t.job.closed = true
		_ = syscall.CloseHandle(t.job.handle) // closing kills what is left; nothing to report
	}
}

var errGone = errors.New("llamacpp: process already gone")

// terminate ends the job, or the process alone when there is no job.
func (s *server) terminate() error {
	if j := s.tree.job; j != nil {
		j.mu.Lock()
		defer j.mu.Unlock()
		if j.closed {
			return errGone
		}
		if r, _, callErr := procTerminateJobObject.Call(uintptr(j.handle), 1); r == 0 {
			return fmt.Errorf("llamacpp: TerminateJobObject: %w", callErr)
		}
		return nil
	}
	if err := s.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("llamacpp: kill: %w", err)
	}
	return nil
}

// kill is terminate: on Windows the first stop is already hard.
func (s *server) kill() { _ = s.terminate() }
