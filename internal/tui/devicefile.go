package tui

import (
	"reflect"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/config"
)

// deviceWriter orders this window's saves of the device file. Each writes what the
// window changed since the file as it last had it (config.UpdateDevice), so another
// window's change in between is kept; a save overtaken by a newer one is skipped, the
// newer carrying its change too.
type deviceWriter struct {
	path string

	mu      sync.Mutex
	taken   uint64 // the last place in line handed out
	written uint64 // the newest save written
	// base is the device as this window last had it, which a save's change is measured from.
	base config.Device
}

// take is the next place in line. Nil-safe: without a writer, nothing is saved.
func (w *deviceWriter) take() uint64 {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.taken++
	return w.taken
}

// save writes now's change unless a newer save is written already, returning the file
// as written; skipped when it was.
func (w *deviceWriter) save(gen uint64, now config.Device) (written config.Device, skipped bool, err error) {
	if w == nil {
		return config.Device{}, true, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if gen <= w.written {
		return config.Device{}, true, nil
	}
	written, err = config.UpdateDevice(w.path, w.base, now)
	if err != nil {
		return config.Device{}, false, err //nolint:wrapcheck // config's error names the file
	}
	w.base, w.written = now, gen
	return written, false, nil
}

// adopt makes written, the answer to save gen, the base of the next save, unless a
// later save was taken since; it reports whether the window should run it.
func (w *deviceWriter) adopt(gen uint64, written config.Device) bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if gen != w.taken {
		return false
	}
	w.base = written
	return true
}

// deviceSavedMsg is a device save's outcome: the file as written, which may hold
// another window's changes too.
type deviceSavedMsg struct {
	gen     uint64
	written config.Device
	skipped bool
	err     error
}

// saveDeviceCmd writes dev's change to the device file (assembled by applySettings).
// Its place in line is taken on the event loop, so saves land in change order.
func (m Model) saveDeviceCmd(dev config.Device) tea.Cmd {
	writer := m.conf.deviceFile
	if writer == nil {
		return nil
	}
	gen := writer.take()
	return func() tea.Msg {
		written, skipped, err := writer.save(gen, dev)
		return deviceSavedMsg{gen: gen, written: written, skipped: skipped, err: err}
	}
}

// handleDeviceSaved reports a save that failed, and runs what the latest save wrote
// when another window had changed the file too.
func (m Model) handleDeviceSaved(msg deviceSavedMsg) (Model, tea.Cmd) {
	switch {
	case msg.err != nil:
		return m.sayErr("setting applied but not saved", msg.err), nil
	case msg.skipped || !m.conf.deviceFile.adopt(msg.gen, msg.written):
		return m, nil
	case reflect.DeepEqual(msg.written, m.conf.device):
		return m, nil
	}
	return m.adoptSettings(config.Settings{Bridge: m.conf.base, Device: msg.written}, "")
}
