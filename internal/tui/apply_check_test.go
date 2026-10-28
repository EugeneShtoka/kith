package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// checkingDaemon judges configs: refuse is its verdict on every one.
type checkingDaemon struct {
	apitest.Nop
	refuse error
	asked  int
}

func (d *checkingDaemon) CheckConfig(context.Context, config.Config) error {
	d.asked++
	return d.refuse
}

func checkedModel(t *testing.T, d *checkingDaemon) Model {
	t.Helper()
	cfg, err := starterOnce()
	if err != nil {
		t.Fatal(err)
	}
	return New(context.Background(), d, cfg.Display, config.DeviceDisplay{}).WithConfigFile("", asStarted(cfg.Clone()))
}

// withAccount is m's config with one more WhatsApp account.
func withAccount(m Model, name string) config.Config {
	cfg := m.conf.base.Clone()
	cfg.WhatsApp.Accounts = append(cfg.WhatsApp.Accounts, config.WhatsAppAccount{Name: name, Phone: "+44 7700 900001"})
	return cfg
}

// verdict runs a held change's check and hands the answer back.
func verdict(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		t.Fatal("no check under way")
	}
	msg, ok := cmd().(configCheckedMsg)
	if !ok {
		t.Fatalf("the command answered %T, want kithd's verdict", msg)
	}
	return update(t, m, msg)
}

// An account change applies only once kithd accepts it; refused, nothing changes and
// the status says why. Other changes apply at once.
func TestAnAccountChangeWaitsForKithd(t *testing.T) {
	t.Parallel()
	d := &checkingDaemon{}
	m := checkedModel(t, d)

	held, cmd := m.applyConfig(withAccount(m, "home"), "added")
	if len(held.conf.base.WhatsApp.Accounts) != 0 {
		t.Fatal("an account change applied before kithd answered")
	}
	m = verdict(t, held, cmd)
	if got := m.conf.base.WhatsApp.Accounts; len(got) != 1 || got[0].Name != "home" {
		t.Errorf("accounts = %+v after kithd passed it, want home", got)
	}

	d.refuse = errors.New(`whatsapp.account[1] (work): phone is listed twice`)
	held, cmd = m.applyConfig(withAccount(m, "work"), "added")
	m = verdict(t, held, cmd)
	if got := m.conf.base.WhatsApp.Accounts; len(got) != 1 {
		t.Errorf("accounts = %+v after kithd refused, want home alone", got)
	}
	if !strings.Contains(m.status(), "listed twice") {
		t.Errorf("status %q, want kithd's reason", m.status())
	}

	asked := d.asked
	cfg := m.conf.base.Clone()
	cfg.Notifications.MaxPerRoom = 24
	m, _ = m.applyConfig(cfg, "set")
	if m.conf.base.Notifications.MaxPerRoom != 24 || d.asked != asked {
		t.Errorf("max_per_room %d, kithd asked %d more times; want it applied at once, nothing asked", m.conf.base.Notifications.MaxPerRoom, d.asked-asked)
	}
}

// A change kithd passes after another landed is dropped: it was made over the older
// config and would undo the newer one.
func TestAHeldChangeDoesNotUndoANewerOne(t *testing.T) {
	t.Parallel()
	m := checkedModel(t, &checkingDaemon{})

	held, cmd := m.applyConfig(withAccount(m, "home"), "added")
	cfg := held.conf.base.Clone()
	cfg.Notifications.MaxPerRoom = 24
	m, _ = held.applyConfig(cfg, "set")
	said := m.status()
	m = verdict(t, m, cmd)
	if m.conf.base.Notifications.MaxPerRoom != 24 {
		t.Errorf("max_per_room = %d, want the newer change kept", m.conf.base.Notifications.MaxPerRoom)
	}
	if len(m.conf.base.WhatsApp.Accounts) != 0 || m.status() == said {
		t.Errorf("accounts %+v, status %q; want the held change dropped, and said", m.conf.base.WhatsApp.Accounts, m.status())
	}

	// Two held at once: the first passed lands, the second is dropped.
	first, cmd1 := m.applyConfig(withAccount(m, "home"), "added")
	second, cmd2 := first.applyConfig(withAccount(first, "work"), "added")
	m = verdict(t, verdict(t, second, cmd1), cmd2)
	if got := m.conf.base.WhatsApp.Accounts; len(got) != 1 || got[0].Name != "home" {
		t.Errorf("accounts = %+v, want the first held change alone", got)
	}
}
