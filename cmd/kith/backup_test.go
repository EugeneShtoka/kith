//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/buildinfo"
)

// runInKithdChildEnv makes TestRunInKithdChild run `kith backup` from a copy of this
// test binary, so the kithd beside it is the one a test placed there.
const runInKithdChildEnv = "KITH_TEST_RUN_IN_KITHD"

func TestRunInKithdChild(t *testing.T) {
	if os.Getenv(runInKithdChildEnv) == "" {
		t.Skip("run by TestKithBackupRunsItsOwnKithd")
	}
	if err := runBackup([]string{"--no-media", "out.tar.age"}); err != nil {
		fmt.Fprintln(os.Stderr, "kith:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

// kith backup runs the kithd installed beside it, and only when that kithd is this
// kith's build; whatever goes wrong is said, never a silent exit.
func TestKithBackupRunsItsOwnKithd(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	ours := buildinfo.String("kithd")
	const old = "kithd v0.0.1-old\n  commit: 0000000"
	for _, tc := range []struct {
		name       string
		sibling    string // version the kithd beside kith reports; "" for none
		siblingRun string // what the kithd beside kith does for `backup`
		onPath     string // version the kithd on PATH reports; "" for none
		wantCode   int
		wantOut    string
		wantErr    []string
	}{
		{
			name: "the kithd beside kith, not PATH's", sibling: ours, siblingRun: "echo \"beside: $*\"",
			onPath: ours, wantCode: 0, wantOut: "beside: backup --no-media out.tar.age",
		},
		{
			name: "an older kithd on PATH is refused, out loud", onPath: old, wantCode: 1,
			wantErr: []string{"kithd v0.0.1-old"},
		},
		{
			name: "a kithd that fails without a word is still heard", sibling: ours, siblingRun: "exit 3",
			wantCode: 3, wantErr: []string{"exit status 3"},
		},
		{
			name: "no kithd anywhere", wantCode: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			install, path := filepath.Join(root, "install"), filepath.Join(root, "path")
			for _, dir := range []string{install, path} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			kith := filepath.Join(install, "kith")
			if err := os.WriteFile(kith, bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.sibling != "" {
				writeKithd(t, filepath.Join(install, "kithd"), tc.sibling, tc.siblingRun)
			}
			if tc.onPath != "" {
				// PATH's kithd, if ever run for backup, exits 1 with nothing said: the old
				// kithd the rehearsal found did just that.
				writeKithd(t, filepath.Join(path, "kithd"), tc.onPath, "echo \"path: $*\"; exit 1")
			}
			cmd := exec.CommandContext(t.Context(), kith, "-test.run=^TestRunInKithdChild$") // #nosec G204 -- a copy of this test binary
			cmd.Env = append(os.Environ(), runInKithdChildEnv+"=1", "PATH="+path)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if exit, ok := errors.AsType[*exec.ExitError](err); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatalf("child: %v", err)
			}
			if code != tc.wantCode {
				t.Errorf("exit code %d, want %d\nstdout: %s\nstderr: %s", code, tc.wantCode, stdout.String(), stderr.String())
			}
			if tc.wantOut != "" && !strings.Contains(stdout.String(), tc.wantOut) {
				t.Errorf("stdout %q, want it to contain %q", stdout.String(), tc.wantOut)
			}
			if strings.Contains(stdout.String(), "path: ") {
				t.Errorf("PATH's kithd ran: %q", stdout.String())
			}
			if tc.wantCode != 0 && strings.TrimSpace(stderr.String()) == "" {
				t.Errorf("exit code %d with nothing said", code)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(stderr.String(), want) {
					t.Errorf("stderr %q, want it to contain %q", stderr.String(), want)
				}
			}
		})
	}
}

// writeKithd writes a fake kithd reporting version and running run for anything else.
func writeKithd(t *testing.T, path, version, run string) {
	t.Helper()
	script := "#!/bin/sh\nif [ \"$1\" = --version ]; then\nprintf '%s\\n' '" + version + "'\nexit 0\nfi\n" + run + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}
