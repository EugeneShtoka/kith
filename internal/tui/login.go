package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/config"
	"github.com/EugeneShtoka/kith/internal/setup"
)

// :login sets an account up and signs it in, inside kith: choose the network, then
// answer what it needs — a phone number, a workspace, a homeserver — each explained in
// the timeline pane while it is typed on the status line, secrets drawn as dots. The
// account is written to the config as any setting is, the daemon re-reads it, and the
// daemon signs in (`kith login …` does the same from a shell). A network the daemon was
// started without is turned on by restarting it, which the flow does itself.

// loginNetwork is a network :login can set up.
type loginNetwork struct {
	key, label, detail string
}

var loginNetworks = []loginNetwork{
	{"whatsapp", "WhatsApp", "a phone number, linked as one of its devices"},
	{"slack", "Slack", "a workspace, signed in with your browser's session"},
	{"matrix", "Matrix", "an account on a homeserver"},
}

// loginNew is the account picker's row for setting up another account.
const loginNew = "\x00new"

// pairingTimeout bounds waiting for the phone to take a pairing code, as `kith login
// whatsapp` does.
const pairingTimeout = 10 * time.Minute

// loginStage is where a sign-in is.
type loginStage int

const (
	loginOff     loginStage = iota
	loginAsking             // a field's prompt is open
	loginWorking            // saving, restarting, signing in: the daemon is at work
)

// The fields a network asks for, by key.
const (
	fieldPhone      = "phone"
	fieldName       = "name"
	fieldWorkspace  = "workspace"
	fieldToken      = "token"
	fieldCookie     = "cookie"
	fieldHomeserver = "homeserver"
	fieldUser       = "user"
	fieldPassword   = "password"
)

// loginField is one thing a sign-in asks for.
type loginField struct {
	key, label string
	secret     bool
}

// loginState is the sign-in under way.
type loginState struct {
	stage   loginStage
	network loginNetwork
	// account is the name of the account being signed in: set up already, or named in
	// this flow; fresh says it is being set up.
	account string
	fresh   bool
	fields  []loginField
	at      int
	values  map[string]string
	// code is the WhatsApp pairing code to type on the phone; note what is happening.
	code, note string
	cancel     context.CancelFunc
	events     <-chan loginEvent
}

// field is the field being asked for.
func (l loginState) field() (loginField, bool) {
	if l.stage != loginAsking || l.at >= len(l.fields) {
		return loginField{}, false
	}
	return l.fields[l.at], true
}

// loginEvent is news from the sign-in under way: a pairing code, a step, or the end.
type loginEvent struct {
	code, note string
	// done ends the sign-in: what it did, or err.
	done  string
	err   error
	final bool
}

type loginMsg struct {
	event loginEvent
	ok    bool // false: the events ended (canceled)
}

// openLogin is :login: the networks, or straight to the one named.
func (m Model) openLogin(arg string) (Model, tea.Cmd) {
	if m.login.stage != loginOff {
		return m.say("a sign-in is under way — finish it, or cancel it with " + m.keys.keyHint(scopePrompt, actCancel)), nil
	}
	if arg = strings.ToLower(strings.TrimSpace(arg)); arg != "" {
		return m.chooseLoginNetwork(arg)
	}
	items := make([]pickerItem, 0, len(loginNetworks))
	for _, n := range loginNetworks {
		items = append(items, pickerItem{label: n.label, detail: n.detail, value: n.key, match: n.label})
	}
	m.picker = newPicker(pickerLoginNetwork, items)
	return m, nil
}

// chooseLoginNetwork offers the network's accounts to sign in again, and a new one;
// with none set up, it asks for the new one at once.
func (m Model) chooseLoginNetwork(key string) (Model, tea.Cmd) {
	m = m.closePicker()
	i := slices.IndexFunc(loginNetworks, func(n loginNetwork) bool { return n.key == key })
	if i < 0 {
		return m.say("no network is called " + key + " — whatsapp, slack or matrix"), nil
	}
	network, cfg := loginNetworks[i], m.conf.base
	if network.key == "matrix" {
		if len(cfg.Profiles) > 0 {
			return m.say("this config's Matrix accounts are [[profile]]s — log one in with `kith login --profile <name>`"), nil
		}
		if cfg.HasMatrix() {
			return m.startLogin(network, cfg.User)
		}
		return m.startLogin(network, "")
	}
	var items []pickerItem
	for _, a := range loginAccounts(cfg, network.key) {
		items = append(items, pickerItem{label: a[0], detail: a[1], value: a[0], match: a[0] + " " + a[1]})
	}
	if len(items) == 0 {
		return m.startLogin(network, "")
	}
	items = append(items, pickerItem{label: "New account", detail: "set up another", value: loginNew, match: "new account"})
	spec := pickerSpecs[pickerLoginAccount]
	spec.title = network.label + ": sign in which account?"
	m.picker = newPickerWith(pickerLoginAccount, spec, items)
	m.login.network = network // the account picker's answer is about it
	return m, nil
}

