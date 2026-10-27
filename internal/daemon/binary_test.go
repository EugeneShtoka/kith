//go:build !windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// binaryChildEnv makes TestBinaryChild print what Binary("kithd") finds, run from a
// copy of this test binary placed where the test wants an executable to live.
const binaryChildEnv = "KITH_TEST_BINARY_CHILD"

func TestBinaryChild(t *testing.T) {
	if os.Getenv(binaryChildEnv) == "" {
		t.Skip("run by TestBinaryPrefersTheOneBesideThisExecutable")
	}
	path, err := Binary("kithd")
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(0)
	}
	fmt.Println("found:", path)
	os.Exit(0)
}

// The kithd beside the running executable wins over one on PATH, reached through a
// symlink to the executable too; a sibling that cannot run is passed over.
func TestBinaryPrefersTheOneBesideThisExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	bin, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	for _, tc := range []struct {
		name    string
		sibling os.FileMode // 0: no kithd beside the executable
		onPath  bool
		link    bool // run the executable through a symlink in another folder
		want    string
	}{
		{name: "beside wins over PATH", sibling: 0o700, onPath: true, want: "beside"},
		{name: "beside, through a symlink", sibling: 0o700, onPath: true, link: true, want: "beside"},
		{name: "none beside: PATH", onPath: true, want: "path"},
		{name: "one beside that cannot run: PATH", sibling: 0o600, onPath: true, want: "path"},
		{name: "none anywhere", want: "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			install, path := filepath.Join(root, "install"), filepath.Join(root, "path")
			for _, dir := range []string{install, path} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			exe := filepath.Join(install, "kith")
			if err := os.WriteFile(exe, bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.sibling != 0 {
				writeFakeKithd(t, filepath.Join(install, "kithd"), tc.sibling)
			}
			if tc.onPath {
				writeFakeKithd(t, filepath.Join(path, "kithd"), 0o700)
			}
			if tc.link {
				links := filepath.Join(root, "links")
				if err := os.Mkdir(links, 0o700); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(links, "kith")
				if err := os.Symlink(exe, link); err != nil {
					t.Fatal(err)
				}
				exe = link
			}
			cmd := exec.CommandContext(t.Context(), exe, "-test.run=^TestBinaryChild$") // #nosec G204 -- a copy of this test binary
			cmd.Env = append(os.Environ(), binaryChildEnv+"=1", "PATH="+path)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			got := string(out)
			switch tc.want {
			case "beside":
				if !strings.Contains(got, "found: "+filepath.Join(install, "kithd")) {
					t.Errorf("want the kithd beside the executable, got:\n%s", got)
				}
			case "path":
				if !strings.Contains(got, "found: "+filepath.Join(path, "kithd")) {
					t.Errorf("want the kithd on PATH, got:\n%s", got)
				}
			case "error":
				if !strings.Contains(got, "error: daemon: kithd is neither beside") {
					t.Errorf("want an error naming both places, got:\n%s", got)
				}
			}
		})
	}
}

func writeFakeKithd(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), mode); err != nil {
		t.Fatal(err)
	}
}
