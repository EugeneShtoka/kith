package mailbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// A pairing admits a bridge by hand (kith-mailbox pair): it opens one under a token,
// the bridge's kithd answers it over the listener, both show six digits, and the
// person at the mailbox accepts or refuses. kith-mailbox pair and the listener are two
// processes; the store's pairings table is what they share.

// Verdict is how the person at the mailbox answered a pairing.
type Verdict int

// The verdicts.
const (
	Pending Verdict = iota
	Accepted
	Refused
)

// Pairing is an open pairing: when it expires, the bridge that answered it (none yet),
// and the verdict.
type Pairing struct {
	Expires  time.Time
	Answered bool
	Device   devicekey.ID
	Verdict  Verdict
}

// ErrNoPairing is a token no open pairing has.
var ErrNoPairing = errors.New("mailbox: no pairing is open under that token, or it was answered already")

// OpenPairing opens a pairing under token until expires.
func (s *Store) OpenPairing(ctx context.Context, token string, expires time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO pairings (token, expires) VALUES (?, ?)`, token, expires.UnixMilli()); err != nil {
		return fmt.Errorf("mailbox: open a pairing: %w", err)
	}
	return nil
}

// PairingAt is the pairing open under token.
func (s *Store) PairingAt(ctx context.Context, token string) (Pairing, error) {
	var p Pairing
	var expires int64
	var device []byte
	err := s.db.QueryRowContext(ctx, `SELECT expires, device, verdict FROM pairings WHERE token = ?`, token).Scan(&expires, &device, &p.Verdict)
	if errors.Is(err, sql.ErrNoRows) {
		return Pairing{}, ErrNoPairing
	}
	if err != nil {
		return Pairing{}, fmt.Errorf("mailbox: read a pairing: %w", err)
	}
	p.Expires = time.UnixMilli(expires)
	if len(device) == len(p.Device) {
		p.Answered, p.Device = true, devicekey.ID(device)
	}
	return p, nil
}

// AnswerPairing records device as the bridge answering the pairing under token, at
// now: only the first answer, before it expires, is taken.
func (s *Store) AnswerPairing(ctx context.Context, token string, device devicekey.ID, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE pairings SET device = ? WHERE token = ? AND device IS NULL AND expires > ?`,
		device[:], token, now.UnixMilli())
	if err != nil {
		return fmt.Errorf("mailbox: answer a pairing: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return ErrNoPairing
	}
	return nil
}

// DecidePairing records the verdict on the pairing under token.
func (s *Store) DecidePairing(ctx context.Context, token string, v Verdict) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE pairings SET verdict = ? WHERE token = ?`, v, token); err != nil {
		return fmt.Errorf("mailbox: decide a pairing: %w", err)
	}
	return nil
}

// ClosePairing drops the pairing under token.
func (s *Store) ClosePairing(ctx context.Context, token string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pairings WHERE token = ?`, token); err != nil {
		return fmt.Errorf("mailbox: close a pairing: %w", err)
	}
	return nil
}

// PairingOpen reports whether a pairing is open and unanswered at now: while one is,
// a key not admitted is let in, to answer it and nothing else.
func (s *Store) PairingOpen(ctx context.Context, now time.Time) bool {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM pairings WHERE device IS NULL AND expires > ?`, now.UnixMilli()).Scan(&n)
	return err == nil && n > 0
}
