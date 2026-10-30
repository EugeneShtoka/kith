// Package devicekey is a device's identity: an ECDSA P-256 key, the ID that names it
// (the SHA-256 of its public key's SubjectPublicKeyInfo), and the self-signed
// certificate TLS carries it in. The key is what a peer pins, never the certificate,
// so a certificate may be made afresh at every start. Pure: keeping the key is
// internal/session's.
package devicekey

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base32"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// ID names a device: the SHA-256 of its public key's SubjectPublicKeyInfo.
type ID [sha256.Size]byte

// encoding writes an ID as base32 without padding: 52 characters, upper case.
var encoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// groupLen is how many characters each dash-separated group of a written ID holds.
const groupLen = 4

// String writes the ID in groups of four, joined by dashes.
func (id ID) String() string { return grouped(encoding.EncodeToString(id[:])) }

// grouped writes plain in groups of four, joined by dashes.
func grouped(plain string) string {
	var b strings.Builder
	for i := 0; i < len(plain); i += groupLen {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(plain[i:min(i+groupLen, len(plain))])
	}
	return b.String()
}

// ErrNotAnID is text that does not read as a device ID.
var ErrNotAnID = errors.New("not a device ID")

// ParseID reads an ID as String writes it, in either case, with or without the dashes
// and with any spaces. Only the one spelling of each ID is taken, so a typo in its
// last character is not quietly another ID's spelling.
func ParseID(s string) (ID, error) {
	plain := strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(s)))
	raw, err := encoding.DecodeString(plain)
	if err != nil || len(raw) != len(ID{}) || encoding.EncodeToString(raw) != plain {
		return ID{}, fmt.Errorf("%q: %w (52 letters A–Z and digits 2–7, in groups)", s, ErrNotAnID)
	}
	return ID(raw), nil
}

// IDOf is the ID of a public key.
func IDOf(pub crypto.PublicKey) (ID, error) {
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ID{}, fmt.Errorf("devicekey: public key: %w", err)
	}
	return sha256.Sum256(spki), nil
}

// PeerID is the ID of the key the peer's certificate carries.
func PeerID(state tls.ConnectionState) (ID, error) {
	if len(state.PeerCertificates) == 0 {
		return ID{}, errors.New("devicekey: the peer sent no certificate")
	}
	return IDOf(state.PeerCertificates[0].PublicKey)
}

// Generate makes a new device key.
func Generate() (*ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("devicekey: generate: %w", err)
	}
	return key, nil
}

const pemType = "PRIVATE KEY"

// Marshal writes a key as PEM (PKCS #8), for keeping.
func Marshal(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("devicekey: encode: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der}), nil
}

// Parse reads a key Marshal wrote; anything else (another curve, another kind of key)
// is an error.
func Parse(text []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(text)
	if block == nil || block.Type != pemType {
		return nil, errors.New("devicekey: not a PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("devicekey: decode: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, errors.New("devicekey: not an ECDSA P-256 key")
	}
	return key, nil
}

// certLifetime is long: what a peer pins is the key, so the certificate's dates
// guard nothing, and one expiring would only break a connection.
const certLifetime = 50 * 365 * 24 * time.Hour

// Certificate is a self-signed certificate for key, for either end of a TLS
// connection.
func Certificate(key *ecdsa.PrivateKey) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devicekey: serial: %w", err)
	}
	id, err := IDOf(key.Public())
	if err != nil {
		return tls.Certificate{}, err
	}
	// Backdated an hour, so a peer whose clock is a little behind takes it.
	notBefore := time.Now().Add(-time.Hour)
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "kith " + id.String()},
		NotBefore:    notBefore,
		NotAfter:     notBefore.Add(certLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devicekey: certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("devicekey: certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}
