package matrix

import (
	"context"
	"errors"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// A call naming another account is refused here, before anything reaches the
// homeserver: this backend speaks for its own account only.
func TestACallForAnotherAccountIsRefused(t *testing.T) {
	t.Parallel()
	b := &InProc{ids: testIDs}
	other := domain.AccountRooms(domain.ProtocolMatrix, "fedcba9876543210")
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"PublicRooms":       func() error { _, err := b.PublicRooms(ctx, other, "", ""); return err },
		"StartVerification": func() error { _, err := b.StartVerification(ctx, other); return err },
		"RestoreKeyBackup":  func() error { _, err := b.RestoreKeyBackup(ctx, other, "s"); return err },
		"ExportRoomKeys":    func() error { _, err := b.ExportRoomKeys(ctx, other, "p"); return err },
		"ImportRoomKeys":    func() error { _, _, err := b.ImportRoomKeys(ctx, other, "p", nil); return err },
		"BackupRoomKeys":    func() error { _, err := b.BackupRoomKeys(ctx, other); return err },
		"BootstrapKeyBackup": func() error {
			_, err := b.BootstrapKeyBackup(ctx, other, "p")
			return err
		},
	} {
		if err := call(); !errors.Is(err, api.ErrNotOnNetwork) {
			t.Errorf("%s on another account = %v, want it refused", name, err)
		}
	}
}
