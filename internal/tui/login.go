package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/EugeneShtoka/kith/internal/api"
	"github.com/EugeneShtoka/kith/internal/qrtext"
)

// :login sets up and signs in an account inside kith. The daemon lists networks and each leads
// its own login (api.Logins): every step says what to ask, show or write, and this draws it (a
// field on the status line with help in the timeline pane, secrets as dots, a code to type
// elsewhere, a new account written to the config). Nothing here knows a network.

// loginNew is the account picker's row for setting up another account.
const loginNew = "\x00new"

// loginTimeout bounds a sign-in: a code to arrive and be typed, a phone to accept one.
const loginTimeout = 15 * time.Minute

type loginStage int

const (
	loginOff     loginStage = iota
	loginAsking             // a step's field is being typed
	loginWorking            // the daemon is at work: signing in, waiting for a phone
)

type loginState struct {
	stage loginStage
	// network is the network being signed in to; account the account, once known.
	network api.LoginNetwork
	account string
	// networks are the daemon's, for the account picker that follows.
	networks []api.LoginNetwork
	// fields are the step's, at the one being typed; values are the answers so far; answered is
	// every answer in this sign-in, drawn above the field.
	fields   []api.LoginField
	at       int
	values   map[string]string
	answered []answeredField
	// code is a code to type elsewhere, qr data to show as a QR code for a phone to scan;
	// note what the network said last.
	code, qr, note string
	cancel         context.CancelFunc
	events         <-chan loginEvent
	// answer takes the step's answers while the sign-in waits for them.
	answer chan<- map[string]string
}

// answeredField is an answer as the pane shows it: a secret as dots. key is its
// field's, so a field asked again replaces it.
type answeredField struct {
	key, label, shown string
}

func (l loginState) field() (api.LoginField, bool) {
	if l.stage != loginAsking || l.at >= len(l.fields) {
		return api.LoginField{}, false
	}
	return l.fields[l.at], true
}

// loginEvent is news from the sign-in: a step to answer, a code, a record to write, a note, or
// the end.
type loginEvent struct {
	// ask is fields to answer; their answers go to answer.
	ask    []api.LoginField
	answer chan<- map[string]string
	// write is a new account to write into the config; written hears how to save it.
	write   *api.LoginRecord
	written chan<- func() error
	code    string
	qr      string
	note    string
	account string
	// final ends the sign-in: done says what it did, or err why it failed.
	final bool
	done  string
	err   error
}

type loginMsg struct {
	event loginEvent
	ok    bool // false: the events ended (canceled)
}

func wrapLogin(e loginEvent) tea.Msg { return loginMsg{event: e, ok: true} }

// loginNetworksMsg is the daemon's networks for :login's picker; pick is the one named on the
// command line, if any.
type loginNetworksMsg struct {
	networks []api.LoginNetwork
	pick     string
	err      error
}

// errNoLogin reports kith not attached to a daemon, which does the signing in.
var errNoLogin = errors.New("this kith is not attached to kithd, which signs in")

// openLogin is :login: the daemon's networks, or straight to the one named.
func (m Model) openLogin(arg string) (Model, tea.Cmd) {
	if m.login.stage != loginOff {
		return m.say("a sign-in is under way — finish it, or cancel it with " + m.keys.keyHint(scopePrompt, actCancel)), nil
	}
	logins, ok := m.backend.(api.Logins)
	if !ok {
		return m.sayErr("could not sign in", errNoLogin), nil
	}
	ctx, pick := m.ctx, strings.ToLower(strings.TrimSpace(arg))
	return m, func() tea.Msg {
		networks, err := logins.LoginNetworks(ctx)
		return loginNetworksMsg{networks: networks, pick: pick, err: err}
	}
}

// handleLogin is a sign-in's news: the networks to offer, or a step of one under way.
func (m Model) handleLogin(msg tea.Msg) (Model, tea.Cmd) {
	if networks, ok := msg.(loginNetworksMsg); ok {
		return m.handleLoginNetworks(networks)
	}
	news, _ := msg.(loginMsg)
	return m.handleLoginMsg(news)
}

