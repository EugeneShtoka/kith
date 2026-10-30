package devicekey

import (
	"crypto/elliptic"
	"errors"
	"math/big"
	"strings"
	"testing"
)

// The check is OpenPGP's CRC-24: RFC 4880's own value for "123456789".
func TestTheCheckIsCRC24(t *testing.T) {
	t.Parallel()
	if got := check([]byte("123456789")); string(got) != "\x21\xcf\x02" {
		t.Fatalf("check(123456789) = %x, want 21cf02", got)
	}
}

// A recovery key read back is the key it was written from, however it was copied out:
// in either case, with or without its dashes, over several lines.
func TestARecoveryKeyReadsBackHoweverItIsWritten(t *testing.T) {
	t.Parallel()
	for range 20 {
		key, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		written, err := RecoveryKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if groups := strings.Split(written, "-"); len(groups) != 14 || len(groups[13]) != 4 {
			t.Fatalf("RecoveryKey = %q, want 14 groups of 4", written)
		}
		groups := strings.Split(written, "-")
		for _, copied := range []string{
			written,
			strings.ToLower(written),
			strings.ReplaceAll(written, "-", ""),
			strings.Join(groups, " "),
			" " + strings.Join(groups[:7], "-") + "\n\t" + strings.Join(groups[7:], " ") + "\r\n",
			strings.ToLower(groups[0]) + strings.Join(groups[1:], "  "),
		} {
			back, err := ParseRecoveryKey(copied)
			if err != nil || !back.Equal(key) {
				t.Fatalf("ParseRecoveryKey(%q) = %v; want the key it was written from", copied, err)
			}
		}
	}
}

// Every one character mistyped, and every two neighbors swapped, is refused as a
// typo: never read as another key, which would be another device.
func TestATypoInARecoveryKeyIsCaught(t *testing.T) {
	t.Parallel()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
	for range 3 {
		key, err := Generate()
		if err != nil {
			t.Fatal(err)
		}
		written, err := RecoveryKey(key)
		if err != nil {
			t.Fatal(err)
		}
		plain := []byte(strings.ReplaceAll(written, "-", ""))
		refused := func(what string, typed []byte) {
			t.Helper()
			if got, perr := ParseRecoveryKey(string(typed)); !errors.Is(perr, ErrNotARecoveryKey) {
				t.Fatalf("%s: ParseRecoveryKey(%s) = %v, %v; want ErrNotARecoveryKey", what, typed, got != nil, perr)
			}
		}
		for i := range plain {
			for _, c := range []byte(alphabet + "0189") {
				if c == plain[i] {
					continue
				}
				typed := append([]byte(nil), plain...)
				typed[i] = c
				refused("one character mistyped", typed)
			}
			if i+1 < len(plain) && plain[i] != plain[i+1] {
				typed := append([]byte(nil), plain...)
				typed[i], typed[i+1] = typed[i+1], typed[i]
				refused("two neighbors swapped", typed)
			}
		}
		refused("one character short", plain[:len(plain)-1])
		refused("one character long", append(append([]byte(nil), plain...), 'A'))
		refused("nothing", nil)
		id, err := IDOf(key.Public())
		if err != nil {
			t.Fatal(err)
		}
		refused("its device ID", []byte(id.String()))
	}
}

// A number that checks out but is no P-256 private key (zero, the curve's order or
// past it) is refused, not taken as some key.
func TestARecoveryKeyHoldingNoKeyIsRefused(t *testing.T) {
	t.Parallel()
	n := elliptic.P256().Params().N
	for name, v := range map[string]*big.Int{
		"zero":        new(big.Int),
		"the order":   n,
		"past it":     new(big.Int).Add(n, big.NewInt(1)),
		"the largest": new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)),
	} {
		scalar := v.FillBytes(make([]byte, scalarLen))
		typed := grouped(encoding.EncodeToString(append(scalar, check(scalar)...)))
		if got, err := ParseRecoveryKey(typed); !errors.Is(err, ErrNotARecoveryKey) {
			t.Errorf("%s: ParseRecoveryKey = %v, %v; want ErrNotARecoveryKey", name, got != nil, err)
		}
	}
}
