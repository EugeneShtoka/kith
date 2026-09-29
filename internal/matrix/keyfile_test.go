package matrix

import (
	"context"
	"errors"
	"strings"
	"testing"

	"maunium.net/go/mautrix/crypto"

	"github.com/EugeneShtoka/kith/internal/api"
)

// Without a crypto machine both calls refuse rather than panic.
func TestKeyFileOperationsRefuseWithoutEncryption(t *testing.T) {
	t.Parallel()

	b, ctx := New(nil), context.Background()

	if _, err := b.ExportRoomKeys(ctx, "hunter2"); !errors.Is(err, api.ErrNoEncryption) {
		t.Errorf("ExportRoomKeys error = %v, want ErrNoEncryption", err)
	}
	if _, _, err := b.ImportRoomKeys(ctx, "hunter2", []byte("anything")); !errors.Is(err, api.ErrNoEncryption) {
		t.Errorf("ImportRoomKeys error = %v, want ErrNoEncryption", err)
	}
}

// Every unreadable export is ErrBadKeyFile (a MAC failure cannot say which cause).
func TestEveryUnreadableExportIsTheSameAnswer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"wrong passphrase or corrupted", crypto.ErrMismatchingExportHash, true},
		{"not an export at all", crypto.ErrMissingExportPrefix, true},
		{"truncated", crypto.ErrMissingExportSuffix, true},
		{"a format this build predates", crypto.ErrUnsupportedExportVersion, true},
		// A failure that is not the file's keeps its own message.
		{"a canceled context", context.Canceled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := unreadableExport(tc.err); got != tc.want {
				t.Errorf("unreadableExport(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// Exports use the spec's format, for interop with Element.
func TestAnExportIsTheFormatTheSpecDefines(t *testing.T) {
	t.Parallel()

	data, err := crypto.ExportKeys("hunter2", nil)
	if errors.Is(err, crypto.ErrNoSessionsForExport) {
		data = nil
	} else if err != nil {
		t.Fatalf("ExportKeys: %v", err)
	}
	if data != nil && !strings.HasPrefix(string(data), "-----BEGIN MEGOLM SESSION DATA-----") {
		t.Errorf("export does not carry the spec's header: %.40q", data)
	}
}

// An empty export is refused rather than written.
func TestExportingNothingIsRefusedRatherThanWritten(t *testing.T) {
	t.Parallel()

	if _, err := crypto.ExportKeys("hunter2", nil); !errors.Is(err, crypto.ErrNoSessionsForExport) {
		t.Fatalf("an empty export returned %v; the mapping to ErrNoRoomKeys assumes this sentinel", err)
	}
}