// loginAccounts is a network's accounts set up already: name and what it is.
func loginAccounts(cfg config.Config, network string) [][2]string {
	var out [][2]string
	switch network {
	case "whatsapp":
		for _, a := range cfg.WhatsApp.Accounts {
			out = append(out, [2]string{a.Name, a.Phone})
		}
	case "slack":
		for _, a := range cfg.Slack.Accounts {
			out = append(out, [2]string{a.Name, a.Workspace})
		}
	}
	return out
}

// chooseLoginAccount signs an account in again, or sets up a new one.
func (m Model) chooseLoginAccount(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == loginNew {
		return m.startLogin(m.login.network, "")
	}
	return m.startLogin(m.login.network, value)
}

// startLogin asks for what signing account in takes ("" for a new account: what
// setting it up takes, too).
func (m Model) startLogin(network loginNetwork, account string) (Model, tea.Cmd) {
	m.login = loginState{stage: loginAsking, network: network, account: account, fresh: account == "",
		fields: loginFields(network.key, account != ""), values: map[string]string{}}
	if len(m.login.fields) == 0 {
		return m.finishLogin()
	}
	return m.askLoginField(""), nil
}

// loginFields is what a network asks for: to set an account up and sign it in, or,
// for one set up already, to sign it in.
func loginFields(network string, known bool) []loginField {
	switch {
	case network == "whatsapp" && known:
		return nil // the phone is asked, by its pairing code
	case network == "whatsapp":
		return []loginField{{key: fieldPhone, label: "phone"}, {key: fieldName, label: "call it"}}
	case network == "slack" && known:
		return []loginField{
			{key: fieldToken, label: "token (xoxc-…)", secret: true},
			{key: fieldCookie, label: "cookie d (xoxd-…)", secret: true},
		}
	case network == "slack":
		return []loginField{
			{key: fieldWorkspace, label: "workspace"}, {key: fieldName, label: "call it"},
			{key: fieldToken, label: "token (xoxc-…)", secret: true},
			{key: fieldCookie, label: "cookie d (xoxd-…)", secret: true},
		}
	case known: // Matrix, set up
		return []loginField{{key: fieldPassword, label: "password", secret: true}}
	default:
		return []loginField{
			{key: fieldHomeserver, label: "homeserver"}, {key: fieldUser, label: "Matrix ID"},
			{key: fieldPassword, label: "password", secret: true},
		}
	}
}

// askLoginField opens the prompt for the field under way: typed is what was there (a
// refused answer, to fix), else the field's suggestion, selected so typing replaces it.
func (m Model) askLoginField(typed string) Model {
	f, ok := m.login.field()
	if !ok {
		return m
	}
	if typed != "" {
		return m.openPromptWith(promptLogin, typed)
	}
	suggested := m.loginSuggestion(f)
	m = m.openPromptWith(promptLogin, suggested)
	m.prompt.fresh = suggested != ""
	return m
}

// loginSuggestion is a field's likely answer: a name from what was answered before
// it, the usual homeserver.
func (m Model) loginSuggestion(f loginField) string {
	v, cfg := m.login.values, m.conf.base
	switch f.key {
	case fieldName:
		names := accountNames(cfg, m.login.network.key)
		if m.login.network.key == "whatsapp" {
			return setup.WhatsAppName(config.WhatsAppAccount{Phone: v[fieldPhone]}.Digits(), names)
		}
		return setup.SlackName(v[fieldWorkspace], names)
	case fieldHomeserver:
		return "https://matrix.org"
	}
	return ""
}

// accountNames is the names a network's accounts have.
func accountNames(cfg config.Config, network string) []string {
	var names []string
	for _, a := range loginAccounts(cfg, network) {
		names = append(names, a[0])
	}
	return names
}

