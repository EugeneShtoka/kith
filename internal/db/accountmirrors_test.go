package db

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// v25Cache is a cache as v25 left it: one Matrix account's mirrors with no account, a
// Telegram mark kept here alone, and a bridge login's names under no account. With
// matrixRooms, the account's rooms are cached.
func v25Cache(t *testing.T, matrixRooms bool) string {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	c, err := Open(ctx, path, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if matrixRooms {
		if serr := c.SaveRooms(ctx, mxRooms, []domain.Room{{ID: mx + "!a:x", Name: "A"}}); serr != nil {
			t.Fatal(serr)
		}
	}
	for source, name := range map[string]domain.PersonName{
		"bridge:https://example.org/_matrix/provision/wa#l1": {ID: domain.PhoneID("15550100001"), Name: "Dana", Rank: domain.RankSaved},
		"whatsapp:111": {ID: domain.PhoneID("15550100002"), Name: "Eli", Rank: domain.RankSaved},
	} {
		link := domain.PersonLink{ID: name.ID, Other: "whatsapp:" + name.ID[len("tel:"):]}
		if serr := c.SetPeople(ctx, source, []domain.PersonName{name}, []domain.PersonLink{link}); serr != nil {
			t.Fatal(serr)
		}
	}
	if cerr := c.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	raw, err := openRaw(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"DROP TABLE ignored_users", "DROP TABLE person_verdicts",
		`CREATE TABLE ignored_users (
	network TEXT NOT NULL,
	user_id TEXT NOT NULL,
	PRIMARY KEY (network, user_id)
) STRICT, WITHOUT ROWID`,
		`CREATE TABLE person_verdicts (
	person   TEXT    NOT NULL,
	tag      TEXT    NOT NULL,
	rule     INTEGER NOT NULL DEFAULT 0,
	filter   TEXT    NOT NULL DEFAULT '',
	at_ms    INTEGER NOT NULL DEFAULT 0,
	released INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (person, tag)
) STRICT, WITHOUT ROWID`,
		"INSERT INTO ignored_users VALUES ('Matrix', 'matrix:@ann:x')",
		"INSERT INTO person_verdicts VALUES ('matrix:@bob:x', 'spam', 1, 'crypto', 1000, 0), ('telegram:1001', 'spam', 1, '', 2000, 0)",
		"PRAGMA user_version = 25",
	} {
		if _, xerr := raw.ExecContext(ctx, stmt); xerr != nil {
			t.Fatalf("%s: %v", stmt, xerr)
		}
	}
	if rerr := raw.Close(); rerr != nil {
		t.Fatal(rerr)
	}
	return path
}

// Upgrading a v25 cache puts its Matrix mirrors and bridge names under the account its
// rooms are cached for, so that account's next write replaces them; with no Matrix
// room cached they go, as the homeserver holds them. What is kept here alone stays.
func TestAnUpgradedCachesMatrixMirrorsAreTheirAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, matrixRooms := range []bool{true, false} {
		c, err := Open(ctx, v25Cache(t, matrixRooms), testKey)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		ignored, err := c.MatrixIgnored(ctx)
		if err != nil {
			t.Fatal(err)
		}
		verdicts, err := c.PersonVerdicts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var marked []string
		for _, v := range verdicts {
			marked = append(marked, v.Person)
		}
		bridged, err := c.PeopleSources(ctx, "bridge:")
		if err != nil {
			t.Fatal(err)
		}
		kept, err := c.PeopleSources(ctx, "whatsapp:")
		if err != nil {
			t.Fatal(err)
		}
		wantIgnored, wantMarked := []string(nil), []string{"telegram:1001"}
		wantBridged := []string(nil)
		if matrixRooms {
			wantIgnored, wantMarked = []string{"matrix:@ann:x"}, []string{"telegram:1001", "matrix:@bob:x"}
			wantBridged = []string{"bridge:" + string(mxRooms) + "https://example.org/_matrix/provision/wa#l1"}
		}
		if !slices.Equal(ignored, wantIgnored) || !slices.Equal(marked, wantMarked) || !slices.Equal(bridged, wantBridged) {
			t.Errorf("Matrix rooms cached %t: ignored %v, marked %v, bridge sources %v; want %v, %v, %v",
				matrixRooms, ignored, marked, bridged, wantIgnored, wantMarked, wantBridged)
		}
		dir, err := c.Directory(ctx)
		if err != nil {
			t.Fatal(err)
		}
		dana, _ := dir.Named("+15550100001")
		eli, _ := dir.Named("+15550100002")
		wantDana := ""
		if matrixRooms {
			wantDana = "Dana" // the bridge login's, under the account now
		}
		if dana != wantDana || eli != "Eli" {
			t.Errorf("Matrix rooms cached %t: the bridge names Dana %q and WhatsApp Eli %q, want %q and Eli", matrixRooms, dana, eli, wantDana)
		}
		if !slices.Equal(kept, []string{"whatsapp:111"}) {
			t.Errorf("Matrix rooms cached %t: other sources %v, want whatsapp:111 kept", matrixRooms, kept)
		}
		if !matrixRooms {
			continue
		}
		// They are the account's now: its next write replaces them.
		if err := c.SetMatrixIgnored(ctx, mxRooms, nil); err != nil {
			t.Fatal(err)
		}
		if err := c.SetPersonVerdicts(ctx, mxRooms, []domain.PersonVerdict{{Person: "matrix:@cy:x", Tag: "spam", Rule: domain.MoveAlways, At: time.UnixMilli(3000)}}); err != nil {
			t.Fatal(err)
		}
		ignored, _ = c.MatrixIgnored(ctx)
		verdicts, _ = c.PersonVerdicts(ctx)
		marked = marked[:0]
		for _, v := range verdicts {
			marked = append(marked, v.Person)
		}
		if len(ignored) != 0 || !slices.Equal(marked, []string{"matrix:@cy:x", "telegram:1001"}) {
			t.Errorf("after the account's own writes: ignored %v, marked %v; want none, and cy and the Telegram mark", ignored, marked)
		}
	}
}
