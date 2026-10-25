package tui

import (
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// unreadRoom is dayRoom read to the end of the second day, so the unread line and a
// date divider both belong above the third day's first message.
func unreadRoom(t *testing.T) Model {
	t.Helper()
	m := dayRoom(t)
	m.timeline.opened.unreadFrom = mx + "$d1m2" // the last message of day 1
	return m
}

func TestUnreadLineMarksTheFirstUnreadMessage(t *testing.T) {
	t.Parallel()

	m := unreadRoom(t)
	_, w := m.walkFor()
	for i := range w.msgs {
		_, rule := m.cachedRows(w, i)
		want := w.msgs[i].ID == mx+"$d2m0"
		if rule.unread != want {
			t.Errorf("message %s: unread rule = %v, want %v", w.msgs[i].ID, rule.unread, want)
		}
	}
}

// One rule, not two: $d2m0 starts a new day and the unread; stacking both would leave a gap.
func TestUnreadLineReplacesTheDateDivider(t *testing.T) {
	t.Parallel()

	m := unreadRoom(t)
	_, w := m.walkFor()
	i := messageRow(t, w, mx+"$d2m0")
	_, rule := m.cachedRows(w, i)
	if !rule.date || !rule.unread {
		t.Fatalf("setup: rule = %+v, want both a date divider and the unread line", rule)
	}
	if got := m.entryFor(w, i).height; got != len(m.entryFor(w, i).rows)+1 {
		t.Errorf("height = %d for %d rows, want exactly one rule row", got, len(m.entryFor(w, i).rows))
	}
	row := m.ruleAboveRow(rule, w.msgs[i].Timestamp, 60)
	if !strings.Contains(row, "new") {
		t.Errorf("rule = %q, want the unread line to win", row)
	}
	if !strings.Contains(row, dayLabel(w.msgs[i].Timestamp, domain.Clock{})) {
		t.Errorf("rule = %q, want it to carry the date it displaced", row)
	}
}

func TestUnreadLineAbsentWhenEverythingIsRead(t *testing.T) {
	t.Parallel()

	m := dayRoom(t)
	_, w := m.walkFor()
	m.timeline.opened.unreadFrom = w.msgs[len(w.msgs)-1].ID
	for i := range w.msgs {
		if _, rule := m.cachedRows(w, i); rule.unread {
			t.Fatalf("message %s carries an unread line in a fully read room", w.msgs[i].ID)
		}
	}
}

// Turning it off leaves the date dividers.
func TestUnreadLineCanBeTurnedOff(t *testing.T) {
	t.Parallel()

	m := unreadRoom(t)
	off := false
	m.prefs.display.UnreadLine = &off
	_, w := m.walkFor()
	i := messageRow(t, w, mx+"$d2m0")
	_, rule := m.cachedRows(w, i)
	if rule.unread {
		t.Error("the unread line is drawn with the setting off")
	}
	if !rule.date {
		t.Error("turning the unread line off took the date divider with it")
	}
}

func messageRow(t *testing.T, w walk, id domain.EventID) int {
	t.Helper()
	for i := range w.msgs {
		if w.msgs[i].ID == id {
			return i
		}
	}
	t.Fatalf("message %s is not in the timeline", id)
	return -1
}
