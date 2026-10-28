package tui

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The phrase index is extended when the timeline only grew and rebuilt otherwise; either way
// it equals a full rebuild.
func TestThePhraseIndexMatchesARebuild(t *testing.T) {
	t.Parallel()

	m := benchTimelineModel(200)
	if !m.conf.device.Complete.CompleteEnabled() {
		t.Skip("completion is off by default; the index is not kept")
	}
	m = m.armPhrases()
	last := m.timeline.messages[len(m.timeline.messages)-1]
	steps := []struct {
		name string
		edit func([]domain.Message) []domain.Message
	}{
		{"a message appended", func(msgs []domain.Message) []domain.Message {
			return append(msgs, domain.Message{ID: mx + "$new", RoomID: last.RoomID, Sender: m.selves[0],
				Body: "see you at the usual place", Timestamp: last.Timestamp.Add(time.Minute)})
		}},
		{"two more appended", func(msgs []domain.Message) []domain.Message {
			return append(msgs,
				domain.Message{ID: mx + "$n2", RoomID: last.RoomID, Sender: benchSenders[1], Body: "at the usual place then", Timestamp: last.Timestamp.Add(2 * time.Minute)},
				domain.Message{ID: mx + "$n3", RoomID: last.RoomID, Sender: benchSenders[2], Body: "ok", Timestamp: last.Timestamp.Add(3 * time.Minute)})
		}},
		{"an old message edited in place", func(msgs []domain.Message) []domain.Message {
			msgs[10].Body = "an entirely different sentence now"
			return msgs
		}},
		{"an old message redacted", func(msgs []domain.Message) []domain.Message {
			msgs[20].Redacted = true
			return msgs
		}},
		{"the oldest trimmed", func(msgs []domain.Message) []domain.Message { return msgs[5:] }},
		{"one appended after all that", func(msgs []domain.Message) []domain.Message {
			return append(msgs, domain.Message{ID: mx + "$n4", RoomID: last.RoomID, Sender: m.selves[0],
				Body: "the usual place it is", Timestamp: last.Timestamp.Add(4 * time.Minute)})
		}},
	}
	for _, step := range steps {
		// A copy, so the model's own slice changes only through setMessages, as in use.
		m = m.setMessages(step.edit(slices.Clone(m.timeline.messages)))
		m = m.armPhrases()
		if want := domain.PhrasesOf(m.timeline.messages, m.selfIDs()); !reflect.DeepEqual(m.phrases.phrases(), want) {
			t.Fatalf("after %s the index differs from a rebuild", step.name)
		}
	}
}

// The index is extended in place and shared by Model copies: an older copy growing its own
// way must not add to maps a newer copy already grew, nor count a message twice.
func TestAnOlderCopyDoesNotGrowTheSharedIndex(t *testing.T) {
	t.Parallel()

	m := benchTimelineModel(50)
	if !m.conf.device.Complete.CompleteEnabled() {
		t.Skip("completion is off by default; the index is not kept")
	}
	base := m.armPhrases()
	last := base.timeline.messages[len(base.timeline.messages)-1]
	grow := func(from Model, id, body string) Model {
		msgs := append(slices.Clone(from.timeline.messages), domain.Message{ID: domain.EventID(id), RoomID: last.RoomID,
			Sender: from.selves[0], Body: body, Timestamp: last.Timestamp.Add(time.Minute)})
		return from.setMessages(msgs).armPhrases()
	}
	newer := grow(base, mx+"$a", "meet at the usual place")
	older := grow(base, mx+"$b", "meet at the usual place") // the same words: a double count would show
	for name, copyOf := range map[string]Model{"the newer copy": newer, "the older copy": older} {
		if want := domain.PhrasesOf(copyOf.timeline.messages, copyOf.selfIDs()); !reflect.DeepEqual(copyOf.phrases.phrases(), want) {
			t.Errorf("%s: the index differs from indexing its own timeline", name)
		}
	}
}