// submitLogin takes the field's answer: refused, the prompt reopens on it, saying
// why; taken, the next field is asked, or the sign-in begins.
func (m Model) submitLogin(input string) (Model, tea.Cmd) {
	f, ok := m.login.field()
	if !ok {
		return m, nil
	}
	value, err := m.checkLoginField(f, strings.TrimSpace(input))
	if err != nil {
		return m.askLoginField(input).say(err.Error()), nil
	}
	values := maps.Clone(m.login.values) // copies of the model share the map
	values[f.key] = value
	m.login.values = values
	m.login.at++
	if m.login.at < len(m.login.fields) {
		return m.askLoginField(""), nil
	}
	return m.finishLogin()
}

// matrixID is a full Matrix user ID.
var matrixID = regexp.MustCompile(`^@[^:\s]+:\S+$`)

// checkLoginField is an answer as it is kept, or why it is refused.
func (m Model) checkLoginField(f loginField, input string) (string, error) {
	if input == "" {
		return "", errors.New(f.label + " is needed")
	}
	switch f.key {
	case fieldPhone:
		return checkPhone(m.conf.base, input)
	case fieldName:
		return checkName(m.conf.base, m.login.network.key, input)
	case fieldWorkspace:
		return checkWorkspace(m.conf.base, input)
	case fieldToken:
		return input, setup.CheckSlackToken(input)
	case fieldCookie:
		return input, setup.CheckSlackCookie(input)
	case fieldHomeserver:
		return checkHomeserver(input)
	case fieldUser:
		return checkMatrixID(input, m.login.values[fieldHomeserver])
	}
	return input, nil
}

// checkPhone is an international number no account has, kept with its "+".
func checkPhone(cfg config.Config, input string) (string, error) {
	digits := config.WhatsAppAccount{Phone: input}.Digits()
	if err := setup.CheckPhone(digits); err != nil {
		return "", err
	}
	for _, a := range cfg.WhatsApp.Accounts {
		if a.Digits() == digits {
			return "", fmt.Errorf("that number is the account %s already — :login whatsapp and choose it to link it again", a.Name)
		}
	}
	if !strings.HasPrefix(input, "+") {
		input = "+" + input
	}
	return input, nil
}

// checkName is one word no account of the network has.
func checkName(cfg config.Config, network, input string) (string, error) {
	if strings.ContainsFunc(input, func(r rune) bool { return r == ' ' || r == '\t' }) {
		return "", errors.New("a name is one word: `kith login " + network + " <name>` takes it")
	}
	if slices.Contains(accountNames(cfg, network), input) {
		return "", fmt.Errorf("an account is called %s already", input)
	}
	return input, nil
}

// checkWorkspace is a Slack workspace no account has, as Slack writes it.
func checkWorkspace(cfg config.Config, input string) (string, error) {
	workspace := setup.SlackWorkspace(config.SlackAccount{Workspace: input})
	if err := setup.CheckSlackWorkspace(workspace); err != nil {
		return "", err
	}
	for _, a := range cfg.Slack.Accounts {
		if setup.SlackWorkspace(a) == workspace {
			return "", fmt.Errorf("that workspace is the account %s already — :login slack and choose it to sign in again", a.Name)
		}
	}
	return workspace, nil
}

// checkHomeserver is a homeserver's address, https:// when none is said.
func checkHomeserver(input string) (string, error) {
	if !strings.Contains(input, "://") {
		input = "https://" + input
	}
	if u, err := url.Parse(input); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("write the homeserver's address, as https://matrix.org")
	}
	return strings.TrimSuffix(input, "/"), nil
}

// checkMatrixID is a full Matrix ID; a name alone is taken as on the homeserver.
func checkMatrixID(input, homeserver string) (string, error) {
	if !strings.Contains(input, ":") {
		host := ""
		if u, err := url.Parse(homeserver); err == nil {
			host = u.Hostname()
		}
		input = "@" + strings.TrimPrefix(input, "@") + ":" + host
	}
	if !matrixID.MatchString(input) {
		return "", errors.New("write your Matrix ID, as @you:matrix.org")
	}
	return input, nil
}

// cancelLogin abandons the sign-in: a prompt closed, or the daemon's work canceled.
func (m Model) cancelLogin() Model {
	if m.login.cancel != nil {
		m.login.cancel()
	}
	m.login = loginState{}
	return m.say("sign-in canceled")
}

