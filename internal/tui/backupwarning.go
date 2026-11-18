package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
)

// kithd warns about a scheduled backups folder that does not keep them (one under /tmp,
// which its systemd unit replaces). The notice shows once a run per folder, at start
// and when the warning changes (ChangeBackupWarning): "Don't warn again" has kithd
// keep that folder dismissed; closing it warns again next start.

// backupWarningMsg is the folder kithd warns about and why ("" none), or a dismissal
// that failed.
type backupWarningMsg struct {
	dir, why string
	err      error
}

// loadBackupWarningCmd asks kithd for its warning; nil for a backend without one. A
// failure is silent: nobody asked.
func (m Model) loadBackupWarningCmd() tea.Cmd {
	src, ok := m.backend.(api.BackupWarnings)
	if !ok {
		return nil
	}
	ctx := m.ctx
	return func() tea.Msg {
		dir, why, err := src.BackupWarning(ctx)
		if err != nil {
			return nil
		}
		return backupWarningMsg{dir: dir, why: why}
	}
}

// handleBackupWarning raises the notice, unless it was shown for that folder this run
// or another chooser is open.
func (m Model) handleBackupWarning(msg backupWarningMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not keep the backup warning dismissed", msg.err), nil
	}
	if msg.dir == "" || m.offers.backupWarned[msg.dir] || m.picker.active() {
		return m, nil
	}
	m.offers.backupWarned = withEntry(m.offers.backupWarned, msg.dir, true)
	spec := pickerSpecs[pickerBackupWarning]
	spec.title = msg.why
	m.picker = newPickerWith(pickerBackupWarning, spec, []pickerItem{
		{label: "Don't warn again", detail: "about this folder", value: msg.dir},
		{label: "Close", detail: "warn again next start"},
	})
	return m, nil
}

// answerBackupWarning closes the notice, dismissing the folder for good when that was
// chosen.
func (m Model) answerBackupWarning(dir string) (Model, tea.Cmd) {
	m = m.closePicker()
	src, ok := m.backend.(api.BackupWarnings)
	if dir == "" || !ok {
		return m, nil
	}
	ctx := m.ctx
	return m, func() tea.Msg {
		if err := src.DismissBackupWarning(ctx, dir); err != nil {
			return backupWarningMsg{err: err}
		}
		return nil
	}
}
