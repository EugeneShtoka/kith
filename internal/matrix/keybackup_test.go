package matrix

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

// The SSSS backup secret decodes to a 32-byte key whether or not the base64 is padded.
func TestDecodeBackupSecret(t *testing.T) {
	t.Parallel()

	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	raw := key.Bytes()

	tests := map[string]string{
		"unpadded":            base64.RawStdEncoding.EncodeToString(raw),
		"padded":              base64.StdEncoding.EncodeToString(raw),
		"trailing whitespace": base64.StdEncoding.EncodeToString(raw) + "\n",
	}
	for name, secret := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeBackupSecret(secret)
			if err != nil {
				t.Fatalf("decodeBackupSecret(%q): %v", secret, err)
			}
			if !bytes.Equal(got, raw) {
				t.Errorf("decoded = %x, want %x", got, raw)
			}
		})
	}
}

func TestDecodeBackupSecretRejectsGarbage(t *testing.T) {
	t.Parallel()
	if _, err := decodeBackupSecret("not valid base64 !!!"); err == nil {
		t.Error("expected an error decoding garbage, got nil")
	}
}
