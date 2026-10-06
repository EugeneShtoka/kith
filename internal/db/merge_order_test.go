package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/api/backend/v1/protoconv"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// One message's history, as the daemon saves it: the original, two edits, and a
// redaction. Matrix delivers these in any order (live sync, backfill newest page
// first), so every order must leave the same row.
type historyStep struct {
	name string
	msg  domain.Message // saved with SaveMessagesWithRevisions
	// redact, when set, is a redaction instead: MarkRedacted without keep.
	redact bool
	// redactRev, when set, redacts that edit event (not the message).
	redactRev domain.EventID
}

var (
	stepOrig = historyStep{name: "o", msg: domain.Message{ID: "$m", Sender: "@a:x", Body: "v0",
		Format: richtext.FromMarkup("<b>v0</b>"), Timestamp: time.UnixMilli(5000)}}
	stepEdit1 = historyStep{name: "e1", msg: domain.Message{ID: "$m", Sender: "@a:x", Body: "v1",
		Format: richtext.FromMarkup("<b>v1</b>"), RevisionID: "$e1", Timestamp: time.UnixMilli(7000),
		EditedAt: time.UnixMilli(7000), Edited: true}}
	stepEdit2 = historyStep{name: "e2", msg: domain.Message{ID: "$m", Sender: "@a:x", Body: "v2",
		Format: richtext.FromMarkup("<b>v2</b>"), RevisionID: "$e2", Timestamp: time.UnixMilli(9000),
		EditedAt: time.UnixMilli(9000), Edited: true}}
	stepRedact = historyStep{name: "R", redact: true}
	// Redactions of the edit events themselves.
	stepRedactE1 = historyStep{name: "R1", redactRev: "$e1"}
	stepRedactE2 = historyStep{name: "R2", redactRev: "$e2"}
)

func stepOrders(steps []historyStep) [][]historyStep {
	if len(steps) <= 1 {
		return [][]historyStep{append([]historyStep(nil), steps...)}
	}
	var out [][]historyStep
	for i := range steps {
		rest := make([]historyStep, 0, len(steps)-1)
		rest = append(rest, steps[:i]...)
		rest = append(rest, steps[i+1:]...)
		for _, p := range stepOrders(rest) {
			out = append(out, append([]historyStep{steps[i]}, p...))
		}
	}
	return out
}

