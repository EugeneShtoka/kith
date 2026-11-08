package db

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

var (
	// stepOrigAgain is the original delivered a second time (a re-page, a resync).
	stepOrigAgain = historyStep{name: "o'", msg: stepOrig.msg}
	// stepEditUnnamed is an edit marked edited without naming its edit event (LinkedIn,
	// Slack's history).
	stepEditUnnamed = historyStep{name: "eu", msg: domain.Message{ID: mx + "$m", Sender: "matrix:@a:x", Body: "vu",
		Timestamp: time.UnixMilli(5000), EditedAt: time.UnixMilli(8000), Edited: true}}
)

// Under [display.deleted] keep only a message with more than one version keeps rows;
// every message still reads the versions it read when every copy was kept. The oracle
// cache replays the same order and also writes each arriving version as kept before
// (every copy that lands on a live message, unless its edit was deleted first).
func TestOnlyEditedMessagesKeepVersions(t *testing.T) {
	t.Parallel()
	sets := [][]historyStep{
		{stepOrig},
		{stepOrig, stepOrigAgain},
		{stepOrig, stepRedact},
		{stepOrig, stepOrigAgain, stepRedact},
		{stepOrig, stepEdit1},
		{stepOrig, stepEdit1, stepEdit2},
		{stepOrig, stepEdit1, stepRedact},
		{stepOrig, stepOrigAgain, stepEdit1, stepRedact},
		{stepOrig, stepEdit1, stepEdit2, stepRedact},
		{stepOrig, stepEdit1, stepRedactE1},
		{stepOrig, stepEdit1, stepEdit2, stepRedactE1},
		{stepOrig, stepEdit1, stepEdit2, stepRedactE2},
		{stepEdit1},
		{stepEdit1, stepEdit2},
		{stepEdit1, stepEdit2, stepRedact},
		{stepOrig, stepEditUnnamed},
		{stepOrig, stepEditUnnamed, stepRedact},
		{stepOrig, stepEditUnnamed, stepEdit1},
	}
	for _, set := range sets {
		t.Run(stepNames(set), func(t *testing.T) {
			t.Parallel()
			edited := false
			for _, s := range set {
				edited = edited || s.msg.Edited
			}
			for _, order := range stepOrders(set) {
				label := stepNames(order)
				ctx := context.Background()
				cache, oracle := openTemp(t), openTemp(t)
				if err := replay(ctx, cache, order, true); err != nil {
					t.Fatalf("[%s] replay: %v", label, err)
				}
				if err := replayKeepingEveryCopy(ctx, oracle, order); err != nil {
					t.Fatalf("[%s] oracle replay: %v", label, err)
				}
				if err := sameHistory(ctx, cache, oracle); err != nil {
					t.Errorf("[%s] %v", label, err)
				}
				if rows := revisionRows(t, cache); !edited && rows != 0 {
					t.Errorf("[%s] a message never edited keeps %d version rows, want none", label, rows)
				}
			}
		})
	}
}

// replayKeepingEveryCopy is replay under keep, writing every arriving version the way
// the cache did when it kept a row for every message.
func replayKeepingEveryCopy(ctx context.Context, cache *Cache, order []historyStep) error {
	redacted := false
	for i := range order {
		step := order[i]
		save := step.redactRev == "" && !step.redact
		if save && redacted && step.msg.RevisionID == "" {
			// After a redaction the server serves the message stripped, as replay has it.
			step.msg = domain.Message{ID: step.msg.ID, Sender: step.msg.Sender,
				Timestamp: step.msg.Timestamp, Redacted: true, RedactedBy: "matrix:@mod:x"}
		}
		if err := replay(ctx, cache, []historyStep{step}, true); err != nil {
			return err
		}
		redacted = redacted || step.redact
		if !save || step.msg.Redacted {
			continue // a deletion, or a stripped copy: no version
		}
		m := step.msg
		rev := m.RevisionID
		if rev == "" {
			rev = m.ID
		}
		row := revisionRow{event: m.ID, rev: domain.Revision{ID: rev, Body: m.Body, Format: m.Format, At: m.Timestamp}}
		if err := cache.inTx(ctx, func(tx *sql.Tx) error {
			return writeRevisions(ctx, tx, mx+"!r:x", []revisionRow{row})
		}); err != nil {
			return err
		}
	}
	return nil
}

