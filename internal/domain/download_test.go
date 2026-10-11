package domain_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/domain"
)

func place() domain.DownloadPlace {
	return domain.DownloadPlace{
		RoomID: "!standup:x",
		Space:  "Work",
		Room:   "Standup",
		Person: "Alice",
		Name:   "photo.png",
		Sent:   time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC),
	}
}

// With no template a file lands in the directory under its own name.
func TestResolveDownloadWithoutATemplate(t *testing.T) {
	t.Parallel()

	got := domain.ResolveDownload("/dl", "", nil, place())
	if got.Path() != "/dl/photo.png" {
		t.Errorf("path = %q, want it straight in the directory", got.Path())
	}
}

// The template expands to readable folder names.
func TestResolveDownloadTemplate(t *testing.T) {
	t.Parallel()

	got := domain.ResolveDownload("/dl", "{space}/{room}/{person}/{name}-{date}{ext}", nil, place())
	want := "/dl/Work/Standup/Alice/photo-2026-08-24.png"
	if got.Path() != want {
		t.Errorf("path = %q, want %q", got.Path(), want)
	}
	if got.Dir != "/dl/Work/Standup/Alice" {
		t.Errorf("dir = %q — that is what has to be created", got.Dir)
	}
}

// A field that expands to nothing leaves no empty folder ("/dl//Standup").
func TestResolveDownloadDropsEmptySegments(t *testing.T) {
	t.Parallel()

	spaceless := place()
	spaceless.Space = ""
	got := domain.ResolveDownload("/dl", "{space}/{room}/{name}{ext}", nil, spaceless)
	if got.Path() != "/dl/Standup/photo.png" {
		t.Errorf("path = %q, want the empty space folder skipped", got.Path())
	}
}

// Path parts come from others' messages: "Design / Ops" is one folder, and a
// filename cannot climb out.
func TestResolveDownloadCannotEscape(t *testing.T) {
	t.Parallel()

	nasty := place()
	nasty.Room = "Design / Ops"
	nasty.Person = "../../root"
	nasty.Name = "../../.ssh/authorized_keys"
	got := domain.ResolveDownload("/dl", "{room}/{person}/{name}{ext}", nil, nasty)

	if filepath.Clean(got.Path()) != got.Path() {
		t.Errorf("path %q is not already clean", got.Path())
	}
	if strings.Contains(got.Dir, "..") {
		t.Errorf("dir %q still contains \"..\" — a name off the wire must not traverse", got.Dir)
	}
	if got.Dir != "/dl/Design - Ops/root" {
		t.Errorf("dir = %q, want each field flattened to one safe segment", got.Dir)
	}
	if got.Name != "authorized_keys" {
		t.Errorf("name = %q, want only the last element", got.Name)
	}
}

// A room rule beats a space rule, which beats the settings.
func TestResolveDownloadRulesNarrowestWins(t *testing.T) {
	t.Parallel()

	rules := []domain.DownloadRule{
		{Match: "Work", Dir: "/work"},
		{Match: "!standup:x", Dir: "/standup", Template: "{name}{ext}"},
	}
	got := domain.ResolveDownload("/dl", "{space}/{name}{ext}", rules, place())
	if got.Path() != "/standup/photo.png" {
		t.Errorf("path = %q, want the room's own rule to win", got.Path())
	}

	// A rule changes only what it sets; the outer template still applies.
	got = domain.ResolveDownload("/dl", "{space}/{name}{ext}", rules[:1], place())
	if got.Path() != "/work/Work/photo.png" {
		t.Errorf("path = %q, want the space's directory with the outer template", got.Path())
	}
}

// "-" turns the template off for one place; empty means inherit.
func TestResolveDownloadRuleCanTurnOffTheTemplate(t *testing.T) {
	t.Parallel()

	rules := []domain.DownloadRule{{Match: "!standup:x", Template: domain.NoTemplate}}
	got := domain.ResolveDownload("/dl", "{space}/{room}/{name}{ext}", rules, place())
	if got.Path() != "/dl/photo.png" {
		t.Errorf("path = %q, want the template off for this room", got.Path())
	}
}
