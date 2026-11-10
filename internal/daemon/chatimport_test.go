package daemon_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/daemon"
	"github.com/EugeneShtoka/kith/internal/db"
	"github.com/EugeneShtoka/kith/internal/domain"
)

const exportRoom domain.RoomID = "whatsapp:4915550009/4915550001@s.whatsapp.net"

const (
	exportDana  = "whatsapp:4915550001"
	exportRobin = "whatsapp:4915550009"
	exportZone  = "Europe/Berlin"
)

// exporting is a backend with one WhatsApp chat, with Dana, over a real cache, whose
// saves fail from the failAt-th on (0: never).
type exporting struct {
	fakeBackend
	cache  *db.Cache
	mu     sync.Mutex
	saves  int
	failAt int
}

func (b *exporting) ChatCache() (daemon.ChatCache, bool) { return b, true }

func (b *exporting) OldestMessage(ctx context.Context, room domain.RoomID) (domain.Message, bool, error) {
	return b.cache.OldestMessage(ctx, room)
}

func (b *exporting) MessagesSaying(ctx context.Context, room domain.RoomID, bodies []string) ([]domain.Message, error) {
	return b.cache.MessagesSaying(ctx, room, bodies)
}

func (b *exporting) CountMessages(ctx context.Context, room domain.RoomID) (int, error) {
	return b.cache.CountMessages(ctx, room)
}

func (b *exporting) SaveMessages(ctx context.Context, room domain.RoomID, msgs []domain.Message) error {
	b.mu.Lock()
	b.saves++
	failed := b.failAt > 0 && b.saves >= b.failAt
	b.mu.Unlock()
	if failed {
		return errors.New("disk full")
	}
	return b.cache.SaveMessages(ctx, room, msgs)
}

func (*exporting) Members(context.Context, domain.RoomID, int) ([]domain.Member, error) {
	return []domain.Member{{UserID: exportDana, DisplayName: "Dana W."}}, nil
}

func (*exporting) Selves(context.Context) ([]string, error) {
	return []string{exportRobin, "matrix:@robin:example.org"}, nil
}

// newExporting is the backend, its chat having cached the export's last two messages:
// "Dana Wolfe" said the first of them, at 21:05 Berlin time on the export's last day.
func newExporting(t *testing.T) *exporting {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	cache, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "cache.db"), sum[:])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	berlin, err := time.LoadLocation(exportZone)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2021, 3, 4, 21, 5, 20, 0, berlin)
	if err := cache.SaveMessages(t.Context(), exportRoom, []domain.Message{
		{ID: "wa1", Sender: exportDana, Body: "see you at the station at eight", Timestamp: at},
		{ID: "wa2", Sender: exportRobin, Body: "on my way", Timestamp: at.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	return &exporting{fakeBackend: fakeBackend{Nop: apitest.Nop{}, rooms: []domain.Room{
		{ID: exportRoom, Members: []string{"Dana Wolfe"}}, {ID: "whatsapp:4915550009/other", Name: "Book club"},
	}}, cache: cache}
}

// writeExport is an Android export of n older messages, then the two the chat cached.
func writeExport(t *testing.T, n int) string {
	t.Helper()
	var text strings.Builder
	for i := range n {
		fmt.Fprintf(&text, "%02d/02/2021, %02d:%02d - %s: message %d\n", 1+i/1440, i%1440/60, i%60, []string{"Dana Wolfe", "Robin"}[i%2], i)
	}
	text.WriteString("04/03/2021, 21:05 - Dana Wolfe: see you at the station at eight\n04/03/2021, 21:06 - Robin: on my way\n")
	path := filepath.Join(t.TempDir(), "WhatsApp Chat with Dana Wolfe.txt")
	if err := os.WriteFile(path, []byte(text.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// An export is offered the chat that cached its messages, its sender is who the cache
// says, and the import adds what is older, once: imported again it adds nothing.
func TestAChatExportGoesIntoTheChatThatHoldsIt(t *testing.T) {
	t.Parallel()
	backend := newExporting(t)
	r := serve(t, backend).client()
	ctx := t.Context()
	e, err := r.PreviewChatImport(ctx, writeExport(t, 3), exportZone)
	if err != nil {
		t.Fatal(err)
	}
	if e.Messages != 5 || len(e.Rooms) != 1 || e.Rooms[0] != (domain.ExportRoom{ID: exportRoom, Found: 1, Named: true, Adds: 3}) {
		t.Fatalf("preview = %+v", e)
	}
	s, err := r.ChatImportSenders(ctx, e.Upload, exportZone, exportRoom)
	if err != nil {
		t.Fatal(err)
	}
	chosen := map[string]string{}
	for _, sender := range s.Senders {
		chosen[sender.Name] = sender.Chosen
	}
	if chosen["Dana Wolfe"] != exportDana || chosen["Robin"] != "" || len(s.Members) != 2 || s.Adds != 3 {
		t.Fatalf("senders = %+v, members %+v", s.Senders, s.Members)
	}
	added, err := r.ApplyChatImport(ctx, e.Upload, exportZone, exportRoom, map[string]string{"Dana Wolfe": exportDana, "Robin": exportRobin})
	if err != nil || added != 3 {
		t.Fatalf("ApplyChatImport() = %d, %v; want 3", added, err)
	}
	oldest, _, _ := backend.cache.OldestMessage(ctx, exportRoom)
	if oldest.Body != "message 0" || oldest.Sender != exportDana || oldest.Timestamp.UTC().Hour() != 23 {
		t.Errorf("oldest = %+v; want message 0, Dana's, at Berlin's midnight", oldest)
	}
	if _, gone := r.ChatImportSenders(ctx, e.Upload, exportZone, exportRoom); gone == nil {
		t.Error("the upload outlived its import")
	}
	again, err := r.PreviewChatImport(ctx, writeExport(t, 3), exportZone)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := r.ApplyChatImport(ctx, again.Upload, exportZone, exportRoom, nil); err != nil || added != 0 {
		t.Errorf("imported again: %d, %v; want nothing added", added, err)
	}
}

// An import that fails part way leaves what it cached next to the chat's history, and
// run again adds the rest: every message once.
func TestAnImportCutShortFinishesWhenRunAgain(t *testing.T) {
	t.Parallel()
	backend := newExporting(t)
	backend.failAt = 2 // the second batch of three fails
	r := serve(t, backend).client()
	ctx := t.Context()
	e, err := r.PreviewChatImport(ctx, writeExport(t, 2500), exportZone)
	if err != nil {
		t.Fatal(err)
	}
	if _, failed := r.ApplyChatImport(ctx, e.Upload, exportZone, exportRoom, nil); failed == nil {
		t.Fatal("the failing import succeeded")
	}
	backend.mu.Lock()
	backend.failAt = 0
	backend.mu.Unlock()
	added, err := r.ApplyChatImport(ctx, e.Upload, exportZone, exportRoom, nil)
	if err != nil || added != 1500 {
		t.Fatalf("run again: %d, %v; want the 1500 left", added, err)
	}
	if n, _ := backend.cache.CountMessages(ctx, exportRoom); n != 2502 {
		t.Errorf("cached %d, want each of the 2500 once and the 2 there", n)
	}
}