// handleLoginKey is a key while the daemon signs in: the cancel key cancels, and the
// rest wait, as with verification.
func (m Model) handleLoginKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.keys.lookup(key.String(), scopePrompt) == actCancel {
		return m.cancelLogin(), nil
	}
	return m, nil
}

// finishLogin writes a new account into the config, applies it, and sets the daemon
// to signing it in.
func (m Model) finishLogin() (Model, tea.Cmd) {
	if m.login.fresh {
		m.login.account = cmp.Or(m.login.values[fieldName], m.login.values[fieldUser])
	}
	cfg, changed := m.loginConfig()
	var applied tea.Cmd
	var gen uint64
	if changed {
		next, cmd := m.applyConfig(cfg, "set up "+m.login.network.label+" "+m.login.account)
		if !loginApplied(next.conf.base, m.login) {
			next.login = loginState{} // refused: applyConfig said why
			return next, cmd
		}
		m, applied, gen = next, cmd, next.conf.writer.take()
	}
	m.login.stage, m.login.note = loginWorking, "signing in…"
	ctx, cancel := context.WithTimeout(m.ctx, pairingTimeout)
	events := make(chan loginEvent, 4)
	m.login.cancel, m.login.events = cancel, events
	work := m.loginWork(cfg, changed, gen)
	start := func() tea.Msg {
		go work(ctx, events)
		return nil
	}
	return m, tea.Batch(applied, start, listen(ctx, events, wrapLogin))
}

func wrapLogin(e loginEvent) tea.Msg { return loginMsg{event: e, ok: true} }

// loginConfig is the config with the account set up and its network turned on;
// changed is false when it was so already.
func (m Model) loginConfig() (config.Config, bool) {
	cfg, v := m.conf.base.Clone(), m.login.values
	before := m.conf.base
	switch m.login.network.key {
	case "whatsapp":
		cfg.WhatsApp.Enabled = true
		if m.login.fresh {
			cfg.WhatsApp.Accounts = append(cfg.WhatsApp.Accounts, config.WhatsAppAccount{Name: v[fieldName], Phone: v[fieldPhone]})
		}
		return cfg, !before.WhatsApp.Enabled || len(cfg.WhatsApp.Accounts) != len(before.WhatsApp.Accounts)
	case "slack":
		cfg.Slack.Enabled = true
		if m.login.fresh {
			cfg.Slack.Accounts = append(cfg.Slack.Accounts, config.SlackAccount{Name: v[fieldName], Workspace: v[fieldWorkspace]})
		}
		return cfg, !before.Slack.Enabled || len(cfg.Slack.Accounts) != len(before.Slack.Accounts)
	default:
		if m.login.fresh {
			cfg.Homeserver, cfg.User = v[fieldHomeserver], v[fieldUser]
		}
		return cfg, cfg.Homeserver != before.Homeserver || cfg.User != before.User
	}
}

// loginApplied reports whether the config now holds the account being signed in.
func loginApplied(cfg config.Config, l loginState) bool {
	name := l.account
	switch l.network.key {
	case "whatsapp":
		return cfg.WhatsApp.Enabled && slices.ContainsFunc(cfg.WhatsApp.Accounts, func(a config.WhatsAppAccount) bool { return a.Name == name })
	case "slack":
		return cfg.Slack.Enabled && slices.ContainsFunc(cfg.Slack.Accounts, func(a config.SlackAccount) bool { return a.Name == name })
	default:
		return cfg.HasMatrix()
	}
}

// loginWork is the sign-in, run off the update loop: the config written (when it
// changed) and re-read by the daemon, then the network's sign-in; a daemon started
// without the network is restarted, once, and asked again. Everything it learns goes
// to events, which it closes.
func (m Model) loginWork(cfg config.Config, changed bool, gen uint64) func(context.Context, chan<- loginEvent) {
	path, writer, reload, restart := m.conf.path, m.conf.writer, m.notifications.backend, m.link.restart
	signIn := m.loginSignIn()
	label := m.login.network.label
	return func(ctx context.Context, events chan<- loginEvent) {
		defer close(events)
		send := func(e loginEvent) {
			select {
			case events <- e:
			case <-ctx.Done():
			}
		}
		end := func(done string, err error) { send(loginEvent{done: done, err: err, final: true}) }
		if changed && path != "" {
			if err := writer.save(gen, path, cfg); err != nil {
				end("", fmt.Errorf("save the config: %w", err))
				return
			}
		}
		if reload != nil {
			// The daemon reads the config only when asked: the account is new to it.
			if err := reload.ReloadConfig(ctx); err != nil {
				end("", fmt.Errorf("have kithd re-read the config: %w", err))
				return
			}
		}
		done, again, err := signIn(ctx, send)
		if errors.Is(err, api.ErrNetworkOff) && restart != nil {
			// The daemon was started without the network: it starts with the daemon.
			send(loginEvent{note: "restarting kithd to turn " + label + " on…"})
			if err = restart(ctx); err == nil {
				send(loginEvent{note: "signing in…"})
				done, again, err = signIn(ctx, send)
			}
		}
		if err == nil && again && restart != nil {
			// Matrix logged in on a daemon that cannot start it live (its encryption
			// store opened already): the session starts with the daemon.
			send(loginEvent{note: "restarting kithd to start " + label + "…"})
			err = restart(ctx)
		}
		end(done, err)
	}
}