// sameHistory compares the message and its versions as two caches read them.
func sameHistory(ctx context.Context, got, want *Cache) error {
	read := func(c *Cache) (string, error) {
		msgs, err := c.Messages(ctx, mx+"!r:x", 10)
		if err != nil {
			return "", err
		}
		revs, err := c.Revisions(ctx, mx+"!r:x", mx+"$m")
		if err != nil {
			return "", err
		}
		out := ""
		for i := range msgs {
			m := &msgs[i]
			out += fmt.Sprintf("row %q html=%q edited=%v redacted=%v\n", m.Body, m.Format.Markup(), m.Edited, m.Redacted)
		}
		for _, r := range revs {
			out += fmt.Sprintf("version %s %q html=%q at %d\n", r.ID, r.Body, r.Format.Markup(), r.At.UnixMilli())
		}
		return out, nil
	}
	g, err := read(got)
	if err != nil {
		return err
	}
	w, err := read(want)
	if err != nil {
		return err
	}
	if g != w {
		return fmt.Errorf("reads\n%s want, as when every copy was kept,\n%s", g, w)
	}
	return nil
}

func revisionRows(t *testing.T, cache *Cache) int {
	t.Helper()
	var n int
	if err := cache.db.QueryRowContext(context.Background(), "SELECT count(*) FROM message_revisions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Versions fetched for a message (Matrix's history view) are kept only when there are
// two: the original alone is the row itself.
func TestFetchedVersionsKeepRowsOnlyForAnEditedMessage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const room = domain.RoomID(mx + "!r:x")
	at := func(sec int) time.Time { return time.UnixMilli(int64(sec) * 1000) }
	original := domain.Revision{ID: mx + "$m", Body: "v0", At: at(1)}

	cache := openTemp(t)
	mustSave(t, cache, room, domain.Message{ID: mx + "$m", Sender: "matrix:@a:x", Body: "v0", Timestamp: at(1)})
	if err := cache.SaveRevisionsFor(ctx, room, mx+"$m", []domain.Revision{original}); err != nil {
		t.Fatal(err)
	}
	if rows := revisionRows(t, cache); rows != 0 {
		t.Errorf("an unedited message's fetched original kept %d rows, want none", rows)
	}
	if revs, _ := cache.Revisions(ctx, room, mx+"$m"); len(revs) != 1 || revs[0].Body != "v0" {
		t.Errorf("Revisions() = %+v, want the one version it has", revs)
	}

	// Edited before versions were kept: its fetched original is a second version.
	edited := openTemp(t)
	mustSave(t, edited, room, domain.Message{ID: mx + "$m", Sender: "matrix:@a:x", Body: "v1",
		RevisionID: mx + "$e1", Edited: true, EditedAt: at(2), Timestamp: at(2)})
	// Until then it has none: its row is the edit, not what it first said.
	if revs, _ := edited.Revisions(ctx, room, mx+"$m"); len(revs) != 0 {
		t.Errorf("Revisions() = %+v before the fetch, want none", revs)
	}
	if err := edited.SaveRevisionsFor(ctx, room, mx+"$m", []domain.Revision{original}); err != nil {
		t.Fatal(err)
	}
	if revs, _ := edited.Revisions(ctx, room, mx+"$m"); len(revs) != 1 || revs[0].Body != "v0" {
		t.Errorf("Revisions() = %+v, want the fetched original of an edited message", revs)
	}
}

// A copy marked edited without naming its edit (Slack's history) is an edited message,
// so its version is kept, as before.
func TestACopyMarkedEditedKeepsItsVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const room = domain.RoomID(mx + "!r:x")
	cache := openTemp(t)
	edited := stepOrig.msg
	edited.Body, edited.Edited, edited.EditedAt = "v1", true, time.UnixMilli(6000)
	if err := cache.SaveMessagesWithRevisions(ctx, room, []domain.Message{edited}); err != nil {
		t.Fatal(err)
	}
	if revs, err := cache.Revisions(ctx, room, mx+"$m"); err != nil || len(revs) != 1 || revs[0].Body != "v1" {
		t.Errorf("Revisions() = %+v, %v; want its one kept version", revs, err)
	}
}
