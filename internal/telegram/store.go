package telegram

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"sync"

	"github.com/gotd/td/telegram/updates"

	// Pure-Go SQLite driver, as the cache uses.
	_ "modernc.org/sqlite"
)

// The Telegram store keeps, per account user ID, gotd's updates state (for gap
// recovery), access hashes and backfill progress. Nothing secret; daemon-only.
//
// gotd saves the updates position even when the handler failed, so an account whose
// caching failed is held: its position goes unsaved until it reconnects and refetches.

// storeSchema is the store's tables; user_version is the schema version.
const storeSchema = `
CREATE TABLE IF NOT EXISTS state (
	user INTEGER PRIMARY KEY,
	pts INTEGER NOT NULL, qts INTEGER NOT NULL, date INTEGER NOT NULL, seq INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS channel_pts (
	user INTEGER NOT NULL, channel INTEGER NOT NULL, pts INTEGER NOT NULL,
	PRIMARY KEY (user, channel)
);
CREATE TABLE IF NOT EXISTS channel_hashes (
	user INTEGER NOT NULL, channel INTEGER NOT NULL, hash INTEGER NOT NULL,
	PRIMARY KEY (user, channel)
);
CREATE TABLE IF NOT EXISTS user_hashes (
	user INTEGER NOT NULL, target INTEGER NOT NULL, hash INTEGER NOT NULL,
	PRIMARY KEY (user, target)
);
CREATE TABLE IF NOT EXISTS backfill (
	user INTEGER NOT NULL, room TEXT NOT NULL,
	next TEXT NOT NULL, fetched INTEGER NOT NULL, done INTEGER NOT NULL,
	PRIMARY KEY (user, room)
);
PRAGMA user_version = 2;
`

// Store is the Telegram store. Build it with OpenStore.
type Store struct {
	db *sql.DB

	mu sync.Mutex
	// held are the accounts whose updates position goes unsaved (hold).
	held map[int64]bool
}

var (
	_ updates.StateStorage        = (*Store)(nil)
	_ updates.ChannelAccessHasher = (*Store)(nil)
	_ updates.UserAccessHasher    = (*Store)(nil)
)

// OpenStore opens (creating and migrating) the store at path.
func OpenStore(ctx context.Context, path string) (*Store, error) {
	dsn := (&url.URL{Scheme: "file", Opaque: path, RawQuery: url.Values{
		"_pragma": {"journal_mode(WAL)", "busy_timeout(5000)"},
	}.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("telegram: open the store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, storeSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("telegram: migrate the store: %w", err)
	}
	return &Store{db: db, held: map[int64]bool{}}, nil
}

// Close closes the store.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("telegram: close the store: %w", err)
	}
	return nil
}

// hold stops saving user's updates position after a message failed to cache.
func (s *Store) hold(user int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held[user] = true
}

// release resumes saving user's position when its connection restarts.
func (s *Store) release(user int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.held, user)
}

// holding reports whether user's position goes unsaved.
func (s *Store) holding(user int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held[user]
}

// exec runs a write, unless user is held.
func (s *Store) exec(ctx context.Context, user int64, query string, args ...any) error {
	if s.holding(user) {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("telegram: keep the updates position: %w", err)
	}
	return nil
}

// GetState is user's updates state; found false before the first.
func (s *Store) GetState(ctx context.Context, user int64) (updates.State, bool, error) {
	var st updates.State
	err := s.db.QueryRowContext(ctx, `SELECT pts, qts, date, seq FROM state WHERE user = ?`, user).
		Scan(&st.Pts, &st.Qts, &st.Date, &st.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return updates.State{}, false, nil
	}
	if err != nil {
		return updates.State{}, false, fmt.Errorf("telegram: read the updates position: %w", err)
	}
	return st, true, nil
}

// SetState stores user's updates state.
func (s *Store) SetState(ctx context.Context, user int64, st updates.State) error {
	return s.exec(ctx, user, `INSERT INTO state (user, pts, qts, date, seq) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user) DO UPDATE SET pts = excluded.pts, qts = excluded.qts, date = excluded.date, seq = excluded.seq`,
		user, st.Pts, st.Qts, st.Date, st.Seq)
}

// SetPts updates user's pts; a no-op before SetState, which gotd calls first.
func (s *Store) SetPts(ctx context.Context, user int64, pts int) error {
	return s.exec(ctx, user, `UPDATE state SET pts = ? WHERE user = ?`, pts, user)
}

// SetQts is SetPts for qts.
func (s *Store) SetQts(ctx context.Context, user int64, qts int) error {
	return s.exec(ctx, user, `UPDATE state SET qts = ? WHERE user = ?`, qts, user)
}

// SetDate is SetPts for the date.
func (s *Store) SetDate(ctx context.Context, user int64, date int) error {
	return s.exec(ctx, user, `UPDATE state SET date = ? WHERE user = ?`, date, user)
}

// SetSeq is SetPts for seq.
func (s *Store) SetSeq(ctx context.Context, user int64, seq int) error {
	return s.exec(ctx, user, `UPDATE state SET seq = ? WHERE user = ?`, seq, user)
}

