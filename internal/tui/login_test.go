package tui

import (
	"context"
	"fmt"
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

// signingIn is a daemon that signs networks in, recording what it was asked. A
// network is off (api.ErrNetworkOff) until the daemon restarts when off says so.
type signingIn struct {
	apitest.Nop
	mu       sync.Mutex
	off      bool
	restarts int
	paired   []string
	slack    [][3]string
	matrix   []string
	started  bool // whether LoginMatrix starts the session live
}

func (s *signingIn) restart(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts++
	s.off = false
	return nil
}

func (s *signingIn) isOff() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.off
}

func (s *signingIn) PairWhatsApp(_ context.Context, account string, code func(string) error) (string, error) {
	if s.isOff() {
		return "", fmt.Errorf("daemon: link WhatsApp: %w", api.ErrNetworkOff)
	}
	s.mu.Lock()
	s.paired = append(s.paired, account)
	s.mu.Unlock()
	if err := code("ABCD-1234"); err != nil {
		return "", err
	}
	return "+359884650326", nil
}

func (s *signingIn) SignInSlack(_ context.Context, account, token, cookie string) (api.SlackSignedIn, error) {
	if s.isOff() {
		return api.SlackSignedIn{}, fmt.Errorf("daemon: sign in to Slack: %w", api.ErrNetworkOff)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slack = append(s.slack, [3]string{account, token, cookie})
	return api.SlackSignedIn{Workspace: "TipMaster", User: "eshtoka"}, nil
}

func (s *signingIn) LoginMatrix(_ context.Context, password string) (api.MatrixLoggedIn, error) {
	if s.isOff() {
		return api.MatrixLoggedIn{}, fmt.Errorf("daemon: log in to Matrix: %w", api.ErrNetworkOff)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.matrix = append(s.matrix, password)
	return api.MatrixLoggedIn{UserID: "@me:example.org", Started: s.started}, nil
}

// loggingIn is a model over d with cfg, wide enough to draw its status line.
func loggingIn(t *testing.T, d *signingIn, change func(*config.Config)) Model {
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

// signIn runs a sign-in's commands to its end, feeding its news back, and returns the
// pairing codes it showed on the way. A command that does not answer soon (the status
// line's timer) is dropped.
func signIn(t *testing.T, m Model, cmd tea.Cmd) (Model, []string) {
	t.Helper()
	var codes []string
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 100 {
			t.Fatal("the sign-in did not end")
		}
		next := queue[0]
		queue = queue[1:]
		if next == nil {
			continue
		}
		answered := make(chan tea.Msg, 1)
		go func() { answered <- next() }()
		var got tea.Msg
		select {
		case got = <-answered:
		case <-time.After(2 * time.Second):
			continue
		}
		switch msg := got.(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case loginMsg:
			var more tea.Cmd
			m, more = asModel(m.Update(msg))
			if m.login.code != "" && !slices.Contains(codes, m.login.code) {
				codes = append(codes, m.login.code)
			}
			queue = append(queue, more)
		}
	}
	return m, codes
}

// A new WhatsApp account: the number, a name suggested from its country, written into
// the config with WhatsApp turned on; the daemon, started without WhatsApp, is
// restarted and links it, showing the pairing code.
func TestLoginSetsUpWhatsAppAndLinksIt(t *testing.T) {
	t.Parallel()
	d := &signingIn{off: true}
	m := loggingIn(t, d, nil)

	m, _ = m.openLogin("")
	if m.picker.kind != pickerLoginNetwork || len(m.picker.items) != len(loginNetworks) {
		t.Fatalf("picker = %v, want the networks", m.picker.items)
	}
	m, _ = m.chooseLoginNetwork("whatsapp")
	if f, ok := m.login.field(); !ok || f.key != fieldPhone {
		t.Fatalf("asked %+v, want the phone", m.login)
	}
	if !strings.Contains(strings.Join(m.loginLines(100, 30), "\n"), "country code") {
		t.Error("the pane does not say what the phone number is")
	}
	m, _ = answer(t, m, "+359 88 465 0326")
	if m.prompt.input != "bg" || !m.prompt.fresh {
		t.Fatalf("name suggested %q (fresh %v), want bg from the country", m.prompt.input, m.prompt.fresh)
	}
	m, cmd := m.submitPrompt() // enter keeps the suggestion
	if m.login.stage != loginWorking {
		t.Fatalf("stage %v after the last field, want the daemon at work (status %q)", m.login.stage, m.status())
	}
	got := m.conf.base.WhatsApp
	if !got.Enabled || len(got.Accounts) != 1 || got.Accounts[0] != (config.WhatsAppAccount{Name: "bg", Phone: "+359 88 465 0326"}) {
		t.Fatalf("[whatsapp] = %+v, want it on with bg", got)
	}

	m, codes := signIn(t, m, cmd)
	if d.restarts != 1 || len(d.paired) != 1 || d.paired[0] != "bg" {
		t.Errorf("restarts %d, paired %v; want one restart, then bg linked", d.restarts, d.paired)
	}
	if len(codes) != 1 || codes[0] != "ABCD-1234" {
		t.Errorf("codes shown %v, want the pairing code", codes)
	}
	if m.login.stage != loginOff || !strings.Contains(m.status(), "linked WhatsApp bg") {
		t.Errorf("after: stage %v, status %q; want done and said", m.login.stage, m.status())
	}
}

// A Slack workspace pasted as a web-client link is kept as its ID; the session's two
// halves are typed as dots, a half pasted in the wrong field is refused and asked
// again, and the daemon gets the session for the new account.
func TestLoginSetsUpSlackWithASessionTypedAsDots(t *testing.T) {
	t.Parallel()
	d := &signingIn{}
	m := loggingIn(t, d, func(c *config.Config) { c.Slack.Enabled = true })

	m, _ = m.openLogin("slack")
	m, _ = answer(t, m, "https://app.slack.com/client/T01K2D5TCAC/C0123")
	if m.prompt.input != "work" {
		t.Errorf("name suggested %q, want work for a workspace named by its ID", m.prompt.input)
	}
	m, _ = m.submitPrompt()
	if help := strings.Join(m.loginLines(140, 40), "\n"); !strings.Contains(help, `teams["T01K2D5TCAC"].token`) {
		t.Errorf("the token's help does not give the console line for the workspace:\n%s", help)
	}

	m = m.openPromptWith(promptLogin, "xoxd-secret")
	if status := m.renderStatus(); strings.Contains(status, "secret") || !strings.Contains(status, "•••") {
		t.Errorf("status line while typing the token = %q, want dots", status)
	}
	m, _ = m.submitPrompt()
	if f, _ := m.login.field(); f.key != fieldToken || !strings.Contains(m.status(), "xoxc-") {
		t.Fatalf("a cookie in the token field: field %q, status %q; want it refused", f.key, m.status())
	}
	m, _ = answer(t, m, "xoxc-token")
	m, cmd := answer(t, m, "xoxd-a%2Bb")
	if got := m.conf.base.Slack.Accounts; len(got) != 1 || got[0] != (config.SlackAccount{Name: "work", Workspace: "T01K2D5TCAC"}) {
		t.Fatalf("[[slack.account]] = %+v, want work in T01K2D5TCAC", got)
	}
	m, _ = signIn(t, m, cmd)
	if len(d.slack) != 1 || d.slack[0] != [3]string{"work", "xoxc-token", "xoxd-a%2Bb"} {
		t.Errorf("signed in with %v, want work's session", d.slack)
	}
	if d.restarts != 0 {
		t.Errorf("restarted %d times for a network that was on", d.restarts)
	}
	if !strings.Contains(m.status(), "signed in to TipMaster") {
		t.Errorf("status = %q", m.status())
	}
	for _, line := range m.loginLines(100, 30) {
		if strings.Contains(line, "xoxc-token") {
			t.Error("the pane shows the token")
		}
	}
}

// An account set up already signs in again from the network's list, writing nothing;
// canceling a new one midway leaves the config as it was.
func TestLoginAgainOrCancel(t *testing.T) {
	t.Parallel()
	d := &signingIn{}
	m := loggingIn(t, d, func(c *config.Config) {
		c.WhatsApp.Enabled = true
		c.WhatsApp.Accounts = []config.WhatsAppAccount{{Name: "il", Phone: "+972 54 534 7450"}}
	})
	before := m.conf.base

	m, _ = m.openLogin("whatsapp")
	if m.picker.kind != pickerLoginAccount || len(m.picker.items) != 2 || m.picker.items[1].value != loginNew {
		t.Fatalf("picker = %+v, want il and a new account", m.picker.items)
	}
	m, cmd := m.chooseLoginAccount("il")
	if m.login.stage != loginWorking {
		t.Fatalf("stage %v, want il linking at once", m.login.stage)
	}
	m, _ = signIn(t, m, cmd)
	if len(d.paired) != 1 || d.paired[0] != "il" || len(m.conf.base.WhatsApp.Accounts) != 1 {
		t.Errorf("paired %v, accounts %v; want il linked and nothing added", d.paired, m.conf.base.WhatsApp.Accounts)
	}

	m, _ = m.openLogin("whatsapp")
	m, _ = m.chooseLoginAccount(loginNew)
	m, _ = answer(t, m, "+972 54 534 7450")
	if !strings.Contains(m.status(), "the account il already") {
		t.Errorf("status = %q, want the number refused as il's", m.status())
	}
	m, _ = m.cancelPrompt()
	if m.login.stage != loginOff || len(m.conf.base.WhatsApp.Accounts) != len(before.WhatsApp.Accounts) {
		t.Errorf("after canceling: stage %v, accounts %v", m.login.stage, m.conf.base.WhatsApp.Accounts)
	}
}

// Matrix set up already asks only the password; a login the daemon cannot start live
// restarts it. A new Matrix account takes the homeserver and the ID, the name alone
// taken as on the homeserver.
func TestLoginMatrix(t *testing.T) {
	t.Parallel()
	d := &signingIn{}
	m := loggingIn(t, d, func(c *config.Config) { c.Homeserver, c.User = "https://example.org", "@me:example.org" })
	m, _ = m.openLogin("matrix")
	if len(m.login.fields) != 1 || m.login.fields[0].key != fieldPassword {
		t.Fatalf("fields %+v, want the password alone", m.login.fields)
	}
	m, cmd := answer(t, m, "hunter2")
	m, _ = signIn(t, m, cmd)
	if len(d.matrix) != 1 || d.matrix[0] != "hunter2" || d.restarts != 1 {
		t.Errorf("logged in %v with %d restarts; want once, then a restart to start it", d.matrix, d.restarts)
	}
	if !strings.Contains(m.status(), "logged in to Matrix as @me:example.org") {
		t.Errorf("status = %q", m.status())
	}

	fresh := loggingIn(t, &signingIn{started: true}, func(c *config.Config) { c.Homeserver, c.User = "", "" })
	fresh, _ = fresh.openLogin("matrix")
	fresh, _ = answer(t, fresh, "matrix.example.org")
	fresh, _ = answer(t, fresh, "me")
	fresh, _ = answer(t, fresh, "hunter2")
	if fresh.conf.base.Homeserver != "https://matrix.example.org" || fresh.conf.base.User != "@me:matrix.example.org" {
		t.Errorf("Matrix set up as %q %q", fresh.conf.base.Homeserver, fresh.conf.base.User)
	}
}

// Without a daemon to restart, a network the daemon was started without says so.
func TestLoginWithoutARestartSaysWhy(t *testing.T) {
	t.Parallel()
	d := &signingIn{off: true}
	m := loggingIn(t, d, nil).WithRestart(nil)
	m, _ = m.openLogin("whatsapp")
	m, _ = answer(t, m, "+359 88 465 0326")
	m, cmd := m.submitPrompt()
	m, _ = signIn(t, m, cmd)
	if m.login.stage != loginOff || !strings.Contains(m.status(), "could not sign in to WhatsApp") {
		t.Errorf("stage %v, status %q; want the failure said", m.login.stage, m.status())
	}
}
