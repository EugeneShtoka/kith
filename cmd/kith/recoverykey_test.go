package main

import (
	"bytes"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
)

func newDeviceKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func recoveryOf(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	written, err := devicekey.RecoveryKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

func idOf(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// typo is written with one character changed.
func typo(written string) string {
	b := []byte(written)
	if b[10] == 'A' {
		b[10] = 'B'
	} else {
		b[10] = 'A'
	}
	return string(b)
}

// shownRecoveryKey is the recovery key out shows, indented on a line of its own.
func shownRecoveryKey(out string) string {
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, "    ") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// What kithd's device key is after askBridgeKey, over every situation it can meet
// (kithd's key there or not, after a restore or not, kithd running or not, a terminal
// or not) and every way the questions can be answered: a key made or recovered only
// when nobody else has one to keep, a typo never taken as another key, a closed
// terminal leaving everything as it was.
func TestKithdsKeyIsAskedForOnlyWhenItIsFirstMade(t *testing.T) {
	keyring.MockInit()
	type answer struct {
		text string
		err  error
	}
	closed := errors.New("the terminal closed")
	recovered := newDeviceKey(t)
	for _, had := range []bool{false, true} {
		for _, restored := range []bool{false, true} {
			for _, running := range []bool{false, true} {
				for _, terminal := range []bool{false, true} {
					have := newDeviceKey(t)
					scripts := map[string][]answer{
						"enter":                          {{text: ""}},
						"a recovery key":                 {{text: recoveryOf(t, recovered)}},
						"its own recovery key":           {{text: recoveryOf(t, have)}},
						"spaced, lower case, two lines":  {{text: strings.ToLower(strings.Replace(strings.ReplaceAll(recoveryOf(t, recovered), "-", " "), " ", "\n", 5))}},
						"a typo, then the key":           {{text: typo(recoveryOf(t, recovered))}, {text: recoveryOf(t, recovered)}},
						"words, then enter":              {{text: "my old laptop"}, {text: "  "}},
						"the terminal closes":            {{err: closed}},
						"a typo, then the terminal ends": {{text: typo(recoveryOf(t, recovered))}, {err: closed}},
					}
					for script, answers := range scripts {
						name := fmt.Sprintf("had %v, restored %v, running %v, terminal %v, %s", had, restored, running, terminal, script)
						t.Run(name, func(t *testing.T) {
							storage := domain.Storage{
								Instance: "test", KeyringService: "kith-" + t.Name(),
								StateDir: t.TempDir(), RuntimeDir: filepath.Join(t.TempDir(), "run"),
							}
							st := keystore.DeviceStore(storage, "device")
							if had {
								if err := keystore.KeepDeviceKey(st, false, have); err != nil {
									t.Fatal(err)
								}
							}
							if running {
								lock, held, err := daemon.Acquire(storage)
								if err != nil || !held {
									t.Fatalf("Acquire() = %v, %v", held, err)
								}
								defer func() { _ = lock.Release() }()
							}
							asked := 0
							var ask func(string) (string, error)
							if terminal {
								ask = func(string) (string, error) {
									if asked == len(answers) {
										t.Fatalf("asked %d times, answered %d", asked+1, len(answers))
									}
									asked++
									return answers[asked-1].text, answers[asked-1].err
								}
							}
							var out bytes.Buffer
							err := askBridgeKey(storage, false, restored, ask, &out)
							after, ferr := keystore.FindDeviceKey(st, false)
							if ferr != nil {
								t.Fatal(ferr)
							}
							before := map[bool]*ecdsa.PrivateKey{true: have}[had]

							if !terminal || running || had && !restored {
								if asked != 0 || err != nil || out.Len() != 0 || !sameKey(after, before) {
									t.Fatalf("not to be asked: asked %d, err %v, key changed %v, said %q", asked, err, !sameKey(after, before), out.String())
								}
								return
							}
							if asked != len(answers) {
								t.Fatalf("asked %d times, want %d", asked, len(answers))
							}
							last := answers[len(answers)-1]
							switch {
							case last.err != nil:
								if !errors.Is(err, closed) || !sameKey(after, before) {
									t.Fatalf("the terminal closed: err %v, key changed %v", err, !sameKey(after, before))
								}
							case strings.TrimSpace(last.text) == "" && had:
								if err != nil || !sameKey(after, before) {
									t.Fatalf("enter keeps the key there: err %v, key changed %v", err, !sameKey(after, before))
								}
							case strings.TrimSpace(last.text) == "":
								shown, perr := devicekey.ParseRecoveryKey(shownRecoveryKey(out.String()))
								if err != nil || after == nil || perr != nil || !shown.Equal(after) {
									t.Fatalf("enter makes a key and shows its recovery key: err %v, made %v, shown %v\n%s", err, after != nil, perr, out.String())
								}
							default:
								want, perr := devicekey.ParseRecoveryKey(last.text)
								if perr != nil {
									t.Fatal(perr)
								}
								if err != nil || !sameKey(after, want) {
									t.Fatalf("a recovery key typed is kithd's key: err %v, kept it %v", err, sameKey(after, want))
								}
								if !strings.Contains(out.String(), idOf(t, want)) || strings.Contains(out.String(), recoveryOf(t, want)) {
									t.Fatalf("want its device ID said, and the key typed never echoed:\n%s", out.String())
								}
							}
						})
					}
				}
			}
		}
	}
}

// After `kith restore` the recovery key is asked for the config kithd restored, and
// not at all when kithd was asked for no restore (its help, a usage error).
func TestTheRestoredConfigIsTheOneKithdRestored(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args     []string
		path     string
		restored bool
	}{
		{args: []string{"b.tar.age"}, restored: true},
		{args: []string{"--config", "/x/config.toml", "--replace", "--key-file", "k.txt", "b.tar.age"}, path: "/x/config.toml", restored: true},
		{args: []string{"-config=/x/config.toml", "b.tar.age"}, path: "/x/config.toml", restored: true},
		{args: []string{"--help"}},
		{args: []string{"-h", "b.tar.age"}},
		{args: nil},
		{args: []string{"a.tar.age", "b.tar.age"}},
		{args: []string{"--unknown", "b.tar.age"}},
	} {
		path, restored := restoredConfig(tc.args)
		if path != tc.path || restored != tc.restored {
			t.Errorf("restoredConfig(%q) = %q, %v; want %q, %v", tc.args, path, restored, tc.path, tc.restored)
		}
	}
}

// With stdin not a terminal (a script, a pipe, a unit), nothing is asked: kithd makes
// its own key as ever.
func TestNothingIsAskedWithoutATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	stdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = stdin }()
	if terminalAsk() != nil {
		t.Fatal("a pipe on stdin is asked as a terminal")
	}
}