// handleLoginNetworks offers the networks, or goes on with the one named.
func (m Model) handleLoginNetworks(msg loginNetworksMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		return m.sayErr("could not list the networks", msg.err), nil
	}
	m.login.networks = msg.networks
	if msg.pick != "" {
		return m.chooseLoginNetwork(msg.pick)
	}
	items := make([]pickerItem, 0, len(msg.networks))
	for _, n := range msg.networks {
		items = append(items, pickerItem{label: n.Label, detail: n.Detail, value: n.Network, match: n.Label})
	}
	m.picker = newPicker(pickerLoginNetwork, items)
	return m, nil
}

// chooseLoginNetwork offers the network's accounts and a new one; with none set up, it sets up
// a new one at once.
func (m Model) chooseLoginNetwork(key string) (Model, tea.Cmd) {
	m = m.closePicker()
	i := slices.IndexFunc(m.login.networks, func(n api.LoginNetwork) bool { return n.Network == key })
	if i < 0 {
		known := make([]string, 0, len(m.login.networks))
		for _, n := range m.login.networks {
			known = append(known, n.Network)
		}
		return m.say("no network is called " + key + " — " + strings.Join(known, ", ")), nil
	}
	network := m.login.networks[i]
	m.login.network = network
	if len(network.Accounts) == 0 {
		return m.startLogin(network, "")
	}
	items := make([]pickerItem, 0, len(network.Accounts)+1)
	for _, a := range network.Accounts {
		items = append(items, pickerItem{label: a.Name, detail: a.Detail, value: a.Name, match: a.Name + " " + a.Detail})
	}
	items = append(items, pickerItem{label: "New account", detail: "set up another", value: loginNew, match: "new account"})
	spec := pickerSpecs[pickerLoginAccount]
	spec.title = network.Label + ": sign in which account?"
	m.picker = newPickerWith(pickerLoginAccount, spec, items)
	return m, nil
}

// chooseLoginAccount signs an account in again, or sets up a new one.
func (m Model) chooseLoginAccount(value string) (Model, tea.Cmd) {
	m = m.closePicker()
	if value == loginNew {
		value = ""
	}
	return m.startLogin(m.login.network, value)
}

// startLogin has the daemon begin signing account in ("" sets up a new one); steps arrive as
// events.
func (m Model) startLogin(network api.LoginNetwork, account string) (Model, tea.Cmd) {
	logins, ok := m.backend.(api.Logins)
	if !ok {
		return m.sayErr("could not sign in", errNoLogin), nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, loginTimeout)
	events := make(chan loginEvent, 4)
	m.login = loginState{
		stage: loginWorking, network: network, account: account, networks: m.login.networks,
		note: "signing in…", cancel: cancel, events: events,
	}
	// The daemon re-reads the config when asked; a backend that cannot has none to read.
	reload, _ := m.backend.(configReloader)
	work := loginWork(logins, reload, m.link.restart, network.Network, account)
	start := func() tea.Msg {
		go work(ctx, events)
		return nil
	}
	// The model listens for as long as it runs: the sign-in's own deadline must not
	// silence the event that says it passed.
	return m, tea.Batch(start, listen(m.ctx, events, wrapLogin))
}

// configReloader is a daemon that re-reads the config when asked.
type configReloader interface {
	ReloadConfig(ctx context.Context) error
}

// loginWork runs the client side of a sign-in off the update loop: it begins the login and
// answers each step (asking the model for answers, or to write a new account, then having the
// daemon re-read the config) until the network is done. A step needing a daemon restart
// restarts it once and begins again. Everything goes to events, which it closes.
func loginWork(
	logins api.Logins, reload configReloader, restart func(context.Context) error, network, account string,
) func(context.Context, chan<- loginEvent) {
	return func(ctx context.Context, events chan<- loginEvent) {
		defer close(events)
		r := &loginRun{logins: logins, reload: reload, restart: restart, network: network, account: account, events: events}
		defer r.cancelIfLeft(ctx)
		step, err := logins.BeginLogin(ctx, network, account)
		for err == nil {
			var done bool
			if step, done, err = r.answer(ctx, step); done {
				r.sayTimedOut(ctx)
				return
			}
		}
		if r.sayTimedOut(ctx) {
			return
		}
		if ctx.Err() == nil {
			r.login = "" // the login ended there
		}
		r.send(ctx, loginEvent{final: true, err: err})
	}
}

// errSignInTimedOut is a sign-in that waited loginTimeout, most of it for an answer.
var errSignInTimedOut = fmt.Errorf("it waited %v and gave up; start it again", loginTimeout)

