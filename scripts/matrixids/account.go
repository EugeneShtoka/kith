package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The second part (planning/design-matrix-accounts.md, step 5): the one Matrix account,
// once set at the config's top level, becomes a [[matrix.account]], and its files and
// keyring entries take the per-account names, so its device stays verified.

// moveAccount turns the config's top-level homeserver and user into a
// [[matrix.account]] named after the user's localpart, appended at the end; a config
// without them (none, or [[profile]] blocks) is left as it is.
func moveAccount(user string) func(ids, []byte) ([]byte, int, error) {
	return func(_ ids, in []byte) ([]byte, int, error) {
		var kept strings.Builder
		var moved []string
		top := true // before the first table: bare keys are top-level
		for line := range strings.Lines(string(in)) {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") {
				top = false
			}
			if m := tomlKey.FindStringSubmatch(line); top && m != nil && (m[1] == "homeserver" || m[1] == "user") {
				moved = append(moved, strings.TrimRight(trimmed, "\n"))
				continue
			}
			kept.WriteString(line)
		}
		if len(moved) == 0 {
			return in, 0, nil
		}
		if len(moved) != 2 {
			return nil, 0, errors.New("the top level names homeserver or user without the other: set both, or neither")
		}
		local, _, _ := strings.Cut(strings.TrimPrefix(user, "@"), ":")
		out := strings.TrimRight(kept.String(), "\n") + "\n\n[[matrix.account]]\nname = \"" + local + "\"\n" +
			strings.Join(moved, "\n") + "\n"
		return []byte(out), len(moved), nil
	}
}

// moveStores renames the account's crypto store and session file in from to the
// names that carry its key in to; one already moved or never made is skipped.
func moveStores(from, to domain.Storage, user string, dry bool) (int, error) {
	key := domain.AccountKey(user)
	moved := 0
	for _, f := range []struct{ from, to string }{
		{from.CryptoPath(), to.MatrixCryptoPath(key)},
		{from.CryptoPath() + "-wal", to.MatrixCryptoPath(key) + "-wal"},
		{from.CryptoPath() + "-shm", to.MatrixCryptoPath(key) + "-shm"},
		{from.SessionFile(), to.MatrixSessionFile(key)},
	} {
		if _, err := os.Stat(f.from); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if _, err := os.Stat(f.to); err == nil {
			return moved, fmt.Errorf("%s exists already: move it away first", f.to)
		}
		fmt.Printf("move %s -> %s\n", f.from, f.to)
		moved++
		if dry {
			continue
		}
		if err := os.Rename(f.from, f.to); err != nil {
			return moved, fmt.Errorf("move %s: %w", f.from, err)
		}
	}
	return moved, nil
}

// copyKeys copies the account's session and encryption key in from's keyring entries
// to the ones that carry its key in to. The old entries stay, for the user to delete
// once the account runs; an entry already there is never overwritten.
func copyKeys(fromStorage, toStorage domain.Storage, user string, dry bool) (int, error) {
	from := fromStorage.Instance
	if fromStorage.Instance == domain.AccountKey(user) {
		from = user // an instance from before instances kept them under the MXID
	}
	to := toStorage.Instance + "|matrix|" + domain.AccountKey(user)
	service := fromStorage.KeyringService
	copied := 0
	for _, suffix := range []string{"", "|pickle"} {
		value, err := keyring.Get(service, from+suffix)
		if errors.Is(err, keyring.ErrNotFound) {
			continue
		}
		if err != nil {
			return copied, fmt.Errorf("read %s%s: %w", from, suffix, err)
		}
		if _, err := keyring.Get(service, to+suffix); err == nil {
			return copied, fmt.Errorf("%s%s exists already in the keyring: delete it first", to, suffix)
		}
		fmt.Printf("copy keyring %s%s -> %s%s\n", from, suffix, to, suffix)
		copied++
		if dry {
			continue
		}
		if err := keyring.Set(service, to+suffix, value); err != nil {
			return copied, fmt.Errorf("write %s%s: %w", to, suffix, err)
		}
	}
	return copied, nil
}
