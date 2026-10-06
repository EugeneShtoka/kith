package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// importing is a backend with one Telegram account and two folders: Work, which no tag
// is named after, and Friends, whose tag holds other chats.
type importing struct {
	apitest.Nop
	mu      sync.Mutex
	applied map[string]domain.GroupingChoice
}

func (*importing) LoginNetworks(context.Context) ([]api.LoginNetwork, error) {
	return []api.LoginNetwork{{Network: "telegram", Label: "Telegram", Accounts: []api.LoginAccount{{Name: "personal"}}}}, nil
}

func (*importing) PreviewGroupings(context.Context, string, string) ([]domain.GroupingDiff, error) {
	return []domain.GroupingDiff{
		{Grouping: domain.Grouping{Name: "Work", Rooms: []domain.RoomID{"telegram:42/1", "telegram:42/2"}}},
		{Grouping: domain.Grouping{Name: "Friends", Rooms: []domain.RoomID{"telegram:42/3"}}, Exists: true,
			Add: []domain.RoomID{"telegram:42/3"}, Remove: []domain.RoomID{"telegram:42/4"}},
	}, nil
}

func (b *importing) ApplyGroupings(_ context.Context, _, _ string, choices map[string]domain.GroupingChoice) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.applied = choices
	return nil
}

// :import offers the account's folders: a new tag is made for one no tag is named
// after; one whose tag differs changes nothing until chosen, then is copied as chosen.
func TestFoldersAreCopiedAsChosenPerTag(t *testing.T) {
	t.Parallel()
	backend := &importing{}
	m := starterNew(backend, config.Display{}).WithConfigFile("", config.Config{})
	m, cmd := m.openImport("")
	m = settle(t, m, cmd) // the one account: straight to its folders
	if m.picker.kind != pickerImportFolders {
		t.Fatalf("after :import, picker %v; status %q", m.picker.kind, m.status())
	}
	rows := map[string]string{}
	for _, item := range m.picker.items {
		rows[item.label] = item.detail
	}
	if !strings.Contains(rows["Work"], "a new tag, 2 chats") || !strings.Contains(rows["Friends"], "keep kith's") {
		t.Fatalf("rows = %v", rows)
	}
	m = pickLabel(t, m, "Friends")
	if m.picker.kind != pickerImportChoice {
		t.Fatalf("choosing Friends opened %v", m.picker.kind)
	}
	m = pickLabel(t, m, "Merge")
	m, cmd = m.chooseImportFolder(importCopy)
	m = settle(t, m, cmd)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.applied["Work"] != domain.TakeNetworks || backend.applied["Friends"] != domain.MergeIn {
		t.Errorf("copied with %v", backend.applied)
	}
	if !strings.Contains(m.status(), "copied 2 folders") {
		t.Errorf("status %q", m.status())
	}
}
