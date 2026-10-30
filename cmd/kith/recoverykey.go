package main

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/keystore"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// terminalAsk reads one answer at this terminal without echo; nil when stdin is not a
// terminal, so nothing is asked.
func terminalAsk() func(prompt string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil
	}
	return readSecretOrEmpty
}

// askBridgeKey makes kithd's device key here, before kithd makes one of its own, by
// asking for the recovery key of the kithd this machine takes over from (Enter for a
// new key, its recovery key printed). kithd never asks: without someone to ask (ask
// nil), or while kithd runs, nothing is done and kithd makes a new key when it first
// needs one, as ever. kithd's own lock is held meanwhile, so it cannot start and make
// one under the question. A key kithd already has is kept, but after a restore
// (restored) a recovery key may replace it, so the restored devices are paired with
// this kithd again.
func askBridgeKey(storage domain.Storage, allowFile, restored bool, ask func(prompt string) (string, error), out io.Writer) error {
	if ask == nil {
		return nil
	}
	lock, held, err := daemon.Acquire(storage)
	if err != nil || !held {
		return nil // kithd runs, its key its own; or it cannot, and says why itself
	}
	defer func() { _ = lock.Release() }() // nothing listened, so only the lock to drop
	st := keystore.DeviceStore(storage, "device")
	have, err := keystore.FindDeviceKey(st, allowFile)
	if err != nil || have != nil && !restored {
		return nil // kithd says why when it starts: a locked keyring, a corrupt key
	}
	prompt := "Recovery key (Enter for a new key)"
	if have == nil {
		_, _ = fmt.Fprintln(out, "kith: kithd has no device key yet. To keep the device ID of the kithd this machine\n"+
			"takes over from (its devices stay paired), type that kithd's recovery key.")
	} else {
		id, ierr := idText(have)
		if ierr != nil {
			return ierr
		}
		_, _ = fmt.Fprintf(out, "kith: kithd's device ID here is %s. To pair the restored devices with this kithd\n"+
			"again, type the recovery key of the kithd the backup came from.\n", id)
		prompt = "Recovery key (Enter to keep this key)"
	}
	key, err := askRecoveryKey(prompt, ask, out)
	switch {
	case err != nil:
		return err
	case key == nil && have != nil:
		return nil
	case key == nil:
		return keepNewKey(st, allowFile, out)
	case have != nil && key.Equal(have):
		_, _ = fmt.Fprintln(out, "kith: that is the key kithd has already; kept.")
		return nil
	}
	if kerr := keystore.KeepDeviceKey(st, allowFile, key); kerr != nil {
		return kerr
	}
	id, err := idText(key)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "kith: kithd's device key is the recovery key's: device ID %s.\n", id)
	return nil
}

// keepNewKey makes kithd's device key afresh, keeps it, and shows its recovery key,
// this once.
func keepNewKey(st keystore.Store, allowFile bool, out io.Writer) error {
	key, err := devicekey.Generate()
	if err != nil {
		return fmt.Errorf("kithd's device key: %w", err)
	}
	if kerr := keystore.KeepDeviceKey(st, allowFile, key); kerr != nil {
		return kerr
	}
	_, _ = fmt.Fprintln(out, "kith: kithd has a new device key.")
	return showRecoveryKey(out, key)
}

// askRecoveryKey asks until the answer is a recovery key, or nothing (nil).
func askRecoveryKey(prompt string, ask func(prompt string) (string, error), out io.Writer) (*ecdsa.PrivateKey, error) {
	for {
		typed, err := ask(prompt)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(typed) == "" {
			return nil, nil
		}
		key, err := devicekey.ParseRecoveryKey(typed)
		if err == nil {
			return key, nil
		}
		_, _ = fmt.Fprintf(out, "kith: %v. Type it again.\n", err)
	}
}

// idText is key's device ID, written out.
func idText(key *ecdsa.PrivateKey) (string, error) {
	id, err := devicekey.IDOf(key.Public())
	if err != nil {
		return "", fmt.Errorf("kithd's device key: %w", err)
	}
	return id.String(), nil
}

// showRecoveryKey prints kithd's device ID and recovery key, and what the key is for.
func showRecoveryKey(out io.Writer, key *ecdsa.PrivateKey) error {
	written, err := devicekey.RecoveryKey(key)
	if err != nil {
		return fmt.Errorf("kithd's recovery key: %w", err)
	}
	id, err := idText(key)
	if err != nil {
		return err
	}
	if _, werr := fmt.Fprintf(out, "kithd's device ID: %s\nIts recovery key, to write down:\n\n    %s\n\n"+
		"A backup holds no device key. Typed in when kith asks for it on another machine (after\n"+
		"`kith restore`, or before kithd first starts there), it keeps this device ID, so the\n"+
		"devices paired with it stay paired. Anyone who has it can pose as this bridge: keep it\n"+
		"as you would a password. `kith devices recovery-key` shows it again, on this machine.\n", id, written); werr != nil {
		return fmt.Errorf("show kithd's recovery key: %w", werr)
	}
	return nil
}

// printBridgeRecoveryKey is `kith devices recovery-key`: the recovery key of this
// machine's kithd, read from its keyring here, never asked of a kithd over any
// connection, so no other device can have it shown. The config's storage is found as
// a client's is (setup.ClientStorage), which is kithd's when the config is here.
func printBridgeRecoveryKey(configPath string, out io.Writer) error {
	path, err := configFile(configPath)
	if err != nil {
		return err
	}
	storage, allowFile, err := setup.ClientStorage(path)
	if err != nil {
		return err
	}
	key, err := keystore.FindDeviceKey(keystore.DeviceStore(storage, "device"), allowFile)
	if err != nil {
		return fmt.Errorf("kithd's device key: %w", err)
	}
	if key == nil {
		return errors.New("kithd has no device key on this machine: it makes one when it first starts. " +
			"A bridge's recovery key is shown on the bridge's own machine")
	}
	return showRecoveryKey(out, key)
}
