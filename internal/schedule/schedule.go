// Package schedule is the on-disk queue of messages to send later. It is a TOML file
// rather than part of the disposable cache, since unsent messages exist nowhere else;
// unparseable entries are reported and kept, never dropped.
package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ownerOnly is the file mode: the queue holds message bodies you have not sent yet.
const ownerOnly os.FileMode = 0o600

// Store is the queue on disk. The daemon owns it — one writer, so there is no lock
// here and no two-process race to reason about. The client asks the daemon.
type Store struct {
	path string
}

// New returns a store over the file at path. It does not read it; Load does.
func New(path string) *Store { return &Store{path: path} }

// Path is where the queue lives, for reporting.
func (s *Store) Path() string { return s.path }

// file is the TOML document.
type file struct {
	Message []entry `toml:"message"`
}

// entry is one pending send as written to disk.
type entry struct {
	ID         string `toml:"id"`
	Room       string `toml:"room"`
	Body       string `toml:"body"`
	ThreadRoot string `toml:"thread_root,omitempty"`
	// ReplyTo and Mention are what make a queued message arrive as the message it was:
	// the target it answers, and the pills written into its body.
	ReplyTo string    `toml:"reply_to,omitempty"`
	Mention []mention `toml:"mention,omitempty"`
	// TxnID is what makes retrying safe: the homeserver deduplicates by it, so an entry
	// whose send may already have arrived can be tried again.
	TxnID   string `toml:"txn_id,omitempty"`
	Emote   bool   `toml:"emote,omitempty"`
	At      string `toml:"at"`
	Written string `toml:"written"`
}

// mention is one pill as written to disk: who to notify, and the text that names them.
type mention struct {
	UserID string `toml:"user_id"`
	Name   string `toml:"name"`
}

// Load reads the queue. A missing file is an empty queue, not an error: that is
// every account that has never scheduled anything.
func (s *Store) Load() ([]domain.ScheduledMessage, error) {
	var doc file
	if _, err := toml.DecodeFile(s.path, &doc); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("schedule: read %s: %w", s.path, err)
	}
	out := make([]domain.ScheduledMessage, 0, len(doc.Message))
	var bad []error
	for i := range doc.Message {
		msg, err := doc.Message[i].decode()
		if err != nil {
			bad = append(bad, err)
			continue
		}
		out = append(out, msg)
	}
	domain.SortScheduled(out)
	if len(bad) > 0 {
		return out, fmt.Errorf("schedule: %s: %w", s.path, errors.Join(bad...))
	}
	return out, nil
}

func (e entry) decode() (domain.ScheduledMessage, error) {
	at, err := time.Parse(time.RFC3339, e.At)
	if err != nil {
		return domain.ScheduledMessage{}, fmt.Errorf("entry %q: at: %w", e.ID, err)
	}
	written := at
	if e.Written != "" {
		if w, werr := time.Parse(time.RFC3339, e.Written); werr == nil {
			written = w
		}
	}
	if e.ID == "" || e.Room == "" {
		return domain.ScheduledMessage{}, fmt.Errorf("entry at %s: needs both an id and a room", e.At)
	}
	mentions := make([]domain.Mention, 0, len(e.Mention))
	for _, m := range e.Mention {
		if m.UserID == "" {
			continue // a pill with nobody to notify is not a pill
		}
		mentions = append(mentions, domain.Mention{UserID: m.UserID, Name: m.Name})
	}
	if len(mentions) == 0 {
		mentions = nil
	}
	return domain.ScheduledMessage{
		ID:         e.ID,
		RoomID:     domain.RoomID(e.Room),
		Body:       e.Body,
		ThreadRoot: domain.EventID(e.ThreadRoot),
		ReplyTo:    domain.EventID(e.ReplyTo),
		Mentions:   mentions,
		TxnID:      e.TxnID,
		Emote:      e.Emote,
		At:         at.UTC(),
		Written:    written.UTC(),
	}, nil
}

// Save replaces the queue.
func (s *Store) Save(queue []domain.ScheduledMessage) error {
	domain.SortScheduled(queue)
	doc := file{Message: make([]entry, 0, len(queue))}
	for i := range queue {
		msg := &queue[i]
		pills := make([]mention, 0, len(msg.Mentions))
		for _, m := range msg.Mentions {
			pills = append(pills, mention{UserID: m.UserID, Name: m.Name})
		}
		doc.Message = append(doc.Message, entry{
			ID:         msg.ID,
			Room:       string(msg.RoomID),
			Body:       msg.Body,
			ThreadRoot: string(msg.ThreadRoot),
			ReplyTo:    string(msg.ReplyTo),
			Mention:    pills,
			TxnID:      msg.TxnID,
			Emote:      msg.Emote,
			At:         msg.At.UTC().Format(time.RFC3339),
			Written:    msg.Written.UTC().Format(time.RFC3339),
		})
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("schedule: create %s: %w", filepath.Dir(s.path), err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".scheduled-*.toml")
	if err != nil {
		return fmt.Errorf("schedule: temp file: %w", err)
	}
	name := tmp.Name()
	err = toml.NewEncoder(tmp).Encode(doc)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, ownerOnly)
	}
	if err == nil {
		err = os.Rename(name, s.path)
	}
	if err != nil {
		_ = os.Remove(name) // cleanup on the error path; err is the report
		return fmt.Errorf("schedule: write %s: %w", s.path, err)
	}
	return nil
}
