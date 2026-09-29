package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/agent"
	"github.com/EugeneShtoka/kith/internal/domain"
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

// A ledger that has never been written to is the empty list rather than an error: there
// is nothing to distinguish "wrote nothing" from "wrote nothing yet".
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
		if err := l.Append(agent.Entry{Room: "!a:x", Outcome: outcome, Text: outcome}); err != nil {
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
	// An entry with no time of its own is stamped, because a ledger line that cannot say
	// when is one the cooldown cannot use.
	if entries[0].At.IsZero() {
		t.Error("an appended entry carries no time")
	}
}

// The cooldown asks one question of this file, and a draft is not an answer to it:
// drafting is what the cooldown *does*, so counting one would make the rail hold a room
// shut forever on its own output.
func TestLastOutIgnoresDraftsAndRefusals(t *testing.T) {
	t.Parallel()

	now := time.Now()
	entries := []agent.Entry{
		{Room: "!a:x", Outcome: agent.Sent, At: now.Add(-time.Hour)},
		{Room: "!a:x", Outcome: agent.Drafted, At: now},
		{Room: "!a:x", Outcome: agent.Refused, At: now},
		{Room: "!b:x", Outcome: agent.Queued, At: now},
	}
	last, ever := agent.LastOut(entries, "!a:x")
	if !ever || !last.Equal(now.Add(-time.Hour)) {
		t.Errorf("last out of !a:x = %v (%v), want the send an hour ago", last, ever)
	}
	// A queued message is on its way, so it counts exactly as a send does.
	if _, ever := agent.LastOut(entries, "!b:x"); !ever {
		t.Error("a queued message did not count")
	}
	if _, ever := agent.LastOut(entries, "!c:x"); ever {
		t.Error("a room nothing was written to reported a send")
	}
}

// A long message is trimmed rather than making a line that cannot be appended atomically.
func TestTextIsCapped(t *testing.T) {
	t.Parallel()

	l := ledger(t)
	if err := l.Append(agent.Entry{Room: "!a:x", Outcome: agent.Sent, Text: strings.Repeat("ה", 2000)}); err != nil {
		t.Fatalf("append: %v", err)
	}
	entries, _ := l.Entries()
	if got := len([]rune(entries[0].Text)); got != agent.TextCap+1 {
		t.Errorf("text is %d runes, want %d and an ellipsis", got, agent.TextCap)
	}
}

// One unreadable line must not cost the whole record: the audit trail is worth more than
// the write that went wrong.
func TestABadLineIsSkippedNotFatal(t *testing.T) {
	t.Parallel()

	l, path := ledgerAt(t)
	if err := l.Append(agent.Entry{Room: "!a:x", Outcome: agent.Sent, Text: "one"}); err != nil {
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
	if aerr := l.Append(agent.Entry{Room: "!a:x", Outcome: agent.Sent, Text: "two"}); aerr != nil {
		t.Fatalf("append: %v", aerr)
	}
	entries, err := l.Entries()
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want the two readable ones", entries)
	}
	// Read counts it, so --agent-log can say a line was lost rather than hide it.
	if read, skipped, rerr := l.Read(); rerr != nil || len(read) != 2 || skipped != 1 {
		t.Errorf("Read() = %d entries, %d skipped, %v; want 2, 1, nil", len(read), skipped, rerr)
	}
}

// Writable is a pre-flight rather than a guess, because it is what the send path asks
// before doing anything it cannot record.
func TestWritableReportsABlockedLedger(t *testing.T) {
	t.Parallel()

	if err := ledger(t).Writable(); err != nil {
		t.Fatalf("a fresh ledger reported %v, want writable", err)
	}
	// A directory where the file belongs: openable for neither append nor create, which
	// is the state a broken state directory leaves behind.
	blocked, path := ledgerAt(t)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("blocking it: %v", err)
	}
	if err := blocked.Writable(); err == nil {
		t.Error("a directory in the ledger's place reported writable")
	}
}

// The path is keyed on the account, like the cache, the socket and the scheduled queue —
// two accounts on one machine are two records.
func TestPathIsPerAccount(t *testing.T) {
	t.Parallel()

	first, err := agent.DefaultPath("@one:example.org")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	second, err := agent.DefaultPath("@two:example.org")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if first == second {
		t.Fatalf("both accounts resolved to %s", first)
	}
	if !strings.Contains(first, domain.AccountKey("@one:example.org")) {
		t.Errorf("path = %s, want it keyed on the account", first)
	}
}

// Every field is capped, not only the text: a room named at length (anyone can name a
// room, or themselves in a DM) must not make the ledger unreadable, which refuses every
// later send.
func TestALongNameDoesNotBreakTheLedger(t *testing.T) {
	t.Parallel()
	l := ledger(t)
	long := strings.Repeat("n", 1<<17)
	if err := l.Append(agent.Entry{Room: "!a:x", Name: long, Author: long, Reason: long, Outcome: agent.Sent, Text: "hi"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	entries, err := l.Entries()
	if err != nil || len(entries) != 1 {
		t.Fatalf("Entries = %d, %v; want the entry read back", len(entries), err)
	}
}
