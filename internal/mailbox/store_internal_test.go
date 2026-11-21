package mailbox

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	meshv1 "github.com/EugeneShtoka/kith/internal/api/mesh/v1"
	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/sealeddb"
)

// A bridge behind in two origins, each covered by another bridge's snapshot and
// neither snapshot covering the other, is sent both, once each a session.
func TestABridgeBehindGetsEverySnapshotItNeeds(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "mailbox.db"), make([]byte, sealeddb.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, b := devicekey.ID{1}, devicekey.ID{2}
	snap := func(maker devicekey.ID, va, vb uint64) *meshv1.SealedSnapshot {
		return &meshv1.SealedSnapshot{
			Boxes: [][]byte{{byte(va), byte(vb)}}, Maker: bytes.Clone(maker[:]), Last: true,
			Vector: []*meshv1.Head{{Origin: bytes.Clone(a[:]), Seq: va}, {Origin: bytes.Clone(b[:]), Seq: vb}},
		}
	}
	for _, sn := range []*meshv1.SealedSnapshot{snap(a, 5, 1), snap(b, 1, 5)} {
		if perr := s.putSnapshot(ctx, sn, time.Now()); perr != nil {
			t.Fatal(perr)
		}
	}
	told := map[int64]bool{}
	_, snaps, upTo, err := s.missing(ctx, map[devicekey.ID]uint64{}, told)
	if err != nil || len(snaps) != 2 || upTo[a] != 5 || upTo[b] != 5 {
		t.Fatalf("a bridge holding nothing is sent %d snapshots, up to %v (%v); want both, up to 5 each", len(snaps), upTo, err)
	}
	if _, again, _, _ := s.missing(ctx, map[devicekey.ID]uint64{}, told); len(again) != 0 {
		t.Errorf("sent %d snapshots again in the same session", len(again))
	}
}
