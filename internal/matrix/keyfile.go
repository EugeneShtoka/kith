package matrix

import (
	"context"
	"errors"
	"fmt"

	"maunium.net/go/mautrix/crypto"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// Room keys as a spec-format export file (Element-compatible), for accounts without
// a server-side backup.

// ExportRoomKeys encrypts every megolm session this device holds and returns the
// export's bytes, not a path: the daemon cannot write outside its sandbox.
func (b *InProc) ExportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string) ([]byte, error) {
	if err := b.mine(on); err != nil {
		return nil, err
	}
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
func (b *InProc) ImportRoomKeys(ctx context.Context, on domain.RoomOwner, passphrase string, data []byte) (int, int, error) {
	if err := b.mine(on); err != nil {
		return 0, 0, err
	}
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

// unreadableExport reports whether err means an unreadable key export. A wrong
// passphrase and a corrupt file fail the same MAC check.
func unreadableExport(err error) bool {
	return errors.Is(err, crypto.ErrMismatchingExportHash) ||
		errors.Is(err, crypto.ErrMissingExportPrefix) ||
		errors.Is(err, crypto.ErrMissingExportSuffix) ||
		errors.Is(err, crypto.ErrUnsupportedExportVersion)
}
