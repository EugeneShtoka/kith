package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
	"github.com/EugeneShtoka/kith/internal/filedialog"
)

var (
	exportDana = domain.Member{UserID: "whatsapp:4915550001", DisplayName: "Dana W."}
	exportLee  = domain.Member{UserID: "whatsapp:4915550002", DisplayName: "Lee Park"}
)

// exportImporting is :import's backend importing chat exports too: kithd offers one
// chat for the export, and is sure who one of its two names is.
type exportImporting struct {
	importing
	path    string
	room    domain.RoomID
	senders map[string]string
}

func (b *exportImporting) PreviewChatImport(_ context.Context, path, _ string) (domain.ChatExport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.path = path
	return domain.ChatExport{Upload: "u1", Title: "WhatsApp Chat - Dana", Messages: 40,
		Rooms: []domain.ExportRoom{{ID: "whatsapp:9/1", Found: 3, Adds: 30}}}, nil
}

func (b *exportImporting) ChatImportSenders(_ context.Context, _, _ string, room domain.RoomID) (domain.ExportSenders, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.room = room
	return domain.ExportSenders{Adds: 30, Members: []domain.Member{exportDana, exportLee}, Senders: []domain.ExportSender{
		{Name: "Dana", Messages: 25, Chosen: exportDana.UserID, Likely: []domain.Member{exportDana}},
		{Name: "Mum", Messages: 15},
	}}, nil
}

func (b *exportImporting) ApplyChatImport(_ context.Context, _, _ string, room domain.RoomID, senders map[string]string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room != b.room {
		return 0, nil
	}
	b.senders = senders
	return 30, nil
}

// :import offers a chat export beside the accounts' folders; the export goes into the
// chat chosen, each name as kithd was sure of unless chosen otherwise, any member
// among them.
func TestAChatExportIsImportedAsChosen(t *testing.T) {
	t.Parallel()
	backend := &exportImporting{}
	m := starterNew(backend, config.Display{}).WithConfigFile("", asStarted(config.Config{}))
	m, cmd := m.openImport("")
	m = settle(t, m, cmd)
	if m.picker.kind != pickerImportAccount || len(m.picker.items) != 2 {
		t.Fatalf("after :import, picker %v with %+v", m.picker.kind, m.picker.items)
	}
	m, _ = m.chooseImportAccount(importExport)
	m, _ = updateWith(t, m, exportChosenMsg{err: filedialog.ErrUnavailable})
	if m.prompt.kind != promptImportExport {
		t.Fatalf("with no chooser, prompt %v", m.prompt.kind)
	}
	m = typeIn(t, m, "/exports/chat.zip")
	if backend.path != "/exports/chat.zip" || m.picker.kind != pickerExportRoom || len(m.picker.items) != 2 {
		t.Fatalf("read %q; picker %v with %+v", backend.path, m.picker.kind, m.picker.items)
	}
	if detail := m.picker.items[0].detail; !strings.Contains(detail, "has 3 of its messages") || !strings.Contains(detail, "adds 30") {
		t.Errorf("the chat's row says %q", detail)
	}
	m, cmd = m.chooseExportRoom("whatsapp:9/1")
	m = settle(t, m, cmd)
	if m.picker.kind != pickerExportSenders || !strings.Contains(m.picker.items[0].detail, "Dana W.") {
		t.Fatalf("senders picker %v: %+v", m.picker.kind, m.picker.items)
	}
	m = pickLabel(t, m, "Mum")
	m = pickLabel(t, m, "Someone else")
	m = pickLabel(t, m, "Lee")
	if m.picker.kind != pickerExportSenders || !strings.Contains(m.picker.items[1].detail, "Lee Park") {
		t.Fatalf("after choosing Lee: %v %+v", m.picker.kind, m.picker.items)
	}
	m, cmd = m.chooseExportSender(exportApply)
	m = settle(t, m, cmd)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.senders["Dana"] != exportDana.UserID || backend.senders["Mum"] != exportLee.UserID {
		t.Errorf("imported with %v", backend.senders)
	}
	if !strings.Contains(m.status(), "imported 30 messages") {
		t.Errorf("status %q", m.status())
	}
}