// replay saves one order as the daemon would under [display.deleted] keep. Once
// redacted, the server serves the original stripped (m.replace edits it leaves
// alone), so a later original arrives as a redacted copy.
func replay(ctx context.Context, cache *Cache, order []historyStep, keep bool) error {
	const room = domain.RoomID("!r:x")
	save := cache.SaveMessages
	if keep {
		save = cache.SaveMessagesWithRevisions
	}
	redacted := false
	for i := range order {
		step := &order[i]
		switch {
		case step.redactRev != "":
			if err := cache.MarkRedacted(ctx, room, step.redactRev, "@a:x", "", time.Time{}, keep); err != nil {
				return err
			}
		case step.redact:
			redacted = true
			if err := cache.MarkRedacted(ctx, room, "$m", "@mod:x", "", time.Time{}, keep); err != nil {
				return err
			}
		case redacted && step.msg.RevisionID == "":
			stripped := domain.Message{ID: step.msg.ID, Sender: step.msg.Sender,
				Timestamp: step.msg.Timestamp, Redacted: true, RedactedBy: "@mod:x"}
			if err := save(ctx, room, []domain.Message{stripped}); err != nil {
				return err
			}
		default:
			if err := save(ctx, room, []domain.Message{step.msg}); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestSavedHistoryIsOrderIndependent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		steps []historyStep
		keep  bool
		check func(domain.Message, []domain.Revision) error
	}{
		{
			name:  "edits",
			steps: []historyStep{stepOrig, stepEdit1, stepEdit2},
			check: func(m domain.Message, _ []domain.Revision) error {
				if m.Body != "v2" || m.Format.Markup() != "<b>v2</b>" || !m.Edited || m.Redacted {
					return fmt.Errorf("want the newest edit v2, got body=%q html=%q edited=%v", m.Body, m.Format.Markup(), m.Edited)
				}
				if !m.EditedAt.Equal(time.UnixMilli(9000)) {
					return fmt.Errorf("EditedAt = %v, want the newest edit's", m.EditedAt)
				}
				return nil
			},
		},
		{
			name:  "edits, keeping deleted",
			steps: []historyStep{stepOrig, stepEdit1, stepEdit2},
			keep:  true,
			check: func(m domain.Message, revs []domain.Revision) error {
				if m.Body != "v2" || m.Format.Markup() != "<b>v2</b>" || !m.Edited {
					return fmt.Errorf("want the newest edit v2, got body=%q html=%q", m.Body, m.Format.Markup())
				}
				if len(revs) != 3 {
					return fmt.Errorf("kept %d revisions, want all 3", len(revs))
				}
				return nil
			},
		},
		{
			// What a kept message shows depends on what had arrived when it was
			// deleted, so only the flag is order-independent.
			name:  "edits and a redaction, keeping deleted",
			steps: []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedact},
			keep:  true,
			check: func(m domain.Message, _ []domain.Revision) error {
				if !m.Redacted {
					return fmt.Errorf("a redaction was lost")
				}
				return nil
			},
		},
		{
			// The server never sends the original (it is older than any page asked
			// for); the edits, which it does not redact, still arrive.
			name:  "a redaction before the message, then only its edits",
			steps: []historyStep{stepEdit1, stepEdit2, stepRedact},
			check: func(m domain.Message, revs []domain.Revision) error {
				if !m.Redacted || m.Body != "" || m.Format.Markup() != "" {
					return fmt.Errorf("want redacted with no words, got redacted=%v body=%q html=%q", m.Redacted, m.Body, m.Format.Markup())
				}
				if m.RedactedBy != "@mod:x" {
					return fmt.Errorf("RedactedBy = %q, want who deleted it", m.RedactedBy)
				}
				return nil
			},
		},
		{
			name:  "edits and a redaction",
			steps: []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedact},
			check: func(m domain.Message, revs []domain.Revision) error {
				if !m.Redacted || m.Body != "" || m.Format.Markup() != "" {
					return fmt.Errorf("want redacted with no words, got redacted=%v body=%q html=%q", m.Redacted, m.Body, m.Format.Markup())
				}
				if len(revs) != 0 {
					return fmt.Errorf("a forgotten message kept %d revisions", len(revs))
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, order := range stepOrders(tc.steps) {
				names := make([]string, len(order))
				for i, s := range order {
					names[i] = s.name
				}
				label := strings.Join(names, " ")

				cache, ctx := openTemp(t), context.Background()
				if err := replay(ctx, cache, order, tc.keep); err != nil {
					t.Fatalf("[%s] replay: %v", label, err)
				}
				msgs, err := cache.Messages(ctx, "!r:x", 10)
				if err != nil || len(msgs) != 1 {
					t.Fatalf("[%s] Messages() = %d rows, err %v; want 1", label, len(msgs), err)
				}
				revs, err := cache.Revisions(ctx, "!r:x", "$m")
				if err != nil {
					t.Fatalf("[%s] Revisions(): %v", label, err)
				}
				if err := tc.check(msgs[0], revs); err != nil {
					t.Errorf("[%s] %v", label, err)
				}
				if !msgs[0].Timestamp.Equal(time.UnixMilli(5000)) && !msgs[0].Redacted {
					t.Errorf("[%s] ts %v, want the original's", label, msgs[0].Timestamp)
				}
			}
		})
	}
}

