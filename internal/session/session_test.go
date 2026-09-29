package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/adrg/xdg"
	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// withTempState points the file fallback at a temp dir and returns the path of
// sample()'s account file in it.
func withTempState(t *testing.T) string {
	t.Helper()
	// As a person moves it: the environment, read again. The reload is registered
	// first, so it runs after the environment is put back.
	t.Cleanup(xdg.Reload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	xdg.Reload()
	return sessionPath(sample().UserID)
}

func sample() domain.Session {
	return domain.Session{
		Homeserver:  "https://matrix.example.org",
		UserID:      "@alice:example.org",
		DeviceID:    "DEVICE1",
		AccessToken: "syt_token",
	}
}

func TestKeyringRoundTrip(t *testing.T) {
	keyring.MockInit()
	path := withTempState(t)

	want := sample()
	if err := Save(want.UserID, want, false); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	// The keyring holds it, so no plaintext fallback file should exist.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("fallback file present after keyring Save; stat err = %v", err)
	}

	got, found, err := Load(want.UserID, false)
	if err != nil || !found {
		t.Fatalf("Load() = %+v, found=%v, err=%v", got, found, err)
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestPickleKeyIsStableAndGenerated(t *testing.T) {
	keyring.MockInit()

	first, err := LoadOrCreatePickleKey("@alice:example.org")
	if err != nil {
		t.Fatalf("LoadOrCreatePickleKey() error = %v", err)
	}
	if len(first) != pickleKeyLen {
		t.Fatalf("pickle key len = %d, want %d", len(first), pickleKeyLen)
	}
	// A second call returns the same persisted key.
	second, _ := LoadOrCreatePickleKey("@alice:example.org")
	if !bytes.Equal(second, first) {
		t.Error("pickle key changed between calls; must be stable")
	}
	// A different user gets a different key.
	other, _ := LoadOrCreatePickleKey("@bob:example.org")
	if bytes.Equal(other, first) {
		t.Error("pickle key should differ per user")
	}
}

func TestPickleKeyWithoutKeyring(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	if _, err := LoadOrCreatePickleKey("@alice:example.org"); !errors.Is(err, ErrNoSecretStore) {
		t.Errorf("error = %v, want ErrNoSecretStore", err)
	}
}

func TestLoadNoSessionIsNotFound(t *testing.T) {
	keyring.MockInit()
	withTempState(t)

	got, found, err := Load("@nobody:example.org", false)
	if found || err != nil {
		t.Errorf("Load(unknown) = %+v, found=%v, err=%v; want zero, false, nil", got, found, err)
	}
}

func TestNoKeyringWithoutOptInIsNotPersisted(t *testing.T) {
	keyring.MockInitWithError(keyring.ErrUnsupportedPlatform)
	path := withTempState(t)

	err := Save(sample().UserID, sample(), false)
	if !errors.Is(err, ErrNoSecretStore) {
		t.Fatalf("Save() = %v, want ErrNoSecretStore", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("token written to disk without opt-in; stat err = %v", statErr)
	}
	// Nothing to resume: Load reports first-run.
	if _, found, lerr := Load(sample().UserID, false); found || lerr != nil {
		t.Errorf("Load() = found=%v, err=%v; want false, nil", found, lerr)
	}
}

func TestFileFallbackWhenOptedIn(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	path := withTempState(t)

	want := sample()
	if err := Save(want.UserID, want, true); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fallback file missing: %v", err)
	}
	if perm := info.Mode().Perm(); perm != ownerOnly {
		t.Errorf("fallback perm = %#o, want %#o", perm, ownerOnly)
	}

	got, found, err := Load(want.UserID, true)
	if err != nil || !found {
		t.Fatalf("Load() = %+v, found=%v, err=%v", got, found, err)
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestFileFallbackRejectsInsecurePerms(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	path := withTempState(t)

	if err := Save(sample().UserID, sample(), true); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, _, err := Load(sample().UserID, true); err == nil {
		t.Error("Load() of group/other-readable fallback = nil error, want error")
	}
}

// A save that cannot complete leaves the previous session file untouched.
func TestAFailedSaveKeepsThePreviousSession(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so the write cannot be made to fail this way")
	}
	keyring.MockInitWithError(errors.New("no secret store"))
	path := withTempState(t)

	first := sample()
	if err := Save(first.UserID, first, true); err != nil {
		t.Fatalf("first Save() = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	dir := filepath.Dir(path)
	if cerr := os.Chmod(dir, 0o500); cerr != nil {
		t.Fatalf("could not make the directory read-only: %v", cerr)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	second := sample()
	second.AccessToken = "syt_a_completely_different_token"
	if saveErr := Save(second.UserID, second, true); saveErr == nil {
		t.Fatal("saving into an unwritable directory reported success")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the previous session file is gone after a failed save: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("a failed save changed the stored session.\nbefore: %s\nafter:  %s", before, after)
	}
}

// A successful save leaves no temporary file behind.
func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	path := withTempState(t)

	if err := Save(sample().UserID, sample(), true); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("left %s behind beside the session file", e.Name())
		}
	}
}

// A keyring that is present but will not answer is not a first run.
func TestALockedKeyringIsNotReportedAsAFirstRun(t *testing.T) {
	keyring.MockInitWithError(errors.New("org.freedesktop.DBus.Error.TimedOut: keyring is locked"))
	withTempState(t)

	got, found, err := Load(sample().UserID, false)
	if err == nil {
		t.Fatalf("Load() = %+v found=%v err=nil; a locked keyring must not read as a first run",
			got, found)
	}
	if found {
		t.Error("Load() reported a session it could not read")
	}
	if !strings.Contains(err.Error(), "secret store") {
		t.Errorf("error %q does not say what could not be read", err)
	}
}

// Save says *why* the store refused, rather than only that there wasn't one.
func TestSaveExplainsWhyTheSecretStoreRefused(t *testing.T) {
	keyring.MockInitWithError(errors.New("org.freedesktop.DBus.Error.TimedOut: keyring is locked"))
	withTempState(t)

	err := Save(sample().UserID, sample(), false)
	if !errors.Is(err, ErrNoSecretStore) {
		t.Fatalf("Save() = %v, want it to wrap ErrNoSecretStore", err)
	}
	if !strings.Contains(err.Error(), "locked") {
		t.Errorf("error %q drops the reason the keyring gave", err)
	}
}

func other() domain.Session {
	return domain.Session{
		Homeserver:  "https://work.example.org",
		UserID:      "@bob:work.example.org",
		DeviceID:    "DEVICE2",
		AccessToken: "syt_other",
	}
}

// Two profiles on the file fallback each keep their own session.
func TestFileFallbackIsPerAccount(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	withTempState(t)

	a, b := sample(), other()
	if err := Save(a.UserID, a, true); err != nil {
		t.Fatal(err)
	}
	if err := Save(b.UserID, b, true); err != nil {
		t.Fatal(err)
	}
	if sessionPath(a.UserID) == sessionPath(b.UserID) {
		t.Fatal("two accounts share one fallback file")
	}
	for _, want := range []domain.Session{a, b} {
		got, found, err := Load(want.UserID, true)
		if err != nil || !found || got != want {
			t.Errorf("Load(%s) = %+v found=%v err=%v, want %+v", want.UserID, got, found, err, want)
		}
	}
}

// A keyring save for one account removes only that account's plaintext file.
func TestKeyringSaveRemovesOnlyItsOwnFile(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret store"))
	withTempState(t)
	a, b := sample(), other()
	if err := Save(a.UserID, a, true); err != nil {
		t.Fatal(err)
	}
	if err := Save(b.UserID, b, true); err != nil {
		t.Fatal(err)
	}

	keyring.MockInit()
	if err := Save(a.UserID, a, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sessionPath(a.UserID)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a's plaintext file survived a keyring save; stat err = %v", err)
	}
	if _, err := os.Stat(sessionPath(b.UserID)); err != nil {
		t.Errorf("a's keyring save deleted b's session file: %v", err)
	}
	if got, found, err := Load(b.UserID, true); err != nil || !found || got != other() {
		t.Errorf("Load(b) = %+v found=%v err=%v", got, found, err)
	}
}

// A pickle key that cannot be read is reported, never replaced: the crypto store it
// encrypted, with this device's identity, opens only with the old key.
func TestACorruptPickleKeyIsKeptAndReported(t *testing.T) {
	keyring.MockInit()
	const acct = "@alice:example.org|pickle"
	for _, corrupt := range []string{"not base64!", "c2hvcnQ="} { // garbage, and too short
		if err := keyring.Set(service, acct, corrupt); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOrCreatePickleKey("@alice:example.org"); !errors.Is(err, ErrCorruptPickleKey) {
			t.Errorf("LoadOrCreatePickleKey() with %q stored: error = %v, want ErrCorruptPickleKey", corrupt, err)
		}
		if kept, err := keyring.Get(service, acct); err != nil || kept != corrupt {
			t.Errorf("the entry became %q (err %v); want it left as %q", kept, err, corrupt)
		}
	}
}
