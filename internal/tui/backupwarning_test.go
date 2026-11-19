package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/domain"
)

// backupWarner is a daemon warning about the backups folder dir ("" none), keeping
// what is dismissed.
type backupWarner struct {
	apitest.Nop
	dir       *string
	dismissed *[]string
}

func (b backupWarner) BackupWarning(context.Context) (dir, why string, err error) {
	if *b.dir == "" || slices.Contains(*b.dismissed, *b.dir) {
		return "", "", nil
	}
	return *b.dir, "Backups in " + *b.dir + " vanish when kithd stops.", nil
}

func (b backupWarner) DismissBackupWarning(_ context.Context, dir string) error {
	*b.dismissed = append(*b.dismissed, dir)
	return nil
}

// No warning, no notice; told the warning changed, the client reads it and shows the
// notice, once a run: closed, it is not shown again when the warning is read again, and
// kithd is told nothing. The next start shows it again, and "Don't warn again"
// dismisses that folder in kithd.
func TestTheBackupWarningShowsOnceARunUntilDismissed(t *testing.T) {
	t.Parallel()

	b := backupWarner{dir: new(string), dismissed: new([]string)}
	start := func() Model {
		t.Helper()
		m := sized(t, withRooms(t, starterNew(b, config.Display{})))
		return deliver(t, m, m.loadBackupWarningCmd())
	}
	changed := func(m Model) Model {
		t.Helper()
		m, cmd := m.handleChanged(changedMsg{change: domain.ChangeBackupWarning})
		return deliver(t, m, cmd)
	}

	m := start()
	if m.picker.active() {
		t.Fatalf("no warning raised picker %d", m.picker.kind)
	}
	*b.dir = "/tmp/kith"
	m = changed(m)
	if m.picker.kind != pickerBackupWarning || m.picker.spec.title != "Backups in /tmp/kith vanish when kithd stops." {
		t.Fatalf("the warning raised picker %d titled %q; want the notice, titled with it", m.picker.kind, m.picker.spec.title)
	}
	for _, item := range m.picker.items {
		if strings.HasSuffix(item.label, "…") {
			t.Errorf("row %q ends in an ellipsis", item.label)
		}
	}
	m, cmd := press(t, m, key(tea.KeyEscape))
	m = changed(deliver(t, m, cmd))
	if m.picker.active() || len(*b.dismissed) != 0 {
		t.Fatalf("closed: picker %d open again, dismissed %q; want neither", m.picker.kind, *b.dismissed)
	}

	m = start()
	if m.picker.kind != pickerBackupWarning {
		t.Fatal("the next start did not warn again")
	}
	m, cmd = press(t, m, key(tea.KeyEnter)) // the first row: Don't warn again
	m = deliver(t, m, cmd)
	if m.picker.active() || !slices.Equal(*b.dismissed, []string{"/tmp/kith"}) {
		t.Fatalf("Don't warn again: picker open %v, dismissed %q; want closed, /tmp/kith", m.picker.active(), *b.dismissed)
	}
	if m = start(); m.picker.active() {
		t.Error("a dismissed folder warned again")
	}
}