// sayTimedOut tells the model the sign-in ran out of time, reporting whether it did. The
// deadline is ctx's own, so ctx cannot carry the news: it goes into the events' buffer,
// which the model reads with its own context. Without it the form stays open, takes the
// answers, and shows "signing in" for good.
func (r *loginRun) sayTimedOut(ctx context.Context) bool {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}
	select {
	case r.events <- loginEvent{final: true, err: errSignInTimedOut}:
	default: // the buffer is full only when the model stopped reading
	}
	return true
}

// loginRun is one sign-in's client side, as loginWork leads it.
type loginRun struct {
	logins           api.Logins
	reload           configReloader
	restart          func(context.Context) error
	network, account string
	events           chan<- loginEvent
	// login is the daemon's login in progress, for canceling; restarted marks a restart already
	// done.
	login     string
	restarted bool
}

// send hands the model e; false when the sign-in was canceled first.
func (r *loginRun) send(ctx context.Context, e loginEvent) bool {
	select {
	case r.events <- e:
		return true
	case <-ctx.Done():
		return false
	}
}

// cancelIfLeft ends the daemon's login when this side was canceled.
func (r *loginRun) cancelIfLeft(ctx context.Context) {
	if r.login == "" || ctx.Err() == nil {
		return
	}
	stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = r.logins.CancelLogin(stop, r.login)
}

// answer does what step asks and returns the next step; done when the sign-in ended (finished
// or canceled).
func (r *loginRun) answer(ctx context.Context, step api.LoginStep) (api.LoginStep, bool, error) {
	r.login, r.account = step.Login, cmp.Or(step.Account, r.account)
	switch {
	case step.Restart:
		if r.restarted || r.restart == nil {
			return step, false, fmt.Errorf("%s: restart kithd, then sign in again", cmp.Or(step.Done, "kithd must restart"))
		}
		r.restarted, r.login = true, ""
		r.send(ctx, loginEvent{note: cmp.Or(step.Done, "set up") + "; restarting kithd…", account: r.account})
		if err := r.restart(ctx); err != nil {
			return step, false, err
		}
		next, err := r.logins.BeginLogin(ctx, r.network, r.account)
		return next, false, err //nolint:wrapcheck // the daemon's own words
	case step.Done != "":
		r.login = ""
		r.send(ctx, loginEvent{final: true, done: step.Done})
		return step, true, nil
	case step.Code != "" || step.QR != "":
		r.send(ctx, loginEvent{code: step.Code, qr: step.QR, note: step.Note, account: r.account})
		next, err := r.logins.AnswerLogin(ctx, r.login, nil)
		return next, false, err //nolint:wrapcheck // the daemon's own words
	case step.Configure != nil:
		next, err := writeAndGoOn(ctx, r.logins, r.reload, step, func(e loginEvent) bool { return r.send(ctx, e) })
		return next, ctx.Err() != nil, err
	}
	answer := make(chan map[string]string, 1)
	if !r.send(ctx, loginEvent{ask: step.Ask, answer: answer, note: step.Note, account: r.account}) {
		return step, true, nil
	}
	select {
	case values := <-answer:
		next, err := r.logins.AnswerLogin(ctx, r.login, values)
		return next, false, err //nolint:wrapcheck // the daemon's own words
	case <-ctx.Done():
		return step, true, nil
	}
}

// writeAndGoOn has the model write the step's new account to the config, the daemon re-read
// it, and the login go on.
func writeAndGoOn(
	ctx context.Context, logins api.Logins, reload configReloader, step api.LoginStep, send func(loginEvent) bool,
) (api.LoginStep, error) {
	written := make(chan func() error, 1)
	if !send(loginEvent{write: step.Configure, written: written, account: step.Account}) {
		return api.LoginStep{}, ctx.Err() //nolint:wrapcheck // canceled
	}
	var save func() error
	select {
	case save = <-written:
	case <-ctx.Done():
		return api.LoginStep{}, ctx.Err() //nolint:wrapcheck // canceled
	}
	if err := save(); err != nil {
		return api.LoginStep{}, err
	}
	if reload != nil {
		// The daemon reads the config only when asked, and the account is new to it.
		if err := reload.ReloadConfig(ctx); err != nil {
			return api.LoginStep{}, fmt.Errorf("have kithd re-read the config: %w", err)
		}
	}
	return logins.AnswerLogin(ctx, step.Login, nil) //nolint:wrapcheck // the daemon's own words
}

