//go:build signal

package meow_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
	"github.com/EugeneShtoka/kith/internal/signalhelper/meow"
)

// Open creates the key store encrypted, migrates it, and lists no devices; the store
// opens again with its key and with no other.
func TestOpenCreatesAnEncryptedStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "signal.db")
	key := bytes.Repeat([]byte{7}, sealeddb.KeyLen)
	log := slog.New(slog.DiscardHandler)

	devices, err := meow.New(path, log).Open(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Errorf("a new store lists %v", devices)
	}
	head := make([]byte, 16)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Read(head)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(string(head), "SQLite format 3") {
		t.Error("the key store is written in the clear")
	}

	if _, err := meow.New(path, log).Open(ctx, key); err != nil {
		t.Errorf("the store does not open again with its key: %v", err)
	}
	wrong := bytes.Repeat([]byte{8}, sealeddb.KeyLen)
	if _, err := meow.New(path, log).Open(ctx, wrong); !errors.Is(err, sealeddb.ErrWrongKey) {
		t.Errorf("another key = %v, want ErrWrongKey", err)
	}
	network := meow.New(path, log)
	if _, err := network.Open(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := network.Open(ctx, key); err == nil {
		t.Error("a second Open of one Network succeeded")
	}
}
