package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// Detecting the languages someone writes reads every cached message, so no client runs
// it at start: kithd owes it once a new account's history has arrived. An account a
// login set up (not one signed in again) is new; once it is online and no history (a
// message sent before it came online) has arrived for historySettle, the offer is
// owed. Both are kept in a file, since a login may restart kithd. A client makes the
// owed offer when it starts or is told (ChangeLanguages); asking for it clears it.

// historySettle is how long no history may arrive before a new account's is taken to
// have arrived: backfills page with pauses, and a sync writes in batches.
const historySettle = 2 * time.Minute

// languagesFile is what survives a restart.
type languagesFile struct {
	// New is the accounts set up whose history has not arrived yet.
	New []newAccount `toml:"new"`
	// Owed is the offer being owed.
	Owed bool `toml:"owed"`
}

type newAccount struct {
	Network domain.Protocol `toml:"network"`
	Account string          `toml:"account"`
}

// Languages is the language detection kithd owes its clients.
type Languages struct {
	path string
	log  *slog.Logger
	// owe tells the attached clients the offer is owed (Streams.LanguagesOwed).
	owe func()

	mu   sync.Mutex
	file languagesFile
	// online is the new accounts online this run; since is when the first came online,
	// and history is what arrives sent before it. last is when history last arrived.
	online map[newAccount]bool
	since  time.Time
	last   time.Time
	wake   chan struct{}
}

// NewLanguages reads what is owed from path; a file that cannot be read owes nothing.
func NewLanguages(path string, owe func(), log *slog.Logger) *Languages {
	l := &Languages{path: path, log: log, owe: owe, wake: make(chan struct{}, 1)}
	if _, err := toml.DecodeFile(path, &l.file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Warn("read which languages are owed failed; none are", "path", path, "err", err)
		l.file = languagesFile{}
	}
	return l
}

// Added remembers that a login set up account on network.
func (l *Languages) Added(network domain.Protocol, account string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key := newAccount{Network: network, Account: account}
	if slices.Contains(l.file.New, key) {
		return
	}
	l.file.New = append(l.file.New, key)
	l.saveLocked()
}

// Status starts waiting for a new account's history once it is online; any other
// account's state is not news here.
func (l *Languages) Status(s domain.AccountStatus) {
	if s.Phase != domain.AccountOnline {
		return
	}
	key := newAccount{Network: s.Network, Account: s.Account}
	l.mu.Lock()
	if !slices.Contains(l.file.New, key) || l.online[key] {
		l.mu.Unlock()
		return
	}
	if l.online == nil {
		l.online = map[newAccount]bool{}
	}
	l.online[key] = true
	if l.since.IsZero() {
		l.since = time.Now()
	}
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// Arrived notes history arriving: messages sent before a new account came online. kithd
// hands it to the cache (db.Cache.OnArrived); it never blocks.
func (l *Languages) Arrived(_ domain.RoomID, sent time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.since.IsZero() && sent.Before(l.since) {
		l.last = time.Now()
	}
}

// Run owes the offer once the new accounts online have had no history for
// historySettle, until ctx ends. It blocks.
func (l *Languages) Run(ctx context.Context) {
	for {
		l.mu.Lock()
		due, waiting := l.dueLocked()
		l.mu.Unlock()
		var fire <-chan time.Time
		var timer *time.Timer
		if waiting {
			timer = time.NewTimer(time.Until(due))
			fire = timer.C
		}
		select {
		case <-ctx.Done():
		case <-l.wake:
		case <-fire:
			l.settle()
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// dueLocked is when the history of the new accounts online counts as arrived; false
// while none is online.
func (l *Languages) dueLocked() (time.Time, bool) {
	if l.since.IsZero() {
		return time.Time{}, false
	}
	from := l.since
	if l.last.After(from) {
		from = l.last
	}
	return from.Add(historySettle), true
}

// settle owes the offer if the history has stopped arriving; Run waits again if not.
func (l *Languages) settle() {
	l.mu.Lock()
	due, waiting := l.dueLocked()
	if !waiting || time.Now().Before(due) {
		l.mu.Unlock()
		return
	}
	l.file.New = slices.DeleteFunc(l.file.New, func(a newAccount) bool { return l.online[a] })
	l.file.Owed = true
	l.online, l.since, l.last = nil, time.Time{}, time.Time{}
	l.saveLocked()
	l.mu.Unlock()
	l.owe()
}

// take clears the owed offer, reporting whether it was.
func (l *Languages) take() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.file.Owed {
		return false
	}
	l.file.Owed = false
	l.saveLocked()
	return true
}

// restore owes again an offer taken that could not be made.
func (l *Languages) restore() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.file.Owed = true
	l.saveLocked()
}

// saveLocked writes the file, logging a failure: the next start then owes what the
// last write said.
func (l *Languages) saveLocked() {
	if err := writeLanguages(l.path, l.file); err != nil {
		l.log.Warn("keep which languages are owed failed", "path", l.path, "err", err)
	}
}

// writeLanguages replaces the file at path with f atomically.
func writeLanguages(path string, f languagesFile) error {
	var body bytes.Buffer
	if err := toml.NewEncoder(&body).Encode(f); err != nil {
		return fmt.Errorf("daemon: encode the owed languages: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("daemon: write the owed languages: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".languages-*.toml")
	if err != nil {
		return fmt.Errorf("daemon: write the owed languages: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // gone after the rename; a leftover on failure
	_, err = tmp.Write(body.Bytes())
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return fmt.Errorf("daemon: write the owed languages: %w", err)
	}
	return nil
}
