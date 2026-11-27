// Package schedule is the on-disk TOML queue of messages to send later. It is not in
// the disposable cache since unsent messages exist nowhere else.
package schedule

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// ownerOnly: the queue holds unsent message bodies.
const ownerOnly os.FileMode = 0o600

// Store is the queue on disk. Only the daemon writes it, so there is no lock.
type Store struct {
	path string
}

// New returns a store over the file at path without reading it.
func New(path string) *Store { return &Store{path: path} }

// Path is the queue file's path.
func (s *Store) Path() string { return s.path }

type file struct {
	Message []entry `toml:"message"`
}

type entry struct {
	ID         string    `toml:"id"`
	Room       string    `toml:"room"`
	Body       string    `toml:"body"`
	ThreadRoot string    `toml:"thread_root,omitempty"`
	ReplyTo    string    `toml:"reply_to,omitempty"`
	Mention    []mention `toml:"mention,omitempty"`
	// TxnID makes retries safe: the homeserver deduplicates by it.
	TxnID   string `toml:"txn_id,omitempty"`
	Emote   bool   `toml:"emote,omitempty"`
	At      string `toml:"at"`
	Written string `toml:"written"`
	// Refused is why the network refused it for good; it waits to be retried.
	Refused string `toml:"refused,omitempty"`
	// File is the kept file it sends (Keep), FileName the name it goes under.
	File     string `toml:"file,omitempty"`
	FileName string `toml:"file_name,omitempty"`
}

type mention struct {
	UserID string `toml:"user_id"`
	Name   string `toml:"name"`
}

// Load reads the queue; a missing file is empty. Bad entries are reported in the error
// while the rest load.
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
	s.sweep(out)
	return out, nil
}

// files is the folder of the files queued messages send, beside the queue: the uploads
// folder they arrive in is emptied on every start.
func (s *Store) files() string { return s.path + ".files" }

// sweep removes the kept files no queued message sends: one sent or canceled by a
// daemon that stopped before removing it.
func (s *Store) sweep(queue []domain.ScheduledMessage) {
	kept, _ := os.ReadDir(s.files()) // none kept is nothing to sweep
	for _, f := range kept {
		path := filepath.Join(s.files(), f.Name())
		if !slices.ContainsFunc(queue, func(m domain.ScheduledMessage) bool { return m.KeptFile == path }) {
			_ = os.Remove(path) // best effort: the next start tries again
		}
	}
}

// Keep moves the file at path into the queue's own folder and returns where it is now;
// across file systems it is copied, and path is the caller's to remove.
func (s *Store) Keep(path string) (string, error) {
	if err := os.MkdirAll(s.files(), 0o700); err != nil {
		return "", fmt.Errorf("schedule: create %s: %w", s.files(), err)
	}
	dst, err := os.CreateTemp(s.files(), "file-*")
	if err != nil {
		return "", fmt.Errorf("schedule: keep a file: %w", err)
	}
	kept := dst.Name()
	if err = os.Rename(path, kept); err != nil {
		err = copyInto(dst, path)
	}
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(kept) // cleanup on the error path; err is the report
		return "", fmt.Errorf("schedule: keep a file: %w", err)
	}
	return kept, nil
}

// copyInto copies the file at path into dst.
func copyInto(dst *os.File, path string) error {
	src, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = src.Close() }() // read only
	if _, err = io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	return nil
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
			continue
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
		Refused:    e.Refused,
		KeptFile:   e.File,
		FileName:   e.FileName,
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
			Refused:    msg.Refused,
			File:       msg.KeptFile,
			FileName:   msg.FileName,
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
