// Package pairing is what both ends of a QR pairing share: the token kithd issues,
// the six digits both screens show, and the kith://pair URI that carries a ticket
// from the trusted device to the new one. Pure: kithd keeps the tokens.
package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

// Token is a pairing token: 128 random bits, good for one pairing.
type Token [16]byte

// encoding writes a token as base32 without padding: 26 characters, URL-safe.
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewToken is a fresh random token.
func NewToken() Token {
	var t Token
	_, _ = rand.Read(t[:]) // crypto/rand.Read never fails
	return t
}

func (t Token) String() string { return encoding.EncodeToString(t[:]) }

// ErrNotAToken is text that does not read as a token.
var ErrNotAToken = errors.New("not a pairing token")

// ParseToken reads a token as String writes it, in either case.
func ParseToken(s string) (Token, error) {
	plain := strings.ToUpper(strings.TrimSpace(s))
	raw, err := encoding.DecodeString(plain)
	if err != nil || len(raw) != len(Token{}) || encoding.EncodeToString(raw) != plain {
		return Token{}, ErrNotAToken
	}
	return Token(raw), nil
}

// Digits are the six decimal digits both screens show for bridge pairing device under
// token: a QR photographed and used first shows digits nobody confirms.
func Digits(bridge, device devicekey.ID, token Token) string {
	h := sha256.New()
	h.Write(bridge[:])
	h.Write(device[:])
	h.Write(token[:])
	// 2^64 over 10^6 leaves a bias far below a guess's one in a million.
	return fmt.Sprintf("%06d", binary.BigEndian.Uint64(h.Sum(nil))%1_000_000)
}

// CheckAddress refuses an address a device cannot dial: no host:port, no host, or a
// port outside 1–65535.
func CheckAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return fmt.Errorf("%q is no host:port (192.168.1.5:7443, or [fd00::5]:7443)", addr)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q: the port must be a number from 1 to 65535", addr)
	}
	return nil
}

// Invite is a pairing as the new device reads it: where kithd listens, the ID its key
// must have, and the token, good until Expires.
type Invite struct {
	Addresses []string
	ID        devicekey.ID
	Token     Token
	Expires   time.Time
}

// The URI: kith://pair?addr=<host:port>&addr=…&exp=<unix seconds>&id=<ID>&token=<token>.
const (
	scheme   = "kith"
	host     = "pair"
	keyAddr  = "addr"
	keyExp   = "exp"
	keyID    = "id"
	keyToken = "token"
)

// URI writes the invite as ParseURI reads it; addresses keep their order, the one a
// device tries them in.
func (i Invite) URI() string {
	q := url.Values{keyAddr: i.Addresses}
	q.Set(keyExp, strconv.FormatInt(i.Expires.Unix(), 10))
	q.Set(keyID, i.ID.String())
	q.Set(keyToken, i.Token.String())
	return (&url.URL{Scheme: scheme, Host: host, RawQuery: q.Encode()}).String()
}

// ErrNotAnInvite is text that does not read as a pairing URI.
var ErrNotAnInvite = errors.New("not a kith://pair URI")

// ParseURI reads a URI as Invite.URI writes it. Anything else is refused: another
// scheme or path, an unknown or repeated key, a missing one, or a value that does not
// read.
func ParseURI(s string) (Invite, error) {
	bad := func(why string, args ...any) (Invite, error) {
		return Invite{}, fmt.Errorf("%w: %s", ErrNotAnInvite, fmt.Sprintf(why, args...))
	}
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return bad("%v", err)
	}
	if u.Scheme != scheme || u.Host != host || u.Opaque != "" || u.User != nil || u.Path != "" || u.Fragment != "" {
		return bad("it must begin kith://pair? and carry nothing else")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return bad("%v", err)
	}
	for k, vs := range q {
		switch {
		case k != keyAddr && k != keyExp && k != keyID && k != keyToken:
			return bad("unknown key %q", k)
		case k != keyAddr && len(vs) != 1:
			return bad("%s given %d times", k, len(vs))
		}
	}
	var inv Invite
	if inv.Addresses = q[keyAddr]; len(inv.Addresses) == 0 {
		return bad("no address")
	}
	for _, a := range inv.Addresses {
		if aerr := CheckAddress(a); aerr != nil {
			return bad("%v", aerr)
		}
	}
	if !q.Has(keyID) || !q.Has(keyToken) || !q.Has(keyExp) {
		return bad("id, token and exp are each needed")
	}
	if inv.ID, err = devicekey.ParseID(q.Get(keyID)); err != nil {
		return bad("%v", err)
	}
	if inv.Token, err = ParseToken(q.Get(keyToken)); err != nil {
		return bad("%v", err)
	}
	exp, err := strconv.ParseInt(q.Get(keyExp), 10, 64)
	if err != nil || exp <= 0 {
		return bad("exp %q is no time", q.Get(keyExp))
	}
	inv.Expires = time.Unix(exp, 0)
	return inv, nil
}
