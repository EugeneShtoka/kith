package setup

import "testing"

// A bridge's contacts are read only from an http(s) URL with a host.
func TestABridgeContactsEntryIsAURL(t *testing.T) {
	t.Parallel()
	for base, ok := range map[string]bool{
		"https://matrix.example.org/_matrix/provision/whatsapp": true,
		"http://localhost:29318/_matrix/provision":              true,
		"matrix.example.org/_matrix/provision":                  false,
		"ftp://matrix.example.org/x":                            false,
		"https:///nohost":                                       false,
	} {
		if err := BridgeContacts([]string{base}); (err == nil) != ok {
			t.Errorf("BridgeContacts(%q) = %v, want ok %v", base, err, ok)
		}
	}
}
