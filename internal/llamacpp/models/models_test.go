package models

import (
	"os"
	"path/filepath"
	"testing"
)

// Installed is what the offer and the daemon both read, and the case it exists for is
// the truncated download: a GGUF of the right name and the wrong length is a server that
// starts, fails to parse it, and never becomes healthy.
func TestInstalledRejectsATruncatedModel(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	tag := Installable()[0]
	src, _ := SourceFor(tag)
	path := filepath.Join(dir, src.Filename())
	if err := os.WriteFile(path, []byte("GGUF\x00truncated"), 0o600); err != nil {
		t.Fatal(err)
	}

	if found, ok := Installed(dir, tag); ok {
		t.Errorf("a %d-byte file passed as a %d-byte model: %s", 15, src.File.Size, found)
	}
	if _, _, ok := Any(dir); ok {
		t.Error("Any accepted it too")
	}
}

// The committed manifest is checked for the properties the installer relies on, which is
// what stops a regenerated one from being subtly wrong: a hash of the wrong length, a
// size of zero, or a tag whose filename would collide with another's.
func TestTheCommittedManifestIsWellFormed(t *testing.T) {
	t.Parallel()

	if len(Installable()) == 0 {
		t.Fatal("no models can be installed")
	}
	names := make(map[string]string, len(Installable()))
	for _, tag := range Installable() {
		src, ok := SourceFor(tag)
		if !ok {
			t.Fatalf("%s is installable and has no source", tag)
		}
		if len(src.File.SHA256) != 64 {
			t.Errorf("%s: sha256 %q is not 64 hex characters", tag, src.File.SHA256)
		}
		if src.File.Size <= 0 {
			t.Errorf("%s: size %d", tag, src.File.Size)
		}
		if src.Recall <= 0 || src.Recall >= 1 {
			// The number the offer shows. A zero here would offer a model with no
			// argument for installing it.
			t.Errorf("%s: recall %v is not a fraction", tag, src.Recall)
		}
		if other, clash := names[src.Filename()]; clash {
			t.Errorf("%s and %s both install as %s", tag, other, src.Filename())
		}
		names[src.Filename()] = tag
	}
}
