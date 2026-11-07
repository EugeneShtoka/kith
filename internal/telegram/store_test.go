package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// A plain store from before stores were encrypted opens encrypted, its positions and
// access hashes all there, and the file no longer holds them in the clear.
func TestAPlainStoreFromBeforeOpensEncrypted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "telegram.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, storeSchema+`
		INSERT INTO state VALUES (1001, 41, 7, 1700000000, 3);
		INSERT INTO channel_hashes VALUES (1001, 2002, 7777777777);
		INSERT INTO backfill VALUES (1001, 'telegram:1001/-1002002', 'msg-90', 40, 0);`); err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()

	st, err := OpenStore(ctx, path, bytes.Repeat([]byte{7}, sealeddb.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if got, ok, err := st.GetState(ctx, 1001); err != nil || !ok || got.Pts != 41 || got.Qts != 7 || got.Seq != 3 {
		t.Errorf("state read back as %+v, %v (%v)", got, ok, err)
	}
	if hash, ok, err := st.GetChannelAccessHash(ctx, 1001, 2002); err != nil || !ok || hash != 7777777777 {
		t.Errorf("channel hash read back as %d, %v (%v)", hash, ok, err)
	}
	if next, fetched, done, err := st.backfilled(ctx, 1001, "telegram:1001/-1002002"); err != nil || next != "msg-90" || fetched != 40 || done {
		t.Errorf("backfill read back as %q %d %v (%v)", next, fetched, done, err)
	}
	if file, _ := os.ReadFile(path); bytes.Contains(file, []byte("telegram:1001")) || bytes.HasPrefix(file, []byte("SQLite format 3")) {
		t.Error("the store is still in the clear")
	}
}
