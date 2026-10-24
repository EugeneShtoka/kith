package main

import "testing"

// --open hands over what the client can follow, and refuses the rest before starting a
// terminal for it.
func TestOpenAcceptsWhatTheClientFollows(t *testing.T) {
	t.Parallel()
	for uri, want := range map[string]bool{
		"matrix:r/kith:example.org":             true,
		"tg://resolve?domain=novosalsa":         true,
		"whatsapp://send?phone=15550100009":     true,
		"https://wa.me/15550100009?text=hi":     true,
		"https://chat.whatsapp.com/AbCdEf12345": true,
		"https://example.org/":                  false,
		"whatsapp://send?text=only-text":        false,
	} {
		if got := openableURI(uri); got != want {
			t.Errorf("openableURI(%q) = %v, want %v", uri, got, want)
		}
	}
}
