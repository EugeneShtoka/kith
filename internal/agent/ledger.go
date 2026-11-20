// Package agent keeps the append-only JSON-lines ledger of messages an assistant
// (kith-mcp) sent as this account: the audit trail and the source of the per-room
// cooldown. It lives under XDG_STATE_HOME since no server has this record.
package agent

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/gofrs/flock"
)

// Outcomes of one write request.
const (
	Sent      = "sent"      // the homeserver took it
	Queued    = "queued"    // the homeserver refused it; the daemon's queue retries
	Scheduled = "scheduled" // queued to send later
	Drafted   = "drafted"   // left in the room's composer
	Refused   = "refused"   // outside [agent.read]/[agent.write]; nothing was written
)

// Entry is one ledger line.
type Entry struct {
	At      time.Time     `json:"at"`
	Room    domain.RoomID `json:"room"`
	Name    string        `json:"room_name,omitempty"`
	Author  string        `json:"author"`
	Outcome string        `json:"outcome"`
	// Reason explains the outcome, e.g. cooldown running or send failed.
	Reason string `json:"reason,omitempty"`
	Text   string `json:"text"` // capped by TextCap
	// Due is when a scheduled message goes out.
	Due time.Time `json:"due,omitzero"`
}

// Field caps in runes. Every field is capped: a line over maxLine could not be read
// back, and every later send would then be refused.
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

// Exclusive locks the ledger across sessions until release, so two kith-mcp sessions
// cannot both pass the cooldown check and send.
func (l *Ledger) Exclusive(ctx context.Context) (release func(), err error) {
	fl := flock.New(l.path + ".lock")
	if _, err := fl.TryLockContext(ctx, lockRetry); err != nil {
		return nil, fmt.Errorf("agent: hold the ledger: %w", err)
	}
	return func() { _ = fl.Unlock() }, nil
}

const lockRetry = 20 * time.Millisecond

// The ledger holds message text, so it is as private as the cache.
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
	// One Write per line: a small O_APPEND write lands whole, so concurrent sessions
	// interleave lines, not bytes.
	if _, err := file.Write(append(line, '\n')); err != nil {
		_ = file.Close() // the write error is the report
		return fmt.Errorf("agent: write ledger %s: %w", l.path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("agent: close ledger %s: %w", l.path, err)
	}
	return nil
}

// Entries is the whole ledger, oldest first, skipping unparsable lines.
func (l *Ledger) Entries() ([]Entry, error) {
	entries, _, err := l.Read()
	return entries, err
}

// Read is the whole ledger, oldest first, and the count of unparsable lines skipped
// (a torn write or hand edit); one bad line must not hide the rest.
func (l *Ledger) Read() (entries []Entry, skipped int, err error) {
	file, err := os.Open(l.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
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

// maxLine bounds one line on read, well above a capped entry, so a corrupt file is not
// read whole.
const maxLine = 1 << 16

// Outs are when messages went or go out (sent, queued or scheduled) to room, earliest
// first, for the cooldown: a scheduled one at its due time, which may be ahead.
func Outs(entries []Entry, room domain.RoomID) []time.Time {
	var outs []time.Time
	for i := range entries {
		if entries[i].Room != room {
			continue
		}
		if entries[i].Outcome != Sent && entries[i].Outcome != Queued && entries[i].Outcome != Scheduled {
			continue
		}
		outs = append(outs, cmp.Or(entries[i].Due, entries[i].At))
	}
	slices.SortFunc(outs, time.Time.Compare)
	return outs
}

// capped trims text to n runes plus an ellipsis.
func capped(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}
