package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
)

func ledger(t *testing.T) *agent.Ledger {
	t.Helper()
	out, _ := ledgerAt(t)
	return out
}

// ledgerAt is a ledger in a fresh directory, and the file it writes.
func ledgerAt(t *testing.T) (*agent.Ledger, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sends.jsonl")
	out, err := agent.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return out, path
}

// A ledger never written to reads as empty, not an error.
func TestEmptyLedgerReadsAsNothing(t *testing.T) {
	t.Parallel()

	entries, err := ledger(t).Entries()
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %v, %v; want none and no error", entries, err)
	}
}

func TestAppendedEntriesComeBackInOrder(t *testing.T) {
	t.Parallel()

	l := ledger(t)
	for _, outcome := range []string{agent.Sent, agent.Drafted, agent.Queued} {
		if err := l.Append(agent.Entry{Room: mx + "!a:x", Outcome: outcome, Text: outcome}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	entries, err := l.Entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 3 || entries[0].Outcome != agent.Sent || entries[2].Outcome != agent.Queued {
		t.Fatalf("entries = %+v, want the three in the order they were written", entries)
	}
	// An entry without a time is stamped; the cooldown needs it.
	if entries[0].At.IsZero() {
		t.Error("an appended entry carries no time")
	}
}

// Drafts are what the cooldown produces, so counting them would lock a room forever.
func TestLastOutIgnoresDraftsAndRefusals(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entries := []agent.Entry{
		{Room: mx + "!a:x", Outcome: agent.Sent, At: now.Add(-time.Hour)},
		{Room: mx + "!a:x", Outcome: agent.Drafted, At: now},
		{Room: mx + "!a:x", Outcome: agent.Refused, At: now},
		{Room: mx + "!b:x", Outcome: agent.Queued, At: now},
	}
	last, ever := agent.LastOut(entries, mx+"!a:x")
	if !ever || !last.Equal(now.Add(-time.Hour)) {
		t.Errorf("last out of !a:x = %v (%v), want the send an hour ago", last, ever)
	}
	// A queued message counts as sent.
	if _, ever := agent.LastOut(entries, mx+"!b:x"); !ever {
		t.Error("a queued message did not count")
	}
	if _, ever := agent.LastOut(entries, mx+"!c:x"); ever {
		t.Error("a room nothing was written to reported a send")
	}
}

func TestTextIsCapped(t *testing.T) {
	t.Parallel()

	l := ledger(t)
	if err := l.Append(agent.Entry{Room: mx + "!a:x", Outcome: agent.Sent, Text: strings.Repeat("ה", 2000)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	entries, _ := l.Entries()
	if got := len([]rune(entries[0].Text)); got != agent.TextCap+1 {
		t.Errorf("text is %d runes, want %d and an ellipsis", got, agent.TextCap)
	}
}

func TestABadLineIsSkippedNotFatal(t *testing.T) {
	t.Parallel()

	l, path := ledgerAt(t)
	if err := l.Append(agent.Entry{Room: mx + "!a:x", Outcome: agent.Sent, Text: "one"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, werr := file.WriteString("{not json at all\n"); werr != nil {
		t.Fatalf("write: %v", werr)
	}
	_ = file.Close()
	if aerr := l.Append(agent.Entry{Room: mx + "!a:x", Outcome: agent.Sent, Text: "two"}); aerr != nil {
		t.Fatalf("append: %v", aerr)
	}
	entries, err := l.Entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the two readable ones", entries)
	}
	// Read counts it, so --agent-log can report it.
	if read, skipped, rerr := l.Read(); rerr != nil || len(read) != 2 || skipped != 1 {
		t.Errorf("Read() = %d entries, %d skipped, %v; want 2, 1, nil", len(read), skipped, rerr)
	}
}

func TestWritableReportsABlockedLedger(t *testing.T) {
	t.Parallel()

	if err := ledger(t).Writable(); err != nil {
		t.Fatalf("a fresh ledger reported %v, want writable", err)
	}
	// A directory where the file belongs can be neither appended nor created.
	blocked, path := ledgerAt(t)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("blocking it: %v", err)
	}
	if err := blocked.Writable(); err == nil {
		t.Error("a directory in the ledger's place reported writable")
	}
}

// Every field is capped: an outsider-chosen long room name must not make the ledger
// unreadable.
func TestALongNameDoesNotBreakTheLedger(t *testing.T) {
	t.Parallel()
	l := ledger(t)
	long := strings.Repeat("n", 1<<17)
	if err := l.Append(agent.Entry{Room: mx + "!a:x", Name: long, Author: long, Reason: long, Outcome: agent.Sent, Text: "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	entries, err := l.Entries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("Entries = %d, %v; want the entry read back", len(entries), err)
	}
}
