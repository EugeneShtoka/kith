package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/richtext"
)

// One message's history: the original, two edits, and a redaction. Matrix delivers
// these in any order (live sync, backfill newest page first, a cached row plus a
// page), so every order must fold to the same message.
var (
	mergeOrig = domain.Message{ID: "$m", RoomID: "!r", Sender: "@a:x", Body: "v0",
		Format: richtext.FromMarkup("<b>v0</b>"), Timestamp: time.UnixMilli(5000)}
	mergeEdit1 = domain.Message{ID: "$m", RoomID: "!r", Sender: "@a:x", Body: "v1",
		Format: richtext.FromMarkup("<b>v1</b>"), RevisionID: "$e1", Timestamp: time.UnixMilli(7000),
		EditedAt: time.UnixMilli(7000), Edited: true}
	mergeEdit2 = domain.Message{ID: "$m", RoomID: "!r", Sender: "@a:x", Body: "v2",
		Format: richtext.FromMarkup("<b>v2</b>"), RevisionID: "$e2", Timestamp: time.UnixMilli(9000),
		EditedAt: time.UnixMilli(9000), Edited: true}
	// Servers strip the target's content but never redact its m.replace edits, so a
	// redaction can be followed by an edit that still carries the words.
	mergeRedaction = domain.Message{ID: "$m", RoomID: "!r", Redacted: true, RedactedBy: "@mod:x"}
)

// permutations is every order of msgs.
func permutations(msgs []domain.Message) [][]domain.Message {
	if len(msgs) <= 1 {
		return [][]domain.Message{append([]domain.Message(nil), msgs...)}
	}
	var out [][]domain.Message
	for i := range msgs {
		rest := make([]domain.Message, 0, len(msgs)-1)
		rest = append(rest, msgs[:i]...)
		rest = append(rest, msgs[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]domain.Message{msgs[i]}, p...))
		}
	}
	return out
}

func orderName(msgs []domain.Message) string {
	name := ""
	for i := range msgs {
		m := &msgs[i]
		switch {
		case m.Redacted:
			name += "R"
		case m.RevisionID != "":
			name += string(m.RevisionID[1:])
		default:
			name += "o"
		}
		name += " "
	}
	return name
}

func TestMergeMessagesIsOrderIndependent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		events []domain.Message
		check  func(domain.Message) error
	}{
		{
			name:   "edits",
			events: []domain.Message{mergeOrig, mergeEdit1, mergeEdit2},
			check: func(m domain.Message) error {
				if m.Body != "v2" || m.Format.Markup() != "<b>v2</b>" || !m.Edited || m.Redacted {
					return fmt.Errorf("want the newest edit v2, got body=%q html=%q edited=%v", m.Body, m.Format.Markup(), m.Edited)
				}
				return nil
			},
		},
		{
			name:   "edits and a redaction",
			events: []domain.Message{mergeOrig, mergeEdit1, mergeEdit2, mergeRedaction},
			check: func(m domain.Message) error {
				if !m.Redacted || m.Body != "" || m.Format.Markup() != "" {
					return fmt.Errorf("want redacted with no words, got redacted=%v body=%q html=%q", m.Redacted, m.Body, m.Format.Markup())
				}
				return nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, order := range permutations(tc.events) {
				// One at a time, as live events fold onto the timeline.
				var folded []domain.Message
				for _, m := range order {
					folded = domain.MergeMessages(folded, []domain.Message{m})
				}
				// All in one page.
				page := domain.MergeMessages(nil, order)
				for how, got := range map[string][]domain.Message{"one at a time": folded, "one page": page} {
					if len(got) != 1 {
						t.Fatalf("%s [%s]: %d messages, want 1", how, orderName(order), len(got))
					}
					if err := tc.check(got[0]); err != nil {
						t.Errorf("%s [%s]: %v", how, orderName(order), err)
					}
					if !got[0].Timestamp.Equal(mergeOrig.Timestamp) {
						t.Errorf("%s [%s]: ts %v, want the original's", how, orderName(order), got[0].Timestamp)
					}
				}
			}
		})
	}
}
