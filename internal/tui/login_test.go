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
	// telegram is each code sent: the account and its app; tried is each answer.
	telegram []string
	tried    [][2]string
	twoStep  bool // the Telegram account has a password ("right")
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
	return "+447700900123", nil
}

func (s *signingIn) SignInSlack(_ context.Context, account, token, cookie string) (api.SlackSignedIn, error) {
	if s.isOff() {
		return api.SlackSignedIn{}, fmt.Errorf("daemon: sign in to Slack: %w", api.ErrNetworkOff)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.slack = append(s.slack, [3]string{account, token, cookie})
	return api.SlackSignedIn{Workspace: "Acme", User: "dana"}, nil
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

func (s *signingIn) SendTelegramCode(_ context.Context, account string, app api.TelegramApp) (api.TelegramCodeSent, error) {
	if s.isOff() {
		return api.TelegramCodeSent{}, fmt.Errorf("daemon: send a Telegram login code: %w", api.ErrNetworkOff)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.telegram = append(s.telegram, fmt.Sprintf("%s %d %s", account, app.ID, app.Hash))
	return api.TelegramCodeSent{Via: "your Telegram app"}, nil
}

func (s *signingIn) SignInTelegram(_ context.Context, _, code, password string) (api.TelegramSignedIn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tried = append(s.tried, [2]string{code, password})
	switch {
	case code != "12345":
		return api.TelegramSignedIn{}, fmt.Errorf("daemon: %w", api.ErrBadCode)
	case s.twoStep && password == "":
		return api.TelegramSignedIn{}, fmt.Errorf("daemon: %w", api.ErrPasswordNeeded)
	case s.twoStep && password != "right":
		return api.TelegramSignedIn{}, fmt.Errorf("daemon: %w", api.ErrBadPassword)
	}
	return api.TelegramSignedIn{Name: "Dana Lee", ID: "telegram:42"}, nil
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
	return signInAnswering(t, m, cmd, nil)
}

// signInAnswering is signIn answering what the sign-in asks midway from answers, by
// field, in turn, and returns the codes shown.
func signInAnswering(t *testing.T, m Model, cmd tea.Cmd, answers map[string][]string) (Model, []string) {
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
			if f, ok := m.login.field(); ok && m.login.answer != nil {
				if len(answers[f.key]) == 0 {
					t.Fatalf("asked %q midway, with no answer left (status %q)", f.key, m.status())
				}
				var input string
				input, answers[f.key] = answers[f.key][0], answers[f.key][1:]
				m, _ = answer(t, m, input)
			}
		}
	}
	return m, codes
}

// A new WhatsApp account: the number, a name suggested from its country, written into
// the config; a daemon that answers the network is off is restarted, and links it,
// showing the pairing code.
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
	m, _ = answer(t, m, "+44 7700 900123")
	if m.prompt.input != "gb" || !m.prompt.fresh {
		t.Fatalf("name suggested %q (fresh %v), want gb from the country", m.prompt.input, m.prompt.fresh)
	}
	m, cmd := m.submitPrompt() // enter keeps the suggestion
	if m.login.stage != loginWorking {
		t.Fatalf("stage %v after the last field, want the daemon at work (status %q)", m.login.stage, m.status())
	}
	got := m.conf.base.WhatsApp
	if len(got.Accounts) != 1 || got.Accounts[0] != (config.WhatsAppAccount{Name: "gb", Phone: "+44 7700 900123"}) {
		t.Fatalf("[whatsapp] = %+v, want gb", got)
	}

	m, codes := signIn(t, m, cmd)
	if d.restarts != 1 || len(d.paired) != 1 || d.paired[0] != "gb" {
		t.Errorf("restarts %d, paired %v; want one restart, then gb linked", d.restarts, d.paired)
	}
	if len(codes) != 1 || codes[0] != "ABCD-1234" {
		t.Errorf("codes shown %v, want the pairing code", codes)
	}
	if m.login.stage != loginOff || !strings.Contains(m.status(), "linked WhatsApp gb") {
		t.Errorf("after: stage %v, status %q; want done and said", m.login.stage, m.status())
	}
}