// loginSignIn is the network's sign-in through the daemon: what it did, whether the
// daemon must restart to use it, or why it failed. send hears its news.
func (m Model) loginSignIn() func(context.Context, func(loginEvent)) (string, bool, error) {
	backend, v, account, network := m.backend, m.login.values, m.login.account, m.login.network.key
	return func(ctx context.Context, send func(loginEvent)) (string, bool, error) {
		switch network {
		case "whatsapp":
			link, ok := backend.(api.WhatsAppLink)
			if !ok {
				return "", false, errNoLogin
			}
			linked, err := link.PairWhatsApp(ctx, account, func(code string) error {
				send(loginEvent{code: code, note: "waiting for the phone…"})
				return nil
			})
			return "linked WhatsApp " + account + " as " + linked + " — its chats arrive over the next minutes", false, err //nolint:wrapcheck // the daemon's own words
		case "slack":
			in, ok := backend.(api.SlackSignIn)
			if !ok {
				return "", false, errNoLogin
			}
			signed, err := in.SignInSlack(ctx, account, v[fieldToken], v[fieldCookie])
			return "signed in to " + signed.Workspace + " as " + signed.User, false, err //nolint:wrapcheck // the daemon's own words
		default:
			in, ok := backend.(api.MatrixLogin)
			if !ok {
				return "", false, errNoLogin
			}
			logged, err := in.LoginMatrix(ctx, v[fieldPassword])
			return "logged in to Matrix as " + logged.UserID, err == nil && !logged.Started, err //nolint:wrapcheck // the daemon's own words
		}
	}
}

// errNoLogin is a kith not attached to a daemon, which is what signs in.
var errNoLogin = errors.New("this kith is not attached to kithd, which signs in")

// handleLoginMsg is news from the sign-in: shown in the pane, or, at the end, said on
// the status line, the rooms and spaces read again to show what it brought.
func (m Model) handleLoginMsg(msg loginMsg) (Model, tea.Cmd) {
	if m.login.stage != loginWorking || !msg.ok {
		return m, nil // canceled meanwhile
	}
	e := msg.event
	if !e.final {
		if e.code != "" {
			m.login.code = e.code
		}
		if e.note != "" {
			m.login.note = e.note
		}
		return m, listen(m.ctx, m.login.events, wrapLogin)
	}
	label := m.login.network.label
	if m.login.cancel != nil {
		m.login.cancel()
	}
	m.login = loginState{}
	if e.err != nil {
		if errors.Is(e.err, context.Canceled) {
			return m, nil
		}
		return m.sayErr("could not sign in to "+label, e.err), nil
	}
	return m.say(e.done), tea.Batch(m.refreshRoomsCmd(), m.refreshSpacesCmd())
}

// loginTitle is the timeline pane's title while signing in.
func (m Model) loginTitle() string {
	what := m.login.network.label
	if name := cmp.Or(m.login.account, m.login.values[fieldName]); name != "" && m.login.network.key != "matrix" {
		what += " " + name
	} else if m.login.fresh && m.login.stage == loginAsking {
		what += " — a new account"
	}
	return "Sign in: " + what
}

