// Package local is what the daemon answers from its own stores whatever the network:
// drafts, search, emoji and color memory, and the assistant (spelling, completion,
// language models). It never talks to a chat network except through Network.
package local

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// timelineCacheLimit is how many cached messages CachedTimeline reads on room entry,
// enough to restore a backfilled room instantly.
const timelineCacheLimit = 2000

// Network is what the service asks of the chat networks it serves alongside.
type Network interface {
	// Me is every ID that is this person on every network.
	Me() []string
	// RoomEncryption reports which rooms are encrypted; unknown reports as encrypted.
	RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
	// RewindSync makes the networks refill an emptied cache from the start.
	RewindSync(ctx context.Context) error
	// RecountUnread has rooms' networks count their unread again from the cache.
	RecountUnread(ctx context.Context, rooms []domain.RoomID)
}

// Service answers from the cache and the local engines; zero engines answer as
// unconfigured until Use* wires them.
type Service struct {
	cache *db.Cache
	net   Network
	// logger receives what the service cannot return; nil is silent.
	logger *slog.Logger

	// spell is the spelling engine; its zero value answers ErrSpellUnavailable.
	spell spellcheck
	// model is the optional remote language-model layer.
	model modelLayer
	// completion is the local word-completion model.
	completion completionModel
	// vocab is word completion's recent-history windows.
	vocab recentVocab

	// places are the names, space order and pins a room's facts are read with.
	placesMu sync.Mutex
	places   domain.Places
}

// UsePlaces sets the room names, space priority and pins the model opt-in reads room
// facts with. Called at startup and on reload.
func (s *Service) UsePlaces(places domain.Places) {
	s.placesMu.Lock()
	defer s.placesMu.Unlock()
	s.places = places
}

func (s *Service) placesNow() domain.Places {
	s.placesMu.Lock()
	defer s.placesMu.Unlock()
	return s.places
}

// New is a service over cache (nil: every read answers empty) for net.
func New(cache *db.Cache, net Network) *Service {
	return &Service{cache: cache, net: net}
}

// UseLogger sets where the service reports what it cannot return; nil is silent.
func (s *Service) UseLogger(log *slog.Logger) {
	if log == nil {
		return
	}
	s.logger = log
	s.spell.mu.Lock()
	s.spell.log = log
	s.spell.mu.Unlock()
}

// UseDataDir sets the data directory ([storage] data_dir) where dictionaries, word
// counts and models are installed.
func (s *Service) UseDataDir(dir string) {
	s.spell.mu.Lock()
	defer s.spell.mu.Unlock()
	s.spell.data = dir
}

// Close stops the engines the service started.
func (s *Service) Close() {
	s.spell.stop()
	s.closeCompletionModel()
}

// MessageCached feeds a newly cached message into word completion.
func (s *Service) MessageCached(msg domain.Message) { s.vocab.added(msg, s.me()) }

// RoomChanged marks a room's completion windows for rebuild after an edit or deletion.
func (s *Service) RoomChanged(roomID domain.RoomID) { s.vocab.changed(roomID) }

func (s *Service) me() []string {
	if s.net == nil {
		return nil
	}
	return s.net.Me()
}

// roomEncrypted reports whether a room is encrypted; unknown counts as yes.
func (s *Service) roomEncrypted(ctx context.Context, roomID domain.RoomID) bool {
	if s.net == nil {
		return true
	}
	encrypted, err := s.net.RoomEncryption(ctx, []domain.RoomID{roomID})
	if err != nil {
		s.warnIf(ctx, err, "read room encryption", "room", roomID)
		return true
	}
	is, known := encrypted[roomID]
	return is || !known
}

// log is the service's logger, never nil.
func (s *Service) log() *slog.Logger {
	if s.logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.logger
}

// warnIf logs a failed best-effort step at warn; nil err is silent.
func (s *Service) warnIf(ctx context.Context, err error, op string, attrs ...any) {
	if err == nil {
		return
	}
	level := slog.LevelWarn
	// Our own shutdown cancels everything in flight; that is not a failure.
	if ctx.Err() != nil {
		level = slog.LevelDebug
	}
	s.log().Log(ctx, level, op+" failed", append([]any{"op", op, "err", err}, attrs...)...)
}

