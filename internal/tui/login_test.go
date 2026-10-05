package tui

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/apitest"
	"github.com/EugeneShtoka/kith/internal/config"
)

// loginDaemon is a daemon whose logins follow scripts: each BeginLogin takes the next
// script, and each step of it is answered in turn. It records what it was given.
type loginDaemon struct {
	apitest.Nop
	networks []api.LoginNetwork

	mu       sync.Mutex
	scripts  [][]api.LoginStep
	steps    []api.LoginStep
	begun    []string // "network account"
	answers  []map[string]string
	canceled int
	restarts int
	reloads  int
}

func (d *loginDaemon) LoginNetworks(context.Context) ([]api.LoginNetwork, error) {
	return d.networks, nil
}

func (d *loginDaemon) BeginLogin(_ context.Context, network, account string) (api.LoginStep, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.begun = append(d.begun, network+" "+account)
	if len(d.scripts) == 0 {
		return api.LoginStep{}, errors.New("no script left")
	}
	d.steps, d.scripts = d.scripts[0], d.scripts[1:]
	return d.next()
}

func (d *loginDaemon) AnswerLogin(_ context.Context, _ string, values map[string]string) (api.LoginStep, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answers = append(d.answers, values)
	return d.next()
}

func (d *loginDaemon) next() (api.LoginStep, error) {
	if len(d.steps) == 0 {
		return api.LoginStep{}, errors.New("the script ended")
	}
	step := d.steps[0]
	d.steps = d.steps[1:]
	step.Login = "L1"
	return step, nil
}

func (d *loginDaemon) CancelLogin(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.canceled++
	return nil
}

func (d *loginDaemon) ReloadConfig(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reloads++
	return nil
}

func (d *loginDaemon) restart(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restarts++
	return nil
}

// loggingIn is a model over d with cfg, wide enough to draw its status line.
func loggingIn(t *testing.T, d *loginDaemon, change func(*config.Config)) Model {
	t.Helper()
	cfg, err := starterOnce()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.Clone()
	if change != nil {
		change(&cfg)
	}
	m := New(context.Background(), d, cfg.Display).WithConfigFile("", cfg).WithRestart(d.restart)
	return update(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
}

// answer types input into the open login prompt and submits it.
func answer(t *testing.T, m Model, input string) (Model, tea.Cmd) {
	t.Helper()
	if m.prompt.kind != promptLogin {
		t.Fatalf("no login prompt open to answer %q (status %q)", input, m.status())
	}
	m = m.openPromptWith(promptLogin, input)
	return m.submitPrompt()
}

// untilAsked feeds cmd's messages back until the sign-in waits for a field or ends,
// and is what is left to run (the sign-in's listener, while it goes on); a command that
// does not answer soon (the status line's timer) is dropped.
func untilAsked(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 200 {
			t.Fatal("the sign-in did not settle")
		}
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		got := make(chan tea.Msg, 1)
		go func() { got <- next() }()
		var msg tea.Msg
		select {
		case msg = <-got:
		case <-time.After(time.Second):
			continue
		}
		switch msg := msg.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case loginMsg, loginNetworksMsg:
			var more tea.Cmd
			m, more = asModel(m.Update(msg))
			queue = append(queue, more)
			if _, asking := m.login.field(); asking || m.picker.active() || m.login.stage == loginOff {
				return m, tea.Batch(queue...)
			}
		}
	}
	return m, nil
}

var fakeNetwork = api.LoginNetwork{Network: "fake", Label: "Fake", Detail: "a phone number"}