// loginLines is the timeline pane while signing in: what was answered, then the field
// being typed and how to find its answer, or what the daemon is doing.
func (m Model) loginLines(width, rows int) []string {
	var lines []string
	block := func(text string) {
		for _, para := range docParagraphs(text) {
			lines = append(lines, drawBlock(para, blockSpec{width: width})...)
		}
	}
	lines = append(lines, "")
	for _, f := range m.login.fields[:min(m.login.at, len(m.login.fields))] {
		shown := m.login.values[f.key]
		if f.secret {
			shown = strings.Repeat("•", min(utf8.RuneCountInString(shown), 12))
		}
		lines = append(lines, m.theme.Muted.Render(fmt.Sprintf("%-12s", f.label))+" "+shown)
	}
	if len(lines) > 1 {
		lines = append(lines, "")
	}
	if f, ok := m.login.field(); ok {
		block(m.loginHelp(f))
		lines = append(lines, "", m.theme.Muted.Render(fmt.Sprintf("%d of %d", m.login.at+1, len(m.login.fields))))
	} else {
		lines = append(lines, m.login.note)
		if m.login.code != "" {
			lines = append(lines, "", "    "+m.theme.TitleActive.Render(m.login.code), "")
			block(setup.WhatsAppLinkSteps)
		}
		lines = append(lines, "", m.theme.Muted.Render(m.keys.keyHint(scopePrompt, actCancel)+" cancels"))
	}
	if len(lines) > rows {
		lines = lines[:rows]
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return lines
}

// loginHelp is what a field is and how to find it, as paragraphs (an indented line
// is kept as written).
func (m Model) loginHelp(f loginField) string {
	v := m.login.values
	workspace := v[fieldWorkspace]
	for _, a := range m.conf.base.Slack.Accounts {
		if a.Name == m.login.account {
			workspace = setup.SlackWorkspace(a)
		}
	}
	session := setup.SlackSession(workspace)
	switch f.key {
	case fieldPhone:
		return "The WhatsApp account's phone number, with its country code: +44 7700 900123.\n\n" +
			"kith becomes one of the phone's linked devices, as WhatsApp Web is. The phone must come " +
			"online every couple of weeks, or WhatsApp unlinks it. Linking asks the phone for all the " +
			"history it holds; it arrives over the minutes after."
	case fieldName:
		if m.login.network.key == "whatsapp" {
			return "What kith calls this account: its rooms are the space “WhatsApp <name>” in the rail, " +
				"and `kith login whatsapp <name>` links it again. Suggested from the number's country; " +
				"enter keeps it."
		}
		return "What kith calls this workspace: `kith login slack <name>` signs it in again. " +
			"Suggested from its address; enter keeps it."
	case fieldWorkspace:
		return "The workspace: its address — acme, for acme.slack.com — or, with Slack open in a " +
			"browser, the link in its address bar (https://app.slack.com/client/T…/…), pasted whole."
	case fieldToken:
		return "kith signs in with the session your browser holds. In a browser signed in to " +
			session.Where + ", open " + session.Open + ", then the developer tools (F12) → Console, " +
			"and paste this line; it prints the token, which starts with xoxc-:\n\n    " + session.Token +
			"\n\n" + setup.SlackSessionWarning
	case fieldCookie:
		return "In the same developer tools: " + session.Cookie + ", which starts with xoxd-. " +
			"Copied as it shows or decoded, either works.\n\n" + setup.SlackSessionWarning
	case fieldHomeserver:
		return "Your homeserver's address: https://matrix.org for an account there, or your own " +
			"server's — what follows the colon in your Matrix ID (@you:matrix.org)."
	case fieldUser:
		return "Your Matrix ID: @you:matrix.org. The name alone is taken as on this homeserver."
	case fieldPassword:
		help := "The account's password. kith logs in as a new session and keeps that session in the " +
			"system keyring, not the password."
		if m.login.fresh {
			help += "\n\nSetting Matrix up restarts kithd, which takes a few seconds."
		}
		return help
	}
	return ""
}

// loginPromptLabel is the status line's label for the field being typed.
func (m Model) loginPromptLabel() string {
	if f, ok := m.login.field(); ok {
		return f.label + ": "
	}
	return ""
}

// loginSecret reports whether the field being typed is drawn as dots.
func (m Model) loginSecret() bool {
	f, ok := m.login.field()
	return ok && f.secret && m.prompt.kind == promptLogin
}

// masked is an editor drawn as dots, the caret where it was.
func masked(e editor) editor {
	dots := func(s string) string { return strings.Repeat("•", utf8.RuneCountInString(s)) }
	before := dots(e.text[:e.at])
	return editor{text: before + dots(e.text[e.at:]), at: len(before)}
}