// handleLoginMsg handles sign-in news: a step to answer, a record to write, a code or note to
// show, or at the end what it did, re-reading rooms and spaces.
func (m Model) handleLoginMsg(msg loginMsg) (Model, tea.Cmd) {
	if m.login.stage == loginOff || !msg.ok {
		return m, nil // canceled meanwhile
	}
	e := msg.event
	m.login.account = cmp.Or(e.account, m.login.account)
	if e.note != "" {
		m.login.note = e.note
	}
	next := listen(m.ctx, m.login.events, wrapLogin)
	switch {
	case e.final:
		label, network, account := m.login.network.Label, m.login.network, m.login.account
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
		next, offer := m.say(e.done).offerImport(network, account)
		return next, tea.Batch(m.refreshRoomsCmd(), m.refreshSpacesCmd(), offer)
	case e.write != nil:
		var applied tea.Cmd
		var save func() error
		m, applied, save = m.writeLoginRecord(*e.write)
		e.written <- save
		return m, tea.Batch(applied, next)
	case e.ask != nil:
		m.login.stage, m.login.fields, m.login.at, m.login.values, m.login.answer = loginAsking, e.ask, 0, map[string]string{}, e.answer
		m.login.code, m.login.qr = "", ""
		// A field asked again was refused: its earlier answer goes, and why it was
		// refused shows with the field (no note: nothing to say, and none kept).
		m.login.note = e.note
		m.login.answered = slices.DeleteFunc(slices.Clone(m.login.answered), func(a answeredField) bool {
			return slices.ContainsFunc(e.ask, func(f api.LoginField) bool { return f.Key == a.key })
		})
		m = m.askLoginField()
		if e.note != "" {
			m = m.say(e.note)
		}
		return m, next
	case e.code != "" || e.qr != "":
		m.login.code, m.login.qr = e.code, e.qr
	}
	return m, next
}

// writeLoginRecord writes a new account into the config as a setting is, returning how to save
// it; if refused (applyConfig said why), saving fails.
func (m Model) writeLoginRecord(rec api.LoginRecord) (Model, tea.Cmd, func() error) {
	cfg := m.conf.base.Clone()
	if err := cfg.Write(rec.Table, rec.Values); err != nil {
		return m, nil, func() error { return fmt.Errorf("set the account up: %w", err) }
	}
	// Not held to kithd's check (applyConfig): the record is the network's own, and kithd judges
	// it again on re-read.
	derived, err := derive(cfg)
	if err != nil {
		return m.sayErr("could not apply", err), nil, func() error {
			return errors.New("the config refused the new account (the status line says why)")
		}
	}
	next, applied := m.runDerived(cfg, derived, "set up "+m.login.network.Label+" "+m.login.account)
	// Saved by the login itself, which waits for the daemon to have it before going on.
	gen, writer, app := next.conf.writer.take(), next.conf.writer, m.ctx
	return next, applied, func() error {
		if err := writer.save(app, gen, cfg); err != nil && !errors.Is(err, errNoConfigStore) {
			return fmt.Errorf("save the config: %w", err)
		}
		return nil
	}
}

// askLoginField opens the prompt for the current field with its suggestion selected, so typing
// replaces it.
func (m Model) askLoginField() Model {
	f, ok := m.login.field()
	if !ok {
		return m
	}
	m = m.openPromptWith(promptLogin, f.Value)
	m.prompt.fresh = f.Value != ""
	return m
}

// submitLogin takes the field's answer: an empty required one is asked again; the step's last
// field sends them all.
func (m Model) submitLogin(input string) (Model, tea.Cmd) {
	f, ok := m.login.field()
	if !ok {
		return m, nil
	}
	value := strings.TrimSpace(input)
	if value == "" && !f.Optional {
		return m.askLoginField().say(f.Label + " is needed"), nil
	}
	values := maps.Clone(m.login.values) // copies of the model share the map
	values[f.Key] = value
	m.login.values = values
	shown := value
	if f.Secret {
		shown = strings.Repeat("•", min(utf8.RuneCountInString(value), 12))
	}
	m.login.answered = append(slices.Clone(m.login.answered), answeredField{key: f.Key, label: f.Label, shown: shown})
	m.login.at++
	if m.login.at < len(m.login.fields) {
		return m.askLoginField(), nil
	}
	// The sign-in waits for them; its buffer holds one.
	m.login.answer <- values
	m.login.stage, m.login.answer, m.login.note = loginWorking, nil, "signing in…"
	return m, nil
}

