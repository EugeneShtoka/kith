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

// A key file that is not there, or holds no key, is said as such, before anything is
// restored.
func TestAKeyFileThatIsNotThereOrNotAKeyIsSaid(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "gone.txt")
	if _, err := readKey(missing); err == nil || !strings.Contains(err.Error(), "gone.txt") {
		t.Errorf("readKey(missing) = %v; want the file named", err)
	}
	notKey := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notKey, []byte("my notes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readKey(notKey); !errors.Is(err, backup.ErrWrongKey) {
		t.Errorf("readKey(not a key) = %v; want ErrWrongKey", err)
	}
}

// A new key is shown once, whole, saying it is the only way to restore; the file it
// is saved to reads back as the key and is never written over.
func TestANewKeyIsShownAndSavedAsTheUserAsks(t *testing.T) {
	t.Parallel()
	id, err := backup.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	shown := newKeyText(id)
	for _, want := range []string{id.String(), "the only way to open your kith backups", "kith keeps no copy"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the new key's text lacks %q:\n%s", want, shown)
		}
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