// Trimming a room drops the tombstones older than what it keeps: a message is older
// than its deletion, so one deleted before the cutoff would be trimmed on arrival.
func TestTrimDropsTombstonesItNoLongerNeeds(t *testing.T) {
	t.Parallel()

	cache, ctx := openTemp(t), context.Background()
	const room = domain.RoomID("!r:x")
	if err := cache.MarkRedacted(ctx, room, "$old", "@mod:x", "", time.UnixMilli(1000), false); err != nil {
		t.Fatal(err)
	}
	if err := cache.MarkRedacted(ctx, room, "$new", "@mod:x", "", time.UnixMilli(1e12), false); err != nil {
		t.Fatal(err)
	}
	cache.UseKeep(func(domain.RoomID) int { return testKeep })
	msgs := make([]domain.Message, testKeep+1)
	for i := range msgs {
		msgs[i] = domain.Message{ID: domain.EventID(fmt.Sprintf("$%d", i)), Sender: "@a:x",
			Body: "x", Timestamp: time.UnixMilli(int64(10_000 + i))}
	}
	if err := cache.SaveMessages(ctx, room, msgs); err != nil {
		t.Fatal(err)
	}
	var left []string
	rows, err := cache.db.QueryContext(ctx, "SELECT event_id FROM message_tombstone ORDER BY event_id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(left, ",") != "$new" {
		t.Errorf("tombstones left = %v, want only the one newer than the cutoff", left)
	}
}

// The daemon's cache, the wire and the client's in-memory merge each resolve a
// message's history, and must resolve it alike: the client shows a cached row (read
// over the socket) and folds in what arrives live after it. So for every delivery
// order, and every point in it where the client might have loaded the row, the row
// the client ends with must equal what the cache ends with, read over the wire.
// Each layer's own permutation test passed while they disagreed on an edit tie: the
// cache broke it by event ID, and the client, which never received the ID, by
// arrival.
func TestTheCacheAndTheClientResolveHistoryAlike(t *testing.T) {
	t.Parallel()

	tie1, tie2 := stepEdit1, stepEdit2
	tie1.name, tie2.name = "e1=", "e2="
	tie1.msg.EditedAt, tie1.msg.Timestamp = time.UnixMilli(8000), time.UnixMilli(8000)
	tie2.msg.EditedAt, tie2.msg.Timestamp = time.UnixMilli(8000), time.UnixMilli(8000)

	for _, tc := range []struct {
		name  string
		steps []historyStep
	}{
		{"edits", []historyStep{stepOrig, stepEdit1, stepEdit2}},
		{"edits sent in the same millisecond", []historyStep{stepOrig, tie1, tie2}},
		{"edits and a redaction", []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedact}},
		{"tied edits and a redaction", []historyStep{stepOrig, tie1, tie2, stepRedact}},
		{"the newest edit deleted", []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedactE2}},
		{"an older edit deleted", []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedactE1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, order := range stepOrders(tc.steps) {
				for loadedAfter := 1; loadedAfter <= len(order); loadedAfter++ {
					label := fmt.Sprintf("%s, loaded after %d", stepNames(order), loadedAfter)
					if err := resolvedAlike(openTemp(t), order, loadedAfter); err != nil {
						t.Errorf("[%s] %v", label, err)
					}
				}
			}
		})
	}
}

func stepNames(order []historyStep) string {
	names := make([]string, len(order))
	for i := range order {
		names[i] = order[i].name
	}
	return strings.Join(names, " ")
}

