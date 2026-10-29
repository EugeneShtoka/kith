package keystore

import (
	"crypto/ecdsa"
	"crypto/tls"
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"

	"github.com/EugeneShtoka/kith/internal/devicekey"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// DeviceStore is where the instance keeps the device key named name: "device" is
// kithd's, "client-device" this machine's as a client of a bridge.
func DeviceStore(storage domain.Storage, name string) Store {
	return Store{Service: storage.KeyringService, Account: storage.Instance + "|" + name, File: storage.DeviceKeyFile(name)}
}

// ClientCertificate is this machine's device key as a client of a bridge ("client-device"),
// made on first use as LoadOrCreateDeviceKey makes one, as the certificate it presents.
func ClientCertificate(storage domain.Storage, allowFile bool) (tls.Certificate, error) {
	key, err := LoadOrCreateDeviceKey(DeviceStore(storage, "client-device"), allowFile)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("this machine's device key: %w", err)
	}
	cert, err := devicekey.Certificate(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("this machine's device key: %w", err)
	}
	return cert, nil
}

// ErrCorruptDeviceKey is a kept device key that does not read as one. It is never
// replaced: a new key is a new device, which every peer would have to pair again.
var ErrCorruptDeviceKey = errors.New("keystore: the device key is corrupt")

// LoadOrCreateDeviceKey returns the device key st keeps, making and keeping one on
// first use: in the keyring, or with allowFile in st.File when there is no keyring. A
// file kept while there was none moves into the keyring once it answers, so the
// device keeps its ID. A keyring that does not answer is ErrKeyringNotAnswering, and
// no key is made for it.
func LoadOrCreateDeviceKey(st Store, allowFile bool) (*ecdsa.PrivateKey, error) {
	existing, err := Get(st.Service, st.Account)
	switch {
	case err == nil:
		return parseDeviceKey([]byte(existing), "keyring account "+st.Account)
	case errors.Is(err, ErrKeyringNotAnswering):
		return nil, err
	case errors.Is(err, keyring.ErrNotFound):
		return createDeviceKey(st, allowFile)
	case !allowFile:
		return nil, ErrNoSecretStore
	}
	// No keyring: the file, made on first use.
	blob, found, err := ReadOwnerOnly(st.File, "a device key")
	if err != nil {
		return nil, err
	}
	if found {
		return parseDeviceKey(blob, st.File)
	}
	key, text, err := newDeviceKey()
	if err != nil {
		return nil, err
	}
	if err := SaveFile(st.File, text); err != nil {
		return nil, err
	}
	return key, nil
}

// createDeviceKey keeps a device key in a keyring that has none: the fallback file's,
// when allowFile kept one, else a new one.
func createDeviceKey(st Store, allowFile bool) (*ecdsa.PrivateKey, error) {
	var text []byte
	if allowFile {
		blob, found, err := ReadOwnerOnly(st.File, "a device key")
		if err != nil {
			return nil, err
		}
		if found {
			if _, err := parseDeviceKey(blob, st.File); err != nil {
				return nil, err
			}
			text = blob
		}
	}
	if text == nil {
		var err error
		if _, text, err = newDeviceKey(); err != nil {
			return nil, err
		}
	}
	kept, err := Set(st.Service, st.Account, string(text))
	switch {
	case errors.Is(err, ErrKeyringNotAnswering):
		return nil, err
	case err != nil:
		return nil, ErrNoSecretStore
	}
	if allowFile {
		if err := RemoveFile(st.File); err != nil {
			return nil, err
		}
	}
	return parseDeviceKey([]byte(kept), "keyring account "+st.Account)
}

func newDeviceKey() (*ecdsa.PrivateKey, []byte, error) {
	key, err := devicekey.Generate()
	if err != nil {
		return nil, nil, fmt.Errorf("keystore: %w", err)
	}
	text, err := devicekey.Marshal(key)
	if err != nil {
		return nil, nil, fmt.Errorf("keystore: %w", err)
	}
	return key, text, nil
}

func parseDeviceKey(text []byte, where string) (*ecdsa.PrivateKey, error) {
	key, err := devicekey.Parse(text)
	if err != nil {
		return nil, fmt.Errorf("%w (%s): restore it, or delete it to make a new one and pair every device again: %w",
			ErrCorruptDeviceKey, where, err)
	}
	return key, nil
}