// SetDateSeq is SetPts for the date and seq together.
func (s *Store) SetDateSeq(ctx context.Context, user int64, date, seq int) error {
	return s.exec(ctx, user, `UPDATE state SET date = ?, seq = ? WHERE user = ?`, date, seq, user)
}

// GetChannelPts is a channel's pts, for user.
func (s *Store) GetChannelPts(ctx context.Context, user, channel int64) (int, bool, error) {
	var pts int
	err := s.db.QueryRowContext(ctx, `SELECT pts FROM channel_pts WHERE user = ? AND channel = ?`, user, channel).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("telegram: read a channel's updates position: %w", err)
	}
	return pts, true, nil
}

// SetChannelPts stores a channel's pts, for user.
func (s *Store) SetChannelPts(ctx context.Context, user, channel int64, pts int) error {
	return s.exec(ctx, user, `INSERT INTO channel_pts (user, channel, pts) VALUES (?, ?, ?)
		ON CONFLICT (user, channel) DO UPDATE SET pts = excluded.pts`, user, channel, pts)
}

// ForEachChannels calls f for each channel with a stored pts, for user.
func (s *Store) ForEachChannels(ctx context.Context, user int64, f func(ctx context.Context, channel int64, pts int) error) error {
	all, err := s.channelPositions(ctx, user)
	if err != nil {
		return err
	}
	for _, p := range all {
		if err := f(ctx, p.channel, p.pts); err != nil {
			return err
		}
	}
	return nil
}

// ForgetChannel drops a left channel's pts so it isn't followed on reconnect.
func (s *Store) ForgetChannel(ctx context.Context, user, channel int64) error {
	return s.exec(ctx, user, `DELETE FROM channel_pts WHERE user = ? AND channel = ?`, user, channel)
}

// position is one channel's pts.
type position struct {
	channel int64
	pts     int
}

// channelPositions is every channel pts for user, read whole because f may write to
// the single-connection store.
func (s *Store) channelPositions(ctx context.Context, user int64) ([]position, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT channel, pts FROM channel_pts WHERE user = ?`, user)
	if err != nil {
		return nil, fmt.Errorf("telegram: read the channels' positions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var all []position
	for rows.Next() {
		var p position
		if err := rows.Scan(&p.channel, &p.pts); err != nil {
			return nil, fmt.Errorf("telegram: read the channels' positions: %w", err)
		}
		all = append(all, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("telegram: read the channels' positions: %w", err)
	}
	return all, nil
}

// SetChannelAccessHash keeps a channel's access hash, for user.
func (s *Store) SetChannelAccessHash(ctx context.Context, user, channel, hash int64) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO channel_hashes (user, channel, hash) VALUES (?, ?, ?)
		ON CONFLICT (user, channel) DO UPDATE SET hash = excluded.hash`, user, channel, hash); err != nil {
		return fmt.Errorf("telegram: keep a channel's access hash: %w", err)
	}
	return nil
}

// GetChannelAccessHash is a channel's access hash, for user.
func (s *Store) GetChannelAccessHash(ctx context.Context, user, channel int64) (int64, bool, error) {
	return s.hash(ctx, `SELECT hash FROM channel_hashes WHERE user = ? AND channel = ?`, user, channel)
}

// SetUserAccessHash keeps a user's access hash, for user.
func (s *Store) SetUserAccessHash(ctx context.Context, user, target, hash int64) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO user_hashes (user, target, hash) VALUES (?, ?, ?)
		ON CONFLICT (user, target) DO UPDATE SET hash = excluded.hash`, user, target, hash); err != nil {
		return fmt.Errorf("telegram: keep a user's access hash: %w", err)
	}
	return nil
}

// GetUserAccessHash is a user's access hash, for user.
func (s *Store) GetUserAccessHash(ctx context.Context, user, target int64) (int64, bool, error) {
	return s.hash(ctx, `SELECT hash FROM user_hashes WHERE user = ? AND target = ?`, user, target)
}

func (s *Store) hash(ctx context.Context, query string, user, id int64) (int64, bool, error) {
	var hash int64
	err := s.db.QueryRowContext(ctx, query, user, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("telegram: read an access hash: %w", err)
	}
	return hash, true, nil
}

// backfilled is a chat's backfill progress: the next page's offset ("" for newest),
// messages read, and whether done (start of chat or kith's depth limit).
func (s *Store) backfilled(ctx context.Context, user int64, room string) (next string, fetched int, done bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT next, fetched, done FROM backfill WHERE user = ? AND room = ?`, user, room).
		Scan(&next, &fetched, &done)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, fmt.Errorf("telegram: read how far %s was read back: %w", room, err)
	}
	return next, fetched, done, nil
}

// keepBackfill stores a chat's backfill progress.
func (s *Store) keepBackfill(ctx context.Context, user int64, room, next string, fetched int, done bool) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO backfill (user, room, next, fetched, done) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (user, room) DO UPDATE SET next = excluded.next, fetched = excluded.fetched, done = excluded.done`,
		user, room, next, fetched, done); err != nil {
		return fmt.Errorf("telegram: keep how far %s was read back: %w", room, err)
	}
	return nil
}
