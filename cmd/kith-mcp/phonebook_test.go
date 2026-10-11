package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// The phonebook is handed to the server only when [agent.read] people says so.
func TestThePhonebookIsReadOnlyWhereAllowed(t *testing.T) {
	t.Parallel()
	var cfg config.Config
	if phonebookFor(cfg) != nil {
		t.Error("a phonebook without [agent.read] people")
	}
	cfg.Agent.Read.People = true
	cfg.Phonebook.Dir = t.TempDir()
	if book := phonebookFor(cfg); book == nil || book.Dir() != cfg.Phonebook.Dir {
		t.Errorf("with people on: %v", book)
	}
}

// Where [agent.read] people allows it, a person find_people names carries their card's
// fields, a list as a list; without it, none.
func TestFindPeopleCarriesTheCardWhereAllowed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	card := "---\nmatrix: [\"@dana:x\"]\nbirthday: 1990-05-04\npets: [Rex, Mia]\n---\nnotes stay home\n"
	if err := os.WriteFile(filepath.Join(dir, "Dana.md"), []byte(card), 0o600); err != nil {
		t.Fatal(err)
	}
	f := twoRooms()
	f.senders = []domain.Member{{UserID: "@dana:x", DisplayName: "Dana"}}
	s := newServer(f, shareAll)
	s.book = phonebook.New(dir)
	out := call(t, s, "find_people", map[string]any{"query": "Dana"})
	person := out["people"].([]any)[0].(map[string]any)
	fields, _ := person["fields"].(map[string]any)
	pets, _ := fields["pets"].([]any)
	if fields["birthday"] != "1990-05-04" || len(pets) != 2 || pets[1] != "Mia" {
		t.Errorf("fields = %v", person["fields"])
	}
	if _, leaked := fields["notes"]; leaked {
		t.Error("the card's body reached the assistant")
	}

	s.book = nil
	out = call(t, s, "find_people", map[string]any{"query": "Dana"})
	if _, has := out["people"].([]any)[0].(map[string]any)["fields"]; has {
		t.Error("fields went out without [agent.read] people")
	}
}