// resolvedAlike replays order into a cache step by step, as the daemon saves what
// it receives. The client loads the row after loadedAfter steps (over the wire) and
// folds in the rest as they arrive live: each as the daemon emits it, and a deleted
// edit as the row the cache reverted to, marked to replace what the client shows.
func resolvedAlike(cache *Cache, order []historyStep, loadedAfter int) error {
	ctx := context.Background()
	var client []domain.Message
	redacted := false
	deletedEdits := map[domain.EventID]bool{}
	for i := range order {
		step := order[i]
		if err := replay(ctx, cache, order[i:i+1], false); err != nil {
			return err
		}
		if i+1 == loadedAfter {
			var err error
			if client, err = overTheWire(ctx, cache); err != nil {
				return err
			}
		}
		if step.redactRev != "" {
			deletedEdits[step.redactRev] = true
		}
		if i < loadedAfter {
			redacted = redacted || step.redact
			continue
		}
		if deletedEdits[step.msg.RevisionID] && step.redactRev == "" {
			// The server strips a redacted event: this edit arrives as no edit at all.
			continue
		}
		live := step.msg
		switch {
		case step.redactRev != "":
			reverted, err := overTheWire(ctx, cache)
			if err != nil {
				return err
			}
			if len(reverted) == 0 {
				continue // nothing cached to revert
			}
			live = reverted[0]
			live.Reverted = true
		case step.redact:
			redacted = true
			live = domain.Message{ID: "$m", Redacted: true, RedactedBy: "@mod:x"}
		case redacted && live.RevisionID == "":
			live = domain.Message{ID: "$m", Sender: live.Sender, Timestamp: live.Timestamp, Redacted: true, RedactedBy: "@mod:x"}
		}
		live.RoomID = "!r:x"
		client = domain.MergeMessages(client, []domain.Message{live})
	}
	cached, err := overTheWire(ctx, cache)
	if err != nil {
		return err
	}
	if len(client) != len(cached) {
		return fmt.Errorf("the client holds %d rows, the cache %d", len(client), len(cached))
	}
	if len(cached) == 0 {
		return nil
	}
	c, k := client[0], cached[0]
	// Formatting is compared as it draws: over the wire it has no markup.
	if c.Body != k.Body || drawn(c.Format) != drawn(k.Format) || c.Redacted != k.Redacted || c.Edited != k.Edited ||
		!c.EditedAt.Equal(k.EditedAt) || c.RevisionID != k.RevisionID {
		return fmt.Errorf("the client shows body=%q format=%s redacted=%v edited=%v at %v rev %q,\n"+
			"the cache body=%q format=%s redacted=%v edited=%v at %v rev %q",
			c.Body, drawn(c.Format), c.Redacted, c.Edited, c.EditedAt.UnixMilli(), c.RevisionID,
			k.Body, drawn(k.Format), k.Redacted, k.Edited, k.EditedAt.UnixMilli(), k.RevisionID)
	}
	return nil
}

// drawn is formatting as a client draws it.
func drawn(f richtext.Formatted) string { return fmt.Sprintf("%q %+v", f.Text(), f.Spans()) }

// overTheWire is the room as the client receives it: read from the cache and sent
// through the socket's wire types.
func overTheWire(ctx context.Context, cache *Cache) ([]domain.Message, error) {
	msgs, err := cache.Messages(ctx, "!r:x", 10)
	if err != nil {
		return nil, err
	}
	return protoconv.ProtoToMessages(protoconv.MessagesToProto(msgs)), nil
}

// Deleting an edit (its own event, not the message) takes its words away for good:
// whatever order the original, the edits and the deletion arrive in, the words of a
// deleted edit are never what the message shows. With [display.deleted] keep the
// versions are all cached, so it shows exactly the newest one left; without, the
// cache may know none older, and shows what it has (or nothing, until one arrives).
func TestADeletedEditIsNeverShownAgain(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		steps []historyStep
		gone  []string // bodies that must never show
		// kept is the body under keep, where every version is cached.
		kept string
	}{
		{"the newest edit", []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedactE2}, []string{"v2"}, "v1"},
		{"an older edit", []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedactE1}, []string{"v1"}, "v2"},
		{"both edits", []historyStep{stepOrig, stepEdit1, stepEdit2, stepRedactE1, stepRedactE2}, []string{"v1", "v2"}, "v0"},
	} {
		for _, keep := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, keep %v", tc.name, keep), func(t *testing.T) {
				t.Parallel()
				for _, order := range stepOrders(tc.steps) {
					cache, ctx := openTemp(t), context.Background()
					if err := replay(ctx, cache, order, keep); err != nil {
						t.Fatalf("[%s] replay: %v", stepNames(order), err)
					}
					msgs, err := cache.Messages(ctx, "!r:x", 10)
					if err != nil || len(msgs) != 1 {
						t.Fatalf("[%s] Messages() = %d rows, %v; want 1", stepNames(order), len(msgs), err)
					}
					m := msgs[0]
					for _, gone := range tc.gone {
						if m.Body == gone || m.Format.Markup() == "<b>"+gone+"</b>" {
							t.Errorf("[%s] shows %q (html %q), the words of a deleted edit", stepNames(order), m.Body, m.Format.Markup())
						}
					}
					if keep && m.Body != tc.kept {
						t.Errorf("[%s] keeping every version, shows %q; want the newest left, %q", stepNames(order), m.Body, tc.kept)
					}
				}
			})
		}
	}
}
