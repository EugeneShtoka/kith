package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/domain"
)

// The assistant runs in the first profile, so it reads none of the people that
// profile's people.exclude hides: by ID, or by a card field reaching them through a
// linked ID (the card names Telegram 9, the directory links them to @spam), with the
// replies to them; a card naming you hides nothing.
func TestTheAssistantDoesNotReadTheFirstProfilesPeople(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, text := range map[string]string{
		"Spam.md": "---\nrole: colleagues\ntelegram: [\"9\"]\n---\n",
		"Me.md":   "---\nrole: colleagues\nmatrix: [\"@me:x\"]\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for what, filter := range map[string]domain.PeopleFilter{
		"by a card": {Fields: []string{"role:colleagues"}},
		"by an ID":  {IDs: []string{"telegram:9"}},
	} {
		s := newServer(hiddenRoom(), shareAllEncrypted)
		s.excludes, s.cardsDir = filter, dir
		if got, want := ids(t, call(t, s, "read_room", map[string]any{"room": "Standup"})), []string{mx + "$1", mx + "$4"}; !slices.Equal(got, want) {
			t.Errorf("%s: read_room gave %v, want %v", what, got, want)
		}
	}
}

// A phonebook the profile's fields need that cannot be read refuses the read rather
// than show the people a card may hide.
func TestAnUnreadablePhonebookRefusesTheRead(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder without permission")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	s := newServer(hiddenRoom(), shareAllEncrypted)
	s.excludes, s.cardsDir = domain.PeopleFilter{Fields: []string{"role:colleagues"}}, dir
	if got := callErr(t, s, "read_room", map[string]any{"room": "Standup"}); !strings.Contains(got, "hide") {
		t.Errorf("refused with %q, want it to say why", got)
	}
}
