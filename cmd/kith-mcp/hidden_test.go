package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// hiddenRoom has a hidden person (by a linked ID), a reply to them, and two others.
func hiddenRoom() *fake {
	f := twoRooms()
	f.dir = domain.NewDirectory(nil, []domain.PersonLink{{Source: "book", ID: "matrix:@spam:x", Other: "telegram:9"}})
	f.messages[mx+"!open:x"] = []domain.Message{
		{ID: mx + "$1", Sender: "matrix:@dana:x", Body: "morning", Timestamp: time.Unix(1_700_000_000, 0)},
		{ID: mx + "$2", Sender: "matrix:@spam:x", Body: "buy", Timestamp: time.Unix(1_700_000_060, 0)},
		{ID: mx + "$3", Sender: "matrix:@dana:x", Body: "no", ReplyTo: mx + "$2", Timestamp: time.Unix(1_700_000_120, 0)},
		{ID: mx + "$4", Sender: "matrix:@me:x", Body: "ok", Timestamp: time.Unix(1_700_000_180, 0)},
	}
	f.around = f.messages[mx+"!open:x"]
	return f
}

func hiddenServer(f *fake) *server {
	s := newServer(f, shareAllEncrypted)
	s.marked = []string{"telegram:9"}
	return s
}

func ids(t *testing.T, out map[string]any) []string {
	t.Helper()
	msgs, _ := out["messages"].([]any)
	var got []string
	for _, m := range msgs {
		view, _ := m.(map[string]any)
		id, _ := view["event_id"].(string)
		got = append(got, id)
	}
	return got
}

// read_room and read_around leave out a hidden person and direct replies to them.
func TestTheAssistantDoesNotReadHiddenPeople(t *testing.T) {
	t.Parallel()

	s := hiddenServer(hiddenRoom())
	want := []string{mx + "$1", mx + "$4"}
	if got := ids(t, call(t, s, "read_room", map[string]any{"room": "Standup"})); !slices.Equal(got, want) {
		t.Errorf("read_room gave %v, want %v", got, want)
	}
	if got := ids(t, call(t, s, "read_around", map[string]any{"room": "Standup", "event": mx + "$1"})); !slices.Equal(got, want) {
		t.Errorf("read_around gave %v, want %v", got, want)
	}
}

// Without the directory, who is hidden is unknown: the read refuses rather than leak.
func TestAnUnreadableDirectoryRefusesTheRead(t *testing.T) {
	t.Parallel()

	f := hiddenRoom()
	f.dirErr = errors.New("daemon restarting")
	got := callErr(t, hiddenServer(f), "read_room", map[string]any{"room": "Standup"})
	if !strings.Contains(got, "hide") {
		t.Errorf("refused with %q, want it to say why", got)
	}
	// Nobody marked: nothing to protect, so the read goes ahead.
	if out := call(t, newServer(f, shareAllEncrypted), "read_room", map[string]any{"room": "Standup"}); len(ids(t, out)) != 4 {
		t.Errorf("with nobody hidden read %v", ids(t, out))
	}
}

// A person a filter put in a muted tag is hidden as one listed there; a mark in a tag
// that notifies, or a release, hides nobody.
func TestAPersonMarkedByAFilterIsNotRead(t *testing.T) {
	t.Parallel()

	f := hiddenRoom()
	f.people = []domain.PersonVerdict{
		{Person: "telegram:9", Tag: "spam", Rule: domain.MoveAlways},
		{Person: "matrix:@dana:x", Tag: "work", Rule: domain.MoveAlways},
		{Person: "matrix:@me:x", Tag: "spam", Released: true},
	}
	s := newServer(f, shareAllEncrypted)
	s.muted = []string{"Spam"}
	if got, want := ids(t, call(t, s, "read_room", map[string]any{"room": "Standup"})), []string{mx + "$1", mx + "$4"}; !slices.Equal(got, want) {
		t.Errorf("read_room gave %v, want %v", got, want)
	}
}

// Without the verdicts, anyone may be marked in a muted tag: the read refuses.
func TestUnreadableVerdictsRefuseTheRead(t *testing.T) {
	t.Parallel()

	f := hiddenRoom()
	f.peopleErr = errors.New("daemon restarting")
	s := newServer(f, shareAllEncrypted)
	s.muted = []string{"Spam"}
	if got := callErr(t, s, "read_room", map[string]any{"room": "Standup"}); !strings.Contains(got, "hide") {
		t.Errorf("refused with %q, want it to say why", got)
	}
	// No muted tag: a verdict could hide nobody, so the read goes ahead.
	if out := call(t, newServer(f, shareAllEncrypted), "read_room", map[string]any{"room": "Standup"}); len(ids(t, out)) != 4 {
		t.Errorf("with no muted tag read %v", ids(t, out))
	}
}