// :login lists the daemon's networks; one with no account sets a new one up at once:
// its fields asked one by one with their help, the suggestion kept by enter, a secret
// drawn as dots; the record it asks for written into the config and re-read by the
// daemon; its code shown; its end said.
func TestLoginDrawsWhatTheNetworkAsks(t *testing.T) {
	t.Parallel()
	d := &loginDaemon{networks: []api.LoginNetwork{fakeNetwork}, scripts: [][]api.LoginStep{{
		{Ask: []api.LoginField{
			{Key: "phone", Label: "phone", Help: "The number, with its country code."},
			{Key: "name", Label: "call it", Value: "gb"},
		}},
		{Note: "that is the account home already", Ask: []api.LoginField{{Key: "phone", Label: "phone"}}},
		{Configure: &api.LoginRecord{Table: "whatsapp.account", Values: map[string]string{"name": "gb", "phone": "+44 7700 900123"}}, Account: "gb"},
		{Ask: []api.LoginField{{Key: "token", Label: "token", Secret: true}}},
		{Code: "ABCD-1234", Note: "type it on the phone"},
		{Done: "linked Fake gb"},
	}}}
	m := loggingIn(t, d, nil)
	var pending tea.Cmd

	m, cmd := m.openLogin("")
	m, pending = untilAsked(t, m, tea.Batch(cmd, pending))
	if m.picker.kind != pickerLoginNetwork || len(m.picker.items) != 1 || m.picker.items[0].label != "Fake" {
		t.Fatalf("picker = %+v, want the daemon's networks", m.picker.items)
	}
	m, cmd = m.chooseLoginNetwork("fake")
	m, pending = untilAsked(t, m, tea.Batch(cmd, pending))
	if f, ok := m.login.field(); !ok || f.Key != "phone" || !strings.Contains(strings.Join(m.loginLines(100, 30), "\n"), "country code") {
		t.Fatalf("asked %+v, want the phone with its help", f)
	}
	m, _ = answer(t, m, "+44 7700 900123")
	if m.prompt.input != "gb" || !m.prompt.fresh {
		t.Fatalf("name offered %q (fresh %v), want the suggestion", m.prompt.input, m.prompt.fresh)
	}
	m, cmd = m.submitPrompt() // enter keeps it
	m, pending = untilAsked(t, m, tea.Batch(cmd, pending))
	if !strings.Contains(m.status(), "home already") {
		t.Errorf("status %q, want the network's note", m.status())
	}
	m, cmd = answer(t, m, "+44 7700 900123")
	m, pending = untilAsked(t, m, tea.Batch(cmd, pending))
	if got := m.conf.base.WhatsApp.Accounts; len(got) != 1 || got[0] != (config.WhatsAppAccount{Name: "gb", Phone: "+44 7700 900123"}) {
		t.Fatalf("[[whatsapp.account]] = %+v, want gb written", got)
	}
	if f, ok := m.login.field(); !ok || f.Key != "token" {
		t.Fatalf("asked %+v after writing, want the token", f)
	}
	m = m.openPromptWith(promptLogin, "secret-token")
	if status := m.renderStatus(); strings.Contains(status, "secret") || !strings.Contains(status, "•••") {
		t.Errorf("status line while typing the token = %q, want dots", status)
	}
	m, cmd = m.submitPrompt()
	for _, line := range m.loginLines(100, 30) {
		if strings.Contains(line, "secret-token") {
			t.Error("the pane shows the token")
		}
	}
	m, _ = untilAsked(t, m, tea.Batch(cmd, pending))
	if m.login.stage != loginOff || !strings.Contains(m.status(), "linked Fake gb") {
		t.Errorf("after: stage %v, status %q; want done and said", m.login.stage, m.status())
	}
	want := []map[string]string{
		{"phone": "+44 7700 900123", "name": "gb"}, {"phone": "+44 7700 900123"}, nil, {"token": "secret-token"}, nil,
	}
	if !slices.EqualFunc(d.answers, want, maps.Equal) {
		t.Errorf("answers %v, want %v", d.answers, want)
	}
	if d.reloads != 1 || d.begun[0] != "fake " {
		t.Errorf("reloads %d, begun %v; want one re-read after writing, a new account begun", d.reloads, d.begun)
	}
}

// A network with accounts offers them, and a new one; a required field left empty is
// asked again; canceling midway ends the login on the daemon too.
func TestLoginAgainOrCancel(t *testing.T) {
	t.Parallel()
	network := fakeNetwork
	network.Accounts = []api.LoginAccount{{Name: "home", Detail: "+1"}}
	d := &loginDaemon{networks: []api.LoginNetwork{network}, scripts: [][]api.LoginStep{{
		{Ask: []api.LoginField{{Key: "code", Label: "code"}}},
	}}}
	m := loggingIn(t, d, nil)
	var pending tea.Cmd
	m, cmd := m.openLogin("fake")
	m, pending = untilAsked(t, m, tea.Batch(cmd, pending))
	if m.picker.kind != pickerLoginAccount || len(m.picker.items) != 2 || m.picker.items[0].label != "home" {
		t.Fatalf("picker = %+v, want home and a new account", m.picker.items)
	}
	m, cmd = m.chooseLoginAccount("home")
	m, _ = untilAsked(t, m, tea.Batch(cmd, pending))
	m, _ = answer(t, m, "")
	if f, _ := m.login.field(); f.Key != "code" || !strings.Contains(m.status(), "needed") {
		t.Fatalf("an empty code: field %q, status %q; want it asked again", f.Key, m.status())
	}
	m, _ = m.cancelPrompt()
	if m.login.stage != loginOff {
		t.Errorf("stage %v after cancel, want off", m.login.stage)
	}
	for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		d.mu.Lock()
		canceled := d.canceled
		d.mu.Unlock()
		if canceled == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon's login was not canceled")
		}
	}
	if d.begun[0] != "fake home" {
		t.Errorf("begun %v, want home", d.begun)
	}
}

// A login that needs the daemon restarted restarts it and begins again, on the
// account it set up.
func TestLoginRestartsTheDaemonWhenAsked(t *testing.T) {
	t.Parallel()
	d := &loginDaemon{networks: []api.LoginNetwork{fakeNetwork}, scripts: [][]api.LoginStep{
		{{Restart: true, Done: "set Fake up", Account: "@me:x"}},
		{{Done: "logged in as @me:x"}},
	}}
	m := loggingIn(t, d, nil)
	var pending tea.Cmd
	m, cmd := m.openLogin("fake")
	m, _ = untilAsked(t, m, tea.Batch(cmd, pending))
	if d.restarts != 1 || !slices.Equal(d.begun, []string{"fake ", "fake @me:x"}) {
		t.Errorf("restarts %d, begun %v; want one restart, then @me:x begun", d.restarts, d.begun)
	}
	if !strings.Contains(m.status(), "logged in as @me:x") {
		t.Errorf("status = %q", m.status())
	}
}
