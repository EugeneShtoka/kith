package devicekey_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/devicekey"
)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := devicekey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestAnIDReadsBackHoweverItIsPasted(t *testing.T) {
	t.Parallel()
	id, err := devicekey.IDOf(newKey(t).Public())
	if err != nil {
		t.Fatal(err)
	}
	written := id.String()
	if groups := strings.Split(written, "-"); len(groups) != 13 || len(groups[0]) != 4 || len(groups[12]) != 4 {
		t.Fatalf("String() = %q, want 13 groups of 4", written)
	}
	for _, pasted := range []string{
		written,
		strings.ToLower(written),
		strings.ReplaceAll(written, "-", ""),
		" " + strings.ReplaceAll(written, "-", " ") + "\n",
	} {
		got, err := devicekey.ParseID(pasted)
		if err != nil || got != id {
			t.Errorf("ParseID(%q) = %v, %v; want %v", pasted, got, err, id)
		}
	}
}

func TestATypoIsNotAnotherID(t *testing.T) {
	t.Parallel()
	id, err := devicekey.IDOf(newKey(t).Public())
	if err != nil {
		t.Fatal(err)
	}
	written := id.String()
	// The last character carries one bit of the ID and four unused ones (it is A or
	// Q): setting an unused one spells no ID.
	other := written[len(written)-1] + 1
	for _, bad := range []string{
		"",
		written[:len(written)-1],
		written + "A",
		written[:len(written)-1] + string(other),
		strings.Replace(written, written[:1], "1", 1), // 1 is not base32
	} {
		if _, err := devicekey.ParseID(bad); !errors.Is(err, devicekey.ErrNotAnID) {
			t.Errorf("ParseID(%q) = %v, want ErrNotAnID", bad, err)
		}
	}
}

func TestAKeyKeptReadsBackAsItself(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	text, err := devicekey.Marshal(key)
	if err != nil {
		t.Fatal(err)
	}
	back, err := devicekey.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(key) {
		t.Fatal("Parse(Marshal(key)) is another key")
	}
}

func TestOnlyAP256KeyIsADeviceKey(t *testing.T) {
	t.Parallel()
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, key := range map[string]any{"P-384": p384, "ed25519": edKey} {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := devicekey.Parse(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err == nil {
			t.Errorf("Parse(%s) took it", name)
		}
	}
	if _, err := devicekey.Parse([]byte("not a key")); err == nil {
		t.Error("Parse(text) took it")
	}
}

func TestTheCertificateCarriesTheKeysID(t *testing.T) {
	t.Parallel()
	key := newKey(t)
	want, err := devicekey.IDOf(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := devicekey.PeerID(tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert.Leaf}})
	if err != nil || got != want {
		t.Fatalf("PeerID = %v, %v; want %v", got, err, want)
	}
	// A second certificate for the same key names the same device.
	again, err := devicekey.Certificate(key)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := devicekey.PeerID(tls.ConnectionState{PeerCertificates: []*x509.Certificate{again.Leaf}}); got != want {
		t.Fatal("a reissued certificate names another device")
	}
	if _, err := devicekey.PeerID(tls.ConnectionState{}); err == nil {
		t.Fatal("PeerID with no certificate took it")
	}
}
