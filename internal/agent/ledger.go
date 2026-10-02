// Package agent keeps the append-only JSON-lines ledger of messages an assistant
// (kith-mcp) sent as this account. It is the audit trail and the source of the per-room
// cooldown, and lives under XDG_STATE_HOME since no server has this record.
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/gofrs/flock"
)

// What became of one request to write.
const (
	// Sent is a message the homeserver took.
	Sent = "sent"
	// Queued is a message the homeserver would not take, handed to the daemon's queue
	// to retry.
	Queued = "queued"
	// Drafted is a message left in a room's composer for a person to send or not.
	Drafted = "drafted"
	// Refused is a write the policy did not allow — a room outside `[agent.read]` or
	// `[agent.write]` — so nothing reached any room or composer.
	Refused = "refused"
)

// Entry is one line of the ledger: who wrote what, where it went, and why it went there
// rather than the other way.
type Entry struct {
	At      time.Time     `json:"at"`
	Room    domain.RoomID `json:"room"`
	Name    string        `json:"room_name,omitempty"`
	Author  string        `json:"author"`
	Outcome string        `json:"outcome"`
	// Reason is why this outcome and not the other — the room is not on the send list,
	// the cooldown is still running, the send failed.
	Reason string `json:"reason,omitempty"`
	// Text is the message, capped by TextCap.
	Text string `json:"text"`
}

// TextCap is how much of a message the ledger keeps; NameCap of a room's name, and
// ReasonCap of why. Every field is capped: a line longer than maxLine could not be
// read back, and every later send, counted from the ledger, would be refused.
const (
	TextCap   = 500
	NameCap   = 200
	ReasonCap = 500
)

// Ledger is one account's record, at a path.
type Ledger struct{ path string }

// Open is the ledger at path, with its directory made.
func Open(path string) (*Ledger, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("agent: no ledger path")
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, fmt.Errorf("agent: create ledger directory: %w", err)
	}
	return &Ledger{path: path}, nil
}

// Exclusive holds the ledger for one decision and its record: another assistant
// session (another kith-mcp) waits until release is called. The cooldown is read from the
// ledger and the send appended to it, so without this two sessions could both read "no
// message lately" and both send.
func (l *Ledger) Exclusive(ctx context.Context) (release func(), err error) {
	fl := flock.New(l.path + ".lock")
	if _, err := fl.TryLockContext(ctx, lockRetry); err != nil {
		return nil, fmt.Errorf("agent: hold the ledger: %w", err)
	}
	return func() { _ = fl.Unlock() }, nil
}

// lockRetry is how often a session waiting for the ledger asks again.
const lockRetry = 20 * time.Millisecond

// ownerOnly and dirMode keep the record as private as the messages it names. It holds
// the text of things said in rooms, so it is exactly as sensitive as the cache.
const (
	ownerOnly = 0o600
	dirMode   = 0o700
)

// Writable reports whether an entry could be appended, without appending one.
func (l *Ledger) Writable() error {
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, ownerOnly)
	if err != nil {
		return fmt.Errorf("agent: ledger %s is not writable: %w", l.path, err)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("agent: ledger %s: %w", l.path, closeErr)
	}
	return nil
}

// Append records one entry.
func (l *Ledger) Append(entry Entry) error {
	if entry.At.IsZero() {
		entry.At = time.Now()
	}
	entry.At = entry.At.UTC()
	entry.Text = capped(entry.Text, TextCap)
	entry.Name = capped(entry.Name, NameCap)
	entry.Reason = capped(entry.Reason, ReasonCap)
	entry.Author = capped(entry.Author, NameCap)
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("agent: encode ledger entry: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, ownerOnly)
	if err != nil {
		return fmt.Errorf("agent: open ledger %s: %w", l.path, err)
	}
	// One Write of one line, deliberately: an O_APPEND write under the pipe-buffer size
	// lands whole, so two sessions writing at once interleave lines rather than bytes.
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close() // the write error is the report
		return fmt.Errorf("agent: write ledger %s: %w", l.path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("agent: close ledger %s: %w", l.path, err)
	}
	return nil
}

// Entries is the whole ledger, oldest first, skipping lines that do not parse (see
// Read, which counts them).
func (l *Ledger) Entries() ([]Entry, error) {
	entries, _, err := l.Read()
	return entries, err
}

// Read is the whole ledger, oldest first, and how many lines were skipped because they
// do not parse — a torn write or a hand edit. Skipped, not fatal: one bad line must not
// hide the record around it; but a reader showing the ledger should say so.
func (l *Ledger) Read() (entries []Entry, skipped int, err error) {
	file, err := os.Open(l.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("agent: read ledger %s: %w", l.path, err)
	}
	defer func() { _ = file.Close() }()

	var out []Entry
	lines := bufio.NewScanner(file)
	lines.Buffer(make([]byte, 0, 4096), maxLine)
	for lines.Scan() {
		var entry Entry
		if jerr := json.Unmarshal(lines.Bytes(), &entry); jerr != nil {
			skipped++
			continue
		}
		out = append(out, entry)
	}
	if serr := lines.Err(); serr != nil {
		return nil, skipped, fmt.Errorf("agent: scan ledger %s: %w", l.path, serr)
	}
	return out, skipped, nil
}

// maxLine bounds one ledger line when reading it back. Far above TextCap plus the rest
// of an entry, and there so a corrupted file cannot be read into memory whole.
const maxLine = 1 << 16

// LastOut is when a message last went out to one room — sent or queued — and whether one
// ever did. It is the whole of what the cooldown asks.
func LastOut(entries []Entry, room domain.RoomID) (time.Time, bool) {
	var latest time.Time
	var found bool
	for i := range entries {
		if entries[i].Room != room {
			continue
		}
		if entries[i].Outcome != Sent && entries[i].Outcome != Queued {
			continue
		}
		if !found || entries[i].At.After(latest) {
			latest, found = entries[i].At, true
		}
	}
	return latest, found
}

// capped trims text to at most n runes, marking that it was trimmed.
func capped(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}
