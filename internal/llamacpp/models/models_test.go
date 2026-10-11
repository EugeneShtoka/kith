package models

import (
	"os"
	"path/filepath"
	"testing"
)

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

// The manifest has 64-char hashes, positive sizes, and no colliding filenames.
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
			// The offer shows this number.
			t.Errorf("%s: recall %v is not a fraction", tag, src.Recall)
		}
		if other, clash := names[src.Filename()]; clash {
			t.Errorf("%s and %s both install as %s", tag, other, src.Filename())
		}
		names[src.Filename()] = tag
	}
}
