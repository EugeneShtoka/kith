package matrix

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix/crypto"

	"github.com/EugeneShtoka/kith/internal/api"
)

// Room keys as a spec-format export file, interoperable with Element: the answer for
// accounts without a server-side backup.

// ExportRoomKeys encrypts every megolm session this device holds and returns the
// export's bytes. Bytes, not a path: the daemon cannot write outside its sandbox,
// and what crosses the socket is already ciphertext.
func (b *InProc) ExportRoomKeys(ctx context.Context, passphrase string) ([]byte, error) {
	helper := b.cryptoHelper()
	if helper == nil {
		return nil, api.ErrNoEncryption
	}
	mach := helper.Machine()
	data, err := crypto.ExportKeysIter(passphrase, mach.CryptoStore.GetAllGroupSessions(ctx))
	switch {
	case errors.Is(err, crypto.ErrNoSessionsForExport):
		// An empty export would look like a backup and restore nothing.
		return nil, api.ErrNoRoomKeys
	case err != nil:
		return nil, fmt.Errorf("matrix: export room keys: %w", err)
	}
	return data, nil
}

// ImportRoomKeys adds a key export's sessions, returning how many were new and how
// many the file held.
func (b *InProc) ImportRoomKeys(ctx context.Context, passphrase string, data []byte) (int, int, error) {
	helper := b.cryptoHelper()
	if helper == nil {
		return 0, 0, api.ErrNoEncryption
	}
	imported, total, err := helper.Machine().ImportKeys(ctx, passphrase, data)
	if err != nil {
		if unreadableExport(err) {
			return 0, 0, api.ErrBadKeyFile
		}
		return 0, 0, fmt.Errorf("matrix: import room keys: %w", err)
	}
	return imported, total, nil
}

// unreadableExport reports whether err means "not a key export I can read". A wrong
// passphrase and a corrupt file fail the same MAC check, so they are one answer.
func unreadableExport(err error) bool {
	return errors.Is(err, crypto.ErrMismatchingExportHash) ||
		errors.Is(err, crypto.ErrMissingExportPrefix) ||
		errors.Is(err, crypto.ErrMissingExportSuffix) ||
		errors.Is(err, crypto.ErrUnsupportedExportVersion)
}