// fromCache runs a cache read, wrapping its error; without a cache it answers zero.
func fromCache[T any](s *Service, op string, read func(*db.Cache) (T, error)) (T, error) {
	var zero T
	if s.cache == nil {
		return zero, nil
	}
	v, err := read(s.cache)
	if err != nil {
		return zero, fmt.Errorf("local: %s: %w", op, err)
	}
	return v, nil
}

// toCache runs a cache write, wrapping its error; a no-op without a cache.
func toCache(s *Service, op string, write func(*db.Cache) error) error {
	if s.cache == nil {
		return nil
	}
	if err := write(s.cache); err != nil {
		return fmt.Errorf("local: %s: %w", op, err)
	}
	return nil
}

// errNoCache refuses to clear a cache that is not open.
var errNoCache = errors.New("local: the cache is not open (the daemon log says why), so there is nothing to clear; " +
	"remove the cache file and restart kithd to start it anew")

// ClearCache empties the local cache and has the networks refill it.
func (s *Service) ClearCache(ctx context.Context) error {
	if s.cache == nil {
		// Silence would read as done. The open failure is in the daemon's log; removing the
		// file and restarting starts anew.
		return errNoCache
	}
	if err := s.cache.Clear(ctx); err != nil {
		return fmt.Errorf("local: clear cache: %w", err)
	}
	s.vocab.cleared()
	if s.net == nil {
		return nil
	}
	return s.net.RewindSync(ctx) //nolint:wrapcheck // the network's own error, already worded
}

// CachedTimeline returns a room's most recent cached messages (nil without a cache).
func (s *Service) CachedTimeline(ctx context.Context, roomID domain.RoomID) ([]domain.Message, error) {
	return fromCache(s, "read cached timeline", func(c *db.Cache) ([]domain.Message, error) {
		return c.Messages(ctx, roomID, timelineCacheLimit)
	})
}

// LastMessages is when each room last had a message, from the cache (nil without one).
func (s *Service) LastMessages(ctx context.Context) (map[domain.RoomID]time.Time, error) {
	return fromCache(s, "read last messages", func(c *db.Cache) (map[domain.RoomID]time.Time, error) {
		return c.LastMessages(ctx)
	})
}

// StarredIn is the room's starred set from the cache mirror.
func (s *Service) StarredIn(ctx context.Context, roomID domain.RoomID) ([]domain.EventID, error) {
	return fromCache(s, "list starred in "+string(roomID), func(c *db.Cache) ([]domain.EventID, error) {
		return c.Starred(ctx, roomID)
	})
}

// PersonVerdicts is every person's marks and releases from the cache (Matrix IDs'
// mirrored from account data).
func (s *Service) PersonVerdicts(ctx context.Context) ([]domain.PersonVerdict, error) {
	return fromCache(s, "list person verdicts", func(c *db.Cache) ([]domain.PersonVerdict, error) {
		return c.PersonVerdicts(ctx)
	})
}

// BlockedPeople is each network's block list, from the cache mirror. The server
// copy is the record; the adapters refresh the mirror on connect and after a
// blocklist-change event.
func (s *Service) BlockedPeople(ctx context.Context) (map[domain.Protocol][]string, error) {
	return fromCache(s, "list blocked people", func(c *db.Cache) (map[domain.Protocol][]string, error) {
		return c.AllNetworkBlocked(ctx)
	})
}

// SenderMessages is a sender's cached messages in every room since since, oldest
// first, at most limit (db.Cache.SenderMessages).
func (s *Service) SenderMessages(ctx context.Context, sender string, since time.Time, limit int) ([]domain.Message, error) {
	return fromCache(s, "read a sender's messages", func(c *db.Cache) ([]domain.Message, error) {
		return c.SenderMessages(ctx, sender, since, limit)
	})
}

// WrittenTo is everyone you have written to (db.Cache.WrittenTo).
func (s *Service) WrittenTo(ctx context.Context, me []string) ([]string, error) {
	return fromCache(s, "read whom you wrote to", func(c *db.Cache) ([]string, error) {
		return c.WrittenTo(ctx, me)
	})
}

// TagVerdicts is every move and release from the cache (mirrored from account data).
func (s *Service) TagVerdicts(ctx context.Context) ([]domain.TagVerdict, error) {
	return fromCache(s, "list tag verdicts", func(c *db.Cache) ([]domain.TagVerdict, error) {
		return c.TagVerdicts(ctx)
	})
}