// cancelLogin abandons the sign-in: a prompt closed, or the daemon's work canceled.
func (m Model) cancelLogin() Model {
	if m.login.cancel != nil {
		m.login.cancel()
	}
	m.login = loginState{}
	return m.say("sign-in canceled")
}

// handleLoginKey handles a key while the daemon signs in: cancel cancels, the rest wait (as with
// verification).
func (m Model) handleLoginKey(key tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.keys.lookup(key.String(), scopePrompt) == actCancel {
		return m.cancelLogin(), nil
	}
	return m, nil
}

// loginTitle is the timeline pane's title while signing in.
func (m Model) loginTitle() string {
	if m.login.account == "" {
		return "Sign in: " + m.login.network.Label + " — a new account"
	}
	return "Sign in: " + m.login.network.Label + " " + m.login.account
}

// loginLines is the timeline pane while signing in: answers so far, then the current field and
// how to find its answer, or what the daemon is doing.
func (m Model) loginLines(width, rows int) []string {
	var lines []string
	block := func(text string) {
		for _, para := range docParagraphs(text) {
			lines = append(lines, drawBlock(para, blockSpec{width: width})...)
		}
	}
	lines = append(lines, "")
	for _, a := range m.login.answered {
		lines = append(lines, m.theme.Muted.Render(fmt.Sprintf("%-12s", a.label))+" "+a.shown)
	}
	if len(lines) > 1 {
		lines = append(lines, "")
	}
	if f, ok := m.login.field(); ok {
		if m.login.note != "" {
			for _, para := range docParagraphs(m.login.note) {
				for _, line := range drawBlock(para, blockSpec{width: width}) {
					lines = append(lines, m.theme.Badge(true).Render(line))
				}
			}
			lines = append(lines, "")
		}
		block(f.Help)
		if m.promptInline() {
			lines = append(lines, m.promptLines(width)...)
		}
		if len(m.login.fields) > 1 {
			lines = append(lines, "", m.theme.Muted.Render(fmt.Sprintf("%d of %d", m.login.at+1, len(m.login.fields))))
		}
	} else {
		if m.login.code != "" {
			lines = append(lines, "    "+m.theme.TitleActive.Render(m.login.code), "")
		}
		block(m.login.note)
		if m.login.qr != "" {
			lines = append(lines, "")
			lines = append(lines, qrLines(m.login.qr, width, rows-len(lines)-2)...)
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

// loginPromptLabel is the status line's label for the field being typed.
func (m Model) loginPromptLabel() string {
	if f, ok := m.login.field(); ok {
		return f.Label + ": "
	}
	return ""
}

// loginSecret reports whether the field being typed is drawn as dots: a secret login
// field, or a command's secret.
func (m Model) loginSecret() bool {
	if m.prompt.kind == promptCommandSecret {
		return true
	}
	f, ok := m.login.field()
	return ok && f.Secret && m.prompt.kind == promptLogin
}

// masked is an editor drawn as dots, the caret where it was.
func masked(e editor) editor {
	dots := func(s string) string { return strings.Repeat("•", utf8.RuneCountInString(s)) }
	before := dots(e.text[:e.at])
	return editor{text: before + dots(e.text[e.at:]), at: len(before)}
}

// qrLines is data as a QR code that fits width and rows, light on dark whatever the
// theme so a phone can read it; a window too small for it says so instead.
func qrLines(data string, width, rows int) []string {
	code, err := qrtext.Lines(data)
	if err != nil {
		return []string{"the QR code could not be drawn: " + err.Error()}
	}
	if side := utf8.RuneCountInString(code[0]); side > width || len(code) > rows {
		return []string{fmt.Sprintf("the window is too small for the QR code: it needs %d columns and %d rows", side, len(code)+2)}
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(qrtext.Light)).Background(lipgloss.Color(qrtext.Dark))
	out := make([]string, len(code))
	for i, line := range code {
		out[i] = style.Render(line)
	}
	return out
}
