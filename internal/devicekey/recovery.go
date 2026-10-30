package devicekey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"errors"
	"fmt"
	"strings"
)

// scalarLen is a P-256 private key's length, and checkLen its check's: 35 bytes are
// 56 base32 characters exactly.
const (
	scalarLen = 32
	checkLen  = 3
)

// ErrNotARecoveryKey is text that does not read as a recovery key.
var ErrNotARecoveryKey = errors.New("not a recovery key")

// RecoveryKey writes key to be written down: its private scalar and a check, in
// base32 groups of four joined by dashes. ParseRecoveryKey reads it back as the same
// key, so the same device ID.
func RecoveryKey(key *ecdsa.PrivateKey) (string, error) {
	scalar, err := key.Bytes()
	if err != nil || key.Curve != elliptic.P256() {
		return "", errors.New("devicekey: not an ECDSA P-256 key")
	}
	return grouped(encoding.EncodeToString(append(scalar, check(scalar)...))), nil
}

// ParseRecoveryKey reads a key as RecoveryKey writes it, in either case, with or
// without its dashes and with any spaces or line breaks. A check that does not hold is
// a typo, never another key; one that holds over a number that is no P-256 key is
// refused too.
func ParseRecoveryKey(s string) (*ecdsa.PrivateKey, error) {
	plain := strings.ToUpper(strings.Join(strings.Fields(strings.ReplaceAll(s, "-", " ")), ""))
	raw, err := encoding.DecodeString(plain)
	// Spelled back as typed, or a 57th character would be quietly dropped.
	if err != nil || len(raw) != scalarLen+checkLen || encoding.EncodeToString(raw) != plain {
		return nil, fmt.Errorf("%w (56 letters A–Z and digits 2–7, in groups of four)", ErrNotARecoveryKey)
	}
	scalar := raw[:scalarLen]
	if string(check(scalar)) != string(raw[scalarLen:]) {
		return nil, fmt.Errorf("%w: it does not check out, so a character is mistyped", ErrNotARecoveryKey)
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar)
	if err != nil {
		return nil, fmt.Errorf("%w: it holds no P-256 key", ErrNotARecoveryKey)
	}
	return key, nil
}

// check is OpenPGP's CRC-24 of scalar (RFC 4880, 6.1), big-endian. A CRC changes
// under any error confined to as many consecutive bits as it has, so one character
// mistyped (5 bits) or two neighbours swapped (10) is always caught, as a hash's
// truncation would only almost always.
func check(scalar []byte) []byte {
	crc := uint32(0xB704CE)
	for _, b := range scalar {
		crc ^= uint32(b) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= 0x1864CFB
			}
		}
	}
	return []byte{byte(crc >> 16), byte(crc >> 8), byte(crc)}
}
