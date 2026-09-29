package tui

import (
	"context"
	"testing"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// receiptSpy records every read receipt and whether it went out privately.
type receiptSpy struct {
	apitest.Nop
	reads   []domain.EventID
	private []bool
	bulk    [][]domain.RoomID
	bulkPvt []bool
}

func (s *receiptSpy) MarkRead(_ context.Context, _ domain.RoomID, eventID domain.EventID, private bool) error {
	s.reads = append(s.reads, eventID)
	s.private = append(s.private, private)
	return nil
}

func (s *receiptSpy) MarkRoomsRead(_ context.Context, ids []domain.RoomID, private bool) (domain.ReadResult, error) {
	s.bulk = append(s.bulk, ids)
	s.bulkPvt = append(s.bulkPvt, private)
	return domain.ReadResult{Marked: len(ids)}, nil
}

func readingRoom(t *testing.T, spy *receiptSpy, display config.Display) Model {
	t.Helper()
	m := sized(t, update(t, New(context.Background(), spy, display),
		roomsMsg{rooms: []domain.Room{{ID: "!a:x", Name: "Alpha"}}}))
	m.conf.base.Display = display
	next, _ := m.selectRoom(domain.Room{ID: "!a:x", Name: "Alpha"})
	m = next
	m = update(t, m, timelineMsg{roomID: "!a:x", page: domain.TimelinePage{Messages: []domain.Message{
		{ID: "$1", RoomID: "!a:x", Sender: "@bob:x", Body: "hello", Timestamp: at(1)},
	}}})
	m.focus = paneTimeline
	return m
}

// By default entering a room reads it, announced.
func TestReadOnEnteringByDefault(t *testing.T) {
	spy := &receiptSpy{}
	m := readingRoom(t, spy, config.Display{})
	m, cmd := m.markRead()
	deliver(t, m, cmd)
	if len(spy.reads) != 1 || spy.reads[0] != "$1" {
		t.Errorf("receipts = %v, want one for the newest message", spy.reads)
	}
	if spy.private[0] {
		t.Error("the receipt went out privately; the default is to announce it")
	}
}

// read_delay = 0 reads every room the cursor lands on.
func TestReadOnFocusImmediately(t *testing.T) {
	spy := &receiptSpy{}
	zero := 0
	m := readingRoom(t, spy, config.Display{ReadDelay: &zero})
	m, cmd := m.armFocusRead()
	deliver(t, m, cmd)
	if len(spy.reads) != 1 || spy.reads[0] != "$1" {
		t.Errorf("receipts = %v, want the selected room read at once", spy.reads)
	}
}

// A positive delay reads a room the cursor lingers on, and not one it passes over.
func TestReadOnFocusAfterTheDelay(t *testing.T) {
	spy := &receiptSpy{}
	delay := 15
	m := readingRoom(t, spy, config.Display{ReadDelay: &delay})

	m, cmd := m.armFocusRead()
	if cmd == nil {
		t.Fatal("no timer was started")
	}
	if len(spy.reads) != 0 {
		t.Fatalf("selecting the room sent %v; it should be waiting", spy.reads)
	}

	next, fired := m.handleReadTimer(readTimerMsg{roomID: "!a:x", armed: m.receipts.armed})
	after := next
	deliver(t, after, fired)
	if len(spy.reads) != 1 || spy.reads[0] != "$1" {
		t.Errorf("after the delay, receipts = %v, want one", spy.reads)
	}
}

// A timer that outlived what it was armed for does nothing.
func TestStaleReadTimerIsIgnored(t *testing.T) {
	spy := &receiptSpy{}
	delay := 15
	m := readingRoom(t, spy, config.Display{ReadDelay: &delay})
	m, _ = m.armFocusRead()

	for _, stale := range []readTimerMsg{
		{roomID: "!a:x", armed: m.receipts.armed - 1},
		{roomID: "!other:x", armed: m.receipts.armed},
		{roomID: "!a:x", armed: m.receipts.armed, thread: "$t"},
	} {
		next, cmd := m.handleReadTimer(stale)
		after := next
		deliver(t, after, cmd)
		if len(spy.reads) != 0 {
			t.Errorf("a stale timer %+v marked the room read", stale)
		}
	}
}

// -1 (the default) never reads on focus; opening a room still does.
func TestReadDelayNeverOnFocus(t *testing.T) {
	never := -1
	for _, delay := range []*int{nil, &never} {
		spy := &receiptSpy{}
		m := readingRoom(t, spy, config.Display{ReadDelay: delay})
		m, cmd := m.armFocusRead()
		deliver(t, m, cmd)
		if cmd != nil || len(spy.reads) != 0 {
			t.Errorf("read_delay %v still read on focus: %v", delay, spy.reads)
		}
		m, openCmd := m.markRead()
		deliver(t, m, openCmd)
		if len(spy.reads) != 1 {
			t.Errorf("opening the room did not read it: %v", spy.reads)
		}
	}
}

// Turning receipts off still reads the room — it just does not announce it.
func TestReceiptsOffSendPrivately(t *testing.T) {
	spy := &receiptSpy{}
	off := false
	m := readingRoom(t, spy, config.Display{SendReceipts: &off})
	m, cmd := m.markRead()
	deliver(t, m, cmd)
	if len(spy.reads) != 1 {
		t.Fatalf("receipts = %v, want the room still marked read", spy.reads)
	}
	if !spy.private[0] {
		t.Error("the receipt was announced; send_receipts = false must send it privately")
	}
}

// A rule beats the global setting for the place it names.
func TestReceiptRuleOverridesTheGlobal(t *testing.T) {
	spy := &receiptSpy{}
	off := false
	m := readingRoom(t, spy, config.Display{
		ReadRules: []config.ReadRule{{Match: "!a:x", Send: &off}},
	})
	m, cmd := m.markRead()
	deliver(t, m, cmd)
	if len(spy.reads) != 1 || !spy.private[0] {
		t.Errorf("reads = %v private = %v; the room's rule should have silenced it", spy.reads, spy.private)
	}
}

// One gesture over rooms with different settings is two requests and one report.
func TestBulkMarkReadSplitsByPolicy(t *testing.T) {
	spy := &receiptSpy{}
	off := false
	m := sized(t, update(t, New(context.Background(), spy, config.Display{
		ReadRules: []config.ReadRule{{Match: "!quiet:x", Send: &off}},
	}), roomsMsg{rooms: []domain.Room{
		{ID: "!loud:x", Name: "Loud"}, {ID: "!quiet:x", Name: "Quiet"},
	}}))
	m.receipts.policy = readSettingsFrom(config.Display{ReadRules: []config.ReadRule{{Match: "!quiet:x", Send: &off}}})

	cmd := m.markRoomsReadCmd([]domain.RoomID{"!loud:x", "!quiet:x"}, "both")
	msg, _ := cmd().(markedReadMsg)

	if len(spy.bulk) != 2 {
		t.Fatalf("made %d requests, want one announced and one quiet", len(spy.bulk))
	}
	announced, quiet := spy.bulk[0], spy.bulk[1]
	if len(announced) != 1 || announced[0] != "!loud:x" || spy.bulkPvt[0] {
		t.Errorf("announced batch = %v (private %v), want just !loud:x", announced, spy.bulkPvt[0])
	}
	if len(quiet) != 1 || quiet[0] != "!quiet:x" || !spy.bulkPvt[1] {
		t.Errorf("quiet batch = %v (private %v), want just !quiet:x", quiet, spy.bulkPvt[1])
	}
	// And it reports once, for the whole gesture.
	if msg.rooms != 2 || msg.result.Marked != 2 {
		t.Errorf("reported %+v, want 2 rooms marked in one message", msg)
	}
}