func sameKey(a, b *ecdsa.PrivateKey) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(b)
}

// `kith devices recovery-key` shows the key of this machine's kithd, read from its
// keyring here: never this machine's own client key, and nothing on a machine whose
// clients dial a bridge elsewhere and that runs no kithd of its own.
func TestTheRecoveryKeyShownIsThisMachinesKithds(t *testing.T) {
	keyring.MockInit()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(path, []byte(fmt.Sprintf("[storage]\ninstance = \"test\"\nstate_dir = %q\nkeyring_service = %q\n", state, "kith-"+t.Name())), 0o600); err != nil {
		t.Fatal(err)
	}
	storage := domain.Storage{Instance: "test", KeyringService: "kith-" + t.Name(), StateDir: state}
	client := newDeviceKey(t)
	if err := keystore.KeepDeviceKey(keystore.DeviceStore(storage, "client-device"), false, client); err != nil {
		t.Fatal(err)
	}
	// Joined to a bridge elsewhere, with no kithd here.
	if err := os.WriteFile(filepath.Join(dir, "device.toml"), []byte("[[bridge]]\nname = \"home\"\naddresses = [\"192.168.1.5:7443\"]\nid = \""+idOf(t, newDeviceKey(t))+"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printBridgeRecoveryKey(path, &out); err == nil || out.Len() != 0 {
		t.Fatalf("with no kithd here: err %v, shown %q", err, out.String())
	}

	bridge := newDeviceKey(t)
	if err := keystore.KeepDeviceKey(keystore.DeviceStore(storage, "device"), false, bridge); err != nil {
		t.Fatal(err)
	}
	if err := printBridgeRecoveryKey(path, &out); err != nil {
		t.Fatal(err)
	}
	shown, err := devicekey.ParseRecoveryKey(shownRecoveryKey(out.String()))
	if err != nil || !shown.Equal(bridge) || !strings.Contains(out.String(), idOf(t, bridge)) {
		t.Fatalf("want kithd's recovery key and ID shown: %v\n%s", err, out.String())
	}
}
