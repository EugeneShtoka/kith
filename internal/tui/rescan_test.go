package tui

import (
	"context"
	"slices"
	"strings"
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
	found []domain.MoveCandidate

	mu    sync.Mutex
	calls []rescanCall
}

type rescanCall struct {
	since time.Time
	apply bool
	rooms []domain.RoomID
}

func (b *rescanBackend) RescanFilters(_ context.Context, since time.Time, apply bool, rooms []domain.RoomID) ([]domain.MoveCandidate, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, rescanCall{since, apply, rooms})
	if !apply {
		return b.found, nil
	}
	var out []domain.MoveCandidate
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
	candidate := func(room domain.RoomID, body string) domain.MoveCandidate {
		return domain.MoveCandidate{
			Verdict: domain.TagVerdict{Room: room, Tag: "Spam", Rule: domain.MoveAlways, Filter: "elections"},
			Sample:  domain.Message{RoomID: room, Body: body},
		}
	}
	b := &rescanBackend{found: []domain.MoveCandidate{candidate(mx+"!a:x", "go vote"), candidate(mx+"!b:x", "vote today")}}
	m := New(context.Background(), b, config.Display{}).WithConfigFile("", asStarted(config.Config{}))

	m, cmd := m.runCommandLine(":rescan 30d")
	if cmd == nil {
		t.Fatal(":rescan asked nothing")
	}
	m = update(t, m, cmd())
	if m.picker.kind != pickerRescan || len(m.picker.checkedValues()) != 2 {
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
	if !last.apply || !slices.Equal(last.rooms, []domain.RoomID{mx + "!b:x"}) || !last.since.Equal(since) {
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

// peopleRescanBackend also judges senders, keeping each people call.
type peopleRescanBackend struct {
	rescanBackend
	people      []domain.PersonCandidate
	peopleCalls []rescanCall
	marked      []string
}

func (b *peopleRescanBackend) RescanPeople(_ context.Context, since time.Time, apply bool, people []string) ([]domain.PersonCandidate, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.peopleCalls = append(b.peopleCalls, rescanCall{since: since, apply: apply})
	if !apply {
		return b.people, nil
	}
	b.marked = append(b.marked, people...)
	return b.people[:len(people)], nil
}

// :rescan lists the people the filters would mark beside the rooms, all ticked; what is
// left ticked is applied to each list alone, and nothing is asked of a list left empty
// (an empty list means everything to the daemon).
func TestRescanListsPeopleBesideRooms(t *testing.T) {
	t.Parallel()
	b := &peopleRescanBackend{
		rescanBackend: rescanBackend{found: []domain.MoveCandidate{{
			Verdict: domain.TagVerdict{Room: mx + "!a:x", Tag: "Spam", Rule: domain.MoveAlways, Filter: "elections"},
			Sample:  domain.Message{RoomID: mx + "!a:x", Body: "go vote"},
		}}},
		people: []domain.PersonCandidate{{
			Verdict: domain.PersonVerdict{Person: "matrix:@spam:x", Tag: "Spam", Rule: domain.MoveSenderSpread, Filter: "runs"},
			Sample:  domain.Message{RoomID: mx + "!b:x", Body: "free bitcoin"},
		}},
	}
	m := New(context.Background(), b, config.Display{}).WithConfigFile("", asStarted(config.Config{}))
	m, cmd := m.runCommandLine(":rescan")
	m = update(t, m, cmd())
	if got := m.picker.checkedValues(); m.picker.kind != pickerRescan || len(got) != 2 || !strings.Contains(m.picker.spec.title, "1 move and 1 mark") {
		t.Fatalf("picker %v %q ticked %v, want the room and the person", m.picker.kind, m.picker.spec.title, got)
	}

	m.picker.checked = map[string]bool{markValue(b.people[0].Verdict): true}
	m, cmd = m.acceptPick()
	m = update(t, m, cmd())
	if !slices.Equal(b.marked, []string{"matrix:@spam:x"}) {
		t.Errorf("marked %v, want the ticked person", b.marked)
	}
	for _, call := range b.calls {
		if call.apply {
			t.Errorf("rooms were applied with %v though none was ticked", call.rooms)
		}
	}
	if !strings.Contains(m.status(), "1 mark") {
		t.Errorf("status %q, want it to count the mark", m.status())
	}
}