// A Slack workspace pasted as a web-client link is kept as its ID; the session's two
// halves are typed as dots, a half pasted in the wrong field is refused and asked
// again, and the daemon gets the session for the new account.
func TestLoginSetsUpSlackWithASessionTypedAsDots(t *testing.T) {
	t.Parallel()
	d := &signingIn{}
	m := loggingIn(t, d, nil)

	m, _ = m.openLogin("slack")
	m, _ = answer(t, m, "https://app.slack.com/client/T0123456789/C0123")
	if m.prompt.input != "work" {
		t.Errorf("name suggested %q, want work for a workspace named by its ID", m.prompt.input)
	}
	m, _ = m.submitPrompt()
	if help := strings.Join(m.loginLines(140, 40), "\n"); !strings.Contains(help, `teams["T0123456789"].token`) {
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
	if got := m.conf.base.Slack.Accounts; len(got) != 1 || got[0] != (config.SlackAccount{Name: "work", Workspace: "T0123456789"}) {
		t.Fatalf("[[slack.account]] = %+v, want work in T0123456789", got)
	}
	m, _ = signIn(t, m, cmd)
	if len(d.slack) != 1 || d.slack[0] != [3]string{"work", "xoxc-token", "xoxd-a%2Bb"} {
		t.Errorf("signed in with %v, want work's session", d.slack)
	}
	if d.restarts != 0 {
		t.Errorf("restarted %d times for a network that was on", d.restarts)
	}
	if !strings.Contains(m.status(), "signed in to Acme") {
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
		c.WhatsApp.Accounts = []config.WhatsAppAccount{{Name: "work", Phone: "+1 202 555 0147"}}
	})
	before := m.conf.base

	m, _ = m.openLogin("whatsapp")
	if m.picker.kind != pickerLoginAccount || len(m.picker.items) != 2 || m.picker.items[1].value != loginNew {
		t.Fatalf("picker = %+v, want work and a new account", m.picker.items)
	}
	m, cmd := m.chooseLoginAccount("work")
	if m.login.stage != loginWorking {
		t.Fatalf("stage %v, want work linking at once", m.login.stage)
	}
	m, _ = signIn(t, m, cmd)
	if len(d.paired) != 1 || d.paired[0] != "work" || len(m.conf.base.WhatsApp.Accounts) != 1 {
		t.Errorf("paired %v, accounts %v; want work linked and nothing added", d.paired, m.conf.base.WhatsApp.Accounts)
	}

	m, _ = m.openLogin("whatsapp")
	m, _ = m.chooseLoginAccount(loginNew)
	m, _ = answer(t, m, "+1 202 555 0147")
	if !strings.Contains(m.status(), "the account work already") {
		t.Errorf("status = %q, want the number refused as work's", m.status())
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
	m, _ = answer(t, m, "+44 7700 900123")
	m, cmd := m.submitPrompt()
	m, _ = signIn(t, m, cmd)
	if m.login.stage != loginOff || !strings.Contains(m.status(), "could not sign in to WhatsApp") {
		t.Errorf("stage %v, status %q; want the failure said", m.login.stage, m.status())
	}
}

// A new Telegram account: the number, a name from its country, the app (empty: kith's
// own, so no hash is asked); written into the config, the daemon restarted to run
// Telegram, the code sent and asked for — a wrong one again — then the two-step
// password, a wrong one again too.
func TestLoginSetsUpTelegramAndAsksForTheCodeMidway(t *testing.T) {
	t.Parallel()
	d := &signingIn{off: true, twoStep: true}
	m := loggingIn(t, d, nil)

	m, _ = m.openLogin("telegram")
	m, _ = answer(t, m, "+44 7700 900123")
	if m.prompt.input != "gb" {
		t.Errorf("name suggested %q, want gb from the country", m.prompt.input)
	}
	m, _ = m.submitPrompt()
	if f, _ := m.login.field(); f.key != fieldAppID || !strings.Contains(strings.Join(m.loginLines(140, 40), "\n"), "my.telegram.org") {
		t.Fatalf("asked %q, want the api_id with where to get it", f.key)
	}
	m, cmd := answer(t, m, "")
	if m.login.stage != loginWorking {
		t.Fatalf("an empty api_id asked on (%v): kith's own app needs no hash", m.login.stage)
	}
	if got := m.conf.base.Telegram.Accounts; len(got) != 1 || got[0] != (config.TelegramAccount{Name: "gb", Phone: "+44 7700 900123"}) {
		t.Fatalf("[[telegram.account]] = %+v, want gb", got)
	}
	m, _ = signInAnswering(t, m, cmd, map[string][]string{fieldCode: {"11111", "12345"}, fieldTwoStep: {"wrong", "right"}})
	if d.restarts != 1 || !slices.Equal(d.telegram, []string{"gb 0 "}) {
		t.Errorf("restarts %d, codes sent %q; want a restart, then gb's code through kith's app", d.restarts, d.telegram)
	}
	want := [][2]string{{"11111", ""}, {"12345", ""}, {"12345", "wrong"}, {"12345", "right"}}
	if !slices.Equal(d.tried, want) {
		t.Errorf("answers %q, want %q", d.tried, want)
	}
	if m.login.stage != loginOff || !strings.Contains(m.status(), "logged in to Telegram as Dana Lee") {
		t.Errorf("after: stage %v, status %q; want done and said", m.login.stage, m.status())
	}
}

// One's own app: its hash is checked as typed and reaches the daemon; canceling while
// the code is asked for ends the login.
func TestLoginTelegramWithOwnAppAndCancelMidway(t *testing.T) {
	t.Parallel()
	d := &signingIn{}
	m := loggingIn(t, d, func(c *config.Config) {
		c.Telegram.Accounts = []config.TelegramAccount{{Name: "home", Phone: "+44 7700 900123"}}
	})
	m, _ = m.openLogin("telegram")
	m, _ = m.chooseLoginAccount("home")
	m, _ = answer(t, m, "12345")
	m, _ = answer(t, m, "not-a-hash")
	if f, _ := m.login.field(); f.key != fieldAppHash || !strings.Contains(m.status(), "32") {
		t.Fatalf("a bad hash: field %q, status %q; want it refused", f.key, m.status())
	}
	m, cmd := answer(t, m, "0123456789abcdef0123456789abcdef")
	// Run until the code is asked for, then cancel.
	for range 20 {
		if m.login.answer != nil {
			break
		}
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					if lm, ok := c().(loginMsg); ok {
						m, cmd = asModel(m.Update(lm))
					}
				}
			}
			continue
		}
		if lm, ok := msg.(loginMsg); ok {
			m, cmd = asModel(m.Update(lm))
		}
	}
	if f, ok := m.login.field(); !ok || f.key != fieldCode || !strings.Contains(m.status(), "your Telegram app") {
		t.Fatalf("asked %q (status %q), want the code, saying where it went", f.key, m.status())
	}
	if len(d.telegram) != 1 || d.telegram[0] != "home 12345 0123456789abcdef0123456789abcdef" {
		t.Errorf("codes sent %q, want home's through its own app", d.telegram)
	}
	m, _ = m.cancelPrompt()
	if m.login.stage != loginOff {
		t.Errorf("stage %v after cancel, want off", m.login.stage)
	}
}
