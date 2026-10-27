package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// The daemon's handlers count unread for a profile through this (daemon.profileCounter).
var _ interface {
	ProfileUnread(ctx context.Context, unread []domain.Unread) ([]domain.Unread, error)
} = served{}

// A reload that gives a profile people.exclude, naming a phonebook field a card meets,
// recounts the rooms the person wrote in, and the profile counts them without the
// person, while the first profile still counts them; the profile's own archive (step 8)
// changes none of it for a room it keeps.
func TestAReloadThatExcludesSomeoneFromAProfileCountsThemOut(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	book := t.TempDir()
	card := "---\nrole: colleagues\nphone: [\"+1 555 000 0003\"]\n---\n"
	if err := os.WriteFile(filepath.Join(book, "Sam.md"), []byte(card), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Profiles: everyRoom, WhatsApp: config.WhatsApp{Accounts: []config.WhatsAppAccount{{Name: "home", Phone: "+1 555 000 0001"}}}}
	cfg.Phonebook.Dir = book
	backend := servedFor(t, cfg, domain.Session{})
	const room domain.RoomID = "whatsapp:15550000001/120363@g.us"
	start := time.Unix(1_700_000_000, 0)
	if err := backend.cache.SaveMessages(ctx, room, []domain.Message{
		{ID: "whatsapp:15550000001/A1", RoomID: room, Sender: "whatsapp:15550000002@s.whatsapp.net", Body: "hi", Timestamp: start.Add(time.Minute)},
		{ID: "whatsapp:15550000001/A2", RoomID: room, Sender: "whatsapp:15550000003@s.whatsapp.net", Body: "meeting", Timestamp: start.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.cache.SaveUnread(ctx, domain.Unread{RoomID: room}, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := newWorkers(slog.New(slog.DiscardHandler), cfg, backend); err != nil {
		t.Fatal(err)
	}
	stream := backend.Unread()
	// Home has an archive of its own too (its DMs), which leaves this group in it.
	cfg.Tags = []config.Tag{{Name: "Archived"}}
	cfg.Profiles = []config.Profile{
		{Name: "first", Include: []string{"*"}},
		{
			Name: "home", Include: []string{"*"}, Exclude: []string{"tag:Archived"},
			Archive: config.ProfileArchive{Rooms: []string{"dm"}},
			People:  config.ProfilePeople{Exclude: []string{"role:colleagues"}},
		},
	}
	if err := backend.useHidden(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var u domain.Unread
	select {
	case u = <-stream:
		if u.RoomID != room || !u.Counted || u.Messages != 2 {
			t.Fatalf("streamed %+v, want the room recounted at 2 for everyone", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("excluding someone streamed no recount")
	}
	for profile, want := range map[string]int{"home": 1, "first": 2} {
		got, err := backend.ProfileUnread(domain.WithProfile(ctx, profile), []domain.Unread{u})
		if err != nil || got[0].Messages != want {
			t.Errorf("profile %s counts %+v, %v; want %d", profile, got, err, want)
		}
	}
}
