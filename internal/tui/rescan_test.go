package tui

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// rescanBackend answers rescans with found, keeping each call.
type rescanBackend struct {
	apitest.Nop
	found []domain.SpamCandidate

	mu    sync.Mutex
	calls []rescanCall
}

type rescanCall struct {
	since time.Time
	apply bool
	rooms []domain.RoomID
}

func (b *rescanBackend) RescanSpam(_ context.Context, since time.Time, apply bool, rooms []domain.RoomID) ([]domain.SpamCandidate, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, rescanCall{since, apply, rooms})
	if !apply {
		return b.found, nil
	}
	var out []domain.SpamCandidate
	for i := range b.found {
		if slices.Contains(rooms, b.found[i].Verdict.Room) {
			out = append(out, b.found[i])
		}
	}
	return out, nil
}

// :rescan previews with every room ticked; what is left ticked moves, judged as far
// back as the preview was.
func TestRescanPreviewsThenMovesTheTickedRooms(t *testing.T) {
	t.Parallel()
	candidate := func(room domain.RoomID, body string) domain.SpamCandidate {
		return domain.SpamCandidate{
			Verdict: domain.SpamVerdict{Room: room, Rule: domain.SpamAlways, Filter: "elections"},
			Sample:  domain.Message{RoomID: room, Body: body},
		}
	}
	b := &rescanBackend{found: []domain.SpamCandidate{candidate("!a:x", "go vote"), candidate("!b:x", "vote today")}}
	m := New(context.Background(), b, config.Display{}).WithConfigFile("", config.Config{})

	m, cmd := m.runCommandLine(":rescan 30d")
	if cmd == nil {
		t.Fatal(":rescan asked nothing")
	}
	m = update(t, m, cmd())
	if m.picker.kind != pickerSpamRescan || len(m.picker.checkedValues()) != 2 {
		t.Fatalf("picker %v ticked %v, want both rooms ticked", m.picker.kind, m.picker.checkedValues())
	}
	since := b.calls[0].since
	if b.calls[0].apply || time.Since(since) < 29*24*time.Hour || time.Since(since) > 31*24*time.Hour {
		t.Fatalf("preview call = %+v, want no apply, since 30 days ago", b.calls[0])
	}

	m, _ = press(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}) // untick the first
	m, cmd = m.acceptPick()
	if cmd == nil {
		t.Fatal("applying asked nothing")
	}
	m = update(t, m, cmd())
	last := b.calls[len(b.calls)-1]
	if !last.apply || !slices.Equal(last.rooms, []domain.RoomID{"!b:x"}) || !last.since.Equal(since) {
		t.Errorf("apply call = %+v, want the still-ticked room since the preview's start", last)
	}
	if m.picker.kind != pickerNone {
		t.Error("the picker stayed open after applying")
	}
}

// A span kith cannot read is refused before anything is asked.
func TestRescanRefusesABadSpan(t *testing.T) {
	t.Parallel()
	for _, arg := range []string{"soon", "0d", "-3h"} {
		if _, err := parseSince(arg, time.Now()); err == nil {
			t.Errorf("parseSince(%q) was accepted", arg)
		}
	}
	if at, err := parseSince("", time.Now()); err != nil || !at.IsZero() {
		t.Errorf("parseSince(\"\") = %v, %v, want everything", at, err)
	}
}
