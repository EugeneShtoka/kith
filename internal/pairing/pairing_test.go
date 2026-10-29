package pairing_test

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/pairing"
)

func id(b byte) devicekey.ID { return devicekey.ID(sha256.Sum256([]byte{b})) }

func invite() pairing.Invite {
	return pairing.Invite{
		Addresses: []string{"bridge.lan:7443", "192.168.1.5:7443", "[fd00::5]:7443"}, // not sorted: order is kept
		ID:        id(1),
		Token:     pairing.NewToken(),
		Expires:   time.Unix(1_800_000_000, 0),
	}
}

func TestAnInviteSurvivesItsURI(t *testing.T) {
	t.Parallel()
	in := invite()
	uri := in.URI()
	if !strings.HasPrefix(uri, "kith://pair?") {
		t.Fatalf("URI() = %q, want kith://pair?…", uri)
	}
	// URL-safe: nothing a QR reader or a shell would mangle.
	if strings.ContainsAny(uri, " []\"'#") {
		t.Errorf("URI() = %q holds a character that needs quoting", uri)
	}
	out, err := pairing.ParseURI(uri)
	if err != nil {
		t.Fatalf("ParseURI(%q): %v", uri, err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("ParseURI(URI()) = %+v, want %+v", out, in)
	}
	// Pasted with the shell's quotes stripped but spaces left, it still reads.
	if _, err := pairing.ParseURI("  " + uri + "\n"); err != nil {
		t.Fatalf("a URI with spaces around it: %v", err)
	}
}

func TestAMalformedURIIsRefused(t *testing.T) {
	t.Parallel()
	in := invite()
	good := in.URI()
	q := good[len("kith://pair?"):]
	tok, ident := "token="+in.Token.String(), "id="+in.ID.String()
	for _, tc := range []struct{ name, uri string }{
		{"empty", ""},
		{"another scheme", "https://pair?" + q},
		{"another host", "kith://pear?" + q},
		{"a path", "kith://pair/x?" + q},
		{"a fragment", good + "#x"},
		{"a user", "kith://me@pair?" + q},
		{"no query", "kith://pair"},
		{"an unknown key", good + "&extra=1"},
		{"no address", strings.NewReplacer("addr=192.168.1.5%3A7443&", "", "addr=%5Bfd00%3A%3A5%5D%3A7443&", "", "addr=bridge.lan%3A7443&", "").Replace(good)},
		{"an address with no port", strings.Replace(good, "192.168.1.5%3A7443", "192.168.1.5", 1)},
		{"an address with no host", strings.Replace(good, "192.168.1.5%3A7443", "%3A7443", 1)},
		{"port 0", strings.Replace(good, "192.168.1.5%3A7443", "192.168.1.5%3A0", 1)},
		{"port 65536", strings.Replace(good, "192.168.1.5%3A7443", "192.168.1.5%3A65536", 1)},
		{"no id", strings.Replace(good, "&"+ident, "", 1)},
		{"two ids", good + "&" + ident},
		{"an id that does not read", strings.Replace(good, ident, ident[:len(ident)-1]+"1", 1)},
		{"no token", strings.Replace(good, "&"+tok, "", 1)},
		{"two tokens", good + "&" + tok},
		{"a short token", strings.Replace(good, tok, tok[:len(tok)-2], 1)},
		{"a token not in base32", strings.Replace(good, tok, tok[:len(tok)-1]+"1", 1)},
		{"no expiry", strings.Replace(good, "exp=1800000000&", "", 1)},
		{"an expiry that is no number", strings.Replace(good, "exp=1800000000", "exp=soon", 1)},
		{"a negative expiry", strings.Replace(good, "exp=1800000000", "exp=-5", 1)},
		{"bad escaping", good + "&addr=%zz"},
	} {
		if tc.uri == good {
			t.Fatalf("%s: the case changed nothing", tc.name)
		}
		if _, err := pairing.ParseURI(tc.uri); !errors.Is(err, pairing.ErrNotAnInvite) {
			t.Errorf("%s: ParseURI(%q) = %v, want ErrNotAnInvite", tc.name, tc.uri, err)
		}
	}
}

func TestATokenReadsOnlyAsWritten(t *testing.T) {
	t.Parallel()
	tok := pairing.NewToken()
	if tok == pairing.NewToken() {
		t.Fatal("two new tokens are equal")
	}
	for _, s := range []string{tok.String(), strings.ToLower(tok.String()), " " + tok.String() + " "} {
		if got, err := pairing.ParseToken(s); err != nil || got != tok {
			t.Errorf("ParseToken(%q) = %v, %v; want %v", s, got, err, tok)
		}
	}
	for _, s := range []string{"", "AAAA", tok.String() + "A", tok.String()[:25] + "1"} {
		if _, err := pairing.ParseToken(s); !errors.Is(err, pairing.ErrNotAToken) {
			t.Errorf("ParseToken(%q) = %v, want ErrNotAToken", s, err)
		}
	}
}

// The digits are six decimal figures that change with each of their three inputs, and
// depend on which side is the bridge.
func TestTheDigitsBindBothKeysAndTheToken(t *testing.T) {
	t.Parallel()
	tok := pairing.NewToken()
	base := pairing.Digits(id(1), id(2), tok)
	if len(base) != 6 || strings.Trim(base, "0123456789") != "" {
		t.Fatalf("Digits = %q, want six decimal digits", base)
	}
	if again := pairing.Digits(id(1), id(2), tok); again != base {
		t.Fatalf("Digits changed between calls: %q, %q", base, again)
	}
	for name, other := range map[string]string{
		"another bridge": pairing.Digits(id(3), id(2), tok),
		"another device": pairing.Digits(id(1), id(3), tok),
		"another token":  pairing.Digits(id(1), id(2), pairing.NewToken()),
		"sides swapped":  pairing.Digits(id(2), id(1), tok),
	} {
		if other == base {
			t.Errorf("%s gives the same digits %s", name, base)
		}
	}
	// Leading zeros are kept: every result is six wide.
	for b := range byte(200) {
		if d := pairing.Digits(id(b), id(b+1), pairing.Token{b}); len(d) != 6 {
			t.Fatalf("Digits = %q, want six wide", d)
		}
	}
}

func TestCheckAddress(t *testing.T) {
	t.Parallel()
	for addr, ok := range map[string]bool{
		"192.168.1.5:7443": true, "[fd00::5]:7443": true, "bridge.lan:1": true, "h:65535": true,
		"192.168.1.5": false, ":7443": false, "h:0": false, "h:65536": false, "h:port": false, "": false,
	} {
		if err := pairing.CheckAddress(addr); (err == nil) != ok {
			t.Errorf("CheckAddress(%q) = %v, want ok %v", addr, err, ok)
		}
	}
}
