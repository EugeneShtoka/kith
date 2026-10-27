package daemon_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// danaCard is a card (invented) whose role is fields, for Telegram user 42.
func danaCard(role string) string {
	return "---\nrole: " + role + "\ntelegram: [\"42\"]\n---\nnotes\n"
}

// dana is every ID Dana goes by, as the cache asks a card test.
var dana = []string{"telegram:42"}

// watchedPeople serves ProfilePeople over the folder dir for one profile excluding
// role:colleagues, and returns each application in turn.
func watchedPeople(t *testing.T, dir string) (*daemon.ProfilePeople, <-chan map[string]domain.ExcludedPeople) {
	t.Helper()
	applied := make(chan map[string]domain.ExcludedPeople, 16)
	people := daemon.NewProfilePeople(func(_ context.Context, ex map[string]domain.ExcludedPeople) error {
		applied <- ex
		return nil
	}, slog.New(slog.DiscardHandler))
	t.Cleanup(people.Stop)
	profiles := []domain.Profile{{Name: "home", People: domain.PeopleFilter{Fields: []string{"role:colleagues"}}}}
	if err := people.Use(t.Context(), profiles, dir); err != nil {
		t.Fatal(err)
	}
	return people, applied
}

// hides waits for the next application whose answer for Dana is want.
func hides(t *testing.T, applied <-chan map[string]domain.ExcludedPeople, want bool, step string) {
	t.Helper()
	deadline := time.After(settle)
	for {
		select {
		case ex := <-applied:
			card := ex["home"].Card
			if card != nil && card(dana) == want {
				return
			}
		case <-deadline:
			t.Fatalf("%s: Dana never became hidden = %v", step, want)
		}
	}
}

// A card edited by another program hides or shows its person within a moment: edited
// to meet the profile's field, edited away from it, removed. A phonebook folder made
// only later is followed once it exists.
func TestAProfileFollowsItsCards(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "notes", "phonebook") // not made yet
	_, applied := watchedPeople(t, dir)
	hides(t, applied, false, "no folder")

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	card := filepath.Join(dir, "Dana Levi.md")
	if err := os.WriteFile(card, []byte(danaCard("former colleagues")), 0o600); err != nil {
		t.Fatal(err)
	}
	hides(t, applied, true, "a card made in a folder made later")

	if err := os.WriteFile(card, []byte(danaCard("friends")), 0o600); err != nil {
		t.Fatal(err)
	}
	hides(t, applied, false, "the card edited away from the field")

	if err := os.WriteFile(card, []byte(danaCard("Colleagues")), 0o600); err != nil {
		t.Fatal(err)
	}
	hides(t, applied, true, "the card edited back")

	if err := os.Remove(card); err != nil {
		t.Fatal(err)
	}
	hides(t, applied, false, "the card removed")

	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(card, []byte(danaCard("colleagues")), 0o600); err != nil {
		t.Fatal(err)
	}
	hides(t, applied, true, "the folder removed and made again, with the card")
}

// A folder that cannot be read keeps the cards read before: a passing error never shows
// the people they hide.
func TestAnUnreadablePhonebookKeepsItsCards(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a folder without permission")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dana Levi.md"), []byte(danaCard("colleagues")), 0o600); err != nil {
		t.Fatal(err)
	}
	people, applied := watchedPeople(t, dir)
	hides(t, applied, true, "a card meeting the field")

	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	profiles := []domain.Profile{{Name: "home", People: domain.PeopleFilter{Fields: []string{"role:colleagues"}}}}
	if err := people.Use(t.Context(), profiles, dir); err != nil {
		t.Fatal(err)
	}
	hides(t, applied, true, "the folder unreadable at a reload")
}
