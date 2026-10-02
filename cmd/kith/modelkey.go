package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/EugeneShtoka/kith/internal/session"
)

// maxKeyBytes bounds what is read from a pipe.
const maxKeyBytes = 8 << 10

// setModelKey stores the model API key under ref in the OS keyring (not scoped to the
// Matrix account). An empty answer deletes it. The value is never printed.
func setModelKey(keyring, ref string) error {
	key, err := readKeyInput(fmt.Sprintf("API key for [assist] key_ref = %q", ref))
	if err != nil {
		return err
	}
	if key == "" {
		if derr := session.DeleteSecret(keyring, ref); derr != nil {
			return fmt.Errorf("removing the key: %w", derr)
		}
		fmt.Printf("kith: removed the key stored as %q\n", ref)
		return nil
	}
	if err := session.StoreSecret(keyring, ref, key); err != nil {
		return fmt.Errorf("storing the key: %w", err)
	}
	fmt.Printf("kith: stored a key as %q — set `key_ref = %q` under [assist]\n", ref, ref)
	return nil
}

// readKeyInput reads the key from the terminal, or from a pipe. Unlike passwords, a
// rotatable API key is allowed from a pipe: it usually lives in a password manager.
func readKeyInput(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return readSecret(prompt)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maxKeyBytes))
	if err != nil {
		return "", fmt.Errorf("read key: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}
