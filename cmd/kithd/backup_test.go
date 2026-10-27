package main

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/backup"
)

// A key file that is not there, or holds no key, is refused before anything is
// restored.
func TestAKeyFileThatIsNotThereOrNotAKeyIsRefused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readKey(filepath.Join(dir, "gone.txt")); err == nil {
		t.Error("readKey(missing) took a file that is not there")
	}
	notKey := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notKey, []byte("my notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKey(notKey); !errors.Is(err, backup.ErrWrongKey) {
		t.Errorf("readKey(not a key) = %v; want ErrWrongKey", err)
	}
}

// A new key is shown whole; the file it is saved to reads back as the key and is never
// written over.
func TestANewKeyIsShownAndSavedAsTheUserAsks(t *testing.T) {
	t.Parallel()
	id, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if shown := newKeyText(id); !strings.Contains(shown, id.String()) {
		t.Errorf("the new key's text does not show the key:\n%s", shown)
	}
	dir := t.TempDir()
	taken := filepath.Join(dir, "taken.txt")
	if werr := os.WriteFile(taken, []byte("mine"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	saved := filepath.Join(dir, "kith-key.txt")
	// The first path is taken, so it is asked again; the second is saved.
	if kerr := keepKeyFile(id, bufio.NewReader(strings.NewReader(taken+"\n"+saved+"\n"))); kerr != nil {
		t.Fatal(kerr)
	}
	if body, _ := os.ReadFile(taken); string(body) != "mine" { // #nosec G304 -- the test's own file
		t.Errorf("a file there was written over: %q", body)
	}
	got, err := readKey(saved)
	if err != nil || got.String() != id.String() {
		t.Fatalf("readKey(the saved file) = %v; want the key", err)
	}
	if info, err := os.Stat(saved); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("the saved key's mode = %v, %v; want 0600", info.Mode(), err)
	}
	// An empty answer: kept elsewhere, nothing written.
	if err := keepKeyFile(id, bufio.NewReader(strings.NewReader("\n"))); err != nil {
		t.Fatal(err)
	}
}
