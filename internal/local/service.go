// Package local is what the daemon answers from its own stores, whatever network a
// room is on: the cache's drafts, search, emoji and color memory, and the assistant
// (spelling, word completion, the language models). Nothing here talks to a chat
// network; what it needs to know about one comes through Network.
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

// timelineCacheLimit is how many cached messages CachedTimeline reads on room
// entry — large enough to restore a backfilled room's history instantly.
const timelineCacheLimit = 2000

// Network is what the service asks of the chat networks it serves alongside.
type Network interface {
	// Account is this person's own account ID, or "" before a session exists.
	Account() string
	// Me is every ID that is this person: the account and its configured identity.
	Me() []string
	// RoomEncryption reports which rooms are encrypted; unknown reports as encrypted.
	RoomEncryption(ctx context.Context, roomIDs []domain.RoomID) (map[domain.RoomID]bool, error)
	// RewindSync makes the networks refill an emptied cache from the start.
	RewindSync(ctx context.Context) error
}

// Service answers from the cache and the local engines. Its zero engines answer as
// unconfigured; Use* wires them.
type Service struct {
	cache *db.Cache
	net   Network
	// logger receives what the service cannot return (see UseLogger); nil is silent.
	logger *slog.Logger

	// spell is the spelling engine; its zero value answers ErrSpellUnavailable.
	spell spellcheck
	// model is the optional remote language-model layer (see modeltask.go).
	model modelLayer
	// completion is the local word-completion model (see completionmodel.go).
	completion completionModel
	// vocab is word completion's recent-history windows (see recentvocab.go).
	vocab recentVocab

	// places are the names, space order and pins a room's facts are read with.
	placesMu sync.Mutex
	places   domain.Places
}

// UsePlaces sets the room names, space priority and pins the model opt-in reads a
// room's facts with, as every other scope does. Called at startup and on reload.
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

// UseLogger sets where the service reports what it cannot return. nil keeps the
// silent default.
func (s *Service) UseLogger(log *slog.Logger) {
	if log == nil {
		return
	}
	s.logger = log
	s.spell.mu.Lock()
	s.spell.log = log
	s.spell.mu.Unlock()
}

// UseDataDir sets kith's data directory ([storage] data_dir), where dictionaries,
// word counts and models are installed. Called at startup.
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

// MessageCached counts a message the network side just cached into word completion.
func (s *Service) MessageCached(msg domain.Message) { s.vocab.added(msg, s.account()) }

// RoomChanged is a room's cached messages changing other than by a new one (an edit,
// a deletion): its completion windows are rebuilt on next use.
func (s *Service) RoomChanged(roomID domain.RoomID) { s.vocab.changed(roomID) }

// account is this person's own account ID ("" before a session).
func (s *Service) account() string {
	if s.net == nil {
		return ""
	}
	return s.net.Account()
}

// me is every ID that is this person.
func (s *Service) me() []string {
	if s.net == nil {
		return nil
	}
	return s.net.Me()
}

// roomEncrypted is whether a room is encrypted; anything unknown counts as yes.
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

// warnIf logs a failed best-effort step at warn, with op naming it; nil is silent.
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

// fromCache runs a cache read, wrapping its error; without a cache it answers the
// zero value.
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
		// Saying nothing would read as done. A cache that failed to open is in the
		// daemon's log; removing the file and restarting the daemon starts it anew.
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

// SpamRooms is every caught room from the cache (mirrored from account data).
func (s *Service) SpamRooms(ctx context.Context) ([]domain.SpamVerdict, error) {
	return fromCache(s, "list spam rooms", func(c *db.Cache) ([]domain.SpamVerdict, error) {
		return c.SpamRooms(ctx)
	})
}
