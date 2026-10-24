package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/phonebook"
)

// The server gets the phonebook only when [agent.read] people says so.
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

// Where allowed, find_people carries each person's card fields, lists as lists.
func TestFindPeopleCarriesTheCardWhereAllowed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	card := "---\nmatrix: [\"@dana:x\"]\nbirthday: 1990-05-04\npets: [Rex, Mia]\n---\nnotes stay home\n"
	if err := os.WriteFile(filepath.Join(dir, "Dana.md"), []byte(card), 0o600); err != nil {
		t.Fatal(err)
	}
	f := twoRooms()
	f.senders = []domain.Member{{UserID: "matrix:@dana:x", DisplayName: "Dana"}}
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

// key:value terms find the people whose card meets them all, beside a name; without
// [agent.read] people they find nobody and say why. An ID is never a field term.
func TestFindPeopleByCardFields(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, card := range map[string]string{
		"Dana.md": "---\nmatrix: [\"@dana:x\"]\nrole: Family Attorney\ncompany: [Acme, Meta]\n---\n",
		"Eli.md":  "---\nmatrix: [\"@eli:x\"]\nrole: architect\n---\n",
		"Zed.md":  "---\nmatrix: [\"@zed:x\"]\nrole: attorney\n---\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(card), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := twoRooms()
	f.senders = []domain.Member{{UserID: "matrix:@dana:x", DisplayName: "Dana"}, {UserID: "matrix:@eli:x", DisplayName: "Eli"}, {UserID: "matrix:@noa:x", DisplayName: "Noa"}, {UserID: "matrix:@zed:x", DisplayName: "Zed"}}
	// Zed's card meets role:attorney, but he posted only in the encrypted room, out of scope.
	f.postedIn = map[string][]domain.RoomID{"matrix:@dana:x": {mx + "!open:x"}, "matrix:@eli:x": {mx + "!open:x"}, "matrix:@noa:x": {mx + "!open:x"}, "matrix:@zed:x": {mx + "!secret:x"}}
	s := newServer(f, shareAll)
	s.book = phonebook.New(dir)
	found := func(query string) string {
		t.Helper()
		var ids []string
		for _, p := range call(t, s, "find_people", map[string]any{"query": query})["people"].([]any) {
			ids = append(ids, p.(map[string]any)["user_id"].(string))
		}
		return strings.Join(ids, " ")
	}
	for query, want := range map[string]string{
		"ROLE:attorney":            "matrix:@dana:x",
		"role:a":                   "matrix:@dana:x matrix:@eli:x",
		"company:meta role:family": "matrix:@dana:x",
		"eli role:a":               "matrix:@eli:x",
		"matrix:@eli":              "matrix:@eli:x",
		"matrix:@dana:x":           "matrix:@dana:x",
	} {
		if got := found(query); got != want {
			t.Errorf("find_people(%q) = %q, want %q", query, got, want)
		}
	}
	if got := callErr(t, s, "find_people", map[string]any{"query": "company:acme role:arch"}); !strings.Contains(got, "nobody whose card meets") {
		t.Errorf("no card meets both: %q", got)
	}

	// A folder that cannot be read meets no term.
	s.book = phonebook.New(filepath.Join(dir, "Dana.md"))
	if got := callErr(t, s, "find_people", map[string]any{"query": "role:attorney"}); !strings.Contains(got, "nobody whose card meets") {
		t.Errorf("an unreadable folder: %q", got)
	}

	s.book = nil
	out := call(t, s, "find_people", map[string]any{"query": "role:attorney"})
	if people, _ := out["people"].([]any); len(people) != 0 || !strings.Contains(out["note"].(string), "not shared") {
		t.Errorf("without [agent.read] people: %v", out)
	}
	if got := found("Dana"); got != "matrix:@dana:x" {
		t.Errorf("a name without the phonebook: %v", got)
	}
}
